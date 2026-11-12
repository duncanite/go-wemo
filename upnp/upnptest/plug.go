// Package upnptest is a Wemo plug for tests: an HTTP server speaking what
// one first-generation Switch was seen to speak, documents it served and
// answers it gave, over a state held in memory; and an SSDP responder for
// a search pointed at it. What the real plug did is what this does: a
// fault inside a 200 for GetTime, 501 for the actions it declares and
// never built, a steady network status of 3 once joined, a first NOTIFY
// with the current state on every subscription.
package upnptest

import (
	"bytes"
	"context"
	"embed"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed testdata/*.xml
var documents embed.FS

// The strings the plug answers with, as it spells them.
const (
	binaryState   = "BinaryState"
	success       = "success"
	defaultSignal = 100
	defaultLease  = 30 * time.Minute
)

// Identity the embedded description carries, for tests that look it up.
const (
	Name   = "Kitchen Lamp"
	Serial = "221234K0100001"
	MAC    = "000C29000001"
	UDN    = "uuid:Socket-1_0-" + Serial
	// Firmware is the build the house's plugs run.
	Firmware = "WeMo_WW_2.00.11565.PVT-OWRT-SNSV2"
)

// Plug is the fake. Its exported fields are read and set by tests between
// requests; the server holds its own lock around them.
type Plug struct {
	*httptest.Server

	mu sync.Mutex
	// On is the relay.
	On bool
	// FriendlyName is what ChangeFriendlyName sets.
	FriendlyName string
	// Signal is what GetSignalStrength answers.
	Signal int
	// AutoUpdate is what setAutoFWUpdate last set; nil until it is called.
	AutoUpdate *bool
	// TimeSynced holds TimeSync's arguments once called.
	TimeSynced map[string]string
	// Resets is every ReSetup code received.
	Resets []string
	// Connects is every ConnectHomeNetwork call's arguments.
	Connects []map[string]string
	// Leaves makes the plug stop answering after the first ConnectHomeNetwork,
	// as a real one does when it goes to join the network.
	Leaves bool
	// Dead makes the plug hang up on the first ConnectHomeNetwork without
	// answering it: a plug that took nothing, as opposed to one that left.
	Dead bool
	// Searches counts the M-SEARCHes the SSDP responder received.
	Searches atomic.Int32
	// Statuses is what GetNetworkStatus answers, in order, the last one
	// repeated; empty is 0, 0, 3.
	Statuses []string
	// LeaseGranted is the subscription length the plug grants, whatever is
	// asked; zero grants what is asked.
	LeaseGranted time.Duration
	// Silent makes the plug accept a subscription and never notify, as one
	// does when the callback is on a network it cannot reach.
	Silent bool
	// Extra is XML added to the device element of the description before
	// the icon list: "<rtos>1</rtos>" makes a newer plug.
	Extra string
	// Calls is every SOAP action received, "service.Action", in order.
	Calls []string
	// Bodies is the raw body of every SOAP call, in the same order.
	Bodies [][]byte

	statusAt int
	gone     bool
	subs     map[string]*subscription
	nextSID  int
	ssdp     *net.UDPConn
	ssdpAddr string
	done     chan struct{}
}

type subscription struct {
	callback string
	expires  time.Time
	seq      uint64
}

// New starts a plug. Close it with the test.
func New() *Plug {
	plug := &Plug{
		On:           false,
		FriendlyName: Name,
		Signal:       defaultSignal,
		subs:         make(map[string]*subscription),
		done:         make(chan struct{}),
	}

	plug.Server = httptest.NewServer(http.HandlerFunc(plug.handle))

	return plug
}

// Close stops the server and the SSDP responder.
func (p *Plug) Close() {
	close(p.done)

	if p.ssdp != nil {
		_ = p.ssdp.Close()
	}

	p.Server.Close()
}

// Host is the server's host:port.
func (p *Plug) Host() string {
	return strings.TrimPrefix(p.URL, "http://")
}

// Location is the description's URL.
func (p *Plug) Location() string {
	return p.URL + "/setup.xml"
}

// Switch sets the relay and notifies every subscriber, as a plug does when
// its button is pressed.
func (p *Plug) Switch(ctx context.Context, powered bool) {
	p.mu.Lock()
	p.On = powered
	subs := p.snapshot()
	p.mu.Unlock()

	for sid, sub := range subs {
		p.notify(ctx, sid, sub, map[string]string{binaryState: state(powered)})
	}
}

// Subscriptions is how many are live.
func (p *Plug) Subscriptions() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.subs)
}

func (p *Plug) snapshot() map[string]*subscription {
	out := make(map[string]*subscription, len(p.subs))
	for sid, sub := range p.subs {
		copied := *sub
		out[sid] = &copied
	}

	return out
}

func state(powered bool) string {
	if powered {
		return "1"
	}

	return "0"
}

func (p *Plug) handle(writer http.ResponseWriter, req *http.Request) {
	p.mu.Lock()
	gone := p.gone
	p.mu.Unlock()

	if gone {
		// The plug has left for the network: hang up without a word.
		if hijacker, ok := writer.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		}

		return
	}

	switch {
	case req.Method == http.MethodGet:
		p.document(writer, req)
	case req.Method == http.MethodPost && strings.HasPrefix(req.URL.Path, "/upnp/control/"):
		p.soap(writer, req)
	case req.Method == "SUBSCRIBE" && strings.HasPrefix(req.URL.Path, "/upnp/event/"):
		p.subscribe(writer, req)
	case req.Method == "UNSUBSCRIBE" && strings.HasPrefix(req.URL.Path, "/upnp/event/"):
		p.unsubscribe(writer, req)
	default:
		http.NotFound(writer, req)
	}
}

// document serves the description and the service descriptions, and 404
// for the files the plug advertises and does not have.
func (p *Plug) document(writer http.ResponseWriter, req *http.Request) {
	name := strings.TrimPrefix(req.URL.Path, "/")

	data, err := documents.ReadFile("testdata/" + name)
	if err != nil {
		http.Error(writer, "<html><body><h1>404 Not Found</h1></body></html>", http.StatusNotFound)

		return
	}

	if name == "setup.xml" {
		p.mu.Lock()
		data = bytes.Replace(data, []byte("<friendlyName>"+Name+"</friendlyName>"),
			[]byte("<friendlyName>"+p.FriendlyName+"</friendlyName>"), 1)
		data = bytes.Replace(data, []byte("<binaryState>0</binaryState>"),
			[]byte("<binaryState>"+state(p.On)+"</binaryState>"), 1)
		data = bytes.Replace(data, []byte("<iconList>"), []byte(p.Extra+"<iconList>"), 1)
		p.mu.Unlock()
	}

	writer.Header().Set("Content-Type", "text/xml")
	_, _ = writer.Write(
		data,
	) // #nosec G705 -- an embedded document of the fixture, chosen by name, never the request's text
}

// soap answers an action: the service from the path, the action from the
// SOAPACTION header, the arguments from the body.
func (p *Plug) soap(writer http.ResponseWriter, req *http.Request) {
	service := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/upnp/control/"), "1")

	_, action, _ := strings.Cut(strings.Trim(req.Header.Get("SoapAction"), `"`), "#")

	body, _ := io.ReadAll(req.Body)
	args := arguments(body)

	p.mu.Lock()
	p.Calls = append(p.Calls, service+"."+action)
	p.Bodies = append(p.Bodies, body)
	p.mu.Unlock()

	answer, fault, status := p.dispatch(service, action, args)
	if status == hangUp {
		if hijacker, ok := writer.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
			}
		}

		return
	}

	writer.Header().Set("Content-Type", `text/xml; charset="utf-8"`)

	if fault != "" {
		writer.WriteHeader(status)
		_, _ = writer.Write(faultBody(fault))

		return
	}

	_, _ = writer.Write(answerBody(service, action, answer))
}

// hangUp is the status dispatch returns for a plug that closes the
// connection instead of answering; no HTTP status is it.
const hangUp = -1

// Fault codes the plug uses.
const (
	faultNotBuilt    = "501"
	faultInvalidArgs = "402"
	faultNoAction    = "401"
)

// dispatch is the plug's behaviour per action. The answer is name to text
// in the order the plug lists them; a fault is its code, with the status
// the real plug used for it.
func (p *Plug) dispatch(service, action string, args map[string]string) ([][2]string, string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	switch service + "." + action {
	case "basicevent.GetBinaryState":
		return [][2]string{{binaryState, state(p.On)}}, "", http.StatusOK
	case "basicevent.SetBinaryState":
		want := args[binaryState]
		if want != "0" && want != "1" {
			return [][2]string{{binaryState, "Error"}}, "", http.StatusOK
		}

		p.On = want == "1"

		return [][2]string{
			{binaryState, want},
			{"CountdownEndTime", "0"},
			{"deviceCurrentTime", now()},
		}, "", http.StatusOK
	case "basicevent.GetFriendlyName":
		return [][2]string{{"FriendlyName", p.FriendlyName}}, "", http.StatusOK
	case "basicevent.ChangeFriendlyName":
		p.FriendlyName = args["FriendlyName"]

		return [][2]string{{"FriendlyName", p.FriendlyName}}, "", http.StatusOK
	case "basicevent.GetSignalStrength":
		return [][2]string{{"SignalStrength", strconv.Itoa(p.Signal)}}, "", http.StatusOK
	case "basicevent.ReSetup":
		p.Resets = append(p.Resets, args["Reset"])

		return [][2]string{{"Reset", success}}, "", http.StatusOK
	case "basicevent.setAutoFWUpdate":
		enabled := args["enableAutoUpdate"] == "1"
		p.AutoUpdate = &enabled

		return nil, "", http.StatusOK
	case "basicevent.SetSetupDoneStatus":
		return nil, "", http.StatusOK
	case "basicevent.GetMacAddr":
		return [][2]string{{"MacAddr", MAC}, {"PluginUDN", UDN}, {"SerialNo", Serial}}, "", http.StatusOK
	case "basicevent.GetGPIO":
		return nil, faultInvalidArgs, http.StatusInternalServerError
	case "basicevent.GetDeviceId", "basicevent.GetSerialNo", "basicevent.GetPluginUDN", "timesync.GetTime",
		"deviceinfo.GetRouterInformation":
		// Declared, never built: a fault inside a 200.
		return nil, faultNotBuilt, http.StatusOK
	case "timesync.TimeSync":
		p.TimeSynced = args

		return [][2]string{{"status", success}}, "", http.StatusOK
	case "firmwareupdate.GetFirmwareVersion":
		return [][2]string{
			{"FirmwareVersion", "FirmwareVersion:" + Firmware + "|SkuNo:Plugin Device"},
		}, "", http.StatusOK
	case "metainfo.GetMetaInfo":
		return [][2]string{
			{"MetaInfo", MAC + "|" + Serial + "|Plugin Device|" + Firmware + "|Wemo.Mini.001|Socket"},
		}, "", http.StatusOK
	case "WiFiSetup.GetApList":
		return [][2]string{{"ApList", "Page:1/1/4$\nHouse|11|100|WPA2PSK/AES,\nNext Door|6|76|WPA2PSK/AES,\n" +
			"Transition|11|91|UNKNOW/AES,\nCafe|1|40|OPEN/NONE,\n"}}, "", http.StatusOK
	case "WiFiSetup.ConnectHomeNetwork":
		if p.Dead {
			p.gone = true

			return nil, "", hangUp
		}

		p.Connects = append(p.Connects, args)
		if p.Leaves {
			p.gone = true
		}

		return [][2]string{{"PairingStatus", "Connecting"}}, "", http.StatusOK
	case "WiFiSetup.GetNetworkStatus":
		return [][2]string{{"NetworkStatus", p.status()}}, "", http.StatusOK
	case "WiFiSetup.CloseSetup":
		return [][2]string{{"status", success}}, "", http.StatusOK
	}

	if _, known := declared(service, action); !known {
		return nil, faultNoAction, http.StatusInternalServerError
	}

	return nil, faultNotBuilt, http.StatusOK
}

// status walks Statuses, the last one repeated; the default is the real
// plug's: two polls at 0, then 3 for good.
func (p *Plug) status() string {
	statuses := p.Statuses
	if len(statuses) == 0 {
		statuses = []string{"0", "0", "3"}
	}

	at := min(p.statusAt, len(statuses)-1)
	p.statusAt++

	return statuses[at]
}

func now() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}

// declared reports whether the service's description lists the action.
func declared(service, action string) (string, bool) {
	files := map[string]string{
		"basicevent": "eventservice.xml", "WiFiSetup": "setupservice.xml", "timesync": "timesyncservice.xml",
		"firmwareupdate": "firmwareupdate.xml", "rules": "rulesservice.xml", "metainfo": "metainfoservice.xml",
		"deviceinfo": "deviceinfoservice.xml", "remoteaccess": "remoteaccess.xml", "smartsetup": "smartsetup.xml",
		"manufacture": "manufacture.xml",
	}

	file, ok := files[service]
	if !ok {
		return "", false
	}

	data, err := documents.ReadFile("testdata/" + file)
	if err != nil {
		return "", false
	}

	return file, bytes.Contains(data, []byte("<name>"+action+"</name>"))
}

// soapEnvelope is the SOAP namespace, an XML name and never fetched.
//
//revive:disable-next-line:unsecure-url-scheme
const soapEnvelope = "http://schemas.xmlsoap.org/soap/envelope/"

// arguments reads the request's action element children as name to text.
func arguments(body []byte) map[string]string {
	reader := argumentReader{args: make(map[string]string), actionDepth: -1}
	decoder := xml.NewDecoder(bytes.NewReader(body))

	for {
		token, err := decoder.Token()
		if err != nil {
			return reader.args
		}

		reader.step(token)
	}
}

type argumentReader struct {
	args        map[string]string
	depth       int
	actionDepth int
	current     string
	text        strings.Builder
}

func (r *argumentReader) step(token xml.Token) {
	switch elem := token.(type) {
	case xml.StartElement:
		r.depth++

		switch {
		case r.actionDepth < 0 && elem.Name.Space != soapEnvelope:
			r.actionDepth = r.depth
		case r.actionDepth > 0 && r.depth == r.actionDepth+1:
			r.current = elem.Name.Local
			r.text.Reset()
		}
	case xml.CharData:
		if r.current != "" {
			r.text.Write(elem)
		}
	case xml.EndElement:
		if r.actionDepth > 0 && r.depth == r.actionDepth+1 && r.current != "" {
			r.args[r.current] = r.text.String()
			r.current = ""
		}

		r.depth--
	}
}

func answerBody(service, action string, answer [][2]string) []byte {
	var buf bytes.Buffer

	buf.WriteString(
		`<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
			`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`,
	)
	buf.WriteString(`<u:` + action + `Response xmlns:u="urn:Belkin:service:` + service + `:1">`)

	for _, pair := range answer {
		buf.WriteString("<" + pair[0] + ">")
		_ = xml.EscapeText(&buf, []byte(pair[1]))
		buf.WriteString("</" + pair[0] + ">")
	}

	buf.WriteString(`</u:` + action + `Response></s:Body></s:Envelope>`)

	return buf.Bytes()
}

// faultBody is a fault as the plug writes it, the code's text as UPnP
// names it.
func faultBody(code string) []byte {
	texts := map[string]string{
		faultNotBuilt:    "Action Failed",
		faultInvalidArgs: "Invalid Args",
		faultNoAction:    "Invalid Action",
	}

	return fmt.Appendf(
		nil,
		`<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" `+
			`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><s:Fault><faultcode>s:Client</faultcode>`+
			`<faultstring>UPnPError</faultstring><detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0">`+
			`<errorCode>%s</errorCode><errorDescription>%s</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>`,
		code,
		texts[code],
	)
}

// subscribe is SUBSCRIBE, new or renewal. A new one gets a SID, the lease
// granted, and the first NOTIFY with the current state, as the plug sends.
func (p *Plug) subscribe(writer http.ResponseWriter, req *http.Request) {
	asked := leaseOf(req.Header.Get("Timeout"))

	p.mu.Lock()

	granted := asked
	if p.LeaseGranted > 0 {
		granted = p.LeaseGranted
	}

	sid := req.Header.Get("Sid")
	if sid == "" {
		callback := strings.Trim(req.Header.Get("Callback"), "<>")
		if callback == "" || req.Header.Get("Nt") != "upnp:event" {
			p.mu.Unlock()
			http.Error(writer, "precondition failed", http.StatusPreconditionFailed)

			return
		}

		p.nextSID++
		sid = fmt.Sprintf("uuid:fe1325ba-1dd1-11b2-aba7-%012d", p.nextSID)
		p.subs[sid] = &subscription{callback: callback, expires: time.Now().Add(granted)}
	} else {
		sub, known := p.subs[sid]
		if !known {
			p.mu.Unlock()
			http.Error(writer, "precondition failed", http.StatusPreconditionFailed)

			return
		}

		sub.expires = time.Now().Add(granted)
	}

	sub := *p.subs[sid]
	isNew := req.Header.Get("Sid") == "" && !p.Silent
	powered := p.On
	p.mu.Unlock()

	writer.Header().Set("Sid", sid)
	writer.Header().Set("Timeout", "Second-"+strconv.Itoa(int(granted/time.Second)))
	writer.WriteHeader(http.StatusOK)

	if isNew {
		// After the answer, as the plug does; the request's context ends
		// with the answer, so the notification runs on its own.
		go p.notify(context.WithoutCancel(req.Context()), sid, &sub, map[string]string{
			binaryState: state(powered), "TimeZoneNotification": "005", "TimeSyncRequest": "0",
		})
	}
}

func (p *Plug) unsubscribe(writer http.ResponseWriter, req *http.Request) {
	p.mu.Lock()
	_, known := p.subs[req.Header.Get("Sid")]
	delete(p.subs, req.Header.Get("Sid"))
	p.mu.Unlock()

	if !known {
		http.Error(writer, "precondition failed", http.StatusPreconditionFailed)

		return
	}

	writer.WriteHeader(http.StatusOK)
}

// notify sends one NOTIFY to a subscriber. The plug's own stack retries
// nothing; neither does this.
func (p *Plug) notify(ctx context.Context, sid string, sub *subscription, properties map[string]string) {
	p.mu.Lock()

	live, known := p.subs[sid]
	if !known {
		p.mu.Unlock()

		return
	}

	seq := live.seq
	live.seq++
	p.mu.Unlock()

	var buf bytes.Buffer

	buf.WriteString(`<e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0">`)

	for name, value := range properties {
		buf.WriteString("<e:property><" + name + ">")
		_ = xml.EscapeText(&buf, []byte(value))
		buf.WriteString("</" + name + "></e:property>")
	}

	buf.WriteString(`</e:propertyset>`)

	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "NOTIFY", sub.callback, &buf)
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("Nt", "upnp:event")
	req.Header.Set("Nts", "upnp:propchange")
	req.Header.Set("Sid", sid)
	req.Header.Set("Seq", strconv.FormatUint(seq, 10))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

const notifyTimeout = 5 * time.Second

func leaseOf(timeout string) time.Duration {
	_, number, found := strings.Cut(timeout, "-")
	if !found {
		return defaultLease
	}

	seconds, err := strconv.Atoi(number)
	if err != nil || seconds <= 0 {
		return defaultLease
	}

	return time.Duration(seconds) * time.Second
}
