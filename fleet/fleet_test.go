package fleet_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/duncanite/go-wemo/fleet"
	"github.com/duncanite/go-wemo/wemo"
	"github.com/duncanite/go-wemo/wemo/wemotest"
)

func opened(t *testing.T, plug *wemotest.Plug) *wemo.Plug {
	t.Helper()

	p, err := wemo.Open(t.Context(), plug.Client(), plug.Host())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	return p
}

func calls(plug *wemotest.Plug, action string) int {
	count := 0

	for _, call := range plug.Calls {
		if call == action {
			count++
		}
	}

	return count
}

// TestReadAndApply: Read is the name as the plug holds it now; Apply
// renames only when the file says otherwise and reads the name back; a
// MAC that is not the plug's is refused before anything is sent.
func TestReadAndApply(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	p := opened(t, plug)

	settings, err := fleet.Read(t.Context(), p)
	if err != nil || settings.Name != wemotest.Name || settings.MAC != p.Description().MAC {
		t.Fatalf("Read = %+v, %v", settings, err)
	}

	same, err := fleet.Apply(t.Context(), p, settings)
	if err != nil || same != settings || calls(plug, "basicevent.ChangeFriendlyName") != 0 {
		t.Fatalf("Apply of the same = %+v, %v, renames %d", same, err, calls(plug, "basicevent.ChangeFriendlyName"))
	}

	renamed, err := fleet.Apply(
		t.Context(),
		p,
		fleet.Settings{MAC: strings.ToLower(p.Description().MAC), Name: "Garage"},
	)
	if err != nil || renamed.Name != "Garage" || plug.FriendlyName != "Garage" {
		t.Fatalf("Apply of a new name = %+v, %v; plug holds %q", renamed, err, plug.FriendlyName)
	}

	// The auto-update switch went with both applies, off, the file's default.
	if plug.AutoUpdate == nil || *plug.AutoUpdate || calls(plug, "basicevent.setAutoFWUpdate") != 2 {
		t.Fatalf(
			"auto-update after two applies: %v, sent %d times",
			plug.AutoUpdate,
			calls(plug, "basicevent.setAutoFWUpdate"),
		)
	}

	if _, onErr := fleet.Apply(
		t.Context(),
		p,
		fleet.Settings{Name: "Garage", AutoUpdate: true},
	); onErr != nil ||
		!*plug.AutoUpdate {
		t.Fatalf("Apply with auto_update on = %v, plug holds %v", onErr, *plug.AutoUpdate)
	}

	_, err = fleet.Apply(t.Context(), p, fleet.Settings{MAC: "00:00:00:00:00:00", Name: "Garage"})
	if !errors.Is(err, fleet.ErrReadOnly) {
		t.Fatalf("Apply of another MAC = %v, want ErrReadOnly", err)
	}
}

// TestApplyReadsBack: a plug that says ok and keeps its name is ErrKept.
func TestApplyReadsBack(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	plug.KeepName = true

	_, err := fleet.Apply(t.Context(), opened(t, plug), fleet.Settings{Name: "Garage"})
	if !errors.Is(err, fleet.ErrKept) || plug.FriendlyName != wemotest.Name {
		t.Fatalf("Apply on a plug that keeps its name = %v, plug named %q, want ErrKept", err, plug.FriendlyName)
	}
}

// TestSettingsAsYAML: settings go to YAML and come back the same; a key no
// plug has, and what is not YAML or not settings at all, are refused rather
// than read as nothing.
func TestSettingsAsYAML(t *testing.T) {
	t.Parallel()

	want := fleet.Settings{MAC: "000C29000001", Name: "Kitchen Lamp", AutoUpdate: true}

	data, err := fleet.Format(want)
	if err != nil || string(data) != "mac: 000C29000001\nname: Kitchen Lamp\nauto_update: true\n" {
		t.Fatalf("Format = %q, %v", data, err)
	}

	if back, parseErr := fleet.Parse(data); parseErr != nil || back != want {
		t.Fatalf("Parse of what Format wrote = %+v, %v", back, parseErr)
	}

	if partial, parseErr := fleet.Parse([]byte("name: Hall\n")); parseErr != nil ||
		partial != (fleet.Settings{Name: "Hall"}) {
		t.Fatalf("Parse of a name alone = %+v, %v", partial, parseErr)
	}

	for name, text := range map[string]string{
		"a key no plug has": "name: x\ncolour: red\n",
		"not YAML":          "name: [\n",
		"a list":            "- name: x\n",
		"nothing":           "",
	} {
		if settings, parseErr := fleet.Parse([]byte(text)); !errors.Is(parseErr, fleet.ErrSettings) ||
			settings != (fleet.Settings{}) {
			t.Errorf("Parse of %s = %+v, %v, want ErrSettings", name, settings, parseErr)
		}
	}
}

