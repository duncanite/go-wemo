package upnp

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mycophonic/primordium/fault"
)

// DefaultLease is the subscription length asked of the device; a Wemo
// grants what is asked up to its own ceiling, and the renewal runs ahead
// of whatever it granted.
const DefaultLease = 30 * time.Minute

// Event is one NOTIFY the device sent: the subscription it belongs to, its
// sequence number, and every property in it.
type Event struct {
	SID        string
	Seq        uint64
	Properties Arguments
	Received   time.Time
}

// Listener is the HTTP server the device notifies. One listener serves any
// number of subscriptions; the device addresses each by its callback path.
type Listener struct {
	server   *http.Server
	address  string
	events   chan Event
	mu       sync.Mutex
	accepted map[string]struct{}
	// pending counts the SUBSCRIBEs in flight per callback path. The
	// device sends its first NOTIFY as soon as it has answered, and that
	// NOTIFY can reach this server before Subscribe has read the answer
	// and learned the SID; refusing it then loses the first event for
	// good, since a device retries nothing. Such a NOTIFY is taken and
	// its event held, per path, until the answer says whose it is: the
	// stream carries granted SIDs only. A path holds no more than the
	// stream's own buffer: past it a NOTIFY is refused unread, so a
	// device slow to answer cannot make the listener keep whatever is
	// sent to the path meanwhile.
	pending map[string]int
	held    map[string][]Event
}

// Listen opens a listener on addr, "host:port", where host is the address
// the device will reach us at: on a host with several addresses it has to
// be the one on the device's network, since the callback is a plain HTTP
// URL the device dials. Port 0 picks a free one.
func Listen(ctx context.Context, addr string) (*Listener, error) {
	socket, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: event listener on %s: %w", fault.ErrNetworkCommunication, addr, err)
	}

	listener := &Listener{
		address:  socket.Addr().String(),
		events:   make(chan Event, eventBuffer),
		accepted: make(map[string]struct{}),
		pending:  make(map[string]int),
		held:     make(map[string][]Event),
	}

	listener.server = &http.Server{
		Handler:           http.HandlerFunc(listener.notify),
		ReadHeaderTimeout: headerTimeout,
	}

	go func() {
		if err := listener.server.Serve(socket); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "event listener stopped", "err", err)
		}
	}()

	return listener, nil
}

const (
	eventBuffer   = 64
	headerTimeout = 10 * time.Second
)

// Events is the stream of notifications, every subscription's.
func (l *Listener) Events() <-chan Event {
	return l.events
}

// Address is the host and port the listener is reached at.
func (l *Listener) Address() string {
	return l.address
}

// Close stops the listener. Subscriptions are the caller's to end first.
func (l *Listener) Close(ctx context.Context) error {
	if err := l.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("%w: closing the event listener: %w", fault.ErrNetworkCommunication, err)
	}

	return nil
}

// notify is the device's NOTIFY: a property set for a subscription this
// listener holds, answered 200 and emitted; one on a path a SUBSCRIBE is
// in flight on, answered 200 and held until the answer; anything else
// answered as GENA says.
func (l *Listener) notify(writer http.ResponseWriter, req *http.Request) {
	if req.Method != "NOTIFY" {
		http.Error(writer, "NOTIFY only", http.StatusMethodNotAllowed)

		return
	}

	sid := req.Header.Get("Sid")
	path := req.URL.Path

	l.mu.Lock()
	_, known := l.accepted[sid]
	known = known || l.holds(path)
	l.mu.Unlock()

	if !known {
		http.Error(writer, "no such subscription", http.StatusPreconditionFailed)

		return
	}

	data, err := io.ReadAll(io.LimitReader(req.Body, maxDocument))
	if err != nil {
		http.Error(writer, "unreadable", http.StatusBadRequest)

		return
	}

	properties, err := parsePropertySet(data)
	if err != nil {
		http.Error(writer, "not a property set", http.StatusBadRequest)

		return
	}

	seq, _ := strconv.ParseUint(req.Header.Get("Seq"), 10, 64)
	event := Event{SID: sid, Seq: seq, Properties: properties, Received: time.Now()}

	// Decided again under the lock the answer takes: the SUBSCRIBE may
	// have been answered while the body was read.
	l.mu.Lock()
	defer l.mu.Unlock()

	switch _, accepted := l.accepted[sid]; {
	case accepted:
		l.emit(req.Context(), event)
	case l.holds(path):
		l.held[path] = append(l.held[path], event)
	default:
		http.Error(writer, "no such subscription", http.StatusPreconditionFailed)

		return
	}

	writer.WriteHeader(http.StatusOK)
}

