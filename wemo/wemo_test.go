package wemo_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-wemo/ssdp"
	"github.com/duncanite/go-wemo/upnp"
	"github.com/duncanite/go-wemo/upnp/upnptest"
	"github.com/duncanite/go-wemo/wemo"
)

func open(t *testing.T, plug *upnptest.Plug) *wemo.Plug {
	t.Helper()

	opened, err := wemo.Open(t.Context(), plug.Client(), plug.Host())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	return opened
}

// TestDiscoverAndOpen: discovery through a search, and opening by URL and
// by host and port; a bare host nobody answers on is not found, with the
// last port's error.
func TestDiscoverAndOpen(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	plugs, failures, err := wemo.Discover(
		t.Context(),
		plug.Client(),
		ssdp.Options{Target: plug.ServeSSDP(), Wait: time.Second},
	)
	if err != nil || len(failures) != 0 || len(plugs) != 1 || plugs[0].FriendlyName != upnptest.Name {
		t.Fatalf("Discover = %v, %v, %v", plugs, failures, err)
	}

	byURL, urlErr := wemo.Open(t.Context(), plug.Client(), plug.Location())
	if urlErr != nil || byURL.Serial != upnptest.Serial {
		t.Fatalf("Open by URL = %v, %v", byURL, urlErr)
	}

	if found, nameErr := wemo.ByName(plugs, "kitchen lamp"); nameErr != nil || found != plugs[0] {
		t.Fatalf("ByName = %v, %v", found, nameErr)
	}

	if _, nameErr := wemo.ByName(plugs, "Garage"); !errors.Is(nameErr, wemo.ErrNoSuchPlug) {
		t.Fatalf("ByName of a stranger = %v", nameErr)
	}

	_, err = wemo.Open(t.Context(), plug.Client(), "127.0.0.1")
	if !errors.Is(err, fault.ErrNotFound) || !strings.Contains(err.Error(), "49159") {
		t.Fatalf("Open of a bare host with nothing on its ports = %v", err)
	}
}

// TestTheVerbs: state, switching with the countdown, toggle, rename,
// signal, reset codes, the auto-update switch, time sync and the firmware
// version, each as the plug declares it.
func TestTheVerbs(t *testing.T) {
	t.Parallel()

	fake := upnptest.New()
	t.Cleanup(fake.Close)

	plug := open(t, fake)

	if on, raw, err := plug.State(t.Context()); err != nil || on || raw != "0" {
		t.Fatalf("State = %v %q %v", on, raw, err)
	}

	if err := plug.SetState(t.Context(), true, 90*time.Second); err != nil {
		t.Fatal(err)
	}

	if !fake.On ||
		!strings.Contains(
			string(fake.Bodies[len(fake.Bodies)-1]),
			"<BinaryState>1</BinaryState><Duration>90</Duration><EndAction>0</EndAction>",
		) {
		t.Fatalf("SetState with a countdown sent %s", fake.Bodies[len(fake.Bodies)-1])
	}

	if on, err := plug.Toggle(t.Context()); err != nil || on || fake.On {
		t.Fatalf("Toggle = %v %v, plug %v", on, err, fake.On)
	}

	if err := plug.Rename(t.Context(), "Hall"); err != nil || fake.FriendlyName != "Hall" {
		t.Fatalf("Rename: %v, plug %q", err, fake.FriendlyName)
	}

	if name, err := plug.Name(t.Context()); err != nil || name != "Hall" {
		t.Fatalf("Name = %q %v", name, err)
	}

	if err := plug.Rename(t.Context(), "  "); !errors.Is(err, fault.ErrInvalidArgument) {
		t.Fatalf("Rename to nothing = %v", err)
	}

	fake.Signal = 81

	if signal, err := plug.Signal(t.Context()); err != nil || signal != 81 {
		t.Fatalf("Signal = %d %v", signal, err)
	}

	for kind, code := range map[wemo.ResetKind]string{wemo.ResetData: "1", wemo.ResetAll: "2", wemo.ResetWiFi: "5"} {
		if answer, err := plug.Reset(t.Context(), kind); err != nil || answer != "success" {
			t.Fatalf("Reset %d = %q %v", kind, answer, err)
		}

		if fake.Resets[len(fake.Resets)-1] != code {
			t.Fatalf("Reset %d sent %q", kind, fake.Resets[len(fake.Resets)-1])
		}
	}

	if err := plug.AutoUpdate(t.Context(), false); err != nil || fake.AutoUpdate == nil || *fake.AutoUpdate {
		t.Fatalf("AutoUpdate off: %v, plug %v", err, fake.AutoUpdate)
	}

	now := time.Date(2026, time.November, 11, 17, 0, 0, 0, time.FixedZone("PST", -8*3600))

	if answer, err := plug.TimeSync(t.Context(), now); err != nil || answer["status"] != "success" {
		t.Fatalf("TimeSync = %v %v", answer, err)
	}

	if fake.TimeSynced["UTC"] != strconv.FormatInt(now.Unix(), 10) || fake.TimeSynced["TimeZone"] != "-8.00" ||
		fake.TimeSynced["dst"] != "0" || fake.TimeSynced["DstSupported"] != "1" {
		t.Fatalf("TimeSync sent %v", fake.TimeSynced)
	}

	if answer, err := plug.FirmwareVersion(
		t.Context(),
	); err != nil ||
		!strings.HasPrefix(answer["FirmwareVersion"], "FirmwareVersion:"+upnptest.Firmware) {
		t.Fatalf("FirmwareVersion = %v %v", answer, err)
	}
}

