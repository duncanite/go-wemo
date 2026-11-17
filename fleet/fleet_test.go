package fleet_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkcloser/xos"
	"github.com/mycophonic/primordium/filesystem"

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

// data is the one dumped file, as text.
func data(t *testing.T, dir string) string {
	t.Helper()

	raw, err := xos.ReadFile(filepath.Join(dir, wemotest.Serial+fleet.Extension))
	if err != nil {
		t.Fatal(err)
	}

	return string(raw)
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

// TestFiles: a file round-trips by serial; a key no plug has, and a file
// without the extension, are refused rather than skipped.
func TestFiles(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "plugs")
	want := fleet.Settings{MAC: "000C29000001", Name: "Kitchen Lamp"}

	if err := fleet.Save(dir, wemotest.Serial, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	files, err := fleet.Load(dir)
	if err != nil || len(files) != 1 || files[wemotest.Serial] != want {
		t.Fatalf("Load = %+v, %v", files, err)
	}

	stray := filepath.Join(dir, "notes.txt")
	if err := filesystem.WriteFile(stray, []byte("x"), filesystem.FilePermissionsDefault); err != nil {
		t.Fatal(err)
	}

	if _, err := fleet.Load(dir); !errors.Is(err, fleet.ErrInvalidFile) {
		t.Fatalf("Load with a stray file = %v, want ErrInvalidFile", err)
	}

	if err := os.Remove(stray); err != nil {
		t.Fatal(err)
	}

	typo := filepath.Join(dir, "SERIAL2"+fleet.Extension)
	if err := filesystem.WriteFile(
		typo,
		[]byte("name: x\ncolour: red\n"),
		filesystem.FilePermissionsDefault,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := fleet.Load(dir); !errors.Is(err, fleet.ErrInvalidFile) {
		t.Fatalf("Load with an unknown key = %v, want ErrInvalidFile", err)
	}
}

// TestSurveyCompareDump: a plug found with no file is unmanaged; dumped,
// it is a quiet plan; an edited file is a change; a file whose plug does
// not answer is unreachable; a second dump leaves a file alone unless
// forced.
func TestSurveyCompareDump(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	plugs := []*wemo.Plug{opened(t, plug)}

	found, failures := fleet.Survey(t.Context(), plugs)
	if len(failures) != 0 || len(found) != 1 || found[wemotest.Serial].Settings.Name != wemotest.Name {
		t.Fatalf("Survey = %+v, %v", found, failures)
	}

	dir := filepath.Join(t.TempDir(), "plugs")

	plan := fleet.Compare(map[string]fleet.Settings{}, found)
	if !plan.Empty() || len(plan.Unmanaged) != 1 {
		t.Fatalf("plan with no files = %+v", plan)
	}

	written, err := fleet.Dump(dir, found, false)
	if err != nil || len(written) != 1 || written[0] != wemotest.Serial {
		t.Fatalf("Dump = %v, %v", written, err)
	}

	files, err := fleet.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	plan = fleet.Compare(files, found)
	plan.Write(&out)

	if !plan.Empty() || len(plan.Unmanaged) != 0 || !strings.Contains(out.String(), "No changes.") ||
		!strings.Contains(
			out.String(),
			"auto_update: sent on every apply to 1 plug;",
		) || !strings.Contains(data(t, dir), "auto_update: false") {
		t.Fatalf("plan after dump = %+v:\n%s", plan, out.String())
	}

	files[wemotest.Serial] = fleet.Settings{MAC: files[wemotest.Serial].MAC, Name: "Garage"}
	files["NOBODY0000001"] = fleet.Settings{Name: "Attic"}

	out.Reset()

	plan = fleet.Compare(files, found)
	plan.Write(&out)

	if plan.Empty() || len(plan.Changes) != 1 || plan.Changes[0].Diffs[0].Key != "name" ||
		len(plan.Unreachable) != 1 || plan.Unreachable[0] != "NOBODY0000001" {
		t.Fatalf("plan after an edit = %+v:\n%s", plan, out.String())
	}

	if !strings.Contains(out.String(), `name: "Kitchen Lamp" -> "Garage"`) ||
		!strings.Contains(out.String(), "unreachable: NOBODY0000001") {
		t.Fatalf("plan said:\n%s", out.String())
	}

	plug.FriendlyName = "Renamed In The App"

	found, failures = fleet.Survey(t.Context(), plugs)
	if len(failures) != 0 {
		t.Fatal(failures)
	}

	if again, dumpErr := fleet.Dump(dir, found, false); dumpErr != nil || len(again) != 0 {
		t.Fatalf("second Dump without force = %v, %v, want nothing written", again, dumpErr)
	}

	if again, dumpErr := fleet.Dump(dir, found, true); dumpErr != nil || len(again) != 1 {
		t.Fatalf("forced Dump = %v, %v", again, dumpErr)
	}

	files, err = fleet.Load(dir)
	if err != nil || files[wemotest.Serial].Name != "Renamed In The App" {
		t.Fatalf("after a forced dump the file reads %+v, %v", files[wemotest.Serial], err)
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

// TestCompareEdges: a file whose plug does not answer is not a quiet plan
// on its own; a file's MAC matches the plug's whatever its case; a dotfile
// in the directory is not a stray.
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
		t.Fatalf("a lower-case MAC in the file is a change: %+v", plan)
	}

	dir := filepath.Join(t.TempDir(), "plugs")
	if err := fleet.Save(dir, wemotest.Serial, lower[wemotest.Serial]); err != nil {
		t.Fatal(err)
	}

	for _, dotfile := range []string{".DS_Store", ".gitkeep"} {
		if err := filesystem.WriteFile(
			filepath.Join(dir, dotfile),
			[]byte{},
			filesystem.FilePermissionsDefault,
		); err != nil {
			t.Fatal(err)
		}
	}

	if files, err := fleet.Load(dir); err != nil || len(files) != 1 {
		t.Fatalf("Load beside dotfiles = %+v, %v", files, err)
	}
}

// TestSerialsThatNameNoFile: a serial that is not a file's name in the
// directory, or names one Load skips, is ErrInvalidFile from Save and from
// Dump, forced or not, and nothing is made, in the directory or beside it.
func TestSerialsThatNameNoFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "plugs")

	for _, serial := range []string{
		"", ".", "..", ".hidden", "../escaped", "sub/serial", "nul\x00serial", strings.Repeat("S", 300),
	} {
		if err := fleet.Save(dir, serial, fleet.Settings{Name: "Garage"}); !errors.Is(err, fleet.ErrInvalidFile) {
			t.Errorf("Save under the serial %q = %v, want ErrInvalidFile", serial, err)
		}

		for _, force := range []bool{false, true} {
			written, err := fleet.Dump(dir, map[string]fleet.Found{serial: {}}, force)
			if !errors.Is(err, fleet.ErrInvalidFile) || len(written) != 0 {
				t.Errorf("Dump of the serial %q, force %t = %v, %v, want ErrInvalidFile", serial, force, written, err)
			}
		}
	}

	if left, err := xos.ReadDir(root); err != nil || len(left) != 0 {
		t.Fatalf("the refused serials left %v behind, %v", left, err)
	}
}

