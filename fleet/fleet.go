// Package fleet keeps plugs as their settings say: for each plug, by its
// serial, what it both takes and reads back, which a program holds as YAML
// wherever it keeps things. Survey reads the plugs a program found, Compare
// sets the settings kept against what answered, and Apply makes a plug match
// its own. Parse and Format are the settings from and to YAML. A plug that
// does not answer is unreachable, never gone: finding plugs is best effort,
// and nothing here says to forget one.
//
// It reads and writes no file: where the settings are kept is the
// program's.
package fleet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/duncanite/go-wemo/wemo"
)

// ErrKept is a plug that said ok and kept its value.
var ErrKept = errors.New("the plug kept its value")

// ErrReadOnly is settings stating a key the plug cannot be given.
var ErrReadOnly = errors.New("not a key a plug can be given")

// ErrSettings is YAML that is not a plug's settings: it does not parse, or
// it states a key no plug has.
var ErrSettings = errors.New("not a plug's settings")

// Settings is what is kept of a plug: the keys it takes, and the MAC for
// the reader. Name is read back and compared; AutoUpdate is write-only,
// since the plug has no action to report it, so Apply sends it on every
// run and Compare never diffs it.
type Settings struct {
	// MAC is the plug's, as its description reports it; a plug cannot be
	// given another, so a file whose MAC is not the plug's is refused.
	MAC string `yaml:"mac"`
	// Name is the friendly name, as GetFriendlyName answers now.
	Name string `yaml:"name"`
	// AutoUpdate is the plug's automatic firmware update switch, sent on
	// every Apply and never read: the plug has no action for that. False
	// is what a dumped file carries, the host the plug would fetch from
	// being nobody's now.
	AutoUpdate bool `yaml:"auto_update"` //nolint:tagliatelle // the files' keys are snake_case, as the console's are
}

// Parse reads a plug's settings from YAML. A key that Settings does not
// have is refused: a typo would otherwise enforce nothing, quietly.
func Parse(data []byte) (Settings, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var settings Settings
	if err := decoder.Decode(&settings); err != nil {
		return Settings{}, fmt.Errorf("%w: %w", ErrSettings, err)
	}

	return settings, nil
}

// Format writes a plug's settings as YAML, which Parse reads back.
func Format(settings Settings) ([]byte, error) {
	data, err := yaml.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("writing settings as YAML: %w", err)
	}

	return data, nil
}

// Read is the plug's settings as it holds them now; AutoUpdate comes back
// false, the plug having no action to report it.
func Read(ctx context.Context, plug *wemo.Plug) (Settings, error) {
	name, err := plug.Name(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("%s: %w", plug.Description().Name, err)
	}

	return Settings{MAC: plug.Description().MAC, Name: name, AutoUpdate: false}, nil
}

// Apply makes the plug match desired and returns what it holds afterwards,
// read back where the plug can be read: a plug answers ok to a change it
// does not make, so the answer is not the proof. A key it kept is
// ErrKept; a MAC other than the plug's is ErrReadOnly and nothing is sent.
// AutoUpdate is sent every time, whether or not the name changed, since
// nothing can say whether the plug still has it.
func Apply(ctx context.Context, plug *wemo.Plug, desired Settings) (Settings, error) {
	current, err := Read(ctx, plug)
	if err != nil {
		return current, err
	}

	if desired.MAC != "" && !strings.EqualFold(desired.MAC, current.MAC) {
		return current, fmt.Errorf("%w: mac is the plug's, %s, not %s", ErrReadOnly, current.MAC, desired.MAC)
	}

	if desired.Name != current.Name {
		if err = plug.Rename(ctx, desired.Name); err != nil {
			return current, fmt.Errorf("%s: %w", plug.Description().Name, err)
		}

		current, err = Read(ctx, plug)
		if err != nil {
			return current, err
		}

		if current.Name != desired.Name {
			return current, fmt.Errorf("%w: name is %q after setting %q", ErrKept, current.Name, desired.Name)
		}
	}

	if err = plug.AutoUpdate(ctx, desired.AutoUpdate); err != nil {
		return current, fmt.Errorf("%s: %w", plug.Description().Name, err)
	}

	current.AutoUpdate = desired.AutoUpdate

	return current, nil
}
