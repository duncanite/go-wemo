package upnp

import (
	"context"
	"encoding/xml"
	"fmt"

	"github.com/mycophonic/primordium/fault"
)

// Description is a service's own account of itself: the actions it takes
// and the state variables behind them.
type Description struct {
	Actions   []Action        `xml:"actionList>action"`
	Variables []StateVariable `xml:"serviceStateTable>stateVariable"`
}

// Action is one action with its arguments, in the order the device lists
// them.
type Action struct {
	Name      string     `xml:"name"`
	Arguments []Argument `xml:"argumentList>argument"`
}

// Argument is one argument of an action. Direction is "in" or "out" as the
// device says it, which a Wemo gets wrong often enough that a caller reads
// the answer whole rather than trusting it.
type Argument struct {
	Name      string `xml:"name"`
	Direction string `xml:"direction"`
	Variable  string `xml:"relatedStateVariable"`
}

// StateVariable is one variable: its type and, when the device states
// them, its default and allowed values.
type StateVariable struct {
	Name        string   `xml:"name"`
	Type        string   `xml:"dataType"`
	Default     string   `xml:"defaultValue"`
	Allowed     []string `xml:"allowedValueList>allowedValue"`
	SendsEvents string   `xml:"sendEvents,attr"`
}

// Describe reads the service description from the device.
func (s *Service) Describe(ctx context.Context) (*Description, error) {
	target := s.Document().String()

	data, err := get(ctx, s.device.client, target)
	if err != nil {
		return nil, err
	}

	var description Description
	if err := xml.Unmarshal(data, &description); err != nil {
		return nil, fmt.Errorf("%w: service description at %s: %w", fault.ErrUnacceptableResponse, target, err)
	}

	return &description, nil
}

// Action returns the named action, or ErrNotFound with the names that exist.
func (d *Description) Action(name string) (*Action, error) {
	for i := range d.Actions {
		if d.Actions[i].Name == name {
			return &d.Actions[i], nil
		}
	}

	return nil, fmt.Errorf("%w: no action %q; the service declares %d actions", fault.ErrNotFound, name, len(d.Actions))
}

// Argument finds name among the action's arguments, either direction.
func (a *Action) Argument(name string) (Argument, bool) {
	for _, arg := range a.Arguments {
		if arg.Name == name {
			return arg, true
		}
	}

	return Argument{}, false
}

// In lists the arguments the device says go in.
func (a *Action) In() []Argument {
	var inputs []Argument

	for _, arg := range a.Arguments {
		if arg.Direction == "in" {
			inputs = append(inputs, arg)
		}
	}

	return inputs
}
