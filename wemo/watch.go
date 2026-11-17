package wemo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	upnp "github.com/forkcloser/go-upnp"
)

// How long whoever connects to the listener is given: a plug has
// headerTime to send an event's headers and requestTime to send the whole
// of it, and a connection that says nothing more is kept for idleTime.
// Without them a host on the network holds a connection for as long as
// it likes, by sending slowly or not at all.
const (
	headerTime  = 10 * time.Second
	requestTime = 30 * time.Second
	idleTime    = time.Minute
)

// Events is where plugs send their events: an HTTP server of this package's
// own, listening where its caller said. One serves every plug, and several
// goroutines may use it.
type Events struct {
	listener   net.Listener
	server     *http.Server
	subscriber *upnp.Subscriber

	// over ends when the server does, with why as its cause.
	over context.Context //nolint:containedctx // the server's life, which every Watch ends with
	end  context.CancelCauseFunc

	closing sync.Once
	served  chan struct{}
}

// Listen opens the listener plugs send their events to, at address: a host
// and a port, "192.0.2.10:49200"; port 0 takes a free one, which Addr then
// gives. The address is the caller's to say, and nothing is guessed: a plug
// has to reach it, and each Watch is told the URL its plug reaches it at.
func Listen(ctx context.Context, address string) (*Events, error) {
	var config net.ListenConfig

	listener, err := config.Listen(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("listening for events at %q: %w", address, err)
	}

	subscriber := upnp.NewSubscriber()

	events := &Events{
		listener: listener,
		server: &http.Server{
			Handler:           subscriber,
			ReadHeaderTimeout: headerTime,
			ReadTimeout:       requestTime,
			IdleTimeout:       idleTime,
		},
		subscriber: subscriber,
		served:     make(chan struct{}),
	}

	events.over, events.end = context.WithCancelCause(context.Background())

	go func() {
		defer close(events.served)

		// Serve returns when the server is closed, and otherwise only
		// when the listener can accept no more.
		events.end(fmt.Errorf("%w: the events listener ended: %w", ErrLost, events.server.Serve(listener)))
	}()

	return events, nil
}

// Addr is where the listener listens: the address Listen was given, with
// the port it took. It is not valid for a listener that has no IP address.
func (e *Events) Addr() netip.AddrPort {
	address, ok := e.listener.Addr().(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}
	}

	return address.AddrPort()
}

// Close ends the listener; every Watch through it returns ErrClosed.
func (e *Events) Close() error {
	var err error

	e.closing.Do(func() {
		e.end(ErrClosed)

		err = e.server.Close()
	})

	<-e.served

	if err != nil {
		return fmt.Errorf("closing the events listener: %w", err)
	}

	return nil
}

// Done is closed when the listener is over: closed, or lost.
func (e *Events) Done() <-chan struct{} {
	return e.over.Done()
}

// Err is nil while the listener lives, ErrClosed after Close, and an ErrLost
// with its cause when it ended by itself.
func (e *Events) Err() error {
	return context.Cause(e.over) //nolint:wrapcheck // the listener's own end, worded where it ended
}

// Event is what a plug says of itself, unasked: the variables of its
// basicevent service that changed, and what each now holds.
type Event struct {
	// Properties is the variables it carries, in the order the plug
	// wrote them: BinaryState for the switch, and others the plug's
	// service description names.
	Properties upnp.Arguments
}

// State is the switch as the event tells it, on or off, with the raw value
// as Plug.State gives it; said is false for an event that says nothing of the
// switch.
func (e Event) State() (on bool, raw string, said bool) {
	raw, said = e.Properties.Get(binaryState)
	if !said {
		return false, "", false
	}

	return switched(raw), raw, true
}

// Watch tells handle first how things stand, the plug's first event, which
// carries its state; and then everything the plug says changed, in order,
// for as long as ctx lasts. Events come one at a time, on the goroutine that
// called Watch; handle may take its time and may ask things of the Plug.
// Several may watch a plug at once.
//
// events is the listener the plug sends them to, and callback the URL the
// plug reaches it at: "http://ADDRESS:PORT/", this host's address on the
// plug's network, which a Found gives as Local, and the listener's port.
// A plug asked from another network than its own, with an address there
// for callback, has been seen to refuse the subscription, an ErrRefused
// that carries the status 412. One that takes a subscription and cannot
// reach the callback sends nothing: the caller holds the deadline on that
// first event.
//
// Watch returns ctx's error when ctx ends. Sooner, it returns ErrClosed
// when the listener was closed, an ErrRefused when the plug takes no
// subscription, and an ErrLost when the subscription can be trusted no
// more or the listener ended by itself. Nothing subscribes again by itself:
// watching again does, and starts from how things stand.
func (p *Plug) Watch(ctx context.Context, events *Events, callback string, handle func(Event)) error {
	if events == nil || handle == nil {
		return fmt.Errorf("%w: a Watch needs a listener and a handler", ErrArgument)
	}

	service, err := p.Service(BasicEvent)
	if err != nil {
		return err
	}

	if err = events.Err(); err != nil {
		return err
	}

	watching, cancel := context.WithCancel(ctx)
	defer cancel()

	// The listener's life is not the caller's context: the Watch ends
	// with whichever ends first.
	stop := context.AfterFunc(events.over, cancel) //nolint:contextcheck // see above
	defer stop()

	err = events.subscriber.Watch(watching, p.client, service, callback, func(told upnp.Event) {
		handle(Event{Properties: told.Properties})
	})

	if over := events.Err(); over != nil && ctx.Err() == nil {
		return over
	}

	return own(err)
}
