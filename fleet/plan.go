package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/duncanite/go-wemo/wemo"
)

// Found is a plug that answered, with its settings as read.
type Found struct {
	Plug     *wemo.Plug
	Settings Settings
}

// Survey reads each of the plugs a program found, keyed by serial. A plug
// that could not be read, that states no serial, or that states the serial
// of a plug read before it, is a failure beside the result, not an error:
// the rest of the fleet is still there. No plug is an empty result.
func Survey(ctx context.Context, plugs []*wemo.Plug) (map[string]Found, []error) {
	var failures []error

	found := make(map[string]Found, len(plugs))

	for _, plug := range plugs {
		said := plug.Description()

		if said.Serial == "" {
			failures = append(failures, fmt.Errorf("%w: %s states no serial", ErrSerial, said.URL))

			continue
		}

		if other, stated := found[said.Serial]; stated {
			failures = append(failures, fmt.Errorf(
				"%w: %s states the serial %q, which %s states too",
				ErrSerial, said.URL, said.Serial, other.Plug.Description().URL,
			))

			continue
		}

		settings, err := Read(ctx, plug)
		if err != nil {
			failures = append(failures, err)

			continue
		}

		found[said.Serial] = Found{Plug: plug, Settings: settings}
	}

	return found, failures
}

// ErrSerial is a plug a fleet cannot know by its serial: its description
// states none, or one that a plug read before it states too.
var ErrSerial = errors.New("not a serial to know a plug by")

// Diff is one key that differs between a file and its plug.
type Diff struct {
	Key  string
	Plug string
	File string
}

// Change is a plug whose file says otherwise.
type Change struct {
	Serial string
	Found  Found
	Diffs  []Diff
}

// Plan is the files set against the plugs that answered.
type Plan struct {
	// Changes are the plugs that answered and differ from their file.
	Changes []Change
	// Unreachable are the serials with a file and no plug answering: not
	// a change, not a deletion, a plug to go and look at.
	Unreachable []string
	// Unmanaged are the plugs that answered and have no file.
	Unmanaged []Found
	// Blind are the serials whose plug answered and whose file Apply will
	// enforce without reading back: the auto-update switch, every time.
	Blind []string
}

// Compare sets files against found, each sorted by serial.
func Compare(files map[string]Settings, found map[string]Found) *Plan {
	plan := &Plan{}

	for _, serial := range sorted(files) {
		desired := files[serial]

		current, answered := found[serial]
		if !answered {
			plan.Unreachable = append(plan.Unreachable, serial)

			continue
		}

		plan.Blind = append(plan.Blind, serial)

		if diffs := diff(desired, current.Settings); len(diffs) > 0 {
			plan.Changes = append(plan.Changes, Change{Serial: serial, Found: current, Diffs: diffs})
		}
	}

	for _, serial := range sorted(found) {
		if _, managed := files[serial]; !managed {
			plan.Unmanaged = append(plan.Unmanaged, found[serial])
		}
	}

	return plan
}

func diff(desired, current Settings) []Diff {
	var diffs []Diff

	if desired.MAC != "" && !strings.EqualFold(desired.MAC, current.MAC) {
		diffs = append(diffs, Diff{Key: "mac", Plug: current.MAC, File: desired.MAC})
	}

	if desired.Name != current.Name {
		diffs = append(diffs, Diff{Key: "name", Plug: current.Name, File: desired.Name})
	}

	return diffs
}

// Empty is a plan with nothing to change and every file's plug answering;
// an unmanaged plug is reported, not counted.
func (p *Plan) Empty() bool {
	return len(p.Changes) == 0 && len(p.Unreachable) == 0
}

// Write prints the plan: each change with its keys, the plug's value on
// the left, then the unreachable, then the unmanaged, then the one line
// on what an apply sends blind.
func (p *Plan) Write(out io.Writer) {
	for _, change := range p.Changes {
		fmt.Fprintf(out, "%s (%s):\n", change.Serial, change.Found.Settings.Name)

		for _, diff := range change.Diffs {
			fmt.Fprintf(out, "  %s: %q -> %q\n", diff.Key, diff.Plug, diff.File)
		}
	}

	for _, serial := range p.Unreachable {
		fmt.Fprintf(out, "unreachable: %s\n", serial)
	}

	for _, found := range p.Unmanaged {
		said := found.Plug.Description()

		fmt.Fprintf(out, "unmanaged: %s %s %s\n", said.Serial, found.Settings.Name, said.URL)
	}

	if n := len(p.Blind); n > 0 {
		noun := "plugs"
		if n == 1 {
			noun = "plug"
		}

		fmt.Fprintf(out, "auto_update: sent on every apply to %d %s; a plug cannot report it\n", n, noun)
	}

	if p.Empty() {
		fmt.Fprintln(out, "No changes.")
	}
}

func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