// TestSurveyAndCompare: a plug found with nothing kept is unmanaged; with
// its settings kept as read, it is a quiet plan; with others, a change; and
// settings whose plug does not answer are unreachable.
func TestSurveyAndCompare(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	found, failures := fleet.Survey(t.Context(), []*wemo.Plug{opened(t, plug)})
	if len(failures) != 0 || len(found) != 1 || found[wemotest.Serial].Settings.Name != wemotest.Name {
		t.Fatalf("Survey = %+v, %v", found, failures)
	}

	plan := fleet.Compare(map[string]fleet.Settings{}, found)
	if !plan.Empty() || len(plan.Unmanaged) != 1 {
		t.Fatalf("plan with nothing kept = %+v", plan)
	}

	var out bytes.Buffer

	plan.Write(&out)

	if want := "unmanaged: " + wemotest.Serial + " " + wemotest.Name + " " + plug.Location() + "\n"; !strings.Contains(
		out.String(), want,
	) {
		t.Fatalf("plan with nothing kept said:\n%s\nwant %q", out.String(), want)
	}

	kept := map[string]fleet.Settings{wemotest.Serial: found[wemotest.Serial].Settings}

	out.Reset()

	plan = fleet.Compare(kept, found)
	plan.Write(&out)

	if !plan.Empty() || len(plan.Unmanaged) != 0 || !strings.Contains(out.String(), "No changes.") ||
		!strings.Contains(out.String(), "auto_update: sent on every apply to 1 plug;") {
		t.Fatalf("plan with the plug's settings kept = %+v:\n%s", plan, out.String())
	}

	kept[wemotest.Serial] = fleet.Settings{MAC: kept[wemotest.Serial].MAC, Name: "Garage"}
	kept["NOBODY0000001"] = fleet.Settings{Name: "Attic"}

	out.Reset()

	plan = fleet.Compare(kept, found)
	plan.Write(&out)

	if plan.Empty() || len(plan.Changes) != 1 || plan.Changes[0].Diffs[0].Key != "name" ||
		len(plan.Unreachable) != 1 || plan.Unreachable[0] != "NOBODY0000001" {
		t.Fatalf("plan after an edit = %+v:\n%s", plan, out.String())
	}

	if !strings.Contains(out.String(), `name: "Kitchen Lamp" -> "Garage"`) ||
		!strings.Contains(out.String(), "unreachable: NOBODY0000001") {
		t.Fatalf("plan said:\n%s", out.String())
	}
}

// TestCompareEdges: settings whose plug does not answer are not a quiet
// plan on their own; a MAC kept matches the plug's whatever its case, and
// one that is not the plug's is a change.
func TestCompareEdges(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	p := opened(t, plug)
	found := map[string]fleet.Found{
		wemotest.Serial: {Plug: p, Settings: fleet.Settings{MAC: p.Description().MAC, Name: wemotest.Name}},
	}

	unreachable := fleet.Compare(map[string]fleet.Settings{"NOBODY0000001": {Name: "Attic"}}, found)
	if unreachable.Empty() || len(unreachable.Changes) != 0 {
		t.Fatalf("a plan with an unreachable plug alone reads Empty: %+v", unreachable)
	}

	lower := map[string]fleet.Settings{
		wemotest.Serial: {MAC: strings.ToLower(p.Description().MAC), Name: wemotest.Name},
	}
	if plan := fleet.Compare(lower, found); !plan.Empty() || len(plan.Changes) != 0 {
		t.Fatalf("a lower-case MAC kept is a change: %+v", plan)
	}

	other := map[string]fleet.Settings{wemotest.Serial: {MAC: "00:00:00:00:00:00", Name: wemotest.Name}}
	if plan := fleet.Compare(other, found); plan.Empty() || len(plan.Changes) != 1 ||
		plan.Changes[0].Diffs[0].Key != "mac" {
		t.Fatalf("a MAC kept that is not the plug's is no change: %+v", plan)
	}
}

// TestSurveyFailures: a plug whose description states no serial, and one
// that cannot be read, are failures naming them, and are not among the
// found; the rest of the fleet is still there.
func TestSurveyFailures(t *testing.T) {
	t.Parallel()

	nameless := wemotest.New()
	t.Cleanup(nameless.Close)

	// A description is read as holding the later of two serialNumber elements.
	nameless.Extra = "<serialNumber></serialNumber>"

	gone := wemotest.New()
	unread := opened(t, gone)

	gone.Close()

	sound := wemotest.New()
	t.Cleanup(sound.Close)

	found, failures := fleet.Survey(t.Context(), []*wemo.Plug{opened(t, nameless), unread, opened(t, sound)})
	if len(found) != 1 || found[wemotest.Serial].Settings.Name != wemotest.Name || len(failures) != 2 ||
		!errors.Is(failures[0], fleet.ErrSerial) || !strings.Contains(failures[0].Error(), nameless.Location()) ||
		!strings.Contains(failures[1].Error(), wemotest.Name) {
		t.Fatalf("Survey = %+v, %v", found, failures)
	}
}

// TestSurveyOfTwoPlugsWithOneSerial: the plug read first is the fleet's, and
// the other is a failure that names them both.
func TestSurveyOfTwoPlugsWithOneSerial(t *testing.T) {
	t.Parallel()

	first, second := wemotest.New(), wemotest.New()
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)

	second.FriendlyName = "Impostor"

	found, failures := fleet.Survey(t.Context(), []*wemo.Plug{opened(t, first), opened(t, second)})
	if len(found) != 1 || found[wemotest.Serial].Settings.Name != wemotest.Name || len(failures) != 1 ||
		!errors.Is(failures[0], fleet.ErrSerial) || !strings.Contains(failures[0].Error(), first.Location()) ||
		!strings.Contains(failures[0].Error(), second.Location()) {
		t.Fatalf("Survey of two plugs with one serial = %+v, %v", found, failures)
	}
}

// TestSurveyOfNoPlug: no plug is an empty fleet.
func TestSurveyOfNoPlug(t *testing.T) {
	t.Parallel()

	if found, failures := fleet.Survey(t.Context(), nil); len(found) != 0 || len(failures) != 0 {
		t.Fatalf("Survey of no plug = %v, %v", found, failures)
	}
}
