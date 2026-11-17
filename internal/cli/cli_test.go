package cli_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkcloser/go-upnp/ssdp"
	"github.com/forkcloser/go-upnp/ssdp/ssdptest"

	"github.com/duncanite/go-wemo/internal/cli"
	"github.com/duncanite/go-wemo/wemo"
	"github.com/duncanite/go-wemo/wemo/wemotest"
)

// run runs the command against a plug, on a network with no device.
func run(t *testing.T, args ...string) (string, int) {
	t.Helper()

	return runOn(t, ssdptest.NewHost(), args...)
}

// runOn runs the command on a network.
func runOn(t *testing.T, network ssdp.Network, args ...string) (string, int) {
	t.Helper()

	var out, errs bytes.Buffer

	status := cli.Run(t.Context(), cli.Environment{
		Args: args, Out: &out, Err: &errs, Client: local{}, Network: network,
	})

	return out.String() + errs.String(), status
}

// errNobody is what local returns for a host that is not this one.
var errNobody = errors.New("nobody answers there")

// local is a client that reaches the loopback, where the plugs of
// these tests are, and no other host: a test that wants a host nobody
// answers on names one that is not there, and asks nothing of the ports
// of the host it runs on.
type local struct{}

func (local) Do(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() != "127.0.0.1" {
		return nil, errNobody
	}

	return http.DefaultClient.Do(request)
}

// advertised is a network the plugs are advertised on, each at the loopback
// address its server listens at, and this host on it.
func advertised(plugs ...*wemotest.Plug) *ssdptest.Host {
	lan, host := ssdptest.NewLAN(), ssdptest.NewHost()

	host.Plug(lan, "lo0", 1, netip.MustParsePrefix("127.0.0.1/8"))

	for index, plug := range plugs {
		usn := "uuid:Socket-1_0-" + string(rune('A'+index)) + "::" + wemo.SearchTarget

		lan.Device(netip.MustParseAddr("127.0.0.1"), ssdptest.Advert{
			USN: usn, Type: wemo.SearchTarget, Location: plug.Location(),
		})
	}

	return host
}

// TestTheCommands: every command against the plug by host and port, its
// output as a person reads it.
func TestTheCommands(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	host := plug.Host()

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"get", host}, "Kitchen Lamp: off (0)\n"},
		{[]string{"on", host, "--for", "2m"}, "Kitchen Lamp: on for 2m0s\n"},
		{[]string{"toggle", host}, "Kitchen Lamp: off\n"},
		{[]string{"off", host}, "Kitchen Lamp: off\n"},
		{[]string{"name", host, "Hall"}, "Hall\n"},
		{[]string{"signal", host}, "Hall: 100%\n"},
		{[]string{"time", host}, "status: success\n"},
		{[]string{"firmware", host, "--disable-auto-update"}, "automatic firmware update: off\n"},
		{[]string{"call", host, "basicevent", "GetSignalStrength"}, "SignalStrength: 100\n"},
		{[]string{"reset", host, "--wifi"}, "would forget the network"},
		{[]string{"reset", host, "--wifi", "--yes"}, `Hall: reset answered "success"` + "\n"},
		{[]string{"networks", host}, "House                            ch 11   100 WPA2PSK/AES\n"},
		{[]string{"describe", host}, "services:\n  WiFiSetup        urn:Belkin:service:WiFiSetup:1\n"},
		{[]string{"describe", host, "-s", "timesync"}, "TimeSync(UTC:in, TimeZone:in, dst:in, DstSupported:in)\nGetTime()\n"},
	} {
		out, status := run(t, step.args...)
		if status != cli.ExitOK || !strings.Contains(out, step.want) {
			t.Fatalf("%v: status %d, output:\n%s\nwant %q", step.args, status, out, step.want)
		}
	}

	if plug.AutoUpdate == nil || *plug.AutoUpdate || plug.Resets[0] != "5" ||
		!strings.Contains(string(plug.Bodies[1]), "<Duration>120</Duration>") {
		t.Fatalf("the plug did not get what the commands said: %v %v", plug.AutoUpdate, plug.Resets)
	}
}

// TestTheErrors: a fault from the plug, an unknown action, a bad argument
// and a missing reset kind are status 1 with the reason; --help is not an
// error.
func TestTheErrors(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	host := plug.Host()

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"call", host, "timesync", "GetTime"}, "501"},
		{[]string{"call", host, "basicevent", "Nope"}, `no action "Nope"`},
		{[]string{"call", host, "basicevent", "GetBinaryState", "x"}, `argument "x" is not name=value`},
		{[]string{"reset", host, "--yes"}, "one of --wifi, --data or --all"},
		{[]string{"get", "192.0.2.1"}, "no Wemo answered on 192.0.2.1"},
		{[]string{"setup", "--ssid", "House", "--host", host}, "WEMO_PASSPHRASE is empty"},
	} {
		out, status := run(t, step.args...)
		if status != cli.ExitError || !strings.Contains(out, step.want) {
			t.Fatalf("%v: status %d, output:\n%s\nwant %q", step.args, status, out, step.want)
		}
	}

	if out, status := run(t, "--help"); status != cli.ExitOK ||
		!strings.Contains(out, "Speak to the Wemo plugs") {
		t.Fatalf("--help: %d\n%s", status, out)
	}
}

