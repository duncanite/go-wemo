// Package cli is the wemo command: every verb a Wemo plug takes on the
// LAN, and call for the rest. A target is a friendly name, found by
// discovery, a host with an optional port, or the URL of a description.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-wemo/ssdp"
	"github.com/duncanite/go-wemo/upnp"
	"github.com/duncanite/go-wemo/wemo"
)

// Exit statuses.
const (
	ExitOK    = 0
	ExitError = 1
)

// exitStatus is how the parser's own exit, on --help, leaves Run: kong
// calls its exit function and goes on, so the function panics with the
// status and Run recovers it.
type exitStatus int

type rootCommand struct {
	Interface string        `env:"WEMO_INTERFACE" help:"The network interface the plugs are on, when the host has several."`
	Wait      time.Duration `default:"3s"         env:"WEMO_WAIT"                                                           help:"How long discovery collects answers."`

	Discover discoverCommand `cmd:"" help:"Find every plug on this network."`
	Describe describeCommand `cmd:"" help:"Print a plug's description, its services, and a service's actions."`
	Get      getCommand      `cmd:"" help:"Print whether the plug is on."`
	On       onCommand       `cmd:"" help:"Switch the plug on."`
	Off      offCommand      `cmd:"" help:"Switch the plug off."`
	Toggle   toggleCommand   `cmd:"" help:"Flip the plug."`
	Watch    watchCommand    `cmd:"" help:"Subscribe to plugs' events and print them until interrupted."`
	Name     nameCommand     `cmd:"" help:"Print or change the plug's name."`
	Signal   signalCommand   `cmd:"" help:"Print the plug's Wi-Fi signal strength."`
	Time     timeCommand     `cmd:"" help:"Set the plug's clock and zone from this host's."`
	Firmware firmwareCommand `cmd:"" help:"Print the firmware version; turn automatic updates off."`
	Reset    resetCommand    `cmd:"" help:"Reset the plug: its data, its Wi-Fi, or both."`
	Networks networksCommand `cmd:"" help:"Print the networks the plug sees."`
	Setup    setupCommand    `cmd:"" help:"Join a plug in setup mode to a network. The passphrase comes from WEMO_PASSPHRASE."`
	Call     callCommand     `cmd:"" help:"Invoke any action a plug's service declares."`

	out    io.Writer
	client *http.Client
}

type target struct {
	Target string `arg:"" help:"A plug: its name, its host with an optional port, or the URL of its description."`
}

type (
	discoverCommand struct {
		Listen bool   `help:"After the search, keep printing the announcements plugs multicast."`
		Target string `help:"Send the search here instead of the multicast group: a plug's host:1900 asks that plug alone."`
	}
	describeCommand struct {
		target

		Service string `help:"A service to describe: its actions and their arguments." short:"s"`
	}
	getCommand    struct{ target }
	toggleCommand struct{ target }
	signalCommand struct{ target }
	timeCommand   struct{ target }
	onCommand     struct {
		target

		For time.Duration `help:"Switch back off after this long, by the plug's own countdown."`
	}
	offCommand struct {
		target

		For time.Duration `help:"Switch back on after this long, by the plug's own countdown."`
	}
	watchCommand struct {
		Targets  []string      `arg:""                                                                                                    help:"The plugs to watch."`
		Callback string        `help:"host:port the plugs notify; empty picks this host's address on the plug's network and a free port."`
		Lease    time.Duration `default:"30m"                                                                                             help:"The subscription length asked of each plug."`
	}
	nameCommand struct {
		target

		New string `arg:"" help:"The new name." optional:""`
	}
	firmwareCommand struct {
		target

		DisableAutoUpdate bool `help:"Turn the plug's automatic firmware update off."`
	}
	resetCommand struct {
		target

		WiFi bool `help:"Forget the network; the plug raises its setup access point."      name:"wifi" xor:"kind"`
		Data bool `help:"Clear the name, the icon and the rules."                          xor:"kind"`
		All  bool `help:"Factory reset: data and Wi-Fi."                                   xor:"kind"`
		Yes  bool `help:"Do it. Without this flag the command only says what it would do."`
	}
	networksCommand struct{ target }
	setupCommand    struct {
		SSID       string        `help:"The network to join." required:"true"`
		Passphrase string        `env:"WEMO_PASSPHRASE"       help:"The network's passphrase; from the environment, never an argument." hidden:""`
		Host       string        `default:"10.22.22.1"        help:"The plug in setup mode, on its own access point."`
		Timeout    time.Duration `default:"60s"               help:"How long to wait for the plug to report itself connected."`
	}
	callCommand struct {
		target

		Service string   `arg:"" help:"The service's short name: basicevent, WiFiSetup, timesync, firmwareupdate, rules, metainfo, deviceinfo."`
		Action  string   `arg:"" help:"The action, as the service declares it."`
		Args    []string `arg:"" help:"Arguments as name=value."                                                                                optional:""`
	}
)