// holds reports whether a NOTIFY on path would be held: a SUBSCRIBE is in
// flight on it and it holds fewer events than the stream buffers. Called
// with the lock held.
func (l *Listener) holds(path string) bool {
	return l.pending[path] > 0 && len(l.held[path]) < eventBuffer
}

// emit hands an event to the stream, with the lock held so events keep
// the order they were taken in.
func (l *Listener) emit(ctx context.Context, event Event) {
	select {
	case l.events <- event:
	default:
		slog.WarnContext(ctx, "event dropped: nobody is reading them", "sid", event.SID, "seq", event.Seq)
	}
}

// Subscription is one GENA subscription on a service.
type Subscription struct {
	// SID is the identifier the device gave it.
	SID string
	// Expires is when the device's lease ends unless renewed.
	Expires time.Time

	service  *Service
	listener *Listener
	callback string
}

// Subscribe asks the service to notify the listener for lease, and returns
// the subscription the device granted. The device's first NOTIFY after a
// subscription carries every evented variable's current value.
func (s *Service) Subscribe(ctx context.Context, listener *Listener, lease time.Duration) (*Subscription, error) {
	if lease <= 0 {
		lease = DefaultLease
	}

	callback := "http://" + listener.address + "/" + strings.Trim(s.Name(), "/")

	req, err := http.NewRequestWithContext(ctx, "SUBSCRIBE", s.Events().String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", fault.ErrNetworkCommunication, err)
	}

	req.Header.Set("Callback", "<"+callback+">")
	req.Header.Set("Nt", "upnp:event")
	req.Header.Set("Timeout", "Second-"+strconv.Itoa(int(lease/time.Second)))

	sub := &Subscription{service: s, listener: listener, callback: callback}

	done := listener.expect(ctx, "/"+strings.Trim(s.Name(), "/"))

	err = sub.send(req)
	done(sub.SID, err == nil)

	if err != nil {
		return nil, err
	}

	return sub, nil
}

// expect marks a SUBSCRIBE in flight on a callback path, so a NOTIFY that
// outruns its answer is held; the function it returns ends that: it
// records the SID the device granted, emits the held events that are a
// granted subscription's, and drops the rest once no SUBSCRIBE is in
// flight on the path.
func (l *Listener) expect(ctx context.Context, path string) func(sid string, granted bool) {
	l.mu.Lock()
	l.pending[path]++
	l.mu.Unlock()

	return func(sid string, granted bool) {
		l.mu.Lock()
		defer l.mu.Unlock()

		if granted {
			l.accepted[sid] = struct{}{}
		}

		var waiting []Event

		for _, event := range l.held[path] {
			if _, accepted := l.accepted[event.SID]; accepted {
				l.emit(ctx, event)
			} else {
				waiting = append(waiting, event)
			}
		}

		l.held[path] = waiting

		l.pending[path]--
		if l.pending[path] > 0 {
			return
		}

		for _, event := range waiting {
			slog.WarnContext(ctx, "event dropped: no subscription of ours", "sid", event.SID, "seq", event.Seq)
		}

		delete(l.pending, path)
		delete(l.held, path)
	}
}

