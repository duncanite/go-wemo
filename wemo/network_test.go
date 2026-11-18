package wemo_test

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/duncanite/go-wemo/wemo"
	"github.com/duncanite/go-wemo/wemo/wemotest"
)

// TestRecognize: a plug is known by the device type in its advertisement's
// name, at its version or a later one, and is who its UDN says; what else a
// plug advertises under that UDN, and what anything else advertises, is not
// one.
func TestRecognize(t *testing.T) {
	t.Parallel()

	local := netip.MustParseAddr("192.0.2.10")
	here := wemo.Location{URL: "http://192.0.2.20:49153/setup.xml", Local: local}
	there := wemo.Location{URL: "http://198.51.100.20:49153/setup.xml"}

	for _, row := range []struct {
		name      string
		usn       string
		locations []wemo.Location
		id        wemo.ID
		want      []wemo.Location
		is        bool
	}{
		{
			name: "a plug", usn: wemotest.UDN + "::urn:Belkin:device:controllee:1",
			locations: []wemo.Location{here}, id: wemotest.UDN, want: []wemo.Location{here}, is: true,
		},
		{
			name: "a plug heard in two places", usn: wemotest.UDN + "::urn:Belkin:device:controllee:1",
			locations: []wemo.Location{here, there}, id: wemotest.UDN, want: []wemo.Location{here, there}, is: true,
		},
		{
			name: "a later version", usn: wemotest.UDN + "::urn:Belkin:device:controllee:2",
			locations: []wemo.Location{here}, id: wemotest.UDN, want: []wemo.Location{here}, is: true,
		},
		{
			name: "a place with no URL", usn: wemotest.UDN + "::urn:Belkin:device:controllee:1",
			locations: []wemo.Location{{Local: local}, here}, id: wemotest.UDN, want: []wemo.Location{here}, is: true,
		},
		{
			name: "no place at all", usn: wemotest.UDN + "::urn:Belkin:device:controllee:1",
			id: wemotest.UDN, want: []wemo.Location{}, is: true,
		},
		{name: "its root device", usn: wemotest.UDN + "::upnp:rootdevice", locations: []wemo.Location{here}},
		{name: "its UDN alone", usn: wemotest.UDN, locations: []wemo.Location{here}},
		{name: "one of its services", usn: wemotest.UDN + "::urn:Belkin:service:basicevent:1", locations: []wemo.Location{here}},
		{name: "another Belkin device", usn: wemotest.UDN + "::urn:Belkin:device:bridge:1", locations: []wemo.Location{here}},
		{name: "a type that only starts so", usn: wemotest.UDN + "::urn:Belkin:device:controllee:1:x", locations: []wemo.Location{here}},
		{name: "no version", usn: wemotest.UDN + "::urn:Belkin:device:controllee:", locations: []wemo.Location{here}},
		{name: "version nought", usn: wemotest.UDN + "::urn:Belkin:device:controllee:0", locations: []wemo.Location{here}},
		{name: "a version that is no number", usn: wemotest.UDN + "::urn:Belkin:device:controllee:one", locations: []wemo.Location{here}},
		{name: "no UDN", usn: "::urn:Belkin:device:controllee:1", locations: []wemo.Location{here}},
		{name: "a UDN cut short", usn: "uuid::urn:Belkin:device:controllee:1", locations: []wemo.Location{here}},
		{name: "a UDN that is no uuid", usn: "Socket-1_0-1::urn:Belkin:device:controllee:1", locations: []wemo.Location{here}},
		{name: "nothing", usn: ""},
	} {
		found, is := wemo.Recognize(wemo.Advertisement{USN: row.usn, Locations: row.locations})
		if is != row.is || found.ID != row.id || !reflect.DeepEqual(found.Locations, row.want) {
			t.Errorf("%s: Recognize = %+v, %v; want %q at %v, %v", row.name, found, is, row.id, row.want, row.is)
		}
	}
}

// TestRecognizeKeepsNothingOfWhatItWasGiven: what Found holds is its own.
func TestRecognizeKeepsNothingOfWhatItWasGiven(t *testing.T) {
	t.Parallel()

	given := []wemo.Location{{URL: "http://192.0.2.20:49153/setup.xml"}}

	found, _ := wemo.Recognize(wemo.Advertisement{USN: wemotest.UDN + "::" + wemo.SearchTarget, Locations: given})

	given[0].URL = "http://203.0.113.1/"

	if found.Locations[0].URL != "http://192.0.2.20:49153/setup.xml" {
		t.Fatalf("Found changed with what Recognize was given: %+v", found)
	}
}

// TestTheSamePlugFoundAndOpened: who a plug says it is on the network is who
// it is once opened.
func TestTheSamePlugFoundAndOpened(t *testing.T) {
	t.Parallel()

	fake := wemotest.New()
	t.Cleanup(fake.Close)

	found, is := wemo.Recognize(wemo.Advertisement{
		USN:       wemotest.UDN + "::" + wemo.SearchTarget,
		Locations: []wemo.Location{{URL: fake.Location()}},
	})
	if !is {
		t.Fatal("the plug's own advertisement is not recognized")
	}

	plug, err := wemo.Open(t.Context(), fake.Client(), found.Locations[0].URL)
	if err != nil {
		t.Fatal(err)
	}

	if plug.ID() != found.ID {
		t.Fatalf("found as %q, opened as %q", found.ID, plug.ID())
	}
}

// browser is a Browser that tells what it was made with, then ends as it
// was made to.
type browser struct {
	asked string
	tells []told
	ends  error
}

type told struct {
	advertisement wemo.Advertisement
	lost          bool
}

func (b *browser) Browse(_ context.Context, target string, handle func(wemo.Advertisement, bool)) error {
	b.asked = target

	for _, each := range b.tells {
		handle(each.advertisement, each.lost)
	}

	return b.ends
}

var errBrowsing = errors.New("the browser can browse no more")

// TestFind: Find browses for what a plug advertises, tells of every plug
// each time the browser does, found or lost, and of nothing else, and ends
// as the browser ends.
func TestFind(t *testing.T) {
	t.Parallel()

	here := wemo.Location{URL: "http://192.0.2.20:49153/setup.xml", Local: netip.MustParseAddr("192.0.2.10")}
	plug := wemo.Advertisement{USN: wemotest.UDN + "::" + wemo.SearchTarget, Locations: []wemo.Location{here}}
	other := wemo.Advertisement{USN: "uuid:Socket-1_0-2::" + wemo.SearchTarget, Locations: []wemo.Location{here}}
	stranger := wemo.Advertisement{
		USN:       "uuid:1::urn:schemas-upnp-org:device:MediaRenderer:1",
		Locations: []wemo.Location{here},
	}

	lent := &browser{
		tells: []told{{plug, false}, {stranger, false}, {other, false}, {plug, false}, {stranger, true}, {plug, true}},
		ends:  errBrowsing,
	}

	type heard struct {
		id   wemo.ID
		lost bool
	}

	var all []heard

	err := wemo.Find(t.Context(), lent, func(found wemo.Found, lost bool) {
		all = append(all, heard{found.ID, lost})
	})

	want := []heard{{wemotest.UDN, false}, {"uuid:Socket-1_0-2", false}, {wemotest.UDN, false}, {wemotest.UDN, true}}

	if !errors.Is(err, errBrowsing) || lent.asked != wemo.SearchTarget || !reflect.DeepEqual(all, want) {
		t.Fatalf("Find asked for %q, told %v, ended %v; want %q, %v, %v",
			lent.asked, all, err, wemo.SearchTarget, want, errBrowsing)
	}
}
