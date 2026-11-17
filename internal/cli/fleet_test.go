package cli_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/forkcloser/xos"

	"github.com/duncanite/go-wemo/internal/cli"
	"github.com/duncanite/go-wemo/internal/files"
	"github.com/duncanite/go-wemo/wemo/wemotest"
)

// TestASerialThatNamesNoFile: a plug whose description states a serial that
// is more than a file's name is said beside the others, with where it
// answered, and gets no file, in the directory or beside it; the rest of the
// fleet is dumped all the same.
func TestASerialThatNamesNoFile(t *testing.T) {
	t.Parallel()

	odd, sound := wemotest.New(), wemotest.New()
	t.Cleanup(odd.Close)
	t.Cleanup(sound.Close)

	// A description is read as holding the later of two serialNumber elements.
	odd.Extra = "<serialNumber>../escaped</serialNumber>"

	root := t.TempDir()
	dir := filepath.Join(root, "plugs")

	out, status := runOn(t, advertised(odd, sound), "--wait", "300ms", "dump", dir)
	if status != cli.ExitOK ||
		!strings.Contains(out, "wrote "+filepath.Join(dir, wemotest.Serial+files.Extension)+"\n") ||
		!strings.Contains(out, "answered, not read: "+odd.Location()) || !strings.Contains(out, "../escaped") {
		t.Fatalf("dump: status %d, output:\n%s", status, out)
	}

	if left, err := xos.ReadDir(root); err != nil || len(left) != 1 {
		t.Fatalf("the dump left %v in the directory's parent, %v", left, err)
	}

	if left, err := xos.ReadDir(dir); err != nil || len(left) != 1 {
		t.Fatalf("the dump left %v in the directory, %v", left, err)
	}
}