// Renew extends the subscription by lease; the device may grant less.
func (sub *Subscription) Renew(ctx context.Context, lease time.Duration) error {
	if lease <= 0 {
		lease = DefaultLease
	}

	req, err := http.NewRequestWithContext(ctx, "SUBSCRIBE", sub.service.Events().String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: %w", fault.ErrNetworkCommunication, err)
	}

	req.Header.Set("Sid", sub.SID)
	req.Header.Set("Timeout", "Second-"+strconv.Itoa(int(lease/time.Second)))

	return sub.send(req)
}

// Unsubscribe ends the subscription on the device and forgets it here.
func (sub *Subscription) Unsubscribe(ctx context.Context) error {
	sub.listener.mu.Lock()
	delete(sub.listener.accepted, sub.SID)
	sub.listener.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, "UNSUBSCRIBE", sub.service.Events().String(), http.NoBody)
	if err != nil {
		return fmt.Errorf("%w: %w", fault.ErrNetworkCommunication, err)
	}

	req.Header.Set("Sid", sub.SID)

	resp, err := sub.service.device.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: unsubscribing %s: %w", fault.ErrNetworkCommunication, sub.SID, err)
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: unsubscribing %s: %s", fault.ErrUnacceptableResponse, sub.SID, resp.Status)
	}

	return nil
}

// send performs a SUBSCRIBE, new or renewal, and reads the SID and the
// lease the device granted.
func (sub *Subscription) send(req *http.Request) error {
	slog.DebugContext(req.Context(), "subscribe", "url", req.URL.String(), "sid", sub.SID, "callback", sub.callback)

	resp, err := sub.service.device.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: subscribing to %s: %w", fault.ErrNetworkCommunication, sub.service.Name(), err)
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: subscribing to %s: %s", fault.ErrUnacceptableResponse, sub.service.Name(), resp.Status)
	}

	if sid := resp.Header.Get("Sid"); sid != "" {
		sub.SID = sid
	}

	if sub.SID == "" {
		return fmt.Errorf("%w: the device granted no SID", fault.ErrUnacceptableResponse)
	}

	sub.Expires = time.Now().Add(granted(resp.Header.Get("Timeout")))

	return nil
}

// granted reads "Second-1800" as a duration; "infinite" or anything odd is
// DefaultLease, so a renewal still happens.
func granted(timeout string) time.Duration {
	_, number, found := strings.Cut(timeout, "-")
	if !found {
		return DefaultLease
	}

	seconds, err := strconv.Atoi(strings.TrimSpace(number))
	if err != nil || seconds <= 0 {
		return DefaultLease
	}

	return time.Duration(seconds) * time.Second
}

// propertySet is a GENA property set: one property per changed variable,
// each holding one element named after it.
type propertySet struct {
	Properties []rawProperty `xml:"property"`
}

type rawProperty struct {
	Inner []byte `xml:",innerxml"`
}

// parsePropertySet reads a GENA property set, every property's one child
// element as name to text.
func parsePropertySet(data []byte) (Arguments, error) {
	var set propertySet
	if err := xml.Unmarshal(data, &set); err != nil {
		return nil, fmt.Errorf("%w: property set: %w", fault.ErrInvalidArgument, err)
	}

	properties := make(Arguments, len(set.Properties))

	for _, property := range set.Properties {
		if name, text, ok := firstElement(property.Inner); ok {
			properties[name] = text
		}
	}

	return properties, nil
}

// firstElement is the name and text of the first element in a fragment.
func firstElement(fragment []byte) (name, text string, ok bool) {
	decoder := xml.NewDecoder(bytes.NewReader(fragment))

	var builder strings.Builder

	for {
		token, err := decoder.Token()
		if err != nil {
			return name, builder.String(), name != ""
		}

		switch elem := token.(type) {
		case xml.StartElement:
			if name == "" {
				name = elem.Name.Local
			}
		case xml.CharData:
			if name != "" {
				builder.Write(elem)
			}
		case xml.EndElement:
			if elem.Name.Local == name {
				return name, builder.String(), true
			}
		}
	}
}
