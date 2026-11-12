// Package upnp is the slice of UPnP a Wemo plug speaks: the device
// description it serves at setup.xml, the service descriptions behind it,
// SOAP calls against its services, and GENA eventing. The device's own
// documents are the specification: every action and argument the caller
// knows is read from them, and an answer is returned whole.
package upnp

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mycophonic/primordium/fault"
)

// maxDocument bounds a description read; a Wemo's largest is a few tens of
// kilobytes.
const maxDocument = 4 << 20

// Device is a UPnP root device as its description states it, with the
// Belkin fields a Wemo adds.
type Device struct {
	// Base is the URL every relative URL in the description resolves
	// against: the description's own, since a Wemo states no URLBase.
	Base *url.URL `xml:"-"`

	Type         string `xml:"device>deviceType"`
	FriendlyName string `xml:"device>friendlyName"`
	Manufacturer string `xml:"device>manufacturer"`
	Model        string `xml:"device>modelName"`
	ModelNumber  string `xml:"device>modelNumber"`
	Description  string `xml:"device>modelDescription"`
	Hardware     string `xml:"device>hwVersion"`
	Serial       string `xml:"device>serialNumber"`
	UDN          string `xml:"device>UDN"`
	MAC          string `xml:"device>macAddress"`
	Firmware     string `xml:"device>firmwareVersion"`
	// BinaryState is the switch as the description was read; the live
	// value is the basicevent service's.
	BinaryState string `xml:"device>binaryState"`
	// RTOS and IoT are the flags a newer firmware sets; a plug with RTOS
	// and not IoT encrypts its setup passphrase another way.
	RTOS     string    `xml:"device>rtos"`
	IoT      string    `xml:"device>iot"`
	Services []Service `xml:"device>serviceList>service"`

	client *http.Client
}

// Service is one service the device lists, with the URLs to drive and
// describe it, relative to the device until Resolve.
type Service struct {
	Type        string `xml:"serviceType"`
	ID          string `xml:"serviceId"`
	ControlURL  string `xml:"controlURL"`
	EventSubURL string `xml:"eventSubURL"`
	SCPDURL     string `xml:"SCPDURL"`

	device *Device
}

// Describe reads the device description at location. The client is the
// one every later call on the device and its services uses.
func Describe(ctx context.Context, client *http.Client, location string) (*Device, error) {
	base, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("%w: description URL %q: %w", fault.ErrInvalidArgument, location, err)
	}

	data, err := get(ctx, client, base.String())
	if err != nil {
		return nil, err
	}

	device := &Device{Base: base, client: client}
	if err := xml.Unmarshal(data, device); err != nil {
		return nil, fmt.Errorf("%w: description at %s: %w", fault.ErrUnacceptableResponse, location, err)
	}

	if device.UDN == "" {
		return nil, fmt.Errorf("%w: %s is not a device description", fault.ErrUnacceptableResponse, location)
	}

	for i := range device.Services {
		device.Services[i].device = device
	}

	return device, nil
}

// Service returns the service of that name, the short form of its type:
// "basicevent" for urn:Belkin:service:basicevent:1.
func (d *Device) Service(name string) (*Service, error) {
	for i := range d.Services {
		if d.Services[i].Name() == name || d.Services[i].Type == name || d.Services[i].ID == name {
			return &d.Services[i], nil
		}
	}

	return nil, fmt.Errorf("%w: %s has no service %q", fault.ErrNotFound, d.FriendlyName, name)
}

// Name is the service's short name: the second to last element of its
// type, "basicevent" for urn:Belkin:service:basicevent:1.
func (s *Service) Name() string {
	parts := strings.Split(s.Type, ":")

	const typeParts = 5 // urn : Belkin : service : name : version
	if len(parts) < typeParts {
		return s.Type
	}

	return parts[len(parts)-2]
}

// Control is the absolute URL SOAP calls go to.
func (s *Service) Control() *url.URL {
	return s.device.resolve(s.ControlURL)
}

// Events is the absolute URL subscriptions go to.
func (s *Service) Events() *url.URL {
	return s.device.resolve(s.EventSubURL)
}

// Document is the absolute URL of the service description.
func (s *Service) Document() *url.URL {
	return s.device.resolve(s.SCPDURL)
}

func (d *Device) resolve(rel string) *url.URL {
	ref, err := url.Parse(rel)
	if err != nil {
		return d.Base
	}

	return d.Base.ResolveReference(ref)
}

// get reads one document from the device.
func get(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", fault.ErrNetworkCommunication, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: GET %s: %w", fault.ErrNetworkCommunication, target, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GET %s: %s", fault.ErrUnacceptableResponse, target, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument))
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %w", fault.ErrNetworkCommunication, target, err)
	}

	return data, nil
}
