// Package files keeps a fleet's settings as files, for the command: one YAML
// file per plug in a directory, named by the plug's serial.
package files

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/forkcloser/xos"
	"github.com/mycophonic/primordium/fault"
	"github.com/mycophonic/primordium/filesystem"
	"github.com/mycophonic/primordium/filesystem/pathcheck"

	"github.com/duncanite/go-wemo/fleet"
)

// Extension is the one the files carry; a file without it is not a
// plug's and is refused rather than skipped.
const Extension = ".yaml"

// ErrInvalidFile is a file that is not a plug's: a stray extension, a key
// no plug has, YAML that does not parse, or a serial that cannot be a
// file's name.
var ErrInvalidFile = fault.ErrInvalidArgument

// Named is the name of the file for serial. A serial is the plug's own
// word, read off the network: one that is more than a file's name would
// leave dir, one that is empty or starts with a dot would name a file
// Load skips, and one that holds anything but letters, digits, a dot, a
// dash or an underscore is none a plug was seen to have, and would put a
// separator of another system, or what a terminal obeys, in a name.
func Named(serial string) (string, error) {
	if serial == "" || strings.HasPrefix(serial, ".") || strings.ContainsFunc(serial, odd) {
		return "", fmt.Errorf("%w: the serial %q cannot name a plug's file", ErrInvalidFile, serial)
	}

	name := serial + Extension
	if err := pathcheck.ValidateComponent(name); err != nil {
		return "", fmt.Errorf("%w: the serial %q cannot name a plug's file: %w", ErrInvalidFile, serial, err)
	}

	return name, nil
}

// odd says a character is none a serial is made of.
func odd(char rune) bool {
	switch {
	case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		return false
	case char == '.', char == '-', char == '_':
		return false
	default:
		return true
	}
}

// Load reads every file under dir, keyed by the serial its name carries.
// A key the file states that a plug's settings do not have is refused: a typo
// would otherwise enforce nothing, quietly.
func Load(dir string) (map[string]fleet.Settings, error) {
	entries, err := xos.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", fault.ErrReadFailure, dir, err)
	}

	files := make(map[string]fleet.Settings, len(entries))

	for _, entry := range entries {
		// A dotfile is the directory's, not a plug's: .DS_Store, .gitkeep.
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		serial, found := strings.CutSuffix(entry.Name(), Extension)
		if !found {
			return nil, fmt.Errorf("%w: %s is not a plug's file", ErrInvalidFile, filepath.Join(dir, entry.Name()))
		}

		settings, err := readFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}

		files[serial] = settings
	}

	return files, nil
}

func readFile(path string) (fleet.Settings, error) {
	data, err := xos.ReadFile(path)
	if err != nil {
		return fleet.Settings{}, fmt.Errorf("%w: reading %s: %w", fault.ErrReadFailure, path, err)
	}

	settings, err := fleet.Parse(data)
	if err != nil {
		return fleet.Settings{}, fmt.Errorf("%w: %s: %w", ErrInvalidFile, path, err)
	}

	return settings, nil
}

// Save writes one plug's file under dir, creating dir when it is missing.
// A serial that cannot be a file's name in dir is ErrInvalidFile, and
// nothing is made.
func Save(dir, serial string, settings fleet.Settings) error {
	name, err := Named(serial)
	if err != nil {
		return err
	}

	if err = os.MkdirAll(dir, filesystem.DirPermissionsDefault); err != nil {
		return fmt.Errorf("%w: creating %s: %w", fault.ErrWriteFailure, dir, err)
	}

	data, err := fleet.Format(settings)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", fault.ErrWriteFailure, serial, err)
	}

	path := filepath.Join(dir, name)
	if err = filesystem.WriteFile(path, data, filesystem.FilePermissionsDefault); err != nil {
		return fmt.Errorf("%w: writing %s: %w", fault.ErrWriteFailure, path, err)
	}

	return nil
}

// Dump writes a file for every plug found, leaving a file already there
// as it is unless force is set, and returns the serials it wrote, in
// order. A file is rewritten whole: a comment in it does not survive a
// forced dump. A plug with a file and no answer is not touched, since
// not answering is not being gone. A serial that cannot be a file's name
// in dir is ErrInvalidFile, and the dump stops there.
func Dump(dir string, found map[string]fleet.Found, force bool) ([]string, error) {
	serials := make([]string, 0, len(found))
	for serial := range found {
		serials = append(serials, serial)
	}

	sort.Strings(serials)

	var written []string

	for _, serial := range serials {
		name, err := Named(serial)
		if err != nil {
			return written, err
		}

		path := filepath.Join(dir, name)

		if !force {
			_, err = xos.Stat(path)

			switch {
			case err == nil:
				continue
			case errors.Is(err, fs.ErrNotExist):
			default:
				return written, fmt.Errorf("%w: %s: %w", fault.ErrReadFailure, path, err)
			}
		}

		if err = Save(dir, serial, found[serial].Settings); err != nil {
			return written, err
		}

		written = append(written, serial)
	}

	return written, nil
}
