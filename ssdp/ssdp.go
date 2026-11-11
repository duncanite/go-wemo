// Package ssdp is the Simple Service Discovery Protocol as a UPnP device
// speaks it: a search sent to the multicast group and the answers that come
// back, and the announcements a device multicasts on its own. Both halves
// take the group address as an option, so a test or an unusual network can
// point them elsewhere.
package ssdp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/mycophonic/primordium/fault"
)

// The multicast group every UPnP device listens on, and the search targets
// that matter here.
const (
	Group = "239.255.255.250:1900"
	// All asks every device to answer with every service it has.
	All = "ssdp:all"
	// RootDevices asks every device to answer once, as a root device.
	RootDevices = "upnp:rootdevice"
	// Belkin is the device type a Wemo plug answers to.
	Belkin = "urn:Belkin:device:controllee:1"
)

// DefaultWait is how long a search collects answers; the M-SEARCH tells the
// devices to spread theirs over it.
const DefaultWait = 3 * time.Second

// ErrNoAnswer is the sentinel for a search nobody answered.
var ErrNoAnswer = fault.ErrNotFound

// Options shape a search or a listener. The zero value searches the
// multicast group for every root device over DefaultWait, from whatever
// local address the system picks.
type Options struct {
	// Target is the address the search is sent to, or the group the
	// listener joins; empty is Group.
	Target string
	// SearchTarget is the ST header, what the search asks for; empty is
	// RootDevices.
	SearchTarget string
	// Wait is the window the devices are told to answer in, the MX header;
	// the search listens a second past it. Zero is DefaultWait.
	Wait time.Duration
	// Interface, when set, is the one the socket binds to; a host with
	// several has to say which network the plugs are on.
	Interface string
}

// Response is one answer to a search, or one announcement: the headers a
// device put on it, read for the few that matter.
type Response struct {
	// Location is the URL of the device description.
	Location string
	// USN is the device's unique service name, its UUID and the type it
	// answered as.
	USN string
	// Type is the ST of an answer or the NT of an announcement.
	Type string
	// Server is the device's own description of its stack.
	Server string
	// MaxAge is how long the device says the answer stays good.
	MaxAge time.Duration
	// From is the address the datagram came from.
	From net.Addr
	// Header is every header as sent, for what the fields above leave out.
	Header http.Header
}

// Search sends the M-SEARCH three times, a second apart, and returns
// every distinct answer that arrives before the wait and a second's grace
// are over, by USN. No answer is ErrNoAnswer.
func Search(ctx context.Context, opts Options) ([]Response, error) {
	target, wait, searchTarget := opts.Target, opts.Wait, opts.SearchTarget
	if target == "" {
		target = Group
	}

	if wait <= 0 {
		wait = DefaultWait
	}

	if searchTarget == "" {
		searchTarget = RootDevices
	}

	remote, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return nil, fmt.Errorf("%w: ssdp target %q: %w", fault.ErrInvalidArgument, target, err)
	}

	local, err := localAddress(opts.Interface)
	if err != nil {
		return nil, err
	}

	conn, err := net.ListenUDP("udp4", local)
	if err != nil {
		return nil, fmt.Errorf("%w: opening the search socket: %w", fault.ErrNetworkCommunication, err)
	}

	// The context closes the socket, which ends the collection at once.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	defer func() { _ = conn.Close() }()

	// The MX header is whole seconds, and a device may take the full span.
	seconds := max(int(wait/time.Second), 1)
	request := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + target + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: " + strconv.Itoa(seconds) + "\r\n" +
		"ST: " + searchTarget + "\r\n" +
		"USER-AGENT: go-wemo/1 UPnP/1.1\r\n\r\n"

	if _, writeErr := conn.WriteTo([]byte(request), remote); writeErr != nil {
		return nil, fmt.Errorf("%w: sending the search: %w", fault.ErrNetworkCommunication, writeErr)
	}

	// A device spreads its answer over the MX window and may land at its
	// far end, and a datagram on Wi-Fi may be lost: the search goes out
	// three times, a second apart, and the socket stays open a while past
	// the window. Answers are one per USN, so a device heard twice counts
	// once.
	resends := make([]*time.Timer, 0, sends-1)

	for i := 1; i < sends; i++ {
		resends = append(
			resends,
			time.AfterFunc(min(time.Duration(i)*resendAfter, wait*time.Duration(i)/sends), func() {
				_, _ = conn.WriteTo([]byte(request), remote)
			}),
		)
	}

	defer func() {
		for _, resend := range resends {
			resend.Stop()
		}
	}()

	deadline := time.Now().Add(wait + grace)
	if due, ok := ctx.Deadline(); ok && due.Before(deadline) {
		deadline = due
	}

	if err = conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("%w: search deadline: %w", fault.ErrNetworkCommunication, err)
	}

	answers, err := collect(ctx, conn)
	if err != nil {
		return nil, err
	}

	if len(answers) == 0 {
		return nil, fmt.Errorf("%w: no device answered %s within %s", ErrNoAnswer, searchTarget, wait)
	}

	return answers, nil
}

