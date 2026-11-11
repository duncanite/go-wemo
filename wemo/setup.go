package wemo

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" // #nosec G501 -- the plug's own key derivation, MD5 by Belkin's design
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-wemo/upnp"
)

// SetupHost is where a plug in setup mode sits on its own access point.
const SetupHost = "10.22.22.1"

// ErrLeft is the sentinel for a plug that stopped answering on its access
// point after taking the connection request: it has gone to join the
// network, and is found there, at a new address, a minute or two later.
var ErrLeft = errors.New("the plug left its access point to join the network")

// AccessPoint is one network the plug can see, from its scan.
type AccessPoint struct {
	SSID    string
	Channel string
	Signal  string
	// Auth is the mode and the cipher as the plug reports them,
	// "WPA2PSK/AES"; "Unknown" is a network it cannot join.
	Auth string
}

// Networks asks the plug for the networks it sees. The answer is a text
// table, one line per network, fields separated by |.
func (p *Plug) Networks(ctx context.Context) ([]AccessPoint, error) {
	svc, err := p.Service(WiFiSetup)
	if err != nil {
		return nil, err
	}

	answer, err := svc.Call(ctx, "GetApList", nil)
	if err != nil {
		return nil, err
	}

	var points []AccessPoint

	lines := strings.Split(answer["ApList"], "\n")
	for _, line := range lines[min(1, len(lines)):] {
		line = strings.TrimSuffix(strings.TrimSpace(line), ",")
		if line == "" || !strings.Contains(line, "|") {
			continue
		}

		fields := strings.Split(line, "|")

		const expected = 4
		if len(fields) < expected {
			continue
		}

		points = append(points, AccessPoint{
			SSID:    strings.TrimSpace(fields[0]),
			Channel: strings.TrimSpace(fields[1]),
			Signal:  strings.TrimSpace(fields[2]),
			Auth:    strings.TrimSpace(fields[len(fields)-1]),
		})
	}

	return points, nil
}

// Meta is what the plug publishes about itself for the passphrase's key:
// the MAC without separators and the serial, the first two fields of its
// MetaInfo.
type Meta struct {
	MAC    string
	Serial string
	Raw    string
}

// MetaInfo reads the plug's meta information.
func (p *Plug) MetaInfo(ctx context.Context) (Meta, error) {
	svc, err := p.Service(MetaInfo)
	if err != nil {
		return Meta{}, err
	}

	answer, err := svc.Call(ctx, "GetMetaInfo", nil)
	if err != nil {
		return Meta{}, err
	}

	raw := answer["MetaInfo"]

	fields := strings.Split(raw, "|")

	const expected = 2
	if len(fields) < expected {
		return Meta{}, fmt.Errorf("%w: MetaInfo %q has no MAC and serial", fault.ErrUnacceptableResponse, raw)
	}

	return Meta{MAC: strings.TrimSpace(fields[0]), Serial: strings.TrimSpace(fields[1]), Raw: raw}, nil
}

