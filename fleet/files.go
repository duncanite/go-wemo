package fleet

import (
	"bytes"
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
	"go.yaml.in/yaml/v4"
)

// Extension is the one the files carry; a file without it is not a
// plug's and is refused rather than skipped.
const Extension = ".yaml"

// ErrInvalidFile is a file that is not a plug's: a stray extension, a key
// no plug has, YAML that does not parse.
var ErrInvalidFile = fault.ErrInvalidArgument

// Load reads every file under dir, keyed by the serial its name carries.
// A key the file states that Settings does not have is refused: a typo
// would otherwise enforce nothing, quietly.
func Load(dir string) (map[string]Settings, error) {
	entries, err := xos.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", fault.ErrReadFailure, dir, err)
	}

	files := make(map[string]Settings, len(entries))

	for _, entry := range entries {
		// A dotfile is the directory's, not a plug's: .DS_Store, .gitkeep.
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		serial, found := strings.CutSuffix(entry.Name(), Extension)
		if !found || serial == "" {
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

func readFile(path string) (Settings, error) {
	data, err := xos.ReadFile(path)
	if err != nil {
		return Settings{}, fmt.Errorf("%w: reading %s: %w", fault.ErrReadFailure, path, err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var settings Settings
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, fmt.Errorf("%w: %s: %w", ErrInvalidFile, path, err)
	}

	return settings, nil
}

// Save writes one plug's file under dir, creating dir when it is missing.
func Save(dir, serial string, settings Settings) error {
	if err := os.MkdirAll(dir, filesystem.DirPermissionsDefault); err != nil {
		return fmt.Errorf("%w: creating %s: %w", fault.ErrWriteFailure, dir, err)
	}

	data, err := yaml.Marshal(settings)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", fault.ErrWriteFailure, serial, err)
	}

	path := filepath.Join(dir, serial+Extension)
	if err := filesystem.WriteFile(path, data, filesystem.FilePermissionsDefault); err != nil {
		return fmt.Errorf("%w: writing %s: %w", fault.ErrWriteFailure, path, err)
	}

	return nil
}

// Dump writes a file for every plug found, leaving a file already there
// as it is unless force is set, and returns the serials it wrote, in
// order. A file is rewritten whole: a comment in it does not survive a
// forced dump. A plug with a file and no answer is not touched, since
// not answering is not being gone.
func Dump(dir string, found map[string]Found, force bool) ([]string, error) {
	serials := make([]string, 0, len(found))
	for serial := range found {
		serials = append(serials, serial)
	}

	sort.Strings(serials)

	var written []string

	for _, serial := range serials {
		path := filepath.Join(dir, serial+Extension)

		if !force {
			_, err := xos.Stat(path)

			switch {
			case err == nil:
				continue
			case errors.Is(err, fs.ErrNotExist):
			default:
				return written, fmt.Errorf("%w: %s: %w", fault.ErrReadFailure, path, err)
			}
		}

		if err := Save(dir, serial, found[serial].Settings); err != nil {
			return written, err
		}

		written = append(written, serial)
	}

	return written, nil
}
