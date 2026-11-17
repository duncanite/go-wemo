package wemo_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	upnp "github.com/forkcloser/go-upnp"

	"github.com/duncanite/go-wemo/wemo"
	"github.com/duncanite/go-wemo/wemo/wemotest"
)

// patience is how long a test waits for what has to happen. Generous: a
// loaded runner has taken seconds over a loopback round trip, and the tests
// are about what is told, not how soon.
const patience = 30 * time.Second

// listening is a listener on the loopback, closed with the test, and the URL
// a plug on the loopback reaches it at.
func listening(t *testing.T) (*wemo.Events, string) {
	t.Helper()

	events, err := wemo.Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	t.Cleanup(func() { _ = events.Close() })

	return events, "http://" + events.Addr().String() + "/"
}

// watching is a Watch under way: its events as they come, and its end.
type watching struct {
	events chan wemo.Event
	ended  chan error
}

func watch(ctx context.Context, plug *wemo.Plug, events *wemo.Events, callback string) *watching {
	under := &watching{events: make(chan wemo.Event, 16), ended: make(chan error, 1)}

	go func() {
		under.ended <- plug.Watch(ctx, events, callback, func(event wemo.Event) { under.events <- event })
	}()

	return under
}

func (w *watching) next(t *testing.T, fake *wemotest.Plug) wemo.Event {
	t.Helper()

	select {
	case event := <-w.events:
		return event
	case err := <-w.ended:
		t.Fatalf("the Watch ended: %v", err)
	case <-time.After(patience):
		t.Fatalf("no event within %s; undelivered: %v", patience, fake.Undelivered())
	}

	return wemo.Event{}
}

func (w *watching) end(t *testing.T) error {
	t.Helper()

	select {
	case err := <-w.ended:
		return err
	case <-time.After(patience):
		t.Fatalf("the Watch did not end within %s", patience)
	}

	return nil
}

// TestWatch: a Watch tells first how the plug stands, then each change, and
// ends with its context, leaving no subscription on the plug.
func TestWatch(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	events, callback := listening(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	under := watch(ctx, open(t, fake), events, callback)

	first := under.next(t, fake)
	if on, raw, said := first.State(); !said || on || raw != "0" {
		t.Fatalf("the first event's state = %v %q %v; event %+v", on, raw, said, first)
	}

	if zone, said := first.Properties.Get("TimeZoneNotification"); !said || zone != "005" {
		t.Fatalf("the first event lacks the plug's other variables: %+v", first)
	}

	fake.Switch(t.Context(), true)

	if on, raw, said := under.next(t, fake).State(); !said || !on || raw != "1" {
		t.Fatalf("the event of a switch on = %v %q %v", on, raw, said)
	}

	cancel()

	if err := under.end(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("a Watch whose context ended = %v", err)
	}

	if left := fake.Subscriptions(); left != 0 {
		t.Fatalf("the Watch left %d subscriptions on the plug", left)
	}
}

// TestAnEventThatSaysNothingOfTheSwitch: State says so.
func TestAnEventThatSaysNothingOfTheSwitch(t *testing.T) {
	t.Parallel()

	event := wemo.Event{Properties: upnp.Arguments{{Name: "TimeSyncRequest", Value: "0"}}}

	if on, raw, said := event.State(); said || on || raw != "" {
		t.Fatalf("State = %v %q %v", on, raw, said)
	}
}

// TestWatchEndsWithItsListener: closing the listener ends every Watch
// through it with ErrClosed, and a Watch through a closed one is ErrClosed
// at once, with nothing asked of the plug.
func TestWatchEndsWithItsListener(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	events, callback := listening(t)
	plug := open(t, fake)

	select {
	case <-events.Done():
		t.Fatal("a listener just opened is done")
	default:
	}

	if err := events.Err(); err != nil {
		t.Fatalf("Err of a listener that lives = %v", err)
	}

	under := watch(t.Context(), plug, events, callback)
	under.next(t, fake)

	if err := events.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := under.end(t); !errors.Is(err, wemo.ErrClosed) {
		t.Fatalf("a Watch whose listener was closed = %v", err)
	}

	select {
	case <-events.Done():
	default:
		t.Fatal("a closed listener is not done")
	}

	if err := events.Err(); !errors.Is(err, wemo.ErrClosed) {
		t.Fatalf("Err of a closed listener = %v", err)
	}

	if err := events.Close(); err != nil {
		t.Fatalf("Close again: %v", err)
	}

	asked := fake.Subscribed

	err := plug.Watch(t.Context(), events, callback, func(wemo.Event) {})
	if !errors.Is(err, wemo.ErrClosed) || fake.Subscriptions() != 0 || fake.Subscribed != asked {
		t.Fatalf("a Watch through a closed listener = %v, %d subscriptions asked for", err, fake.Subscribed-asked)
	}

	dialer := net.Dialer{Timeout: time.Second}

	if _, dialErr := dialer.DialContext(t.Context(), "tcp", events.Addr().String()); dialErr == nil {
		t.Fatal("a closed listener still listens")
	}
}

// TestWatchRefusesWhatItCannotUse: no listener, no handler, and a callback
// that is no URL are refused, with no subscription made.
func TestWatchRefusesWhatItCannotUse(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	events, callback := listening(t)
	plug := open(t, fake)

	for name, err := range map[string]error{
		"no listener":          plug.Watch(t.Context(), nil, callback, func(wemo.Event) {}),
		"no handler":           plug.Watch(t.Context(), events, callback, nil),
		"a callback not a URL": plug.Watch(t.Context(), events, "127.0.0.1", func(wemo.Event) {}),
	} {
		if !errors.Is(err, wemo.ErrArgument) {
			t.Errorf("%s: Watch = %v", name, err)
		}
	}

	if left := fake.Subscriptions(); left != 0 {
		t.Fatalf("%d subscriptions were made", left)
	}
}

// TestListen: a listener is where it was asked to be, and an address that
// cannot be listened at is an error.
func TestListen(t *testing.T) {
	t.Parallel()

	events, _ := listening(t)

	if at := events.Addr(); at.Addr().String() != "127.0.0.1" || at.Port() == 0 {
		t.Fatalf("Addr = %v", at)
	}

	if _, err := wemo.Listen(t.Context(), events.Addr().String()); err == nil {
		t.Fatal("listening twice at one address was allowed")
	}

	if _, err := wemo.Listen(t.Context(), "not an address"); err == nil {
		t.Fatal("listening at what is no address was allowed")
	}
}