// Run parses args and runs the command; the client makes every request to
// the plugs.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, client *http.Client) (status int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			exited, ok := recovered.(exitStatus)
			if !ok {
				panic(recovered)
			}

			status = int(exited)
		}
	}()

	root := rootCommand{out: stdout, client: client}

	parser, err := kong.New(&root,
		kong.Name("wemo"),
		kong.Description("Speak to the Wemo plugs on this network."),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { panic(exitStatus(code)) }),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		fmt.Fprintln(stderr, "wemo:", err)

		return ExitError
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		parser.Errorf("%s", err)

		return ExitError
	}

	if err := kctx.Run(&root); err != nil {
		fmt.Fprintln(stderr, "wemo:", err)

		return ExitError
	}

	return ExitOK
}

func (root *rootCommand) options() ssdp.Options {
	return ssdp.Options{Interface: root.Interface, Wait: root.Wait}
}

// resolve finds the plug a target names: a URL or a host directly, a name
// through discovery.
func (root *rootCommand) resolve(ctx context.Context, name string) (*wemo.Plug, error) {
	if strings.Contains(name, "://") || looksLikeHost(name) {
		return wemo.Open(ctx, root.client, name)
	}

	plugs, failures, err := wemo.Discover(ctx, root.client, root.options())
	if err != nil {
		return nil, err
	}

	plug, err := wemo.ByName(plugs, name)
	if err != nil {
		if len(failures) > 0 {
			return nil, fmt.Errorf(
				"%w; %d devices answered and could not be described: %w",
				err,
				len(failures),
				errors.Join(failures...),
			)
		}

		return nil, err
	}

	return plug, nil
}

// looksLikeHost tells an address from a name: digits and dots, a colon for
// a port, or something with a dot and no space.
func looksLikeHost(name string) bool {
	if strings.ContainsAny(name, " ") {
		return false
	}

	return strings.Count(name, ".") >= 1 || strings.Contains(name, ":")
}

func (cmd *discoverCommand) Run(ctx context.Context, root *rootCommand) error {
	opts := root.options()
	opts.Target = cmd.Target

	plugs, failures, err := wemo.Discover(ctx, root.client, opts)
	if err != nil && !errors.Is(err, ssdp.ErrNoAnswer) {
		return err
	}

	for _, plug := range plugs {
		fmt.Fprintf(root.out, "%-28s %-8s %-36s %s\n", plug.FriendlyName, plug.Model, plug.Firmware, plug.Base.String())
	}

	for _, failure := range failures {
		fmt.Fprintf(root.out, "answered, not described: %v\n", failure)
	}

	if len(plugs) == 0 && len(failures) == 0 {
		fmt.Fprintln(root.out, "No plug answered.")
	}

	if !cmd.Listen {
		return nil
	}

	fmt.Fprintln(root.out, "Listening for announcements; interrupt to stop.")

	return ssdp.Listen(ctx, root.options(), func(ann ssdp.Announcement) {
		state := "alive"
		if !ann.Alive {
			state = "bye"
		}

		fmt.Fprintf(root.out, "%s %-6s %-40s %s\n", time.Now().Format(time.TimeOnly), state, ann.USN, ann.Location)
	})
}

func (cmd *describeCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	if cmd.Service == "" {
		fmt.Fprintf(
			root.out,
			"name:        %s\nmodel:       %s %s (%s)\nhardware:    %s\nfirmware:    %s\nserial:      %s\nmac:         %s\nudn:         %s\nurl:         %s\nservices:\n",
			plug.FriendlyName,
			plug.Model,
			plug.ModelNumber,
			plug.Description,
			plug.Hardware,
			plug.Firmware,
			plug.Serial,
			plug.MAC,
			plug.UDN,
			plug.Base.String(),
		)

		for _, svc := range plug.Services {
			fmt.Fprintf(root.out, "  %-16s %s\n", svc.Name(), svc.Type)
		}

		return nil
	}

	svc, err := plug.Service(cmd.Service)
	if err != nil {
		return err
	}

	description, err := svc.Describe(ctx)
	if err != nil {
		return err
	}

	for _, action := range description.Actions {
		parts := make([]string, 0, len(action.Arguments))
		for _, arg := range action.Arguments {
			parts = append(parts, arg.Name+":"+arg.Direction)
		}

		fmt.Fprintf(root.out, "%s(%s)\n", action.Name, strings.Join(parts, ", "))
	}

	return nil
}

