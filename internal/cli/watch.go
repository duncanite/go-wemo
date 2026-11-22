package cli

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-wemo/internal/terminal"
	"github.com/duncanite/go-wemo/wemo"
)

// ended is one plug's Watch that returned.
type ended struct {
	name string
	err  error
}

// Run watches every target and prints the events as they come, until the
// context ends. The first event of a plug is its current state.
func (cmd *watchCommand) Run(ctx context.Context, root *rootCommand) error {
	plugs := make([]located, 0, len(cmd.Targets))

	for _, name := range cmd.Targets {
		plug, err := root.resolve(ctx, name)
		if err != nil {
			return err
		}

		plugs = append(plugs, plug)
	}

	listen, err := cmd.listen(ctx, plugs[0])
	if err != nil {
		return err
	}

	events, err := wemo.Listen(ctx, listen)
	if err != nil {
		return err
	}

	defer func() { _ = events.Close() }()

	// The plugs reach the listener at the host it was asked to listen
	// at, and the port it took.
	reached, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("%w: the callback %q is not host:port", fault.ErrInvalidArgument, listen)
	}

	callback := "http://" + net.JoinHostPort(reached, strconv.Itoa(int(events.Addr().Port()))) + "/"

	return cmd.print(ctx, root, plugs, events, callback)
}

// listen is where the listener listens: the callback given, or this host's
// address on the first plug's network and a free port.
func (cmd *watchCommand) listen(ctx context.Context, first located) (string, error) {
	if cmd.Callback != "" {
		return cmd.Callback, nil
	}

	if first.local.IsValid() {
		return net.JoinHostPort(first.local.String(), "0"), nil
	}

	toward, err := localHostToward(ctx, host(first.plug))
	if err != nil {
		return "", err
	}

	return net.JoinHostPort(toward, "0"), nil
}

// print watches every plug, each on a goroutine of its own, and prints
// what each says. A plug's first event, its current state, comes within a
// second of the Watch on its own network; a plug with none by the first
// event's time never reached the callback, and that is an error with the
// reason, not a wait.
func (cmd *watchCommand) print(
	ctx context.Context, root *rootCommand, plugs []located, events *wemo.Events, callback string,
) error {
	watching, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		printing sync.Mutex
		heard    = make(chan int, len(plugs))
		over     = make(chan ended, len(plugs))
		silent   = make(map[int]string, len(plugs))
	)

	for index, each := range plugs {
		name := each.plug.Description().Name
		silent[index] = name

		terminal.Printf(root.out, "%s: watching; callback %s\n", name, callback)
	}

	for index, each := range plugs {
		go func() {
			name := each.plug.Description().Name

			over <- ended{name: name, err: follow(watching, root, each.plug, events, callback, &printing, func() {
				heard <- index
			})}
		}()
	}

	// Every Watch has returned before the output is the caller's again.
	left := len(plugs)

	defer func() {
		cancel()

		for ; left > 0; left-- {
			<-over
		}
	}()

	late := time.NewTimer(cmd.FirstEvent)
	defer late.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case index := <-heard:
			delete(silent, index)
		case <-late.C:
			if len(silent) > 0 {
				return fmt.Errorf("%w: no event from %s within %s: the plug cannot reach the callback %s",
					fault.ErrTimeout, names(silent), cmd.FirstEvent, callback)
			}
		case end := <-over:
			left--

			if ctx.Err() != nil {
				return nil
			}

			return fmt.Errorf("%s: %w", end.name, end.err)
		}
	}
}

// follow watches one plug and prints what it says, each event whole and
// apart from every other plug's; first is called once, at its first event.
func follow(
	ctx context.Context, root *rootCommand, plug *wemo.Plug, events *wemo.Events, callback string,
	printing *sync.Mutex, first func(),
) error {
	name, told := plug.Description().Name, false

	return plug.Watch(ctx, events, callback, func(event wemo.Event) {
		printing.Lock()
		defer printing.Unlock()

		for _, property := range event.Properties {
			terminal.Printf(root.out, "%s %-24s %s = %s\n",
				time.Now().Format(time.TimeOnly), name, property.Name, property.Value)
		}

		if !told {
			told = true

			first()
		}
	})
}

// names lists the plugs still without an event.
func names(silent map[int]string) string {
	list := make([]string, 0, len(silent))
	for _, name := range silent {
		list = append(list, name)
	}

	sort.Strings(list)

	return strings.Join(list, ", ")
}

// localHostToward is this host's address on the way to host, read off a
// connected UDP socket, which sends nothing: the callback URL has to name
// an address the plug can reach, and a host with several interfaces has
// one per network.
func localHostToward(ctx context.Context, host string) (string, error) {
	target := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		target = net.JoinHostPort(host, "9")
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", target)
	if err != nil {
		return "", fmt.Errorf("%w: finding this host's address toward %s: %w", fault.ErrNetworkCommunication, host, err)
	}

	defer func() { _ = conn.Close() }()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("%w: the local address is not a UDP address", fault.ErrSystemFailure)
	}

	return addr.IP.String(), nil
}
