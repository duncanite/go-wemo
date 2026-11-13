package upnp_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-wemo/upnp"
	"github.com/duncanite/go-wemo/upnp/upnptest"
)

func describe(t *testing.T, plug *upnptest.Plug) *upnp.Device {
	t.Helper()

	device, err := upnp.Describe(t.Context(), plug.Client(), plug.Location())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	return device
}

// TestDescribeReadsThePlug: the description's fields, the services with
// their URLs resolved against the description's, and a service found by
// its short name.
func TestDescribeReadsThePlug(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	device := describe(t, plug)

	if device.FriendlyName != upnptest.Name || device.Model != "Socket" || device.Serial != upnptest.Serial ||
		device.MAC != upnptest.MAC || device.Firmware != upnptest.Firmware || device.UDN != upnptest.UDN ||
		device.Hardware != "v3" {
		t.Fatalf("device = %+v", device)
	}

	if len(device.Services) != 10 {
		t.Fatalf("services = %d, want the plug's ten", len(device.Services))
	}

	basic, err := device.Service("basicevent")
	if err != nil {
		t.Fatal(err)
	}

	if _, missing := device.Service("nosuch"); !errors.Is(missing, fault.ErrNotFound) {
		t.Fatalf("a missing service = %v", missing)
	}

	if basic.Type != "urn:Belkin:service:basicevent:1" ||
		basic.Control().String() != plug.URL+"/upnp/control/basicevent1" ||
		basic.Events().String() != plug.URL+"/upnp/event/basicevent1" ||
		basic.Document().String() != plug.URL+"/eventservice.xml" {
		t.Fatalf("service = %+v control %s", basic, basic.Control())
	}

	description, err := basic.Describe(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if len(description.Actions) != 47 {
		t.Fatalf("basicevent declares %d actions, want 47", len(description.Actions))
	}

	set, err := description.Action("SetBinaryState")
	if err != nil {
		t.Fatal(err)
	}

	if len(set.In()) != 4 || set.In()[0].Name != "BinaryState" {
		t.Fatalf("SetBinaryState's inputs = %+v", set.In())
	}

	if _, ok := set.Argument("CountdownEndTime"); !ok {
		t.Fatal("an output argument is not found by name")
	}
}

// TestDescribeRefusesWhatIsNotADevice: a 404 and a document without a UDN
// are refused with the URL.
func TestDescribeRefusesWhatIsNotADevice(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	if _, err := upnp.Describe(
		t.Context(),
		plug.Client(),
		plug.URL+"/nothing.xml",
	); !errors.Is(
		err,
		fault.ErrUnacceptableResponse,
	) {
		t.Fatalf("a 404 = %v", err)
	}

	if _, err := upnp.Describe(
		t.Context(),
		plug.Client(),
		plug.URL+"/eventservice.xml",
	); !errors.Is(
		err,
		fault.ErrUnacceptableResponse,
	) {
		t.Fatalf("a service description as a device = %v", err)
	}
}

// TestCallSendsAndReadsWhole: the arguments go out in the order given,
// escaped, under the action in the service's namespace with the SOAPACTION
// header; the answer comes back whole.
func TestCallSendsAndReadsWhole(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	basic, err := describe(t, plug).Service("basicevent")
	if err != nil {
		t.Fatal(err)
	}

	answer, err := basic.Call(
		t.Context(),
		"ChangeFriendlyName",
		upnp.Arguments{"FriendlyName": "Lamp & <Co>"},
		"FriendlyName",
	)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}

	if answer["FriendlyName"] != "Lamp & <Co>" || plug.FriendlyName != "Lamp & <Co>" {
		t.Fatalf("answer = %v, plug %q", answer, plug.FriendlyName)
	}

	body := string(plug.Bodies[0])
	if !strings.Contains(
		body,
		`<u:ChangeFriendlyName xmlns:u="urn:Belkin:service:basicevent:1"><FriendlyName>Lamp &amp; &lt;Co&gt;</FriendlyName>`,
	) {
		t.Fatalf("body = %s", body)
	}

	answer, err = basic.Call(
		t.Context(),
		"SetBinaryState",
		upnp.Arguments{"BinaryState": "1", "Duration": "5"},
		"BinaryState",
		"Duration",
	)
	if err != nil {
		t.Fatal(err)
	}

	if answer["BinaryState"] != "1" || answer["CountdownEndTime"] != "0" || answer["deviceCurrentTime"] == "" {
		t.Fatalf("the answer is not read whole: %v", answer)
	}

	if body := string(plug.Bodies[1]); !strings.Contains(body, "<BinaryState>1</BinaryState><Duration>5</Duration>") {
		t.Fatalf("order lost: %s", body)
	}
}

// TestCallReadsFaults: a 500 with a UPnP error, and a 200 carrying a Fault
// body as the plug sends for an action it never built, are both faults
// with the plug's code and text, and unacceptable responses.
func TestCallReadsFaults(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	device := describe(t, plug)

	basic, err := device.Service("basicevent")
	if err != nil {
		t.Fatal(err)
	}

	_, err = basic.Call(t.Context(), "GetGPIO", nil)

	var soapFault *upnp.FaultError
	if !errors.As(err, &soapFault) || soapFault.Code != "402" || soapFault.Description != "Invalid Args" ||
		soapFault.Status != 500 {
		t.Fatalf("GetGPIO = %v", err)
	}

	if !errors.Is(err, fault.ErrUnacceptableResponse) || !strings.Contains(err.Error(), "402 Invalid Args (HTTP 500)") {
		t.Fatalf("GetGPIO = %v", err)
	}

	timesync, err := device.Service("timesync")
	if err != nil {
		t.Fatal(err)
	}

	_, err = timesync.Call(t.Context(), "GetTime", nil)
	if !errors.As(err, &soapFault) || soapFault.Code != "501" || soapFault.Status != 200 {
		t.Fatalf("GetTime, a fault inside a 200 = %v", err)
	}
}

