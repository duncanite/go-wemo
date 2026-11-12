// Package fleet keeps plugs as files: one YAML file per plug, named by its
// serial, holding what a plug both takes and reads back. Discover finds
// the plugs on the network and reads them, Compare sets the files against
// what answered, Apply makes a plug match its file, and Dump writes the
// files from the plugs. A plug that does not answer is unreachable, never
// gone: discovery is best effort, and nothing here removes a file.
package fleet

import (
	"context"
	"fmt"
	"strings"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-wemo/wemo"
)

// ErrKept is a plug that said ok and kept its value.
var ErrKept = fault.ErrUnacceptableResponse

// ErrReadOnly is a file stating a key the plug cannot be given.
var ErrReadOnly = fault.ErrInvalidArgument

// Settings is what a file holds: the keys a plug takes, and the MAC for
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

// Read is the plug's settings as it holds them now; AutoUpdate comes back
// false, the plug having no action to report it.
func Read(ctx context.Context, plug *wemo.Plug) (Settings, error) {
	name, err := plug.Name(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("%s: %w", plug.FriendlyName, err)
	}

	return Settings{MAC: plug.MAC, Name: name, AutoUpdate: false}, nil
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
			return current, fmt.Errorf("%s: %w", plug.FriendlyName, err)
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
		return current, fmt.Errorf("%s: %w", plug.FriendlyName, err)
	}

	current.AutoUpdate = desired.AutoUpdate

	return current, nil
}
