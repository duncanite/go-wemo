package wemo_test

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	upnp "github.com/forkcloser/go-upnp"

	"github.com/duncanite/go-wemo/wemo"
	"github.com/duncanite/go-wemo/wemo/wemotest"
)

// joinPassphrase is a passphrase, and joinPassword what the fake plug is
// sent for it.
const (
	joinPassphrase = "correct horse battery"
	joinPassword   = "8aZoYhaDijAKwMORA5wATIYQf2gSXYeuuhrJ7LIqFX4=2c15"
)

func open(t *testing.T, plug *wemotest.Plug) *wemo.Plug {
	t.Helper()

	opened, err := wemo.Open(t.Context(), plug.Client(), plug.Host())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	return opened
}

// got is what an answer holds under name.
func got(answer upnp.Arguments, name string) string {
	value, _ := answer.Get(name)

	return value
}

// errNobody is what a client that reaches no one returns.
var errNobody = errors.New("nobody answers there")

// elsewhere is a client for a host that is not there: it reaches no one,
// but for one port when it has a plug, where that plug answers; and it
// keeps what it was asked for. No test asks the host it runs on for the
// ports a Wemo serves on, which anything there may hold.
type elsewhere struct {
	port  string
	plug  *wemotest.Plug
	asked []string
}

func (e *elsewhere) Do(request *http.Request) (*http.Response, error) {
	e.asked = append(e.asked, request.URL.String())

	if e.plug == nil || request.URL.Port() != e.port {
		return nil, errNobody
	}

	// The plug answers at its own address, as the one that was asked.
	sent := request.Clone(request.Context())
	sent.URL.Host = e.plug.Host()
	sent.Host = ""

	response, err := e.plug.Client().Do(sent)
	if err != nil {
		return nil, err
	}

	response.Request = request

	return response, nil
}

// TestOpen: a plug opened by URL and by host and port is the same plug,
// with what its description says; a bare host is probed through a Wemo's
// ports in their order, found on the one that answers and not found when
// none does; and what is no address is refused.
func TestOpen(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	byURL, err := wemo.Open(t.Context(), fake.Client(), fake.Location())
	if err != nil {
		t.Fatalf("Open by URL: %v", err)
	}

	byHost := open(t, fake)

	if byURL.ID() != wemotest.UDN || byHost.ID() != byURL.ID() {
		t.Fatalf("ID = %q by URL, %q by host; want %q", byURL.ID(), byHost.ID(), wemotest.UDN)
	}

	want := wemo.Description{
		URL:              fake.Location(),
		Name:             wemotest.Name,
		Model:            "Socket",
		ModelNumber:      "1.0",
		ModelDescription: "Belkin Plugin Socket 1.0",
		Serial:           wemotest.Serial,
		MAC:              wemotest.MAC,
		Firmware:         wemotest.Firmware,
		Hardware:         "v3",
	}
	if said := byHost.Description(); said != want {
		t.Fatalf("Description = %+v\nwant %+v", said, want)
	}

	if device := byHost.Device(); device.UDN != wemotest.UDN || len(device.Services) != 10 {
		t.Fatalf("Device = %q with %d services", device.UDN, len(device.Services))
	}

	plugs := []*wemo.Plug{byHost}

	if found, nameErr := wemo.ByName(plugs, "kitchen lamp"); nameErr != nil || found != byHost {
		t.Fatalf("ByName = %v, %v", found, nameErr)
	}

	if _, nameErr := wemo.ByName(plugs, "Garage"); !errors.Is(nameErr, wemo.ErrNotFound) {
		t.Fatalf("ByName of a stranger = %v", nameErr)
	}

	// A bare host is probed through the ports a Wemo drifts among, in
	// their order, and is not found when none answers.
	absent := &elsewhere{}

	_, err = wemo.Open(t.Context(), absent, "192.0.2.1")
	if !errors.Is(err, wemo.ErrNotFound) || !errors.Is(err, errNobody) || !strings.Contains(err.Error(), "49159") {
		t.Fatalf("Open of a bare host with nothing on its ports = %v", err)
	}

	tried := make([]string, 0, len(wemo.Ports()))
	for _, port := range wemo.Ports() {
		tried = append(tried, "http://192.0.2.1:"+strconv.Itoa(port)+"/setup.xml")
	}

	if !slices.Equal(absent.asked, tried) {
		t.Fatalf("Open of a bare host asked %v\nwant %v", absent.asked, tried)
	}

	// A plug whose port drifted to the third is found there, after the
	// two before it, and is driven there from then on.
	drifted := &elsewhere{port: "49154", plug: fake}

	moved, err := wemo.Open(t.Context(), drifted, "192.0.2.1")
	if err != nil || moved.ID() != wemotest.UDN ||
		moved.Description().URL != "http://192.0.2.1:49154/setup.xml" || len(drifted.asked) != 3 {
		t.Fatalf("Open of a plug on its third port = %v, %v, after asking %v", moved, err, drifted.asked)
	}

	if on, _, stateErr := moved.State(t.Context()); stateErr != nil || on {
		t.Fatalf("State of a plug on its third port = %v, %v", on, stateErr)
	}

	if _, err = wemo.Open(t.Context(), fake.Client(), "ftp://127.0.0.1/setup.xml"); !errors.Is(err, wemo.ErrArgument) {
		t.Fatalf("Open of what is no http URL = %v", err)
	}

	if _, err = wemo.Open(t.Context(), fake.Client(), fake.URL+"/nothing.xml"); !errors.Is(err, wemo.ErrRefused) {
		t.Fatalf("Open of a URL the plug has nothing at = %v", err)
	}
}

