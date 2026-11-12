// Package wemo is a Wi-Fi Wemo plug as a program drives it on the LAN:
// found by SSDP or by address, read and switched through its basicevent
// service, heard through UPnP eventing, and set up onto a network without
// Belkin. Every verb is a SOAP action the plug itself declares; Call
// reaches the ones with no verb of their own.
package wemo

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-wemo/ssdp"
	"github.com/duncanite/go-wemo/upnp"
)

// Ports are the ports a Wemo serves its description on, most common first;
// the port drifts among them after a reboot, so a host is probed through
// the list.
func Ports() []int {
	return []int{
		49153,
		49152,
		49154,
		49151,
		49155,
		49156,
		49157,
		49158,
		49159,
	}
}

// Plug is one Wemo device: its description and the services on it.
type Plug struct {
	*upnp.Device
}

// Services a plug exposes, by their short names.
const (
	BasicEvent = "basicevent"
	WiFiSetup  = "WiFiSetup"
	TimeSyncS  = "timesync"
	Firmware   = "firmwareupdate"
	MetaInfo   = "metainfo"
	DeviceInfo = "deviceinfo"
	Rules      = "rules"
)

// Discover searches the network and describes every Wemo that answers.
// Devices that answer but will not be described are skipped with their
// error returned alongside, so one odd device does not hide the others.
func Discover(ctx context.Context, client *http.Client, opts ssdp.Options) ([]*Plug, []error, error) {
	if opts.SearchTarget == "" {
		opts.SearchTarget = ssdp.Belkin
	}

	answers, err := ssdp.Search(ctx, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("discovering: %w", err)
	}

	var (
		plugs    []*Plug
		failures []error
	)

	for _, answer := range answers {
		device, err := upnp.Describe(ctx, client, answer.Location)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", answer.Location, err))

			continue
		}

		plugs = append(plugs, &Plug{Device: device})
	}

	return plugs, failures, nil
}

// Open describes the plug at target: a URL is read as the description, a
// host with a port is read at that port, a bare host is probed through
// Ports.
func Open(ctx context.Context, client *http.Client, target string) (*Plug, error) {
	if strings.Contains(target, "://") {
		device, err := upnp.Describe(ctx, client, target)
		if err != nil {
			return nil, err
		}

		return &Plug{Device: device}, nil
	}

	host, port, err := net.SplitHostPort(target)
	if err == nil {
		device, err := upnp.Describe(
			ctx,
			client,
			(&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/setup.xml"}).String(),
		)
		if err != nil {
			return nil, err
		}

		return &Plug{Device: device}, nil
	}

	var last error

	for _, candidate := range Ports() {
		location := (&url.URL{Scheme: "http", Host: net.JoinHostPort(target, strconv.Itoa(candidate)), Path: "/setup.xml"}).String()

		device, err := upnp.Describe(ctx, client, location)
		if err == nil {
			return &Plug{Device: device}, nil
		}

		last = err

		if ctx.Err() != nil {
			break
		}
	}

	return nil, fmt.Errorf("%w: no Wemo answered on %s at any of its ports: %w", fault.ErrNotFound, target, last)
}

// Call invokes any action the plug declares on the named service, the
// arguments in the order the service description lists them. The answer
// is returned whole.
func (p *Plug) Call(ctx context.Context, service, action string, args upnp.Arguments) (upnp.Arguments, error) {
	svc, err := p.Service(service)
	if err != nil {
		return nil, err
	}

	description, err := svc.Describe(ctx)
	if err != nil {
		return nil, err
	}

	declared, err := description.Action(action)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", service, err)
	}

	// Every argument passed goes out, in the order the device declares
	// them, whatever direction it claims for each: the description gets
	// directions wrong both ways, and an argument silently dropped is a
	// call that lies about what it sent.
	order := make([]string, 0, len(declared.Arguments))
	for _, arg := range declared.Arguments {
		if _, passed := args[arg.Name]; passed {
			order = append(order, arg.Name)
		}
	}

	for name := range args {
		if _, ok := declared.Argument(name); !ok {
			inputs := make([]string, 0, len(declared.Arguments))
			for _, arg := range declared.In() {
				inputs = append(inputs, arg.Name)
			}

			return nil, fmt.Errorf(
				"%w: %s.%s takes no argument %q; it declares %v",
				fault.ErrInvalidArgument,
				service,
				action,
				name,
				inputs,
			)
		}
	}

	return svc.Call(ctx, action, args, order...)
}

// State is the switch: on or off. The raw value is returned too: a Wemo
// Insight says 8 for on with nothing drawing power.
func (p *Plug) State(ctx context.Context) (bool, string, error) {
	answer, err := p.basic(ctx, "GetBinaryState", nil)
	if err != nil {
		return false, "", err
	}

	raw := strings.TrimSpace(answer["BinaryState"])

	return raw != "0" && raw != "", raw, nil
}

