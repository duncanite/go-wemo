// Package cli is the wemo command: every verb a Wemo plug takes on the
// LAN, and call for the rest. A target is a friendly name, found by
// discovery, a host with an optional port, or the URL of a description.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	upnp "github.com/forkcloser/go-upnp"
	"github.com/forkcloser/go-upnp/ssdp"
	"github.com/mycophonic/primordium/fault"

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
	Plan     planCommand     `cmd:"" help:"Set the plugs' files in a directory against the plugs that answer."`
	Apply    applyCommand    `cmd:"" help:"Make every plug that answers match its file."`
	Dump     dumpCommand     `cmd:"" help:"Write a file for every plug that answers and has none."`

	out     io.Writer
	client  wemo.Doer
	network ssdp.Network
}

// Environment is where the command runs.
type Environment struct {
	// Args is its arguments, without its name.
	Args []string
	// Out and Err are where it writes.
	Out io.Writer
	Err io.Writer
	// Client makes every request to the plugs. It has to follow no
	// redirect.
	Client wemo.Doer
	// Network is where plugs are searched for; nil is the host's own.
	Network ssdp.Network
}

type target struct {
	Target string `arg:"" help:"A plug: its name, its host with an optional port, or the URL of its description."`
}

type (
	discoverCommand struct {
		Listen bool `help:"After the search, keep printing the plugs that come and go."`
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
		Targets    []string      `arg:""                                                                                                                              help:"The plugs to watch."`
		Callback   string        `help:"host:port to listen at, which the plugs notify; empty picks this host's address on the first plug's network and a free port."`
		FirstEvent time.Duration `default:"10s"                                                                                                                       help:"How long a plug gets to send its first event, its current state, before the callback is judged unreachable."`
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

// Run parses the arguments and runs the command.
func Run(ctx context.Context, env Environment) (status int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			exited, ok := recovered.(exitStatus)
			if !ok {
				panic(recovered)
			}

			status = int(exited)
		}
	}()

	root := rootCommand{out: env.Out, client: env.Client, network: env.Network}

	parser, err := kong.New(&root,
		kong.Name("wemo"),
		kong.Description("Speak to the Wemo plugs on this network."),
		kong.UsageOnError(),
		kong.Writers(env.Out, env.Err),
		kong.Exit(func(code int) { panic(exitStatus(code)) }),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		fmt.Fprintln(env.Err, "wemo:", err)

		return ExitError
	}

	kctx, err := parser.Parse(env.Args)
	if err != nil {
		parser.Errorf("%s", err)

		return ExitError
	}

	if err := kctx.Run(&root); err != nil {
		fmt.Fprintln(env.Err, "wemo:", err)

		return ExitError
	}

	return ExitOK
}

// located is a plug, and this host's address on its network when the search
// that found it said.
type located struct {
	plug  *wemo.Plug
	local netip.Addr
}

// lent is the host's SSDP as the library asks for it.
type lent struct {
	browser *ssdp.Browser
}

func (l lent) Browse(ctx context.Context, target string, handle func(wemo.Advertisement, bool)) error {
	//nolint:wrapcheck // the Browser's own word, which the library hands on as it is
	return l.browser.Browse(ctx, target, func(change ssdp.Change, heard ssdp.Advertisement) {
		locations := make([]wemo.Location, 0, len(heard.Locations))
		for _, location := range heard.Locations {
			locations = append(locations, wemo.Location{URL: location.URL, Local: location.Local})
		}

		handle(wemo.Advertisement{USN: heard.USN, Locations: locations}, change == ssdp.Lost)
	})
}

// browse opens the host's part in SSDP, on the interface named or on all.
func (root *rootCommand) browse(ctx context.Context) (*ssdp.Browser, error) {
	cfg := ssdp.Config{Network: root.network}

	if root.Interface != "" {
		cfg.Interfaces = func(candidate ssdp.Interface) bool { return candidate.Name == root.Interface }
	}

	browser, err := ssdp.Open(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening the network: %w", err)
	}

	return browser, nil
}