// TestSurveyRefusesASerial: a plug whose description states a serial that
// is not a file's name is a failure naming where it answered, and is not
// among the found; one that cannot be read is a failure too, and the rest
// of the fleet is still there.
func TestSurveyRefusesASerial(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	// A description is read as holding the later of two serialNumber elements.
	plug.Extra = "<serialNumber>../../escaped</serialNumber>"

	gone := wemotest.New()
	unread := opened(t, gone)

	gone.Close()

	sound := wemotest.New()
	t.Cleanup(sound.Close)

	found, failures := fleet.Survey(t.Context(), []*wemo.Plug{opened(t, plug), unread, opened(t, sound)})
	if len(found) != 1 || found[wemotest.Serial].Settings.Name != wemotest.Name || len(failures) != 2 ||
		!errors.Is(failures[0], fleet.ErrInvalidFile) || !strings.Contains(failures[0].Error(), plug.Location()) ||
		!strings.Contains(failures[1].Error(), wemotest.Name) {
		t.Fatalf("Survey = %+v, %v", found, failures)
	}
}

// TestSurveyOfNoPlug: no plug is an empty fleet.
func TestSurveyOfNoPlug(t *testing.T) {
	t.Parallel()

	if found, failures := fleet.Survey(t.Context(), nil); len(found) != 0 || len(failures) != 0 {
		t.Fatalf("Survey of no plug = %v, %v", found, failures)
	}
}