// SetState switches the plug. A duration above zero asks the plug to
// revert on its own when it ends, through its countdown.
func (p *Plug) SetState(ctx context.Context, on bool, revert time.Duration) error {
	args := upnp.Arguments{"BinaryState": "0"}
	order := []string{"BinaryState"}

	if on {
		args["BinaryState"] = "1"
	}

	if revert > 0 {
		args["Duration"] = strconv.Itoa(int(revert / time.Second))
		args["EndAction"] = "0"

		order = append(order, "Duration", "EndAction")
	}

	answer, err := p.basic(ctx, "SetBinaryState", args, order...)
	if err != nil {
		return err
	}

	// The plug answers the state it took, or "Error" when it took none.
	if got := strings.TrimSpace(answer["BinaryState"]); strings.EqualFold(got, "error") {
		return fmt.Errorf("%w: the plug refused the state", fault.ErrUnacceptableResponse)
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
	answer, err := p.basic(ctx, "GetFriendlyName", nil)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(answer["FriendlyName"]), nil
}

// Rename sets the friendly name.
func (p *Plug) Rename(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: an empty name", fault.ErrInvalidArgument)
	}

	_, err := p.basic(ctx, "ChangeFriendlyName", upnp.Arguments{"FriendlyName": name}, "FriendlyName")

	return err
}

// Signal is the Wi-Fi signal strength as the plug reports it, a
// percentage.
func (p *Plug) Signal(ctx context.Context) (int, error) {
	answer, err := p.basic(ctx, "GetSignalStrength", nil)
	if err != nil {
		return 0, err
	}

	value, err := strconv.Atoi(strings.TrimSpace(answer["SignalStrength"]))
	if err != nil {
		return 0, fmt.Errorf("%w: signal strength %q", fault.ErrUnacceptableResponse, answer["SignalStrength"])
	}

	return value, nil
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

// Reset asks the plug to reset. The plug answers once and then goes; a
// network error after the answer is the plug leaving, not a failure.
func (p *Plug) Reset(ctx context.Context, kind ResetKind) (string, error) {
	answer, err := p.basic(ctx, "ReSetup", upnp.Arguments{"Reset": strconv.Itoa(int(kind))}, "Reset")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(answer["Reset"]), nil
}

// AutoUpdate turns the plug's automatic firmware update on or off, where
// the firmware has the switch; a plug without it answers a fault.
func (p *Plug) AutoUpdate(ctx context.Context, enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}

	_, err := p.basic(ctx, "setAutoFWUpdate", upnp.Arguments{"enableAutoUpdate": value}, "enableAutoUpdate")

	return err
}

// FirmwareVersion asks the firmware service for the running version; the
// description carries it too, as read at open.
func (p *Plug) FirmwareVersion(ctx context.Context) (upnp.Arguments, error) {
	svc, err := p.Service(Firmware)
	if err != nil {
		return nil, err
	}

	return svc.Call(ctx, "GetFirmwareVersion", nil)
}

// TimeSync sets the plug's clock and zone from now: the time in UTC
// seconds, the zone as an offset in hours, and whether daylight saving is
// in force and observed. A plug reset keeps its zone at zero and reads its
// rules as UTC until this is called.
func (p *Plug) TimeSync(ctx context.Context, now time.Time) (upnp.Arguments, error) {
	svc, err := p.Service(TimeSyncS)
	if err != nil {
		return nil, err
	}

	_, offset := now.Zone()
	dst := "0"

	if now.IsDST() {
		dst = "1"
	}

	const secondsPerHour = 3600

	args := upnp.Arguments{
		"UTC":          strconv.FormatInt(now.Unix(), 10),
		"TimeZone":     strconv.FormatFloat(float64(offset)/secondsPerHour, 'f', 2, 64),
		"dst":          dst,
		"DstSupported": "1",
	}

	return svc.Call(ctx, "TimeSync", args, "UTC", "TimeZone", "dst", "DstSupported")
}

// Watch subscribes to the plug's events and returns the subscription; the
// events arrive on the listener, the first of them the plug's current
// state, within seconds. A plug that cannot reach the callback, on another
// network than the listener say, accepts the subscription and sends
// nothing: the caller holds the deadline on that first event.
func (p *Plug) Watch(ctx context.Context, listener *upnp.Listener, lease time.Duration) (*upnp.Subscription, error) {
	svc, err := p.Service(BasicEvent)
	if err != nil {
		return nil, err
	}

	sub, err := svc.Subscribe(ctx, listener, lease)
	if err != nil {
		return nil, err
	}

	return sub, nil
}

// ErrNoSuchPlug is the sentinel for a name no discovered plug carries.
var ErrNoSuchPlug = errors.New("no plug by that name")

// ByName finds the plug whose friendly name matches, case-insensitively,
// among discovered plugs.
func ByName(plugs []*Plug, name string) (*Plug, error) {
	var names []string

	for _, plug := range plugs {
		if strings.EqualFold(plug.FriendlyName, name) {
			return plug, nil
		}

		names = append(names, plug.FriendlyName)
	}

	return nil, fmt.Errorf("%w: %q; found %v", ErrNoSuchPlug, name, names)
}

// basic calls an action on basicevent, the service every verb below lives on.
func (p *Plug) basic(ctx context.Context, action string, args upnp.Arguments, order ...string) (upnp.Arguments, error) {
	svc, err := p.Service(BasicEvent)
	if err != nil {
		return nil, err
	}

	return svc.Call(ctx, action, args, order...)
}
