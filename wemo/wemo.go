// Package wemo is a Wi-Fi Wemo plug as a program drives it on the LAN: told
// apart from what else is advertised there, opened by address, read and
// switched through its basicevent service, heard through UPnP eventing, and
// set up onto a network without Belkin. Every verb is a SOAP action the plug
// itself declares; Call reaches the ones with no verb of their own.
//
// It opens one socket and no other: the listener plugs send their events to,
// at the address Listen is given. The HTTP client is the caller's, and so is
// the means of finding plugs, a Browser, which a host has one of. It reads and
// writes no file, and logs nothing.
package wemo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	upnp "github.com/forkcloser/go-upnp"
)

// The errors a Plug hands back, to test with errors.Is.
var (
	// ErrNotFound: no Wemo answers there, the plug has no such service or
	// action, or it does not see the network it is asked to join.
	ErrNotFound = errors.New("not found")
	// ErrRefused: the plug said no: a fault, which errors.As gives as a
	// *upnp.FaultError, a status, a *upnp.StatusError, or its own word in
	// an answer.
	ErrRefused = upnp.ErrRefused
	// ErrAnswer: the plug's answer is not what its service has it answer.
	ErrAnswer = upnp.ErrAnswer
	// ErrArgument: what was asked cannot be sent: an empty name, an
	// argument the action does not declare, a passphrase the plug cannot
	// take. Nothing was sent, but for a passphrase the plug found too
	// short, which it says after the fact.
	ErrArgument = errors.New("not a valid argument")
	// ErrLost: a Watch can no longer be trusted to have told everything,
	// or the listener its events come to ended by itself. Watching again
	// starts from how things stand.
	ErrLost = upnp.ErrLost
	// ErrClosed: the Events listener was closed by its owner.
	ErrClosed = errors.New("closed")
	// ErrUnsupported: the plug wants what this package does not have: the
	// passphrase encryption of the firmware that says rtos.
	ErrUnsupported = errors.New("not supported")
)

// Doer is the HTTP client every request to a plug goes through, which is the
// caller's, with its timeouts: an http.Client is one. It has to follow no
// redirect, since a client that follows them sends a request, its arguments
// with it, wherever a plug points:
//
//	client := &http.Client{
//		CheckRedirect: func(*http.Request, []*http.Request) error {
//			return http.ErrUseLastResponse
//		},
//	}
type Doer interface {
	// Do sends a request and returns its answer, as http.Client.Do does.
	Do(request *http.Request) (*http.Response, error)
}

// ID is a plug's identity: its UDN, "uuid:Socket-1_0-" and its serial, the
// same for as long as the plug is the same plug, whatever its address and its
// port. A plug says it on the network, where Recognize reads it, and in its
// description, where Open does.
type ID string

// Services a plug has, by their short names.
const (
	BasicEvent = "basicevent"
	WiFiSetup  = "WiFiSetup"
	TimeSyncS  = "timesync"
	Firmware   = "firmwareupdate"
	MetaInfo   = "metainfo"
	DeviceInfo = "deviceinfo"
	Rules      = "rules"
)

// servicePrefix and serviceVersion surround a service's short name in its
// type.
const (
	servicePrefix  = "urn:Belkin:service:"
	serviceVersion = ":1"
)

// descriptionPath is where a plug serves its description.
const descriptionPath = "/setup.xml"

// Ports are the ports a Wemo serves its description on, most common first;
// the port drifts among them after a reboot, so a host is probed through
// the list.
func Ports() []int {
	return []int{
		49153,
		49152,
		49154,
		49151,
		49155,
		49156,
		49157,
		49158,
		49159,
	}
}

// Description is what a plug says of itself in its description, as it read
// when the plug was opened.
type Description struct {
	// URL is where the description was read.
	URL string
	// Name is the friendly name; Plug.Name asks for it as it is now.
	Name string
	// Model, ModelNumber and ModelDescription are the maker's words:
	// "Socket", "1.0", "Belkin Plugin Socket 1.0".
	Model            string
	ModelNumber      string
	ModelDescription string
	// Serial is the serial number.
	Serial string
	// MAC is twelve hexadecimal digits with no separator: the plug's own,
	// one below the address its radio uses.
	MAC string
	// Firmware and Hardware are the versions of each.
	Firmware string
	Hardware string
}

// belkin is the elements of a description the standard does not name.
type belkin struct {
	MAC      string `xml:"macAddress"`
	Firmware string `xml:"firmwareVersion"`
	Hardware string `xml:"hwVersion"`
	RTOS     string `xml:"rtos"`
	IoT      string `xml:"iot"`
}

// Plug is one Wemo device: its description and the services on it. It holds
// no connection, and its methods may be called from several goroutines.
type Plug struct {
	client Doer
	device *upnp.Device
	said   Description
	// newer says the firmware wants the passphrase encrypted the second
	// way.
	newer bool
}

// Open describes the plug at target: a URL is read as the description, a
// host with a port is read at that port, a bare host is probed through
// Ports. Every request the Plug makes goes through client.
func Open(ctx context.Context, client Doer, target string) (*Plug, error) {
	if strings.Contains(target, "://") {
		return describe(ctx, client, target)
	}

	if host, port, err := net.SplitHostPort(target); err == nil {
		return describe(ctx, client, at(net.JoinHostPort(host, port)))
	}

	var last error

	for _, candidate := range Ports() {
		plug, err := describe(ctx, client, at(net.JoinHostPort(target, strconv.Itoa(candidate))))
		if err == nil {
			return plug, nil
		}

		last = err

		if ctx.Err() != nil {
			break
		}
	}

	return nil, fmt.Errorf("%w: no Wemo answered on %s at any of its ports: %w", ErrNotFound, target, last)
}