// collect reads answers until the socket's deadline or the context ends,
// one per USN, the first seen kept.
func collect(ctx context.Context, conn *net.UDPConn) ([]Response, error) {
	var (
		answers []Response
		seen    = make(map[string]struct{})
		buffer  = make([]byte, datagramSize)
	)

	for {
		count, from, err := conn.ReadFrom(buffer)
		if err != nil {
			if over(ctx, err) {
				return answers, nil
			}

			return nil, fmt.Errorf("%w: reading answers: %w", fault.ErrNetworkCommunication, err)
		}

		if response, ok := parse(buffer[:count], from); ok && remember(seen, response) {
			answers = append(answers, response)
		}
	}
}

// over reports whether a read error is the collection ending: the
// socket's deadline, or the context.
func over(ctx context.Context, err error) bool {
	var timeout net.Error

	return (errors.As(err, &timeout) && timeout.Timeout()) || ctx.Err() != nil
}

// remember records the response's key and reports whether it was new; a
// device answers once per service it was asked for.
func remember(seen map[string]struct{}, response Response) bool {
	key := response.USN
	if key == "" {
		key = response.Location
	}

	if _, dup := seen[key]; dup {
		return false
	}

	seen[key] = struct{}{}

	return true
}

// sends is how many times the search goes out, resendAfter apart; grace
// is how long past the MX window the socket keeps listening.
const (
	sends       = 3
	resendAfter = time.Second
	grace       = time.Second
)

// datagramSize is more than any SSDP message: a UDP payload over the
// link's MTU is fragmented or dropped, and devices keep theirs small.
const datagramSize = 4096

// parse reads one datagram as an HTTP-shaped SSDP message: a search answer
// ("HTTP/1.1 200 OK") or an announcement ("NOTIFY * HTTP/1.1"). A search
// request from another client, or anything else, is not one.
func parse(data []byte, from net.Addr) (Response, bool) {
	reader := bufio.NewReader(bytes.NewReader(data))

	first, err := reader.ReadString('\n')
	if err != nil {
		return Response{}, false
	}

	first = strings.TrimSpace(first)

	switch {
	case strings.HasPrefix(first, "HTTP/1.1 200"), strings.HasPrefix(first, "HTTP/1.0 200"):
	case strings.HasPrefix(first, "NOTIFY "):
	default:
		return Response{}, false
	}

	// A device that ends its headers without the blank line still said
	// everything above it: the reader hands back what it parsed.
	mime, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil && len(mime) == 0 {
		return Response{}, false
	}

	header := http.Header(mime)

	response := Response{
		Location: header.Get("Location"),
		USN:      header.Get("Usn"),
		Type:     header.Get("St"),
		Server:   header.Get("Server"),
		MaxAge:   maxAge(header.Get("Cache-Control")),
		From:     from,
		Header:   header,
	}

	if response.Type == "" {
		response.Type = header.Get("Nt")
	}

	return response, true
}

// maxAge reads max-age out of a Cache-Control header; zero when absent.
func maxAge(value string) time.Duration {
	for part := range strings.SplitSeq(value, ",") {
		key, number, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found || !strings.EqualFold(key, "max-age") {
			continue
		}

		seconds, err := strconv.Atoi(strings.TrimSpace(number))
		if err != nil {
			return 0
		}

		return time.Duration(seconds) * time.Second
	}

	return 0
}

// localAddress is the address to bind: any, or the first IPv4 address of
// the named interface.
func localAddress(name string) (*net.UDPAddr, error) {
	if name == "" {
		return &net.UDPAddr{IP: net.IPv4zero}, nil
	}

	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("%w: interface %q: %w", fault.ErrInvalidArgument, name, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("%w: interface %q addresses: %w", fault.ErrNetworkCommunication, name, err)
	}

	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
			return &net.UDPAddr{IP: ipNet.IP.To4()}, nil
		}
	}

	return nil, fmt.Errorf("%w: interface %q has no IPv4 address", fault.ErrInvalidArgument, name)
}
