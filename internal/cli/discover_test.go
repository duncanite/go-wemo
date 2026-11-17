package cli_test

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/forkcloser/go-upnp/ssdp/ssdptest"

	"github.com/duncanite/go-wemo/internal/cli"
	"github.com/duncanite/go-wemo/wemo"
	"github.com/duncanite/go-wemo/wemo/wemotest"
)

// loopback is where every plug of these tests is, and this host too.
func loopback() netip.Addr {
	return netip.MustParseAddr("127.0.0.1")
}

// usn is the name a plug advertises itself under.
const usn = wemotest.UDN + "::" + wemo.SearchTarget

// TestDiscoverOnAnInterface: a search on the interface named finds the plug
// that is on it, and one on an interface the host does not have finds none.
func TestDiscoverOnAnInterface(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	if out, status := runOn(
		t,
		advertised(plug),
		"--interface",
		"lo0",
		"--wait",
		"300ms",
		"discover",
	); status != cli.ExitOK ||
		!strings.Contains(out, plug.Location()) {
		t.Fatalf("discover on the plug's interface: status %d, output:\n%s", status, out)
	}

	out, status := runOn(t, advertised(plug), "--interface", "en9", "--wait", "300ms", "discover")
	if strings.Contains(out, plug.Location()) {
		t.Fatalf("discover on another interface found the plug: status %d, output:\n%s", status, out)
	}
}

// TestAPlugHeardInTwoPlaces: a plug whose first place does not answer is
// described at its second.
func TestAPlugHeardInTwoPlaces(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	lan, host := ssdptest.NewLAN(), ssdptest.NewHost()

	host.Plug(lan, "lo0", 1, netip.MustParsePrefix("127.0.0.1/8"))

	// Places are told in the order of their URLs: port 1 comes first, and
	// nothing listens there.
	lan.Device(loopback(),
		ssdptest.Advert{USN: usn, Type: wemo.SearchTarget, Location: "http://127.0.0.1:1/setup.xml"},
		ssdptest.Advert{USN: usn, Type: wemo.SearchTarget, Location: plug.Location()},
	)

	out, status := runOn(t, host, "--wait", "300ms", "discover")
	if status != cli.ExitOK || !strings.Contains(out, plug.Location()) || strings.Contains(out, "not described") {
		t.Fatalf("discover of a plug heard in two places: status %d, output:\n%s", status, out)
	}
}

// TestDiscoverListens: after the search, discover says each plug that is
// there, and each that goes, until it is interrupted.
func TestDiscoverListens(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	lan, host := ssdptest.NewLAN(), ssdptest.NewHost()

	host.Plug(lan, "lo0", 1, netip.MustParsePrefix("127.0.0.1/8"))

	device := lan.Device(loopback(), ssdptest.Advert{USN: usn, Type: wemo.SearchTarget, Location: plug.Location()})

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	var out syncBuffer

	done := make(chan int, 1)

	go func() {
		done <- cli.Run(ctx, cli.Environment{
			Args: []string{"--wait", "300ms", "discover", "--listen"},
			Out:  &out, Err: &out, Client: plug.Client(), Network: host,
		})
	}()

	await(t, &out, "Listening for plugs that come and go")
	await(t, &out, " found  "+wemotest.UDN)

	// A plug that started again is the same plug, told again as found.
	device.Boot(2)
	device.Announce()

	deadline := time.Now().Add(5 * time.Second)

	for strings.Count(out.String(), " found  "+wemotest.UDN) < 2 {
		if time.Now().After(deadline) || strings.Contains(out.String(), " lost ") {
			t.Fatalf("a plug that started again was not told as found again:\n%s", out.String())
		}

		time.Sleep(10 * time.Millisecond)
	}

	device.Goodbye()

	await(t, &out, " lost   "+wemotest.UDN)
	cancel()

	if status := <-done; status != cli.ExitOK {
		t.Fatalf("discover --listen: %d\n%s", status, out.String())
	}
}

// TestAPlugThatGoesDuringTheSearch: a plug that says goodbye while the
// search still collects is not among the found.
func TestAPlugThatGoesDuringTheSearch(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	lan, host := ssdptest.NewLAN(), ssdptest.NewHost()

	host.Plug(lan, "lo0", 1, netip.MustParsePrefix("127.0.0.1/8"))

	device := lan.Device(loopback(), ssdptest.Advert{USN: usn, Type: wemo.SearchTarget, Location: plug.Location()})

	// Well inside the search, and well after its start: a goodbye that
	// came before the search began would prove nothing, and fail nothing.
	leaving := time.AfterFunc(500*time.Millisecond, func() {
		// A search is sent more than once: a device that is gone
		// answers none of them.
		device.Mute(true)
		device.Goodbye()
	})
	defer leaving.Stop()

	out, status := runOn(t, host, "--wait", "2s", "discover")
	if status != cli.ExitOK || !strings.Contains(out, "No plug answered.") {
		t.Fatalf("discover while the plug leaves: status %d, output:\n%s", status, out)
	}
}

// TestWatchWhereItIsTold: a watch listens at the callback it is given, and
// a watch of a plug found by its name listens at this host's address on the
// plug's network.
func TestWatchWhereItIsTold(t *testing.T) {
	t.Parallel()

	plug := wemotest.New()
	t.Cleanup(plug.Close)

	// A port that was free a moment ago.
	held, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	_, port, err := net.SplitHostPort(held.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	if err = held.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}

	for _, row := range []struct {
		name string
		args []string
		want string
	}{
		{"the callback given", []string{"watch", plug.Host(), "--callback", "127.0.0.1:" + port}, "callback http://127.0.0.1:" + port + "/"},
		{"a plug found by name", []string{"--wait", "300ms", "watch", "kitchen lamp"}, "callback http://127.0.0.1:"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)

		var out syncBuffer

		done := make(chan int, 1)

		go func() {
			done <- cli.Run(ctx, cli.Environment{
				Args: row.args, Out: &out, Err: &out, Client: plug.Client(), Network: advertised(plug),
			})
		}()

		await(t, &out, "BinaryState = 0")
		cancel()

		if status := <-done; status != cli.ExitOK || !strings.Contains(out.String(), row.want) {
			t.Fatalf("%s: status %d, output:\n%s", row.name, status, out.String())
		}
	}
}