// TestCallReachesEverything: any declared action; the arguments passed go
// out in the declared order whatever direction the description claims;
// an unknown action or argument is refused naming what exists.
func TestCallReachesEverything(t *testing.T) {
	t.Parallel()

	fake := upnptest.New()
	t.Cleanup(fake.Close)

	plug := open(t, fake)

	answer, err := plug.Call(t.Context(), "basicevent", "GetMacAddr", nil)
	if err != nil || answer["MacAddr"] != upnptest.MAC || answer["SerialNo"] != upnptest.Serial {
		t.Fatalf("GetMacAddr = %v %v", answer, err)
	}

	// CountdownEndTime is declared out; passed, it goes out all the same,
	// after the inputs that precede it in the declaration.
	_, err = plug.Call(
		t.Context(),
		"basicevent",
		"SetBinaryState",
		upnp.Arguments{"CountdownEndTime": "7", "BinaryState": "1"},
	)
	if err != nil {
		t.Fatal(err)
	}

	if body := string(
		fake.Bodies[len(fake.Bodies)-1],
	); !strings.Contains(
		body,
		"<BinaryState>1</BinaryState><CountdownEndTime>7</CountdownEndTime>",
	) {
		t.Fatalf("an out argument was dropped or misplaced: %s", body)
	}

	_, err = plug.Call(t.Context(), "basicevent", "NoSuchAction", nil)
	if !errors.Is(err, fault.ErrNotFound) || !strings.Contains(err.Error(), "47 actions") {
		t.Fatalf("an unknown action = %v", err)
	}

	_, err = plug.Call(t.Context(), "basicevent", "SetBinaryState", upnp.Arguments{"Bogus": "1"})
	if !errors.Is(err, fault.ErrInvalidArgument) ||
		!strings.Contains(err.Error(), "[BinaryState Duration EndAction UDN]") {
		t.Fatalf("an unknown argument = %v", err)
	}

	if _, err := plug.Call(t.Context(), "nosuch", "X", nil); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("an unknown service = %v", err)
	}
}

// TestSetup: the scan as a table, the meta information, the passphrase
// encrypted as the app did, and the join: through to a steady status 3;
// through the plug leaving its access point; refused for a mode the plug
// cannot join, a newer firmware's encryption, and a short key.
func TestSetup(t *testing.T) {
	t.Parallel()

	fake := upnptest.New()
	t.Cleanup(fake.Close)

	plug := open(t, fake)

	points, err := plug.Networks(t.Context())
	if err != nil || len(points) != 4 || points[0].SSID != "House" || points[0].Channel != "11" ||
		points[0].Auth != "WPA2PSK/AES" ||
		points[2].Auth != "UNKNOW/AES" {
		t.Fatalf("Networks = %+v %v", points, err)
	}

	meta, err := plug.MetaInfo(t.Context())
	if err != nil || meta.MAC != upnptest.MAC || meta.Serial != upnptest.Serial {
		t.Fatalf("MetaInfo = %+v %v", meta, err)
	}

	// The vector: keydata 000C29 + 221234K0100001 + 000001, computed with
	// openssl enc -aes-128-cbc under MD5(keydata+salt) and the first
	// sixteen bytes as the vector, base64, then the two hex lengths.
	encrypted, err := wemo.EncryptPassphrase("correct horse battery", meta)
	if err != nil || encrypted != "8aZoYhaDijAKwMORA5wATIYQf2gSXYeuuhrJ7LIqFX4=2c15" {
		t.Fatalf("EncryptPassphrase = %q %v", encrypted, err)
	}

	if _, emptyErr := wemo.EncryptPassphrase("", meta); !errors.Is(emptyErr, fault.ErrInvalidArgument) {
		t.Fatal("an empty passphrase was encrypted")
	}

	quick := wemo.SetupOptions{Timeout: 20 * time.Second, Poll: 10 * time.Millisecond}

	started := time.Now()

	if joinErr := plug.Join(t.Context(), "House", "correct horse battery", quick); joinErr != nil {
		t.Fatalf("Join: %v", joinErr)
	}

	// A plug steady at 3 is joined after three polls, not at the timeout.
	if polls := count(fake.Calls, "WiFiSetup.GetNetworkStatus"); polls > 10 || time.Since(started) > 5*time.Second {
		t.Fatalf("Join polled %d times over %s; a steady 3 ends it early", polls, time.Since(started))
	}

	if len(fake.Connects) != 2 || fake.Connects[0]["auth"] != "WPA2PSK" || fake.Connects[0]["encrypt"] != "AES" ||
		fake.Connects[0]["channel"] != "11" || fake.Connects[0]["password"] != encrypted {
		t.Fatalf("ConnectHomeNetwork sent %v", fake.Connects)
	}

	if modeErr := plug.Join(t.Context(), "Transition", "x", quick); !errors.Is(modeErr, fault.ErrInvalidArgument) {
		t.Fatalf("joining an UNKNOW network = %v", modeErr)
	}

	if seeErr := plug.Join(t.Context(), "Elsewhere", "x", quick); !errors.Is(seeErr, fault.ErrNotFound) {
		t.Fatalf("joining a network the plug does not see = %v", seeErr)
	}

	fake.Statuses = []string{"2"}
	fake.Connects = nil

	err = plug.Join(t.Context(), "House", "short", quick)
	if err == nil || !errors.Is(err, fault.ErrInvalidArgument) || !strings.Contains(err.Error(), "eight") {
		t.Fatalf("a short key = %v", err)
	}
}

