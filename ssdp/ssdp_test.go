package ssdp_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/duncanite/go-wemo/ssdp"
	"github.com/duncanite/go-wemo/upnp/upnptest"
)

// TestSearchFindsThePlugOnce: the search goes out three times and the
// plug answers each; the result holds the plug once, by USN, with the
// location of its description.
func TestSearchFindsThePlugOnce(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	answers, err := ssdp.Search(
		t.Context(),
		ssdp.Options{Target: plug.ServeSSDP(), SearchTarget: ssdp.Belkin, Wait: 2 * time.Second},
	)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(answers) != 1 {
		t.Fatalf("answers = %d, want the plug once: %+v", len(answers), answers)
	}

	answer := answers[0]
	if answer.Location != plug.Location() || answer.Type != ssdp.Belkin || answer.USN != upnptest.UDN+"::"+ssdp.Belkin {
		t.Fatalf("answer = %+v", answer)
	}

	if answer.MaxAge != 86400*time.Second || answer.From == nil {
		t.Fatalf("max-age %s from %v", answer.MaxAge, answer.From)
	}

	if searches := plug.Searches.Load(); searches != 3 {
		t.Fatalf("the plug received %d searches, want three", searches)
	}
}

// TestSearchNobodyAnswers: a target that never answers is ErrNoAnswer once
// the wait and its grace are over.
func TestSearchNobodyAnswers(t *testing.T) {
	t.Parallel()

	silent, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = silent.Close() })

	started := time.Now()

	_, err = ssdp.Search(t.Context(), ssdp.Options{Target: silent.LocalAddr().String(), Wait: time.Second})
	if !errors.Is(err, ssdp.ErrNoAnswer) {
		t.Fatalf("err = %v, want ErrNoAnswer", err)
	}

	// The wait, then a second's grace for a late answer: under two seconds
	// the grace is gone, over four something else is wrong.
	if took := time.Since(started); took < 2*time.Second || took > 4*time.Second {
		t.Fatalf("took %s, want the wait plus its grace", took)
	}
}

// TestSearchStopsWithTheContext: a cancelled context ends the collection
// at once, with what arrived so far.
func TestSearchStopsWithTheContext(t *testing.T) {
	t.Parallel()

	plug := upnptest.New()
	t.Cleanup(plug.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	started := time.Now()

	answers, err := ssdp.Search(ctx, ssdp.Options{Target: plug.ServeSSDP(), Wait: 10 * time.Second})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(answers) != 1 || time.Since(started) > 2*time.Second {
		t.Fatalf("answers = %d after %s", len(answers), time.Since(started))
	}
}

// TestSearchRefusesABadTarget: an address that does not parse is an
// argument error, before any socket.
func TestSearchRefusesABadTarget(t *testing.T) {
	t.Parallel()

	if _, err := ssdp.Search(t.Context(), ssdp.Options{Target: "not an address"}); err == nil {
		t.Fatal("a bad target was accepted")
	}

	if _, err := ssdp.Search(t.Context(), ssdp.Options{Interface: "no-such-interface0"}); err == nil {
		t.Fatal("a missing interface was accepted")
	}
}
