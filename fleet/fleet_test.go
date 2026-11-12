package fleet_test

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mycophonic/primordium/filesystem"

	"github.com/duncanite/go-wemo/fleet"
	"github.com/duncanite/go-wemo/ssdp"
	"github.com/duncanite/go-wemo/upnp/upnptest"
	"github.com/duncanite/go-wemo/wemo"
)

func opened(t *testing.T, plug *upnptest.Plug) *wemo.Plug {
	t.Helper()

	p, err := wemo.Open(t.Context(), plug.Client(), plug.Host())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	return p
}

func calls(plug *upnptest.Plug, action string) int {
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

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	p := opened(t, plug)

	settings, err := fleet.Read(t.Context(), p)
	if err != nil || settings.Name != upnptest.Name || settings.MAC != p.MAC {
		t.Fatalf("Read = %+v, %v", settings, err)
	}

	same, err := fleet.Apply(t.Context(), p, settings)
	if err != nil || same != settings || calls(plug, "basicevent.ChangeFriendlyName") != 0 {
		t.Fatalf("Apply of the same = %+v, %v, renames %d", same, err, calls(plug, "basicevent.ChangeFriendlyName"))
	}

	renamed, err := fleet.Apply(t.Context(), p, fleet.Settings{MAC: strings.ToLower(p.MAC), Name: "Garage"})
	if err != nil || renamed.Name != "Garage" || plug.FriendlyName != "Garage" {
		t.Fatalf("Apply of a new name = %+v, %v; plug holds %q", renamed, err, plug.FriendlyName)
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

	if err := fleet.Save(dir, upnptest.Serial, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	files, err := fleet.Load(dir)
	if err != nil || len(files) != 1 || files[upnptest.Serial] != want {
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
		[]byte("name: x\nauto_update: false\n"),
		filesystem.FilePermissionsDefault,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := fleet.Load(dir); !errors.Is(err, fleet.ErrInvalidFile) {
		t.Fatalf("Load with an unknown key = %v, want ErrInvalidFile", err)
	}
}

// TestDiscoverCompareDump: a plug found with no file is unmanaged; dumped,
// it is a quiet plan; an edited file is a change; a file whose plug does
// not answer is unreachable; a second dump leaves a file alone unless
// forced.
func TestDiscoverCompareDump(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	target := plug.ServeSSDP()

	found, failures, err := fleet.Discover(t.Context(), plug.Client(), ssdp.Options{Target: target, Wait: time.Second})
	if err != nil || len(failures) != 0 || len(found) != 1 || found[upnptest.Serial].Settings.Name != upnptest.Name {
		t.Fatalf("Discover = %+v, %v, %v", found, failures, err)
	}

	dir := filepath.Join(t.TempDir(), "plugs")

	plan := fleet.Compare(map[string]fleet.Settings{}, found)
	if !plan.Empty() || len(plan.Unmanaged) != 1 {
		t.Fatalf("plan with no files = %+v", plan)
	}

	written, err := fleet.Dump(dir, found, false)
	if err != nil || len(written) != 1 || written[0] != upnptest.Serial {
		t.Fatalf("Dump = %v, %v", written, err)
	}

	files, err := fleet.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	plan = fleet.Compare(files, found)
	plan.Write(&out)

	if !plan.Empty() || len(plan.Unmanaged) != 0 || !strings.Contains(out.String(), "No changes.") {
		t.Fatalf("plan after dump = %+v:\n%s", plan, out.String())
	}

	files[upnptest.Serial] = fleet.Settings{MAC: files[upnptest.Serial].MAC, Name: "Garage"}
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

	found, _, err = fleet.Discover(t.Context(), plug.Client(), ssdp.Options{Target: target, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	if again, dumpErr := fleet.Dump(dir, found, false); dumpErr != nil || len(again) != 0 {
		t.Fatalf("second Dump without force = %v, %v, want nothing written", again, dumpErr)
	}

	if again, dumpErr := fleet.Dump(dir, found, true); dumpErr != nil || len(again) != 1 {
		t.Fatalf("forced Dump = %v, %v", again, dumpErr)
	}

	files, err = fleet.Load(dir)
	if err != nil || files[upnptest.Serial].Name != "Renamed In The App" {
		t.Fatalf("after a forced dump the file reads %+v, %v", files[upnptest.Serial], err)
	}
}

// TestApplyReadsBack: a plug that says ok and keeps its name is ErrKept.
func TestApplyReadsBack(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	plug.KeepName = true

	_, err := fleet.Apply(t.Context(), opened(t, plug), fleet.Settings{Name: "Garage"})
	if !errors.Is(err, fleet.ErrKept) || plug.FriendlyName != upnptest.Name {
		t.Fatalf("Apply on a plug that keeps its name = %v, plug named %q, want ErrKept", err, plug.FriendlyName)
	}
}

// TestCompareEdges: a file whose plug does not answer is not a quiet plan
// on its own; a file's MAC matches the plug's whatever its case; a dotfile
// in the directory is not a stray.
func TestCompareEdges(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	p := opened(t, plug)
	found := map[string]fleet.Found{
		upnptest.Serial: {Plug: p, Settings: fleet.Settings{MAC: p.MAC, Name: upnptest.Name}},
	}

	unreachable := fleet.Compare(map[string]fleet.Settings{"NOBODY0000001": {Name: "Attic"}}, found)
	if unreachable.Empty() || len(unreachable.Changes) != 0 {
		t.Fatalf("a plan with an unreachable plug alone reads Empty: %+v", unreachable)
	}

	lower := map[string]fleet.Settings{upnptest.Serial: {MAC: strings.ToLower(p.MAC), Name: upnptest.Name}}
	if plan := fleet.Compare(lower, found); !plan.Empty() || len(plan.Changes) != 0 {
		t.Fatalf("a lower-case MAC in the file is a change: %+v", plan)
	}

	dir := filepath.Join(t.TempDir(), "plugs")
	if err := fleet.Save(dir, upnptest.Serial, lower[upnptest.Serial]); err != nil {
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

// TestDiscoverNobodyAnswers: a search nobody answers is an empty fleet,
// not an error.
func TestDiscoverNobodyAnswers(t *testing.T) {
	t.Parallel()

	silent, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = silent.Close() })

	found, failures, err := fleet.Discover(
		t.Context(),
		http.DefaultClient,
		ssdp.Options{Target: silent.LocalAddr().String(), Wait: 300 * time.Millisecond},
	)
	if err != nil || len(failures) != 0 || len(found) != 0 {
		t.Fatalf("Discover with nobody answering = %v, %v, %v", found, failures, err)
	}
}
