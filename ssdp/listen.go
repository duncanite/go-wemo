package ssdp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/mycophonic/primordium/fault"
)

// Announcement is one NOTIFY a device multicast: an alive, a bye, or an
// update, with the response's fields.
type Announcement struct {
	Response

	// Alive is true for ssdp:alive, false for ssdp:byebye.
	Alive bool
}

// Listen joins the multicast group and calls handle for every announcement
// until the context ends. Search answers addressed to other clients are not
// announcements and are skipped.
func Listen(ctx context.Context, opts Options, handle func(Announcement)) error {
	conn, err := join(opts)
	if err != nil {
		return err
	}

	// The context closes the socket, which ends the read below.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	buffer := make([]byte, datagramSize)

	for {
		count, from, err := conn.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}

			return fmt.Errorf("%w: reading announcements: %w", fault.ErrNetworkCommunication, err)
		}

		response, ok := parse(buffer[:count], from)
		if !ok {
			continue
		}

		nts := response.Header.Get("Nts")
		if nts == "" {
			// A unicast search answer that reached the group socket.
			continue
		}

		handle(Announcement{Response: response, Alive: !strings.EqualFold(nts, "ssdp:byebye")})
	}
}

// join opens the multicast socket on the group, on one interface when
// named.
func join(opts Options) (*net.UDPConn, error) {
	target := opts.Target
	if target == "" {
		target = Group
	}

	group, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		return nil, fmt.Errorf("%w: ssdp group %q: %w", fault.ErrInvalidArgument, target, err)
	}

	var iface *net.Interface

	if opts.Interface != "" {
		iface, err = net.InterfaceByName(opts.Interface)
		if err != nil {
			return nil, fmt.Errorf("%w: interface %q: %w", fault.ErrInvalidArgument, opts.Interface, err)
		}
	}

	conn, err := net.ListenMulticastUDP("udp4", iface, group)
	if err != nil {
		return nil, fmt.Errorf("%w: joining %s: %w", fault.ErrNetworkCommunication, target, err)
	}

	return conn, nil
}
