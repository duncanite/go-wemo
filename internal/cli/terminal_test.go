package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/forkcloser/go-upnp/ssdp/ssdptest"

	"github.com/duncanite/go-wemo/internal/cli"
	"github.com/duncanite/go-wemo/wemo/wemotest"
)

// hostile is a name a plug may hold: a line's end and a line of the
// command's own after it, a direction override, and a control that opens
// an escape sequence. shown is that name as the command has to print it.
const (
	hostile = "Lamp\nNo changes.\u202e\u009b2J"
	shown   = `Lamp\nNo changes.\u202e\u009b2J`
)

// TestADevicesTextIsShownAndNotObeyed: a name with a line's end, a
// direction override and a control in it is printed as text on the one
// line the command meant, in what it says and in what it fails with.
func TestADevicesTextIsShownAndNotObeyed(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	plug.FriendlyName = hostile

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"get", plug.Host()}, shown + ": off (0)\n"},
		{[]string{"on", plug.Host()}, shown + ": on\n"},
		{[]string{"name", plug.Host()}, shown + "\n"},
		{[]string{"signal", plug.Host()}, shown + ": 100%\n"},
	} {
		out, status := run(t, step.args...)
		if status != cli.ExitOK || out != step.want {
			t.Fatalf("%v: status %d, output %q, want %q", step.args, status, out, step.want)
		}
	}

	out, status := run(t, "describe", plug.Host())
	if status != cli.ExitOK || !strings.HasPrefix(out, "name:        "+shown+"\nmodel:") {
		t.Fatalf("describe: status %d, output %q", status, out)
	}

	plug.Silent = true

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var failed syncBuffer

	status = cli.Run(ctx, cli.Environment{
		Args: []string{"watch", plug.Host(), "--callback", "127.0.0.1:0", "--first-event", "300ms"},
		Out:  &failed, Err: &failed, Client: plug.Client(), Network: ssdptest.NewHost(),
	})

	text := failed.String()
	if status != cli.ExitError || !strings.Contains(text, "no event from "+shown+" within 300ms") ||
		strings.Count(text, "\n") != 2 || strings.ContainsAny(text, "\u202e\u009b") {
		t.Fatalf("watch on a silent plug: status %d, output %q", status, text)
	}
}