// TestJoinOnAPlugThatLeaves: the plug takes the first request and goes;
// the second gets no answer, and that is ErrLeft, the join. A plug that
// hangs up on the first request took nothing, and that is not ErrLeft.
func TestJoinOnAPlugThatLeaves(t *testing.T) {
	t.Parallel()

	fake := upnptest.New()
	t.Cleanup(fake.Close)

	fake.Leaves = true

	plug := open(t, fake)

	err := plug.Join(t.Context(), "House", "correct horse battery", wemo.SetupOptions{Poll: 10 * time.Millisecond})
	if !errors.Is(err, wemo.ErrLeft) {
		t.Fatalf("Join on a plug that leaves = %v", err)
	}

	dead := upnptest.New()
	t.Cleanup(dead.Close)

	dead.Dead = true

	err = open(
		t,
		dead,
	).Join(t.Context(), "House", "correct horse battery", wemo.SetupOptions{Poll: 10 * time.Millisecond})
	if err == nil || errors.Is(err, wemo.ErrLeft) || !errors.Is(err, fault.ErrNetworkCommunication) {
		t.Fatalf("Join on a plug dead from the first request = %v, want a network error and not ErrLeft", err)
	}
}

// count is how many of calls are name.
func count(calls []string, name string) int {
	total := 0

	for _, call := range calls {
		if call == name {
			total++
		}
	}

	return total
}

// TestJoinRefusesTheNewerEncryption: a description with rtos and no iot
// is a plug whose passphrase encryption this does not implement; on an
// open network, where nothing is encrypted, it joins all the same.
func TestJoinRefusesTheNewerEncryption(t *testing.T) {
	t.Parallel()

	fake := upnptest.New()
	t.Cleanup(fake.Close)

	fake.Extra = "<rtos>1</rtos>"

	plug := open(t, fake)
	if plug.RTOS != "1" {
		t.Fatalf("rtos not read: %+v", plug.Device)
	}

	quick := wemo.SetupOptions{Poll: 10 * time.Millisecond}

	if err := plug.Join(t.Context(), "House", "x", quick); !errors.Is(err, fault.ErrNotImplemented) {
		t.Fatalf("a newer plug on a protected network = %v", err)
	}

	if err := plug.Join(t.Context(), "Cafe", "", quick); err != nil {
		t.Fatalf("a newer plug on an open network = %v", err)
	}

	if fake.Connects[0]["auth"] != "OPEN" || fake.Connects[0]["password"] != "" {
		t.Fatalf("open network sent %v", fake.Connects[0])
	}
}

// TestWatch: a subscription through the plug delivers the first event.
func TestWatch(t *testing.T) {
	t.Parallel()

	fake := upnptest.New()
	t.Cleanup(fake.Close)

	plug := open(t, fake)

	listener, err := upnp.Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close(t.Context()) })

	sub, err := plug.Watch(t.Context(), listener, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// Generous: a loaded runner has taken seconds over a loopback round
	// trip, and the test is about the event, not its latency.
	select {
	case event := <-listener.Events():
		if event.SID != sub.SID || event.Properties["BinaryState"] != "0" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("no first event within 30s; undelivered: %v", fake.Undelivered())
	}
}
