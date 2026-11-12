package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/duncanite/go-wemo/internal/cli"
	"github.com/duncanite/go-wemo/upnp/upnptest"
)

func run(t *testing.T, plug *upnptest.Plug, args ...string) (string, int) {
	t.Helper()

	var out, errs bytes.Buffer

	status := cli.Run(t.Context(), args, &out, &errs, plug.Client())

	return out.String() + errs.String(), status
}

// TestTheCommands: every command against the plug by host and port, its
// output as a person reads it.
func TestTheCommands(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
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
		{[]string{"discover", "--target", plug.ServeSSDP(), "--wait", "1s"}, "Hall                         Socket   " + upnptest.Firmware},
	} {
		out, status := run(t, plug, step.args...)
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

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	host := plug.Host()

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"call", host, "timesync", "GetTime"}, "the device answered 501 Action Failed (HTTP 200)"},
		{[]string{"call", host, "basicevent", "Nope"}, `no action "Nope"`},
		{[]string{"call", host, "basicevent", "GetBinaryState", "x"}, `argument "x" is not name=value`},
		{[]string{"reset", host, "--yes"}, "one of --wifi, --data or --all"},
		{[]string{"get", "127.0.0.1"}, "no Wemo answered on 127.0.0.1"},
		{[]string{"setup", "--ssid", "House", "--host", host}, "WEMO_PASSPHRASE is empty"},
	} {
		out, status := run(t, plug, step.args...)
		if status != cli.ExitError || !strings.Contains(out, step.want) {
			t.Fatalf("%v: status %d, output:\n%s\nwant %q", step.args, status, out, step.want)
		}
	}

	if out, status := run(
		t,
		plug,
		"--help",
	); status != cli.ExitOK ||
		!strings.Contains(out, "Speak to the Wemo plugs") {
		t.Fatalf("--help: %d\n%s", status, out)
	}
}

// TestSetupCommand: the passphrase from the environment, the join through
// a plug that leaves its access point, reported as the outcome it is.
func TestSetupCommand(t *testing.T) {
	plug := upnptest.New()
	t.Cleanup(plug.Close)

	plug.Leaves = true

	t.Setenv("WEMO_PASSPHRASE", "correct horse battery")

	out, status := run(t, plug, "setup", "--ssid", "House", "--host", plug.Host(), "--timeout", "20s")
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
func TestWatchCommand(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	var out bytes.Buffer

	done := make(chan int, 1)

	go func() {
		done <- cli.Run(ctx, []string{"watch", plug.Host(), "--callback", "127.0.0.1:0"}, &out, &out, plug.Client())
	}()

	time.Sleep(500 * time.Millisecond)
	plug.Switch(t.Context(), true)
	time.Sleep(500 * time.Millisecond)
	cancel()

	if status := <-done; status != cli.ExitOK {
		t.Fatalf("watch: %d\n%s", status, out.String())
	}

	text := out.String()
	if !strings.Contains(text, "Kitchen Lamp: subscribed uuid:") || !strings.Contains(text, "BinaryState = 0") ||
		!strings.Contains(text, "BinaryState = 1") {
		t.Fatalf("watch printed:\n%s", text)
	}

	if plug.Subscriptions() != 0 {
		t.Fatal("watch left its subscription on the plug")
	}

	silent := upnptest.New()
	t.Cleanup(silent.Close)

	silent.Silent = true

	out.Reset()

	status := cli.Run(
		t.Context(),
		[]string{"watch", silent.Host(), "--callback", "127.0.0.1:0", "--first-event", "300ms"},
		&out,
		&out,
		silent.Client(),
	)
	if status != cli.ExitError || !strings.Contains(out.String(), "no event from Kitchen Lamp within 300ms") {
		t.Fatalf("watch on a silent plug: %d\n%s", status, out.String())
	}
}