// EncryptPassphrase is the passphrase as the plug wants it in
// ConnectHomeNetwork, the way the Belkin app sent it: AES-128-CBC with
// PKCS#7 padding, the key the first sixteen bytes of MD5 over the key
// material and a salt, both drawn from the MAC and serial; base64; then
// two hexadecimal bytes, the lengths of the ciphertext and of the
// passphrase. This is the original method, the one the plugs without rtos
// or binaryOption in their description take.
func EncryptPassphrase(passphrase string, meta Meta) (string, error) {
	const (
		macLen  = 12
		saltLen = 8
		ivLen   = 16
		maxLen  = 255
	)

	if passphrase == "" {
		return "", fmt.Errorf("%w: an empty passphrase", fault.ErrInvalidArgument)
	}

	if len(meta.MAC) != macLen {
		return "", fmt.Errorf("%w: MAC %q is not twelve hex digits", fault.ErrInvalidArgument, meta.MAC)
	}

	keydata := meta.MAC[:macLen/2] + meta.Serial + meta.MAC[macLen/2:]
	if len(keydata) < ivLen {
		return "", fmt.Errorf("%w: key material %q is too short", fault.ErrInvalidArgument, keydata)
	}

	salt, vector := keydata[:saltLen], keydata[:ivLen]

	sum := md5.Sum([]byte(keydata + salt)) // #nosec G401 -- the plug's own key derivation, MD5 by Belkin's design

	block, err := aes.NewCipher(sum[:aes.BlockSize])
	if err != nil {
		return "", fmt.Errorf("%w: %w", fault.ErrSystemFailure, err)
	}

	plain := []byte(passphrase)
	pad := aes.BlockSize - len(plain)%aes.BlockSize

	for range pad {
		plain = append(plain, byte(pad))
	}

	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, []byte(vector)).CryptBlocks(out, plain)

	encoded := base64.StdEncoding.EncodeToString(out)
	if len(encoded) > maxLen || len(passphrase) > maxLen {
		return "", fmt.Errorf("%w: the passphrase is too long for the plug", fault.ErrInvalidArgument)
	}

	return fmt.Sprintf("%s%02x%02x", encoded, len(encoded), len(passphrase)), nil
}

// SetupOptions shape Join.
type SetupOptions struct {
	// Timeout bounds the wait for the plug to report itself connected;
	// under twenty seconds is too short for most plugs.
	Timeout time.Duration
	// Poll is how often the status is asked.
	Poll time.Duration
}

// Network status codes the plug reports while joining.
const (
	statusConnecting = "0"
	statusConnected  = "1"
	statusShortKey   = "2"
	statusHandshake  = "3"
)

// Join connects the plug to the network named, as the Belkin app did: find
// it in the plug's scan, encrypt the passphrase under the plug's meta
// information, send ConnectHomeNetwork, twice, since one send often does
// nothing, then poll the status until it says connected, and close the
// setup. Run against a plug in setup mode, on its own access point.
func (p *Plug) Join(ctx context.Context, ssid, passphrase string, opts SetupOptions) error {
	const (
		minTimeout  = 20 * time.Second
		defaultPoll = 2 * time.Second
	)

	timeout := max(opts.Timeout, minTimeout)

	poll := opts.Poll
	if poll <= 0 {
		poll = defaultPoll
	}

	point, err := p.network(ctx, ssid)
	if err != nil {
		return err
	}

	args, err := p.credentials(ctx, point, ssid, passphrase)
	if err != nil {
		return err
	}

	svc, err := p.Service(WiFiSetup)
	if err != nil {
		return err
	}

	if joinErr := requestJoin(ctx, svc, args); joinErr != nil {
		return joinErr
	}

	status, err := awaitJoined(ctx, svc, timeout, poll)
	if err != nil {
		if errors.Is(err, fault.ErrNetworkCommunication) {
			return fmt.Errorf("%w: %w", ErrLeft, err)
		}

		return err
	}

	if closed, closeErr := svc.Call(ctx, "CloseSetup", nil); closeErr != nil {
		slog.WarnContext(ctx, "close setup failed; the plug may still be connected", "err", closeErr)
	} else {
		slog.InfoContext(ctx, "setup closed", "status", closed["status"])
	}

	return p.joined(ctx, status, timeout)
}

// requestJoin sends ConnectHomeNetwork twice, as the app did: a plug has
// been seen to ignore the first. The plug may leave its access point for
// the network on the first, and then nothing after it answers: that is
// the join, not a failure.
func requestJoin(ctx context.Context, svc *upnp.Service, args upnp.Arguments) error {
	for attempt := range 2 {
		answer, err := svc.Call(ctx, "ConnectHomeNetwork", args, "ssid", "auth", "password", "encrypt", "channel")
		if err != nil {
			if attempt > 0 && errors.Is(err, fault.ErrNetworkCommunication) {
				return fmt.Errorf("%w: %w", ErrLeft, err)
			}

			return err
		}

		slog.InfoContext(ctx, "connection requested", "attempt", attempt+1, "pairing", answer["PairingStatus"])
	}

	return nil
}

