package wemo

import (
	"context"
	"net/netip"
	"strconv"
	"strings"
)

// SearchTarget is what a Wemo plug advertises itself as on the local network,
// over SSDP, and what a search for plugs asks for: its device type.
const SearchTarget = "urn:Belkin:device:controllee:1"

// Advertisement is one SSDP advertisement as it was heard on the network:
// what this package reads of it, and nothing of the means that heard it.
type Advertisement struct {
	// USN is its name, as the device wrote it: the device's UDN, then
	// "::" and what it advertises.
	USN string
	// Locations is where it was heard to be.
	Locations []Location
}

// Location is one place a plug is at, as it was heard from this host.
type Location struct {
	// URL is the plug's description, as Open takes it.
	URL string
	// Local is this host's address on the network the plug was heard on:
	// what the plug can call back, and what a Watch's callback is made
	// of. It is not valid when that is not known.
	Local netip.Addr
}

// Browser is the means of finding devices on the local network, which this
// package does not have and is lent: SSDP's port is one to a host, and it is
// the program's. Whatever browses satisfies it with a few lines.
type Browser interface {
	// Browse tells handle of the advertisements of a search target: each
	// one it knows of or hears, and each change to one, whole as it then
	// stands, with lost false; and each one that is gone, as it last
	// stood, with lost true. Calls come one at a time and in order, for
	// as long as ctx lasts. It returns ctx's error when ctx ends, and its
	// own when it can browse no more.
	//
	// One that is gone has to come with its USN: Find knows a plug by it.
	Browse(ctx context.Context, target string, handle func(advertisement Advertisement, lost bool)) error
}

// Found is what a device on the network says of itself: that it is a Wemo
// plug, which one, and where.
//
// An advertisement is anyone's to make, and proves nothing: a plug has no
// credential, and whatever answers at an address is taken for the plug it
// says it is. Plug.ID, read once it is opened, is what the description there
// says, and no more.
type Found struct {
	// ID is who it says it is: the value Plug.ID gives once that plug is
	// opened.
	ID ID
	// Locations is where it says it is, in the order they came: none
	// when the advertisement had none. Which of several reaches the plug
	// from here is the caller's to know, or to try.
	Locations []Location
}

// Recognize says whether an advertisement is a Wemo plug's, and which plug.
// A plug advertises several things under one UDN; the one taken is its
// device type, SearchTarget, at that version or a later one.
func Recognize(advertisement Advertisement) (Found, bool) {
	udn, kind, cut := strings.Cut(advertisement.USN, "::")
	if !cut || !isPlug(kind) || !strings.HasPrefix(udn, "uuid:") {
		return Found{}, false
	}

	found := Found{ID: ID(udn), Locations: make([]Location, 0, len(advertisement.Locations))}

	for _, location := range advertisement.Locations {
		if location.URL != "" {
			found.Locations = append(found.Locations, location)
		}
	}

	return found, true
}

// isPlug says whether a device type is SearchTarget's, at its version or a
// later one.
func isPlug(kind string) bool {
	end := strings.LastIndex(SearchTarget, ":") + 1

	written, cut := strings.CutPrefix(kind, SearchTarget[:end])
	if !cut {
		return false
	}

	version, err := strconv.ParseUint(written, 10, 16)

	return err == nil && version > 0
}

// Find tells handle of the Wemo plugs on the local network, through the
// browser it is lent: each one found, and again each time the browser tells
// of it again, whether or not what Found holds changed, with lost false; each
// one gone, with lost true. It is the browser's Browse for SearchTarget,
// narrowed to what Recognize takes, with nothing kept between two calls, and
// lasts and ends as that does.
func Find(ctx context.Context, browser Browser, handle func(found Found, lost bool)) error {
	return browser.Browse(ctx, SearchTarget, func(advertisement Advertisement, lost bool) {
		if found, ok := Recognize(advertisement); ok {
			handle(found, lost)
		}
	})
}