// at is the URL of the description of the plug at host, which has its port.
func at(host string) string {
	return (&url.URL{Scheme: "http", Host: host, Path: descriptionPath}).String()
}

func describe(ctx context.Context, client Doer, location string) (*Plug, error) {
	device, err := upnp.Describe(ctx, client, location)
	if err != nil {
		return nil, own(err)
	}

	var extra belkin
	if err = device.Decode(&extra); err != nil {
		return nil, own(err)
	}

	return &Plug{
		client: client,
		device: device,
		said: Description{
			URL:              location,
			Name:             device.FriendlyName,
			Model:            device.ModelName,
			ModelNumber:      device.ModelNumber,
			ModelDescription: device.ModelDescription,
			Serial:           device.SerialNumber,
			MAC:              strings.TrimSpace(extra.MAC),
			Firmware:         strings.TrimSpace(extra.Firmware),
			Hardware:         strings.TrimSpace(extra.Hardware),
		},
		// pywemo's reading of the app: rtos and not iot.
		newer: strings.TrimSpace(extra.RTOS) == "1" && strings.TrimSpace(extra.IoT) != "1",
	}, nil
}

// own is an error of the UPnP library's under this package's names.
func own(err error) error {
	if errors.Is(err, upnp.ErrInvalid) {
		return fmt.Errorf("%w: %w", ErrArgument, err)
	}

	return err
}

// ID is who the plug is.
func (p *Plug) ID() ID {
	return ID(p.device.UDN)
}

// Description is what the plug said of itself when it was opened.
func (p *Plug) Description() Description {
	return p.said
}

// Device is the plug's UPnP description whole: its services, and what this
// package has no word for.
func (p *Plug) Device() *upnp.Device {
	return p.device
}

// Service is the plug's service of that short name, BasicEvent, or of that
// type or ID.
func (p *Plug) Service(name string) (upnp.Service, error) {
	kind := name
	if !strings.Contains(name, ":") {
		kind = servicePrefix + name + serviceVersion
	}

	service, found := p.device.Service(kind)
	if !found {
		has := make([]string, 0, len(p.device.Services))
		for _, each := range p.device.Services {
			has = append(has, ServiceName(each))
		}

		return upnp.Service{}, fmt.Errorf("%w: the plug has no service %q; it has %v", ErrNotFound, name, has)
	}

	return service, nil
}

// ServiceName is a service's short name, "basicevent" for
// "urn:Belkin:service:basicevent:1"; a type that is not Belkin's is returned
// whole.
func ServiceName(service upnp.Service) string {
	name, found := strings.CutPrefix(service.Type, servicePrefix)
	if !found {
		return service.Type
	}

	if end := strings.LastIndex(name, ":"); end > 0 {
		return name[:end]
	}

	return name
}

// Call invokes any action the plug declares on the named service, and
// returns the answer whole. The arguments go out in the order the service's
// description lists them, whatever order they are given in; one the action
// does not declare is refused, and nothing is sent.
func (p *Plug) Call(ctx context.Context, service, action string, args ...upnp.Argument) (upnp.Arguments, error) {
	found, err := p.Service(service)
	if err != nil {
		return nil, err
	}

	description, err := found.Describe(ctx, p.client)
	if err != nil {
		return nil, own(err)
	}

	declared, known := description.Action(action)
	if !known {
		return nil, fmt.Errorf(
			"%w: %s has no action %q among its %d actions", ErrNotFound, service, action, len(description.Actions),
		)
	}

	// Every argument passed goes out, whatever direction the device
	// claims for it: the description gets directions wrong both ways,
	// and an argument silently dropped is a call that lies about what it
	// sent.
	names := make([]string, 0, len(declared.In)+len(declared.Out))
	for _, parameter := range slices.Concat(declared.In, declared.Out) {
		names = append(names, parameter.Name)
	}

	ordered := slices.Clone(args)

	for _, argument := range ordered {
		if !slices.Contains(names, argument.Name) {
			return nil, fmt.Errorf(
				"%w: %s.%s takes no argument %q; it declares %v",
				ErrArgument, service, action, argument.Name, names[:len(declared.In)],
			)
		}
	}

	slices.SortStableFunc(ordered, func(one, other upnp.Argument) int {
		return slices.Index(names, one.Name) - slices.Index(names, other.Name)
	})

	answer, err := found.Call(ctx, p.client, action, ordered...)

	return answer, own(err)
}

// ByName finds the plug whose friendly name matches, case-insensitively,
// among plugs.
func ByName(plugs []*Plug, name string) (*Plug, error) {
	names := make([]string, 0, len(plugs))

	for _, plug := range plugs {
		if strings.EqualFold(plug.said.Name, name) {
			return plug, nil
		}

		names = append(names, plug.said.Name)
	}

	return nil, fmt.Errorf("%w: no plug named %q; found %q", ErrNotFound, name, names)
}

// invoke calls an action of a service with its arguments in the order
// given, which has to be the service's.
func (p *Plug) invoke(ctx context.Context, service, action string, args ...upnp.Argument) (upnp.Arguments, error) {
	found, err := p.Service(service)
	if err != nil {
		return nil, err
	}

	answer, err := found.Call(ctx, p.client, action, args...)

	return answer, own(err)
}

// basic calls an action on basicevent, the service most verbs live on.
func (p *Plug) basic(ctx context.Context, action string, args ...upnp.Argument) (upnp.Arguments, error) {
	return p.invoke(ctx, BasicEvent, action, args...)
}

// value is what an answer holds under name, with the space around it gone.
func value(answer upnp.Arguments, name string) string {
	held, _ := answer.Get(name)

	return strings.TrimSpace(held)
}