// TestSetupCommand: the passphrase from the environment, the join through
// a plug that leaves its access point, reported as the outcome it is.
func TestSetupCommand(t *testing.T) {
	plug := wemotest.New()
	t.Cleanup(plug.Close)

	plug.Leaves = true

	t.Setenv("WEMO_PASSPHRASE", "correct horse battery")

	out, status := run(t, "setup", "--ssid", "House", "--host", plug.Host(), "--timeout", "20s")
	if status != cli.ExitOK ||
		!strings.Contains(out, "Kitchen Lamp: took the request and left its access point for House") {
		t.Fatalf("setup: %d\n%s", status, out)
	}

	if len(plug.Connects) != 1 || plug.Connects[0]["ssid"] != "House" || plug.Connects[0]["password"] == "" {
		t.Fatalf("ConnectHomeNetwork sent %v", plug.Connects)
	}
}

// TestWatchCommand: the first event, a switch, then the context ends it;
// and a plug that never notifies is named within the first-event window.
// syncBuffer is an output the test reads while the command still writes
// to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf.Reset()
}

// await fails the test unless out comes to contain text within the bound,
// which is generous: the test is ordered by what was printed, not by how
// long it took.
func await(t *testing.T, out *syncBuffer, text string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !strings.Contains(out.String(), text) {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %q; watch printed:\n%s", text, out.String())
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestWatchCommand(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var out syncBuffer

	done := make(chan int, 1)

	go func() {
		done <- cli.Run(ctx, cli.Environment{
			Args: []string{"watch", plug.Host(), "--callback", "127.0.0.1:0"},
			Out:  &out, Err: &out, Client: plug.Client(), Network: ssdptest.NewHost(),
		})
	}()

	// The plug is switched only once the subscription's first event, the
	// state as it was, has been printed: switching earlier makes that first
	// event report the new state, and the old one is never seen.
	await(t, &out, "BinaryState = 0")
	plug.Switch(t.Context(), true)
	await(t, &out, "BinaryState = 1")
	cancel()

	if status := <-done; status != cli.ExitOK {
		t.Fatalf("watch: %d\n%s", status, out.String())
	}

	text := out.String()
	if !strings.Contains(text, "Kitchen Lamp: watching; callback http://127.0.0.1:") ||
		!strings.Contains(text, "BinaryState = 0") ||
		!strings.Contains(text, "BinaryState = 1") {
		t.Fatalf("watch printed:\n%s", text)
	}

	if plug.Subscriptions() != 0 {
		t.Fatal("watch left its subscription on the plug")
	}

	silent := wemotest.New()
	t.Cleanup(silent.Close)

	silent.Silent = true

	out.Reset()

	// Bounded: a watch that waits for no first event would otherwise never end.
	bounded, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()

	status := cli.Run(bounded, cli.Environment{
		Args: []string{"watch", silent.Host(), "--callback", "127.0.0.1:0", "--first-event", "300ms"},
		Out:  &out, Err: &out, Client: silent.Client(), Network: ssdptest.NewHost(),
	})
	if status != cli.ExitError || !strings.Contains(out.String(), "no event from Kitchen Lamp within 300ms") {
		t.Fatalf("watch on a silent plug: %d\n%s", status, out.String())
	}
}

// TestDiscovery: a search finds every plug advertised, sorted by name; a
// plug is then reached by its name; a device that answered and cannot be
// described is said beside the others; and a network with no plug says so.
func TestDiscovery(t *testing.T) {
	t.Parallel()

	hall, kitchen, gone := wemotest.New(), wemotest.New(), wemotest.New()
	t.Cleanup(hall.Close)
	t.Cleanup(kitchen.Close)

	hall.FriendlyName = "Hall"

	// A plug that was advertised and answers no more.
	gone.Close()

	// A network is one run's: its links are closed with the run's Browser.
	network := func() ssdp.Network { return advertised(kitchen, hall, gone) }

	out, status := runOn(t, network(), "discover", "--wait", "300ms")

	first := strings.Index(out, "Hall                         Socket   "+wemotest.Firmware)
	second := strings.Index(out, "Kitchen Lamp                 Socket   "+wemotest.Firmware)

	if status != cli.ExitOK || first < 0 || second < first ||
		!strings.Contains(out, " "+hall.Location()+"\n") || !strings.Contains(out, " "+kitchen.Location()+"\n") ||
		!strings.Contains(out, "answered, not described: "+gone.Location()) {
		t.Fatalf("discover: status %d, output:\n%s", status, out)
	}

	if out, status = runOn(t, network(), "--wait", "300ms", "on", "hall"); status != cli.ExitOK ||
		!strings.Contains(out, "Hall: on\n") || !hall.On || kitchen.On {
		t.Fatalf("on by name: status %d, output:\n%s", status, out)
	}

	out, status = runOn(t, network(), "--wait", "300ms", "get", "Garage")
	if status != cli.ExitError || !strings.Contains(out, `no plug named "Garage"`) ||
		!strings.Contains(out, "1 devices answered and could not be described") {
		t.Fatalf("get of a name no plug has: status %d, output:\n%s", status, out)
	}

	if out, status = run(t, "discover", "--wait", "100ms"); status != cli.ExitOK ||
		!strings.Contains(out, "No plug answered.") {
		t.Fatalf("discover on a network with no plug: status %d, output:\n%s", status, out)
	}
}