// search finds the plugs that answer within the wait and describes each,
// sorted by name. A device that answered and will not be described is
// returned beside them, so one odd device does not hide the others.
func (root *rootCommand) search(ctx context.Context, browser *ssdp.Browser) ([]located, []error, error) {
	searching, cancel := context.WithTimeout(ctx, root.Wait)
	defer cancel()

	found := make(map[wemo.ID]wemo.Found)

	err := wemo.Find(searching, lent{browser: browser}, func(plug wemo.Found, lost bool) {
		if lost {
			delete(found, plug.ID)
		} else {
			found[plug.ID] = plug
		}
	})

	if ctx.Err() != nil {
		return nil, nil, fmt.Errorf("searching for plugs: %w", ctx.Err())
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		return nil, nil, fmt.Errorf("searching for plugs: %w", err)
	}

	var (
		plugs    []located
		failures []error
	)

	for _, id := range slices.Sorted(maps.Keys(found)) {
		plug, openErr := root.open(ctx, found[id])
		if openErr != nil {
			failures = append(failures, openErr)

			continue
		}

		plugs = append(plugs, plug)
	}

	slices.SortStableFunc(plugs, func(one, other located) int {
		return strings.Compare(one.plug.Description().Name, other.plug.Description().Name)
	})

	return plugs, failures, nil
}

// open describes a plug that was found, at the first of its places that
// answers.
func (root *rootCommand) open(ctx context.Context, found wemo.Found) (located, error) {
	failure := fmt.Errorf("%w: %s was heard at no address", fault.ErrNotFound, found.ID)

	for _, location := range found.Locations {
		plug, err := wemo.Open(ctx, root.client, location.URL)
		if err == nil {
			return located{plug: plug, local: location.Local}, nil
		}

		failure = fmt.Errorf("%s: %w", location.URL, err)
	}

	return located{}, failure
}

