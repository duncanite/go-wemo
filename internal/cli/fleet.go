package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/duncanite/go-wemo/fleet"
	"github.com/duncanite/go-wemo/internal/files"
	"github.com/duncanite/go-wemo/internal/terminal"
	"github.com/duncanite/go-wemo/wemo"
)

type directory struct {
	Dir string `arg:"" help:"The directory of the plugs' files: one a plug, YAML, named by the plug's serial."`
}

type (
	planCommand  struct{ directory }
	applyCommand struct{ directory }
	dumpCommand  struct {
		directory

		Force bool `help:"Rewrite the files that are there, from what the plugs hold."`
	}
)

// survey finds the plugs that answer and reads each, keyed by serial. A
// device that answered and could not be described or read, or whose serial
// cannot name a file, is a failure beside the result: the rest of the fleet
// is still there.
func (root *rootCommand) survey(ctx context.Context) (map[string]fleet.Found, []error, error) {
	browser, err := root.browse(ctx)
	if err != nil {
		return nil, nil, err
	}

	defer func() { _ = browser.Close() }()

	located, failures, err := root.search(ctx, browser)
	if err != nil {
		return nil, nil, err
	}

	plugs := make([]*wemo.Plug, 0, len(located))
	for _, each := range located {
		plugs = append(plugs, each.plug)
	}

	found, unread := fleet.Survey(ctx, plugs)
	failures = append(failures, unread...)

	// A serial is the plug's own word, read off the network, and names a
	// file here.
	for _, serial := range slices.Sorted(maps.Keys(found)) {
		if _, nameErr := files.Named(serial); nameErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", found[serial].Plug.Description().URL, nameErr))

			delete(found, serial)
		}
	}

	return found, failures, nil
}

// report prints the devices that answered and are not among the found.
func (root *rootCommand) report(failures []error) {
	for _, failure := range failures {
		terminal.Printf(root.out, "answered, not read: %v\n", failure)
	}
}

func (cmd *planCommand) Run(ctx context.Context, root *rootCommand) error {
	kept, err := files.Load(cmd.Dir)
	if err != nil {
		return err
	}

	found, failures, err := root.survey(ctx)
	if err != nil {
		return err
	}

	fleet.Compare(kept, found).Write(root.out)
	root.report(failures)

	return nil
}

func (cmd *applyCommand) Run(ctx context.Context, root *rootCommand) error {
	kept, err := files.Load(cmd.Dir)
	if err != nil {
		return err
	}

	found, failures, err := root.survey(ctx)
	if err != nil {
		return err
	}

	var refused []error

	for _, serial := range slices.Sorted(maps.Keys(kept)) {
		current, answered := found[serial]
		if !answered {
			terminal.Printf(root.out, "unreachable: %s\n", serial)

			continue
		}

		holds, applyErr := fleet.Apply(ctx, current.Plug, kept[serial])
		if applyErr != nil {
			refused = append(refused, fmt.Errorf("%s: %w", serial, applyErr))

			continue
		}

		terminal.Printf(root.out, "%s (%s): applied\n", serial, holds.Name)
	}

	root.report(failures)

	if len(refused) > 0 {
		return fmt.Errorf(
			"%d of %d plugs do not match their file: %w",
			len(refused),
			len(kept),
			errors.Join(refused...),
		)
	}

	return nil
}

func (cmd *dumpCommand) Run(ctx context.Context, root *rootCommand) error {
	found, failures, err := root.survey(ctx)
	if err != nil {
		return err
	}

	written, err := files.Dump(cmd.Dir, found, cmd.Force)

	for _, serial := range written {
		terminal.Printf(root.out, "wrote %s\n", filepath.Join(cmd.Dir, serial+files.Extension))
	}

	if err != nil {
		return err
	}

	if len(written) == 0 {
		terminal.Printf(root.out, "Nothing written.\n")
	}

	root.report(failures)

	return nil
}