// TestTheVerbs: state, switching with the countdown, toggle, rename,
// signal, reset codes, the auto-update switch, time sync and the firmware
// version, each as the plug declares it.
func TestTheVerbs(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
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

	if on, raw, err := plug.State(t.Context()); err != nil || !on || raw != "1" {
		t.Fatalf("State of a plug that is on = %v %q %v", on, raw, err)
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

	if err := plug.Rename(t.Context(), "  "); !errors.Is(err, wemo.ErrArgument) {
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

	if answer, err := plug.TimeSync(t.Context(), now); err != nil || got(answer, "status") != "success" {
		t.Fatalf("TimeSync = %v %v", answer, err)
	}

	if fake.TimeSynced["UTC"] != strconv.FormatInt(now.Unix(), 10) || fake.TimeSynced["TimeZone"] != "-8.00" ||
		fake.TimeSynced["dst"] != "0" || fake.TimeSynced["DstSupported"] != "1" {
		t.Fatalf("TimeSync sent %v", fake.TimeSynced)
	}

	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}

	summer := time.Date(2026, time.July, 11, 17, 0, 0, 0, zone)

	if _, err = plug.TimeSync(t.Context(), summer); err != nil || fake.TimeSynced["TimeZone"] != "-7.00" ||
		fake.TimeSynced["dst"] != "1" {
		t.Fatalf("TimeSync in daylight saving: %v, sent %v", err, fake.TimeSynced)
	}

	if answer, err := plug.FirmwareVersion(
		t.Context(),
	); err != nil ||
		!strings.HasPrefix(got(answer, "FirmwareVersion"), "FirmwareVersion:"+wemotest.Firmware) {
		t.Fatalf("FirmwareVersion = %v %v", answer, err)
	}
}

// TestCallReachesEverything: any declared action; the arguments passed go
// out in the declared order whatever direction the description claims;
// an unknown action or argument is refused naming what exists.
func TestCallReachesEverything(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	plug := open(t, fake)

	answer, err := plug.Call(t.Context(), "basicevent", "GetMacAddr")
	if err != nil || got(answer, "MacAddr") != wemotest.MAC || got(answer, "SerialNo") != wemotest.Serial {
		t.Fatalf("GetMacAddr = %v %v", answer, err)
	}

	// CountdownEndTime is declared out; passed, it goes out all the same,
	// after the inputs that precede it in the declaration.
	_, err = plug.Call(
		t.Context(),
		"basicevent",
		"SetBinaryState",
		upnp.Argument{Name: "CountdownEndTime", Value: "7"},
		upnp.Argument{Name: "BinaryState", Value: "1"},
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

	_, err = plug.Call(t.Context(), "basicevent", "NoSuchAction")
	if !errors.Is(err, wemo.ErrNotFound) || !strings.Contains(err.Error(), "47 actions") {
		t.Fatalf("an unknown action = %v", err)
	}

	_, err = plug.Call(t.Context(), "basicevent", "SetBinaryState", upnp.Argument{Name: "Bogus", Value: "1"})
	if !errors.Is(err, wemo.ErrArgument) ||
		!strings.Contains(err.Error(), "[BinaryState Duration EndAction UDN]") {
		t.Fatalf("an unknown argument = %v", err)
	}

	if _, err := plug.Call(t.Context(), "nosuch", "X"); !errors.Is(err, wemo.ErrNotFound) {
		t.Fatalf("an unknown service = %v", err)
	}
}

// TestSetup: the scan as a table, the meta information, the passphrase
// encrypted as the app did, and the join: through to a steady status 3;
// through the plug leaving its access point; refused for a mode the plug
// cannot join, a newer firmware's encryption, and a short key.
func TestSetup(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	plug := open(t, fake)

	points, err := plug.Networks(t.Context())
	if err != nil || len(points) != 4 || points[0].SSID != "House" || points[0].Channel != "11" ||
		points[0].Auth != "WPA2PSK/AES" ||
		points[2].Auth != "UNKNOW/AES" {
		t.Fatalf("Networks = %+v %v", points, err)
	}

	meta, err := plug.MetaInfo(t.Context())
	if err != nil || meta.MAC != wemotest.MAC || meta.Serial != wemotest.Serial {
		t.Fatalf("MetaInfo = %+v %v", meta, err)
	}

	// The vector: keydata 000C29 + 221234K0100001 + 000001, computed with
	// openssl enc -aes-128-cbc under MD5(keydata+salt) and the first
	// sixteen bytes as the vector, base64, then the two hex lengths.
	encrypted, err := wemo.EncryptPassphrase(joinPassphrase, meta)
	if err != nil || encrypted != joinPassword {
		t.Fatalf("EncryptPassphrase = %q %v", encrypted, err)
	}

	if _, emptyErr := wemo.EncryptPassphrase("", meta); !errors.Is(emptyErr, wemo.ErrArgument) {
		t.Fatal("an empty passphrase was encrypted")
	}

	quick := wemo.SetupOptions{Timeout: 20 * time.Second, Poll: 10 * time.Millisecond}

	started := time.Now()

	if joinErr := plug.Join(t.Context(), "House", joinPassphrase, quick); joinErr != nil {
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

	if modeErr := plug.Join(t.Context(), "Transition", "x", quick); !errors.Is(modeErr, wemo.ErrArgument) {
		t.Fatalf("joining an UNKNOW network = %v", modeErr)
	}

	if seeErr := plug.Join(t.Context(), "Elsewhere", "x", quick); !errors.Is(seeErr, wemo.ErrNotFound) {
		t.Fatalf("joining a network the plug does not see = %v", seeErr)
	}

	fake.Statuses = []string{"2"}
	fake.Connects = nil

	err = plug.Join(t.Context(), "House", "short", quick)
	if err == nil || !errors.Is(err, wemo.ErrArgument) || !strings.Contains(err.Error(), "eight") {
		t.Fatalf("a short key = %v", err)
	}
}

// TestJoinOnAPlugThatLeaves: the plug takes the first request and goes;
// the second gets no answer, and that is ErrLeft, the join. A plug that
// hangs up on the first request took nothing, and that is not ErrLeft.
func TestJoinOnAPlugThatLeaves(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	fake.Leaves = true

	plug := open(t, fake)

	err := plug.Join(t.Context(), "House", joinPassphrase, wemo.SetupOptions{Poll: 10 * time.Millisecond})
	if !errors.Is(err, wemo.ErrLeft) {
		t.Fatalf("Join on a plug that leaves = %v", err)
	}

	dead := wemotest.New()
	t.Cleanup(dead.Close)

	dead.Dead = true

	err = open(
		t,
		dead,
	).Join(t.Context(), "House", joinPassphrase, wemo.SetupOptions{Poll: 10 * time.Millisecond})
	if err == nil || errors.Is(err, wemo.ErrLeft) || errors.Is(err, wemo.ErrRefused) {
		t.Fatalf("Join on a plug dead from the first request = %v, want the client's error and not ErrLeft", err)
	}
}

// TestJoinOnAPlugThatSaysNo: a plug that answers the status with a fault,
// or with what is no answer, is still there: that is its word, and not
// ErrLeft.
func TestJoinOnAPlugThatSaysNo(t *testing.T) {
	t.Parallel()

	quick := wemo.SetupOptions{Poll: 10 * time.Millisecond}

	faulting := wemotest.New()
	t.Cleanup(faulting.Close)

	faulting.Faults = map[string]string{"WiFiSetup.GetNetworkStatus": "501"}

	err := open(t, faulting).Join(t.Context(), "House", joinPassphrase, quick)
	if !errors.Is(err, wemo.ErrRefused) || errors.Is(err, wemo.ErrLeft) {
		t.Fatalf("Join on a plug that faults at its status = %v, want ErrRefused and not ErrLeft", err)
	}

	garbling := wemotest.New()
	t.Cleanup(garbling.Close)

	garbling.Garbled = map[string]bool{"WiFiSetup.GetNetworkStatus": true}

	err = open(t, garbling).Join(t.Context(), "House", joinPassphrase, quick)
	if !errors.Is(err, wemo.ErrAnswer) || errors.Is(err, wemo.ErrLeft) {
		t.Fatalf("Join on a plug that garbles its status = %v, want ErrAnswer and not ErrLeft", err)
	}

	// The second request refused is a refusal too, where no answer to it
	// is the plug leaving.
	twice := wemotest.New()
	t.Cleanup(twice.Close)

	plug := open(t, twice)

	twice.Faults = map[string]string{"WiFiSetup.ConnectHomeNetwork": "501"}

	if err = plug.Join(t.Context(), "House", joinPassphrase, quick); !errors.Is(err, wemo.ErrRefused) ||
		errors.Is(err, wemo.ErrLeft) {
		t.Fatalf("Join on a plug that faults at the request = %v, want ErrRefused and not ErrLeft", err)
	}
}

// TestJoinGivenUp: a join whose context ends while the plug still connects
// ends with the context's error, and is not taken for the plug leaving.
func TestJoinGivenUp(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	fake.Statuses = []string{"0"}

	plug := open(t, fake)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	err := plug.Join(ctx, "House", joinPassphrase, wemo.SetupOptions{Poll: 10 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, wemo.ErrLeft) || len(fake.Connects) != 2 {
		t.Fatalf(
			"Join given up = %v after %d requests, want the context's error and not ErrLeft",
			err,
			len(fake.Connects),
		)
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

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	fake.Extra = "<rtos>1</rtos>"

	plug := open(t, fake)

	quick := wemo.SetupOptions{Poll: 10 * time.Millisecond}

	if err := plug.Join(t.Context(), "House", "x", quick); !errors.Is(err, wemo.ErrUnsupported) {
		t.Fatalf("a newer plug on a protected network = %v", err)
	}

	if err := plug.Join(t.Context(), "Cafe", "", quick); err != nil {
		t.Fatalf("a newer plug on an open network = %v", err)
	}

	if fake.Connects[0]["auth"] != "OPEN" || fake.Connects[0]["password"] != "" {
		t.Fatalf("open network sent %v", fake.Connects[0])
	}

	// With iot beside rtos, the plug takes the original encryption.
	both := wemotest.New()
	t.Cleanup(both.Close)

	both.Extra = "<rtos>1</rtos><iot>1</iot>"

	if err := open(t, both).Join(t.Context(), "House", joinPassphrase, quick); err != nil ||
		both.Connects[0]["password"] != joinPassword {
		t.Fatalf("a plug with rtos and iot on a protected network = %v, sent %v", err, both.Connects)
	}
}