// resolve finds the plug a target names: a URL or a host directly, a name
// through a search.
func (root *rootCommand) resolve(ctx context.Context, name string) (located, error) {
	if strings.Contains(name, "://") || looksLikeHost(name) {
		plug, err := wemo.Open(ctx, root.client, name)

		return located{plug: plug}, err
	}

	browser, err := root.browse(ctx)
	if err != nil {
		return located{}, err
	}

	defer func() { _ = browser.Close() }()

	found, failures, err := root.search(ctx, browser)
	if err != nil {
		return located{}, err
	}

	plugs := make([]*wemo.Plug, 0, len(found))
	for _, each := range found {
		plugs = append(plugs, each.plug)
	}

	plug, err := wemo.ByName(plugs, name)
	if err != nil {
		if len(failures) > 0 {
			return located{}, fmt.Errorf(
				"%w; %d devices answered and could not be described: %w",
				err,
				len(failures),
				errors.Join(failures...),
			)
		}

		return located{}, err
	}

	for _, each := range found {
		if each.plug == plug {
			return each, nil
		}
	}

	return located{plug: plug}, nil
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
	browser, err := root.browse(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = browser.Close() }()

	plugs, failures, err := root.search(ctx, browser)
	if err != nil {
		return err
	}

	for _, found := range plugs {
		said := found.plug.Description()

		fmt.Fprintf(root.out, "%-28s %-8s %-36s %s\n", said.Name, said.Model, said.Firmware, said.URL)
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

	fmt.Fprintln(root.out, "Listening for plugs that come and go; interrupt to stop.")

	err = wemo.Find(ctx, lent{browser: browser}, func(plug wemo.Found, lost bool) {
		state, where := "found", ""
		if lost {
			state = "lost"
		}

		if len(plug.Locations) > 0 {
			where = plug.Locations[0].URL
		}

		fmt.Fprintf(root.out, "%s %-6s %-40s %s\n", time.Now().Format(time.TimeOnly), state, plug.ID, where)
	})
	if ctx.Err() != nil {
		return nil
	}

	return err
}

func (cmd *describeCommand) Run(ctx context.Context, root *rootCommand) error {
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

	if cmd.Service == "" {
		said := plug.Description()

		fmt.Fprintf(
			root.out,
			"name:        %s\nmodel:       %s %s (%s)\nhardware:    %s\nfirmware:    %s\nserial:      %s\nmac:         %s\nudn:         %s\nurl:         %s\nservices:\n",
			said.Name,
			said.Model,
			said.ModelNumber,
			said.ModelDescription,
			said.Hardware,
			said.Firmware,
			said.Serial,
			said.MAC,
			plug.ID(),
			said.URL,
		)

		for _, service := range plug.Device().Services {
			fmt.Fprintf(root.out, "  %-16s %s\n", wemo.ServiceName(service), service.Type)
		}

		return nil
	}

	service, err := plug.Service(cmd.Service)
	if err != nil {
		return err
	}

	description, err := service.Describe(ctx, root.client)
	if err != nil {
		return fmt.Errorf("%s: %w", cmd.Service, err)
	}

	for _, action := range description.Actions {
		parts := make([]string, 0, len(action.In)+len(action.Out))
		for _, parameter := range action.In {
			parts = append(parts, parameter.Name+":in")
		}

		for _, parameter := range action.Out {
			parts = append(parts, parameter.Name+":out")
		}

		fmt.Fprintf(root.out, "%s(%s)\n", action.Name, strings.Join(parts, ", "))
	}

	return nil
}

func (cmd *getCommand) Run(ctx context.Context, root *rootCommand) error {
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

	on, raw, err := plug.State(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: %s (%s)\n", plug.Description().Name, onOff(on), raw)

	return nil
}

func (cmd *onCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.set(ctx, cmd.Target, true, cmd.For)
}

func (cmd *offCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.set(ctx, cmd.Target, false, cmd.For)
}

func (root *rootCommand) set(ctx context.Context, name string, powered bool, revert time.Duration) error {
	found, err := root.resolve(ctx, name)
	if err != nil {
		return err
	}

	plug := found.plug

	if err := plug.SetState(ctx, powered, revert); err != nil {
		return err
	}

	if revert > 0 {
		fmt.Fprintf(root.out, "%s: %s for %s\n", plug.Description().Name, onOff(powered), revert)
	} else {
		fmt.Fprintf(root.out, "%s: %s\n", plug.Description().Name, onOff(powered))
	}

	return nil
}

func (cmd *toggleCommand) Run(ctx context.Context, root *rootCommand) error {
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

	on, err := plug.Toggle(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: %s\n", plug.Description().Name, onOff(on))

	return nil
}

func (cmd *nameCommand) Run(ctx context.Context, root *rootCommand) error {
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

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
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

	signal, err := plug.Signal(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: %d%%\n", plug.Description().Name, signal)

	return nil
}

func (cmd *timeCommand) Run(ctx context.Context, root *rootCommand) error {
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

	answer, err := plug.TimeSync(ctx, time.Now())
	if err != nil {
		return err
	}

	printArguments(root.out, answer)

	return nil
}

func (cmd *firmwareCommand) Run(ctx context.Context, root *rootCommand) error {
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

	fmt.Fprintf(root.out, "described: %s\n", plug.Description().Firmware)

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

	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

	if !cmd.Yes {
		fmt.Fprintf(root.out, "would %s on %s (%s); add --yes\n", what, plug.Description().Name, host(plug))

		return nil
	}

	answer, err := plug.Reset(ctx, kind)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%s: reset answered %q\n", plug.Description().Name, answer)

	return nil
}

func (cmd *networksCommand) Run(ctx context.Context, root *rootCommand) error {
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

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

	fmt.Fprintf(root.out, "%s: joining %s\n", plug.Description().Name, cmd.SSID)

	err = plug.Join(ctx, cmd.SSID, cmd.Passphrase, wemo.SetupOptions{Timeout: cmd.Timeout})

	switch {
	case err == nil:
		fmt.Fprintf(root.out, "%s: connected to %s\n", plug.Description().Name, cmd.SSID)
	case errors.Is(err, wemo.ErrLeft):
		fmt.Fprintf(
			root.out,
			"%s: took the request and left its access point for %s; discover it there in a minute or two\n",
			plug.Description().Name,
			cmd.SSID,
		)
	default:
		return err
	}

	return nil
}

func (cmd *callCommand) Run(ctx context.Context, root *rootCommand) error {
	found, err := root.resolve(ctx, cmd.Target)
	if err != nil {
		return err
	}

	plug := found.plug

	args := make([]upnp.Argument, 0, len(cmd.Args))

	for _, pair := range cmd.Args {
		name, value, cut := strings.Cut(pair, "=")
		if !cut {
			return fmt.Errorf("%w: argument %q is not name=value", fault.ErrInvalidArgument, pair)
		}

		args = append(args, upnp.Argument{Name: name, Value: value})
	}

	answer, err := plug.Call(ctx, cmd.Service, cmd.Action, args...)
	if err != nil {
		return err
	}

	printArguments(root.out, answer)

	return nil
}

// printArguments prints an answer, in the plug's order.
func printArguments(out io.Writer, answer upnp.Arguments) {
	for _, argument := range answer {
		fmt.Fprintf(out, "%s: %s\n", argument.Name, argument.Value)
	}
}

// host is the host and port a plug was described at.
func host(plug *wemo.Plug) string {
	described, err := url.Parse(plug.Description().URL)
	if err != nil {
		return plug.Description().URL
	}

	return described.Host
}

func onOff(powered bool) string {
	if powered {
		return "on"
	}

	return "off"
}
