package wemo

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	upnp "github.com/forkcloser/go-upnp"
)

// binaryState is the variable the switch is, in an answer and in an event.
const binaryState = "BinaryState"

// switched says whether a BinaryState is on: a Wemo Insight says 8 for on
// with nothing drawing power.
func switched(raw string) bool {
	return raw != "0" && raw != ""
}

func flag(on bool) string {
	if on {
		return "1"
	}

	return "0"
}

// State is the switch: on or off. The raw value is returned too: a Wemo
// Insight says 8 for on with nothing drawing power.
func (p *Plug) State(ctx context.Context) (bool, string, error) {
	answer, err := p.basic(ctx, "GetBinaryState")
	if err != nil {
		return false, "", err
	}

	raw := value(answer, binaryState)

	return switched(raw), raw, nil
}

// SetState switches the plug. A duration above zero asks the plug to
// revert on its own when it ends, through its countdown.
func (p *Plug) SetState(ctx context.Context, on bool, revert time.Duration) error {
	args := []upnp.Argument{{Name: binaryState, Value: flag(on)}}

	if revert > 0 {
		args = append(args,
			upnp.Argument{Name: "Duration", Value: strconv.Itoa(int(revert / time.Second))},
			upnp.Argument{Name: "EndAction", Value: "0"},
		)
	}

	answer, err := p.basic(ctx, "SetBinaryState", args...)
	if err != nil {
		return err
	}

	// The plug answers the state it took, or "Error" when it took none.
	if strings.EqualFold(value(answer, binaryState), "error") {
		return fmt.Errorf("%w: the plug took no state", ErrRefused)
	}

	return nil
}

// Toggle flips the switch and returns the new state.
func (p *Plug) Toggle(ctx context.Context) (bool, error) {
	powered, _, err := p.State(ctx)
	if err != nil {
		return false, err
	}

	if err := p.SetState(ctx, !powered, 0); err != nil {
		return false, err
	}

	return !powered, nil
}

// Name is the friendly name as the plug holds it now, which the
// description read at open may lag.
func (p *Plug) Name(ctx context.Context) (string, error) {
	answer, err := p.basic(ctx, "GetFriendlyName")
	if err != nil {
		return "", err
	}

	return value(answer, "FriendlyName"), nil
}

// Rename sets the friendly name.
func (p *Plug) Rename(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: an empty name", ErrArgument)
	}

	_, err := p.basic(ctx, "ChangeFriendlyName", upnp.Argument{Name: "FriendlyName", Value: name})

	return err
}

// Signal is the Wi-Fi signal strength as the plug reports it, a
// percentage.
func (p *Plug) Signal(ctx context.Context) (int, error) {
	answer, err := p.basic(ctx, "GetSignalStrength")
	if err != nil {
		return 0, err
	}

	raw := value(answer, "SignalStrength")

	strength, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: signal strength %q", ErrAnswer, raw)
	}

	return strength, nil
}

// ResetKind is what a reset clears.
type ResetKind int

// The resets a plug takes, by the code its ReSetup action reads.
const (
	// ResetData clears the name, the icon and the rules.
	ResetData ResetKind = 1
	// ResetAll is a factory reset: data and Wi-Fi.
	ResetAll ResetKind = 2
	// ResetWiFi forgets the network and raises the setup access point.
	ResetWiFi ResetKind = 5
)

// Reset asks the plug to reset, and returns its answer. The plug answers
// once and then goes; a network error after the answer is the plug leaving,
// not a failure.
func (p *Plug) Reset(ctx context.Context, kind ResetKind) (string, error) {
	answer, err := p.basic(ctx, "ReSetup", upnp.Argument{Name: "Reset", Value: strconv.Itoa(int(kind))})
	if err != nil {
		return "", err
	}

	return value(answer, "Reset"), nil
}

// AutoUpdate turns the plug's automatic firmware update on or off, where
// the firmware has the switch; a plug without it answers a fault.
func (p *Plug) AutoUpdate(ctx context.Context, enabled bool) error {
	_, err := p.basic(ctx, "setAutoFWUpdate", upnp.Argument{Name: "enableAutoUpdate", Value: flag(enabled)})

	return err
}

// FirmwareVersion asks the firmware service for the running version; the
// description carries it too, as read at open.
func (p *Plug) FirmwareVersion(ctx context.Context) (upnp.Arguments, error) {
	return p.invoke(ctx, Firmware, "GetFirmwareVersion")
}

// TimeSync sets the plug's clock and zone from now: the time in UTC
// seconds, the zone as an offset in hours, and whether daylight saving is
// in force and observed. A plug reset keeps its zone at zero and reads its
// rules as UTC until this is called.
func (p *Plug) TimeSync(ctx context.Context, now time.Time) (upnp.Arguments, error) {
	const secondsPerHour = 3600

	_, offset := now.Zone()

	return p.invoke(ctx, TimeSyncS, "TimeSync",
		upnp.Argument{Name: "UTC", Value: strconv.FormatInt(now.Unix(), 10)},
		upnp.Argument{Name: "TimeZone", Value: strconv.FormatFloat(float64(offset)/secondsPerHour, 'f', 2, 64)},
		upnp.Argument{Name: "dst", Value: flag(now.IsDST())},
		upnp.Argument{Name: "DstSupported", Value: "1"},
	)
}