func (cmd *getCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	on, raw, err := plug.State(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: %s (%s)\n", plug.FriendlyName, onOff(on), raw)

	return nil
}

func (cmd *onCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.set(ctx, cmd.Target, true, cmd.For)
}

func (cmd *offCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.set(ctx, cmd.Target, false, cmd.For)
}

func (root *rootCommand) set(ctx context.Context, name string, powered bool, revert time.Duration) error {
	plug, err := root.resolve(ctx, name)
	if err != nil {
		return err
	}

	if err := plug.SetState(ctx, powered, revert); err != nil {
		return err
	}

	if revert > 0 {
		fmt.Fprintf(root.out, "%s: %s for %s\n", plug.FriendlyName, onOff(powered), revert)
	} else {
		fmt.Fprintf(root.out, "%s: %s\n", plug.FriendlyName, onOff(powered))
	}

	return nil
}

func (cmd *toggleCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	on, err := plug.Toggle(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: %s\n", plug.FriendlyName, onOff(on))

	return nil
}

func (cmd *nameCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	if cmd.New != "" {
		if renameErr := plug.Rename(ctx, cmd.New); renameErr != nil {
			return renameErr
		}
	}

	name, err := plug.Name(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintln(root.out, name)

	return nil
}

func (cmd *signalCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	signal, err := plug.Signal(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: %d%%\n", plug.FriendlyName, signal)

	return nil
}

func (cmd *timeCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	answer, err := plug.TimeSync(ctx, time.Now())
	if err != nil {
		return err
	}

	printArguments(root.out, answer)

	return nil
}

func (cmd *firmwareCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "described: %s\n", plug.Firmware)

	answer, err := plug.FirmwareVersion(ctx)
	if err != nil {
		fmt.Fprintf(root.out, "GetFirmwareVersion: %v\n", err)
	} else {
		printArguments(root.out, answer)
	}

	if !cmd.DisableAutoUpdate {
		return nil
	}

	if err := plug.AutoUpdate(ctx, false); err != nil {
		return err
	}

	fmt.Fprintln(root.out, "automatic firmware update: off")

	return nil
}

func (cmd *resetCommand) Run(ctx context.Context, root *rootCommand) error {
	var (
		kind wemo.ResetKind
		what string
	)

	switch {
	case cmd.WiFi:
		kind, what = wemo.ResetWiFi, "forget the network and raise the setup access point"
	case cmd.Data:
		kind, what = wemo.ResetData, "clear the name, the icon and the rules"
	case cmd.All:
		kind, what = wemo.ResetAll, "factory reset: data and Wi-Fi"
	default:
		return fmt.Errorf("%w: one of --wifi, --data or --all", fault.ErrInvalidArgument)
	}

	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	if !cmd.Yes {
		fmt.Fprintf(root.out, "would %s on %s (%s); add --yes\n", what, plug.FriendlyName, plug.Base.Host)

		return nil
	}

	answer, err := plug.Reset(ctx, kind)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: reset answered %q\n", plug.FriendlyName, answer)

	return nil
}

func (cmd *networksCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	points, err := plug.Networks(ctx)
	if err != nil {
		return err
	}

	for _, point := range points {
		fmt.Fprintf(root.out, "%-32s ch %-3s %4s %s\n", point.SSID, point.Channel, point.Signal, point.Auth)
	}

	return nil
}

func (cmd *setupCommand) Run(ctx context.Context, root *rootCommand) error {
	if cmd.Passphrase == "" {
		return fmt.Errorf("%w: WEMO_PASSPHRASE is empty", fault.ErrInvalidArgument)
	}

	plug, err := wemo.Open(ctx, root.client, cmd.Host)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: joining %s\n", plug.FriendlyName, cmd.SSID)

	err = plug.Join(ctx, cmd.SSID, cmd.Passphrase, wemo.SetupOptions{Timeout: cmd.Timeout})

	switch {
	case err == nil:
		fmt.Fprintf(root.out, "%s: connected to %s\n", plug.FriendlyName, cmd.SSID)
	case errors.Is(err, wemo.ErrLeft):
		fmt.Fprintf(
			root.out,
			"%s: took the request and left its access point for %s; discover it there in a minute or two\n",
			plug.FriendlyName,
			cmd.SSID,
		)
	default:
		return err
	}

	return nil
}

func (cmd *callCommand) Run(ctx context.Context, root *rootCommand) error {
	plug, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	args := make(upnp.Arguments, len(cmd.Args))

	for _, pair := range cmd.Args {
		name, value, found := strings.Cut(pair, "=")
		if !found {
			return fmt.Errorf("%w: argument %q is not name=value", fault.ErrInvalidArgument, pair)
		}

		args[name] = value
	}

	answer, err := plug.Call(ctx, cmd.Service, cmd.Action, args)
	if err != nil {
		return err
	}

	printArguments(root.out, answer)

	return nil
}

func printArguments(out io.Writer, args upnp.Arguments) {
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		fmt.Fprintf(out, "%s: %s\n", name, args[name])
	}
}

func onOff(powered bool) string {
	if powered {
		return "on"
	}

	return "off"
}
