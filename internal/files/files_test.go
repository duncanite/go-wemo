package files_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkcloser/xos"
	"github.com/mycophonic/primordium/filesystem"

	"github.com/duncanite/go-wemo/fleet"
	"github.com/duncanite/go-wemo/internal/files"
)

// serial is a plug's, as a file is named by it.
const serial = "221234K0100001"

func write(t *testing.T, path, text string) {
	t.Helper()

	if err := filesystem.WriteFile(path, []byte(text), filesystem.FilePermissionsDefault); err != nil {
		t.Fatal(err)
	}
}

// TestFiles: a file round-trips by serial; a dotfile in the directory is
// not a plug's and is passed over; a key no plug has, and a file without
// the extension, are refused rather than skipped.
func TestFiles(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "plugs")
	want := fleet.Settings{MAC: "000C29000001", Name: "Kitchen Lamp"}

	if err := files.Save(dir, serial, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, dotfile := range []string{".DS_Store", ".gitkeep"} {
		write(t, filepath.Join(dir, dotfile), "")
	}

	kept, err := files.Load(dir)
	if err != nil || len(kept) != 1 || kept[serial] != want {
		t.Fatalf("Load = %+v, %v", kept, err)
	}

	stray := filepath.Join(dir, "notes.txt")
	// What it holds would read as a plug's settings: its name is the matter.
	write(t, stray, "name: Kitchen Lamp\n")

	if _, err = files.Load(dir); !errors.Is(err, files.ErrInvalidFile) || !strings.Contains(err.Error(), stray) {
		t.Fatalf("Load with a stray file = %v, want ErrInvalidFile naming it", err)
	}

	if err = os.Remove(stray); err != nil {
		t.Fatal(err)
	}

	typo := filepath.Join(dir, "SERIAL2"+files.Extension)
	write(t, typo, "name: x\ncolour: red\n")

	_, err = files.Load(dir)
	if !errors.Is(err, files.ErrInvalidFile) || !errors.Is(err, fleet.ErrSettings) ||
		!strings.Contains(err.Error(), typo) {
		t.Fatalf("Load with an unknown key = %v, want ErrInvalidFile naming the file", err)
	}

	if _, err = files.Load(filepath.Join(dir, "nowhere")); err == nil {
		t.Fatal("Load of a directory that is not there succeeded")
	}
}

// TestDump: a plug found gets a file; a second dump leaves a file alone
// unless forced, and a forced one writes what the plug now holds.
func TestDump(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "plugs")
	found := map[string]fleet.Found{
		serial:           {Settings: fleet.Settings{MAC: "000C29000001", Name: "Kitchen Lamp"}},
		"221234K0100002": {Settings: fleet.Settings{MAC: "000C29000002", Name: "Hall"}},
	}

	written, err := files.Dump(dir, found, false)
	if err != nil || len(written) != 2 || written[0] != serial || written[1] != "221234K0100002" {
		t.Fatalf("Dump = %v, %v", written, err)
	}

	raw, err := xos.ReadFile(filepath.Join(dir, serial+files.Extension))
	if err != nil || string(raw) != "mac: 000C29000001\nname: Kitchen Lamp\nauto_update: false\n" {
		t.Fatalf("the dumped file reads %q, %v", raw, err)
	}

	found[serial] = fleet.Found{Settings: fleet.Settings{MAC: "000C29000001", Name: "Renamed In The App"}}

	if again, dumpErr := files.Dump(dir, found, false); dumpErr != nil || len(again) != 0 {
		t.Fatalf("second Dump without force = %v, %v, want nothing written", again, dumpErr)
	}

	if kept, loadErr := files.Load(dir); loadErr != nil || kept[serial].Name != "Kitchen Lamp" {
		t.Fatalf("after a second dump the file reads %+v, %v", kept[serial], loadErr)
	}

	if again, dumpErr := files.Dump(dir, found, true); dumpErr != nil || len(again) != 2 {
		t.Fatalf("forced Dump = %v, %v", again, dumpErr)
	}

	if kept, loadErr := files.Load(dir); loadErr != nil || kept[serial].Name != "Renamed In The App" {
		t.Fatalf("after a forced dump the file reads %+v, %v", kept[serial], loadErr)
	}
}

// TestSerialsThatNameNoFile: a serial that is not a file's name in the
// directory, or names one Load skips, is ErrInvalidFile from Named, from
// Save and from Dump, forced or not, and nothing is made, in the directory
// or beside it.
func TestSerialsThatNameNoFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "plugs")

	for _, odd := range []string{
		"", ".", "..", ".hidden", "../escaped", "sub/serial", "nul\x00serial", strings.Repeat("S", 300),
		`back\slash`, "a space", "a\nline", "an\x1b[2Jescape", "turned\u202eround", "accentu\u00e9",
	} {
		if name, err := files.Named(odd); !errors.Is(err, files.ErrInvalidFile) || name != "" {
			t.Errorf("Named for the serial %q = %q, %v, want ErrInvalidFile", odd, name, err)
		}

		if err := files.Save(dir, odd, fleet.Settings{Name: "Garage"}); !errors.Is(err, files.ErrInvalidFile) {
			t.Errorf("Save under the serial %q = %v, want ErrInvalidFile", odd, err)
		}

		for _, force := range []bool{false, true} {
			written, err := files.Dump(dir, map[string]fleet.Found{odd: {}}, force)
			if !errors.Is(err, files.ErrInvalidFile) || len(written) != 0 {
				t.Errorf("Dump of the serial %q, force %t = %v, %v, want ErrInvalidFile", odd, force, written, err)
			}
		}
	}

	if left, err := xos.ReadDir(root); err != nil || len(left) != 0 {
		t.Fatalf("the refused serials left %v behind, %v", left, err)
	}

	if name, err := files.Named(serial); err != nil || name != serial+files.Extension {
		t.Fatalf("Named for a serial = %q, %v", name, err)
	}
}
