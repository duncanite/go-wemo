package cli

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-wemo/upnp"
	"github.com/duncanite/go-wemo/wemo"
)

// closeGrace bounds the unsubscribes and the listener's shutdown on exit.
const closeGrace = 5 * time.Second

// Run subscribes to every target's basicevent and prints the events as
// they come, renewing each lease at two thirds, until the context ends.
// The first event after a subscription is the plug's current state.
func (cmd *watchCommand) Run(ctx context.Context, root *rootCommand) error {
	plugs := make([]*wemo.Plug, 0, len(cmd.Targets))

	for _, name := range cmd.Targets {
		plug, err := root.resolve(ctx, name)
		if err != nil {
			return err
		}

		plugs = append(plugs, plug)
	}

	callback := cmd.Callback
	if callback == "" {
		host, err := localHostToward(ctx, plugs[0].Base.Host)
		if err != nil {
			return err
		}

		callback = net.JoinHostPort(host, "0")
	}

	listener, err := upnp.Listen(ctx, callback)
	if err != nil {
		return err
	}

	defer func() {
		grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeGrace)
		defer cancel()

		_ = listener.Close(grace)
	}()

	subs, names, err := subscribeAll(ctx, root, plugs, listener, cmd.Lease)

	defer func() {
		grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeGrace)
		defer cancel()

		for _, sub := range subs {
			if unsubErr := sub.Unsubscribe(grace); unsubErr != nil {
				fmt.Fprintf(root.out, "%s: %v\n", names[sub.SID], unsubErr)
			}
		}
	}()

	if err != nil {
		return err
	}

	return printEvents(ctx, root, listener, subs, names, cmd.Lease, cmd.FirstEvent)
}

// subscribeAll subscribes to every plug and returns the subscriptions made,
// those before the failure too, so the caller can end them.
func subscribeAll(
	ctx context.Context, root *rootCommand, plugs []*wemo.Plug, listener *upnp.Listener, lease time.Duration,
) ([]*upnp.Subscription, map[string]string, error) {
	names := make(map[string]string, len(plugs))
	subs := make([]*upnp.Subscription, 0, len(plugs))

	for _, plug := range plugs {
		sub, err := plug.Watch(ctx, listener, lease)
		if err != nil {
			return subs, names, fmt.Errorf("%s: %w", plug.FriendlyName, err)
		}

		names[sub.SID] = plug.FriendlyName
		subs = append(subs, sub)

		fmt.Fprintf(root.out, "%s: subscribed %s until %s; callback %s\n",
			plug.FriendlyName, sub.SID, sub.Expires.Format(time.TimeOnly), listener.Address())
	}

	return subs, names, nil
}

// printEvents prints every event and renews every lease at two thirds of
// the shortest the devices granted, until the context ends. A plug's
// first event, its current state, comes within a second of subscribing on
// its own network; a subscription with none by firstEventWithin never
// reached the callback, and that is an error with the reason, not a wait.
func printEvents(
	ctx context.Context, root *rootCommand, listener *upnp.Listener,
	subs []*upnp.Subscription, names map[string]string, lease, firstEventWithin time.Duration,
) error {
	renew := time.NewTicker(renewalPeriod(granted(subs)))
	defer renew.Stop()

	pending := make(map[string]struct{}, len(subs))
	for _, sub := range subs {
		pending[sub.SID] = struct{}{}
	}

	first := time.NewTimer(firstEventWithin)
	defer first.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-listener.Events():
			delete(pending, event.SID)

			for name, value := range event.Properties {
				fmt.Fprintf(root.out, "%s %-24s %s = %s\n",
					event.Received.Format(time.TimeOnly), names[event.SID], name, value)
			}
		case <-first.C:
			if len(pending) > 0 {
				return fmt.Errorf("%w: no event from %s within %s: the plug cannot reach the callback %s",
					fault.ErrTimeout, silent(pending, names), firstEventWithin, listener.Address())
			}
		case <-renew.C:
			if err := renewAll(ctx, subs, names, lease); err != nil {
				return err
			}

			renew.Reset(renewalPeriod(granted(subs)))
		}
	}
}

// renewAll renews every subscription for lease.
func renewAll(ctx context.Context, subs []*upnp.Subscription, names map[string]string, lease time.Duration) error {
	for _, sub := range subs {
		if err := sub.Renew(ctx, lease); err != nil {
			return fmt.Errorf("%s: renewing: %w", names[sub.SID], err)
		}
	}

	return nil
}

// granted is the shortest lease the devices gave, from now.
func granted(subs []*upnp.Subscription) time.Duration {
	shortest := time.Duration(0)

	for _, sub := range subs {
		if left := time.Until(sub.Expires); shortest == 0 || left < shortest {
			shortest = left
		}
	}

	return shortest
}

// silent names the plugs still without an event.
func silent(pending map[string]struct{}, names map[string]string) string {
	var list []string
	for sid := range pending {
		list = append(list, names[sid])
	}

	sort.Strings(list)

	return strings.Join(list, ", ")
}

// renewalPeriod is two thirds of the lease, so a renewal lands well before
// the device drops the subscription; a lease already gone renews at once.
func renewalPeriod(lease time.Duration) time.Duration {
	const (
		num = 2
		den = 3
	)

	return max(lease*num/den, time.Second)
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