// network finds ssid in the plug's scan.
func (p *Plug) network(ctx context.Context, ssid string) (AccessPoint, error) {
	points, err := p.Networks(ctx)
	if err != nil {
		return AccessPoint{}, err
	}

	for _, point := range points {
		if point.SSID == ssid {
			return point, nil
		}
	}

	return AccessPoint{}, fmt.Errorf(
		"%w: the plug does not see %q; it sees %d networks",
		fault.ErrNotFound,
		ssid,
		len(points),
	)
}

// credentials are ConnectHomeNetwork's arguments for the network: the
// mode and cipher as the plug reported them, and the passphrase encrypted
// under the plug's meta information, or nothing on an open network.
func (p *Plug) credentials(ctx context.Context, point AccessPoint, ssid, passphrase string) (upnp.Arguments, error) {
	auth, encryption, found := strings.Cut(point.Auth, "/")
	if !found || strings.HasPrefix(auth, "UNKNOW") {
		return nil, fmt.Errorf(
			"%w: the plug reports %q for %s, a mode it cannot join; WPA3, or a transition network, reads so",
			fault.ErrInvalidArgument, point.Auth, ssid,
		)
	}

	// pywemo's reading of the app: a plug with rtos and not iot takes the
	// second encryption method, which this does not implement.
	if p.RTOS == "1" && p.IoT != "1" {
		return nil, fmt.Errorf("%w: this plug's firmware wants the rtos passphrase encryption", fault.ErrNotImplemented)
	}

	encrypted := ""

	if encryption == "NONE" {
		auth = "OPEN"
	} else {
		meta, err := p.MetaInfo(ctx)
		if err != nil {
			return nil, err
		}

		encrypted, err = EncryptPassphrase(passphrase, meta)
		if err != nil {
			return nil, err
		}
	}

	return upnp.Arguments{
		"ssid": ssid, "auth": auth, "password": encrypted, "encrypt": encryption, "channel": point.Channel,
	}, nil
}

// joined turns the final network status into the verdict. A plug that is
// on its network and at rest reports 3, not 1: this firmware's steady
// state is what pywemo took for a handshake, so 3 after the window is a
// join, not a timeout.
func (p *Plug) joined(ctx context.Context, status string, timeout time.Duration) error {
	switch status {
	case statusConnected, statusHandshake:
		if _, err := p.basic(ctx, "SetSetupDoneStatus", nil); err != nil {
			slog.DebugContext(ctx, "setup done status not taken", "err", err)
		}

		return nil
	case statusShortKey:
		return fmt.Errorf("%w: the plug wants a passphrase of eight characters or more", fault.ErrInvalidArgument)
	default:
		return fmt.Errorf("%w: the plug reports network status %s after %s", fault.ErrTimeout, status, timeout)
	}
}

// awaitJoined polls GetNetworkStatus until it says connected or the key
// was short, or the timeout passes. A plug may sit at 3 before 1, or stay
// there once joined, so 3 at the deadline gets a few more polls and then
// counts.
func awaitJoined(ctx context.Context, svc *upnp.Service, timeout, poll time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	status := statusConnecting

	for grace := 3; time.Now().Before(deadline) || (status == statusHandshake && grace > 0); {
		if !time.Now().Before(deadline) {
			grace--
		}

		select {
		case <-ctx.Done():
			return status, fmt.Errorf("%w: %w", fault.ErrCancelled, ctx.Err())
		case <-time.After(poll):
		}

		answer, err := svc.Call(ctx, "GetNetworkStatus", nil)
		if err != nil {
			return status, err
		}

		status = strings.TrimSpace(answer["NetworkStatus"])
		slog.DebugContext(ctx, "network status", "status", status)

		if status == statusConnected || status == statusShortKey {
			return status, nil
		}
	}

	return status, nil
}