// TestEventing: a subscription is granted what the plug grants, the first
// NOTIFY carries the current state, a switch arrives as an event, a renewal
// extends, and an unsubscribe ends it on both sides.
func TestEventing(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	plug.LeaseGranted = 5 * time.Minute

	basic, err := describe(t, plug).Service("basicevent")
	if err != nil {
		t.Fatal(err)
	}

	listener, err := upnp.Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close(t.Context()) })

	sub, err := basic.Subscribe(t.Context(), listener, 30*time.Minute)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if !strings.HasPrefix(sub.SID, "uuid:") || time.Until(sub.Expires) > 6*time.Minute ||
		time.Until(sub.Expires) < 4*time.Minute {
		t.Fatalf("subscription = %+v, want the granted five minutes", sub)
	}

	first := next(t, plug, listener)
	if first.SID != sub.SID || first.Properties["BinaryState"] != "0" ||
		first.Properties["TimeZoneNotification"] != "005" {
		t.Fatalf("first event = %+v", first)
	}

	plug.Switch(t.Context(), true)

	second := next(t, plug, listener)
	if second.Properties["BinaryState"] != "1" || second.Seq != first.Seq+1 {
		t.Fatalf("second event = %+v", second)
	}

	if err := sub.Renew(t.Context(), 30*time.Minute); err != nil {
		t.Fatalf("Renew: %v", err)
	}

	if err := sub.Unsubscribe(t.Context()); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}

	if plug.Subscriptions() != 0 {
		t.Fatal("the plug still holds the subscription")
	}

	if err := sub.Unsubscribe(t.Context()); !errors.Is(err, fault.ErrUnacceptableResponse) {
		t.Fatalf("unsubscribing twice = %v", err)
	}
}

// eventWait bounds the wait for one event. Generous: a loaded runner has
// taken seconds over a loopback round trip, and the test is about the
// event's order and content, not its latency.
const eventWait = 30 * time.Second

// next is the listener's next event, or the test fails saying what the
// plug could not deliver.
func next(t *testing.T, plug *upnptest.Plug, listener *upnp.Listener) upnp.Event {
	t.Helper()

	select {
	case event := <-listener.Events():
		return event
	case <-time.After(eventWait):
		t.Fatalf("no event within %s; undelivered: %v", eventWait, plug.Undelivered())

		return upnp.Event{}
	}
}

// TestEventingOutrunsTheAnswer: the plug's first NOTIFY reaches the
// listener before Subscribe has read the SUBSCRIBE answer and its SID.
// The listener keeps it, since the plug retries nothing. In that same
// window a NOTIFY under a SID the plug never grants is taken and never
// reaches the stream, and one on another path is refused; so is one for
// a subscription nobody is making. The window holds no more events than
// the stream buffers, 64: past that a NOTIFY is refused.
func TestEventingOutrunsTheAnswer(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	plug.EarlyNotify = true
	plug.Stray = true
	plug.Flood = 100

	basic, err := describe(t, plug).Service("basicevent")
	if err != nil {
		t.Fatal(err)
	}

	listener, err := upnp.Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close(t.Context()) })

	sub, err := basic.Subscribe(t.Context(), listener, 30*time.Minute)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	first := next(t, plug, listener)
	if first.SID != sub.SID || first.Properties["BinaryState"] != "0" {
		t.Fatalf("first event = %+v", first)
	}

	if undelivered := plug.Undelivered(); len(undelivered) != 0 {
		t.Fatalf("the plug could not deliver: %v", undelivered)
	}

	if len(plug.Strays) != 2 || plug.Strays[0] != http.StatusOK || plug.Strays[1] != http.StatusPreconditionFailed {
		t.Fatalf("the strays in the window got %v, want 200 on the path and 412 off it", plug.Strays)
	}

	// One stray and the plug's own event were held when the flood began.
	if plug.Flooded != 62 {
		t.Fatalf("the listener took %d of the flood's 100, want 62: 64 held in all", plug.Flooded)
	}

	// The stray was sent before the first event: had it reached the
	// stream it would have been first, and it is not next either.
	plug.Switch(t.Context(), true)

	second := next(t, plug, listener)
	if second.SID != sub.SID || second.Properties["BinaryState"] != "1" {
		t.Fatalf("second event = %+v", second)
	}

	stray, err := http.NewRequestWithContext(t.Context(), "NOTIFY", "http://"+listener.Address()+"/basicevent",
		strings.NewReader(`<e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0"></e:propertyset>`))
	if err != nil {
		t.Fatal(err)
	}

	stray.Header.Set("Sid", "uuid:nobody-asked")

	resp, err := http.DefaultClient.Do(stray)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("a stray NOTIFY got %s", resp.Status)
	}

	if err := sub.Unsubscribe(t.Context()); err != nil {
		t.Fatalf("Unsubscribe: %v", err)
	}
}
