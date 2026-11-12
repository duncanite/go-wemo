package upnp

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/mycophonic/primordium/fault"
)

// FaultError is a SOAP fault as the device raised it: the UPnP error code and
// its description, when the device gave them, and the SOAP fault string.
type FaultError struct {
	Code        string
	Description string
	String      string
	Status      int
}

func (f *FaultError) Error() string {
	switch {
	case f.Code != "" && f.Description != "":
		return fmt.Sprintf("the device answered %s %s (HTTP %d)", f.Code, f.Description, f.Status)
	case f.String != "":
		return fmt.Sprintf("the device answered %s (HTTP %d)", f.String, f.Status)
	default:
		return fmt.Sprintf("the device answered HTTP %d", f.Status)
	}
}

// Is makes a FaultError an unacceptable response, so a caller may match either.
func (*FaultError) Is(target error) bool {
	return errors.Is(target, fault.ErrUnacceptableResponse)
}

// Arguments is the arguments of a call, or the elements of its answer, by
// name. A Wemo takes and returns strings throughout.
type Arguments map[string]string

// Call invokes action on the service with args and returns every element
// of the answer, whatever direction the description claimed. The
// arguments go out in the order given by Order when the action is known,
// since some devices read them positionally; otherwise sorted, which is
// stable at least.
func (s *Service) Call(ctx context.Context, action string, args Arguments, order ...string) (Arguments, error) {
	body := envelope(s.Type, action, args, order)
	target := s.Control().String()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", fault.ErrNetworkCommunication, err)
	}

	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SoapAction", `"`+s.Type+"#"+action+`"`)

	slog.DebugContext(ctx, "soap call", "url", target, "action", action, "args", args)

	resp, err := s.device.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s %s: %w", fault.ErrNetworkCommunication, action, target, err)
	}

	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the answer to %s: %w", fault.ErrNetworkCommunication, action, err)
	}

	slog.DebugContext(ctx, "soap answer", "action", action, "status", resp.StatusCode, "body", string(data))

	if resp.StatusCode != http.StatusOK {
		return nil, parseFault(data, resp.StatusCode)
	}

	answer, response, err := parseAnswer(data)
	if err != nil {
		return nil, fmt.Errorf("%w: the answer to %s: %w", fault.ErrUnacceptableResponse, action, err)
	}

	// A Wemo answers some faults with 200 and a Fault body all the same.
	if response == "Fault" {
		return nil, parseFault(data, resp.StatusCode)
	}

	return answer, nil
}

// envelope is the request body: the action element in the service's
// namespace, its arguments as child elements, text escaped.
func envelope(serviceType, action string, args Arguments, order []string) []byte {
	names := order
	if len(names) == 0 {
		names = make([]string, 0, len(args))
		for name := range args {
			names = append(names, name)
		}

		sort.Strings(names)
	}

	var buf bytes.Buffer

	buf.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	buf.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>`)
	buf.WriteString(`<u:` + action + ` xmlns:u="` + serviceType + `">`)

	for _, name := range names {
		value, ok := args[name]
		if !ok && len(order) > 0 {
			continue
		}

		buf.WriteString("<" + name + ">")
		_ = xml.EscapeText(&buf, []byte(value))
		buf.WriteString("</" + name + ">")
	}

	buf.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)

	return buf.Bytes()
}

// parseAnswer reads the elements inside the first child of the SOAP body,
// the action's response, as name to text, and returns that child's name:
// the action's response, or Fault. A Wemo answers flat elements; nested
// ones are kept as their inner text.
func parseAnswer(data []byte) (Arguments, string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	walk := answerWalk{answer: make(Arguments), bodyDepth: -1, responseDepth: -1}

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, "", fmt.Errorf("%w: %w", fault.ErrInvalidArgument, err)
		}

		walk.step(token)
	}

	if walk.responseDepth < 0 {
		return nil, "", fmt.Errorf("%w: no SOAP body in the answer", fault.ErrInvalidArgument)
	}

	return walk.answer, walk.response, nil
}

// answerWalk is the state of a pass over the answer's tokens: where the
// body and the response element sit, and the element being read.
type answerWalk struct {
	answer        Arguments
	depth         int
	bodyDepth     int
	responseDepth int
	response      string
	current       string
	text          strings.Builder
}

func (w *answerWalk) step(token xml.Token) {
	switch elem := token.(type) {
	case xml.StartElement:
		w.depth++
		w.open(elem.Name.Local)
	case xml.CharData:
		if w.current != "" {
			w.text.Write(elem)
		}
	case xml.EndElement:
		if w.responseDepth > 0 && w.depth == w.responseDepth+1 && w.current != "" {
			w.answer[w.current] = w.text.String()
			w.current = ""
		}

		w.depth--
	}
}

func (w *answerWalk) open(name string) {
	switch {
	case w.bodyDepth < 0 && name == "Body":
		w.bodyDepth = w.depth
	case w.bodyDepth > 0 && w.responseDepth < 0 && w.depth == w.bodyDepth+1:
		w.responseDepth = w.depth
		w.response = name
	case w.responseDepth > 0 && w.depth == w.responseDepth+1:
		w.current = name
		w.text.Reset()
	}
}

// parseFault reads a SOAP fault, or wraps the status when the body is not
// one.
func parseFault(data []byte, status int) error {
	var parsed struct {
		String      string `xml:"Body>Fault>faultstring"`
		Code        string `xml:"Body>Fault>detail>UPnPError>errorCode"`
		Description string `xml:"Body>Fault>detail>UPnPError>errorDescription"`
	}

	_ = xml.Unmarshal(data, &parsed)

	return &FaultError{Code: parsed.Code, Description: parsed.Description, String: parsed.String, Status: status}
}
