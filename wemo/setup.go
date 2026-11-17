package wemo

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5" // #nosec G501 -- the plug's own key derivation, MD5 by Belkin's design
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	upnp "github.com/forkcloser/go-upnp"
)

// SetupHost is where a plug in setup mode sits on its own access point.
const SetupHost = "10.22.22.1"

// ErrLeft is a plug that stopped answering on its access point after taking
// the connection request: it has gone to join the network, and is found
// there, at a new address, a minute or two later.
var ErrLeft = errors.New("the plug left its access point to join the network")

// ErrNotJoined is a plug that took the connection request, stayed on its
// access point, and did not report itself connected in the time it was
// given.
var ErrNotJoined = errors.New("the plug did not join the network in time")

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
	answer, err := p.invoke(ctx, WiFiSetup, "GetApList")
	if err != nil {
		return nil, err
	}

	var points []AccessPoint

	table, _ := answer.Get("ApList")

	lines := strings.Split(table, "\n")
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
	answer, err := p.invoke(ctx, MetaInfo, "GetMetaInfo")
	if err != nil {
		return Meta{}, err
	}

	raw, _ := answer.Get("MetaInfo")

	fields := strings.Split(raw, "|")

	const expected = 2
	if len(fields) < expected {
		return Meta{}, fmt.Errorf("%w: MetaInfo %q has no MAC and serial", ErrAnswer, raw)
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
		return "", fmt.Errorf("%w: an empty passphrase", ErrArgument)
	}

	if len(meta.MAC) != macLen {
		return "", fmt.Errorf("%w: MAC %q is not twelve hex digits", ErrArgument, meta.MAC)
	}

	keydata := meta.MAC[:macLen/2] + meta.Serial + meta.MAC[macLen/2:]
	if len(keydata) < ivLen {
		return "", fmt.Errorf("%w: key material %q is too short", ErrArgument, keydata)
	}

	salt, vector := keydata[:saltLen], keydata[:ivLen]

	sum := md5.Sum([]byte(keydata + salt)) // #nosec G401 -- the plug's own key derivation, MD5 by Belkin's design

	block, err := aes.NewCipher(sum[:aes.BlockSize])
	if err != nil {
		return "", fmt.Errorf("making the passphrase's cipher: %w", err)
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
		return "", fmt.Errorf("%w: the passphrase is too long for the plug", ErrArgument)
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
//
// It returns ErrLeft when the plug took the request and stopped answering,
// which is how most joins end; ErrNotJoined when it stayed and did not
// connect in time; an ErrArgument for a network it cannot join or a
// passphrase it found too short; and an ErrNotFound for a network it does
// not see.
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

	if err = p.requestJoin(ctx, args); err != nil {
		return err
	}

	status, err := p.awaitJoined(ctx, timeout, poll)
	if err != nil {
		if unanswered(ctx, err) {
			return fmt.Errorf("%w: %w", ErrLeft, err)
		}

		return err
	}

	// A setup that would not close leaves the plug's verdict as it is:
	// the join is judged by the status, and the access point goes when
	// the plug does.
	_, _ = p.invoke(ctx, WiFiSetup, "CloseSetup")

	return p.joined(ctx, status, timeout)
}

// unanswered says a call failed with no word of the plug's and none of the
// caller's: the client's own error, a plug that is not there.
func unanswered(ctx context.Context, err error) bool {
	return ctx.Err() == nil && !errors.Is(err, ErrRefused) && !errors.Is(err, ErrAnswer)
}

// requestJoin sends ConnectHomeNetwork twice, as the app did: a plug has
// been seen to ignore the first. The plug may leave its access point for
// the network on the first, and then nothing after it answers: that is
// the join, not a failure.
func (p *Plug) requestJoin(ctx context.Context, args []upnp.Argument) error {
	for attempt := range 2 {
		if _, err := p.invoke(ctx, WiFiSetup, "ConnectHomeNetwork", args...); err != nil {
			if attempt > 0 && unanswered(ctx, err) {
				return fmt.Errorf("%w: %w", ErrLeft, err)
			}

			return err
		}
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
		ErrNotFound,
		ssid,
		len(points),
	)
}

// credentials are ConnectHomeNetwork's arguments for the network, in the
// service's order: the mode and cipher as the plug reported them, and the
// passphrase encrypted under the plug's meta information, or nothing on an
// open network.
func (p *Plug) credentials(ctx context.Context, point AccessPoint, ssid, passphrase string) ([]upnp.Argument, error) {
	auth, encryption, found := strings.Cut(point.Auth, "/")
	if !found || strings.HasPrefix(auth, "UNKNOW") {
		return nil, fmt.Errorf(
			"%w: the plug reports %q for %s, a mode it cannot join; WPA3, or a transition network, reads so",
			ErrArgument, point.Auth, ssid,
		)
	}

	encrypted := ""

	if encryption == "NONE" {
		auth = "OPEN"
	} else {
		if p.newer {
			return nil, fmt.Errorf("%w: this plug's firmware wants the rtos passphrase encryption", ErrUnsupported)
		}

		meta, err := p.MetaInfo(ctx)
		if err != nil {
			return nil, err
		}

		encrypted, err = EncryptPassphrase(passphrase, meta)
		if err != nil {
			return nil, err
		}
	}

	return []upnp.Argument{
		{Name: "ssid", Value: ssid},
		{Name: "auth", Value: auth},
		{Name: "password", Value: encrypted},
		{Name: "encrypt", Value: encryption},
		{Name: "channel", Value: point.Channel},
	}, nil
}

// joined turns the final network status into the verdict. A plug that is
// on its network and at rest reports 3, not 1: this firmware's steady
// state is what pywemo took for a handshake, so 3 after the window is a
// join, not a timeout.
func (p *Plug) joined(ctx context.Context, status string, timeout time.Duration) error {
	switch status {
	case statusConnected, statusHandshake:
		// Not every firmware has the action, and the join stands
		// without it.
		_, _ = p.basic(ctx, "SetSetupDoneStatus")

		return nil
	case statusShortKey:
		return fmt.Errorf("%w: the plug wants a passphrase of eight characters or more", ErrArgument)
	default:
		return fmt.Errorf("%w: it reports network status %s after %s", ErrNotJoined, status, timeout)
	}
}

// awaitJoined polls GetNetworkStatus until it says connected or the key
// was short, or the timeout passes. A plug may pass through 3 on its way
// to 1, or settle there once joined, this firmware's steady state: 3 held
// over steadyPolls polls in a row counts as joined.
func (p *Plug) awaitJoined(ctx context.Context, timeout, poll time.Duration) (string, error) {
	const steadyPolls = 3

	deadline := time.Now().Add(timeout)
	status := statusConnecting
	steady := 0

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return status, fmt.Errorf("waiting for the plug to join: %w", ctx.Err())
		case <-time.After(poll):
		}

		answer, err := p.invoke(ctx, WiFiSetup, "GetNetworkStatus")
		if err != nil {
			return status, err
		}

		status = value(answer, "NetworkStatus")

		if status == statusConnected || status == statusShortKey {
			return status, nil
		}

		if status == statusHandshake {
			steady++
		} else {
			steady = 0
		}

		if steady >= steadyPolls {
			return status, nil
		}
	}

	return status, nil
}
