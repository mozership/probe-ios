package ios

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mozership/probe/actionrpc"
)

// DefaultTimeout bounds the actions of a step that sets no timeout. It does
// not count building the runner or starting it, which the step's own
// timeout bounds.
const DefaultTimeout = 30 * time.Second

// Req is the with of a step.
type Req struct {
	// App is the bundle identifier of the app the actions drive.
	App string `map:"app"`
	// Device names the simulator by its name or UDID; empty takes the one
	// booted simulator.
	Device  string    `map:"device"`
	Actions []*Action `map:"actions"`
	// Timeout bounds the actions, from the first to the last.
	Timeout time.Duration
}

// Action is one entry of the actions of a step.
type Action struct {
	Name string `map:"name"`
	// ID is the key of what the action returns, in res.results or
	// res.filepaths; empty is the action's name.
	ID string `map:"id"`
	// Selector names the element an action on an element acts on. See
	// ParseSelector.
	Selector string `map:"selector"`
	// Value is the text type types.
	Value string `map:"value"`
	// Args are the launch arguments launch starts the app with.
	Args []string `map:"args"`

	selector Selector
}

// kind is what an action needs and returns.
type kind struct {
	selector bool
	value    bool
	args     bool
	// result is where its result goes: "results", "filepaths" or none.
	result string
}

// kinds are the actions a step takes, by name.
var kinds = map[string]kind{
	"launch":     {args: true},
	"activate":   {},
	"terminate":  {},
	"tap":        {selector: true},
	"type":       {selector: true, value: true},
	"wait":       {selector: true},
	"text":       {selector: true, result: "results"},
	"tree":       {result: "results"},
	"screenshot": {result: "filepaths"},
}

// Params returns the keys the action takes in with.
func Params() []string {
	return append(actionrpc.ParamNames(Req{}), "timeout")
}

// parseReq reads the with of a step, and checks every action before any
// runs, so that a mistake in the last one does not leave the app halfway.
func parseReq(with map[string]any) (*Req, error) {
	req := &Req{Timeout: DefaultTimeout}
	if err := actionrpc.DecodeParams(with, req); err != nil {
		return nil, err
	}
	if v, ok := with["timeout"]; ok {
		d, err := actionrpc.ParseTimeout(v)
		if err != nil {
			return nil, err
		}
		req.Timeout = d
	}
	if req.App == "" {
		return nil, fmt.Errorf("app is required: the bundle identifier of the app to drive, such as com.apple.Preferences")
	}
	if len(req.Actions) == 0 {
		return nil, fmt.Errorf("actions is required: the actions to take, in order")
	}
	for i, a := range req.Actions {
		if err := a.check(); err != nil {
			return nil, fmt.Errorf("actions[%d]: %w", i, err)
		}
	}
	return req, nil
}

func (a *Action) check() error {
	k, ok := kinds[a.Name]
	if !ok {
		return fmt.Errorf("no action is named %q; the actions are %s", a.Name, strings.Join(names(), ", "))
	}
	if k.selector {
		if a.Selector == "" {
			return fmt.Errorf("%s needs a selector", a.Name)
		}
		s, err := ParseSelector(a.Selector)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		a.selector = s
	} else if a.Selector != "" {
		return fmt.Errorf("%s takes no selector", a.Name)
	}
	if k.value && a.Value == "" {
		return fmt.Errorf("%s needs a value, the text to type", a.Name)
	}
	if !k.value && a.Value != "" {
		return fmt.Errorf("%s takes no value", a.Name)
	}
	if !k.args && len(a.Args) > 0 {
		return fmt.Errorf("%s takes no args", a.Name)
	}
	if k.result == "" && a.ID != "" {
		return fmt.Errorf("%s returns nothing for id to name", a.Name)
	}
	return nil
}

// key is where the action's result goes.
func (a *Action) key() string {
	if a.ID != "" {
		return a.ID
	}
	return a.Name
}

func names() []string {
	s := make([]string, 0, len(kinds))
	for n := range kinds {
		s = append(s, n)
	}
	sort.Strings(s)
	return s
}

// Selector names an element of the app, as the runner looks it up: the
// first of the elements of Type whose accessibility identifier and label
// are those given, or the Index-th of them.
type Selector struct {
	Identifier string
	Label      string
	Type       string
	Index      *int
}

// types are the element types a selector names, as the runner knows them.
var types = []string{
	"alert", "any", "button", "cell", "image", "link", "navigationbar", "other",
	"searchfield", "securetextfield", "slider", "switch", "tabbar", "text",
	"textfield", "textview",
}

// ParseSelector reads a selector written as key=value pairs, separated by
// spaces, with a value in double quotes when it has a space:
//
//	identifier=login
//	type=button label="Sign in"
//	type=cell index=2
//
// The keys are identifier (or id), label, type and index. A selector with
// no = at all is an accessibility identifier, so "login" is
// identifier=login.
func ParseSelector(s string) (Selector, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "=") {
		if strings.ContainsAny(s, " \"") {
			return Selector{}, fmt.Errorf("selector %q is not key=value pairs nor an identifier", s)
		}
		return Selector{Identifier: s}, nil
	}
	pairs, err := splitPairs(s)
	if err != nil {
		return Selector{}, fmt.Errorf("selector %q: %w", s, err)
	}
	var sel Selector
	seen := map[string]bool{}
	for _, p := range pairs {
		key := p[0]
		if key == "id" {
			key = "identifier"
		}
		if seen[key] {
			return Selector{}, fmt.Errorf("selector %q gives %s twice", s, key)
		}
		seen[key] = true
		switch key {
		case "identifier":
			sel.Identifier = p[1]
		case "label":
			sel.Label = p[1]
		case "type":
			t := strings.ToLower(p[1])
			if !slices.Contains(types, t) {
				return Selector{}, fmt.Errorf("selector %q: no element type is named %q; the types are %s", s, p[1], strings.Join(types, ", "))
			}
			sel.Type = t
		case "index":
			n, err := strconv.Atoi(p[1])
			if err != nil || n < 0 {
				return Selector{}, fmt.Errorf("selector %q: index must be a number from 0", s)
			}
			sel.Index = &n
		default:
			return Selector{}, fmt.Errorf("selector %q: no key is named %q; the keys are identifier, label, type and index", s, p[0])
		}
	}
	return sel, nil
}

// splitPairs splits key=value pairs separated by spaces, where a value in
// double quotes may hold spaces, and \" and \\ in it stand for " and \.
func splitPairs(s string) ([][2]string, error) {
	var pairs [][2]string
	i := 0
	for i < len(s) {
		if s[i] == ' ' {
			i++
			continue
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq <= 0 {
			return nil, fmt.Errorf("%q is not key=value", strings.Fields(s[i:])[0])
		}
		key := s[i : i+eq]
		if strings.ContainsAny(key, " \"") {
			return nil, fmt.Errorf("%q is not key=value", key)
		}
		i += eq + 1
		var value strings.Builder
		if i < len(s) && s[i] == '"' {
			i++
			closed := false
			for i < len(s) {
				c := s[i]
				if c == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
					value.WriteByte(s[i+1])
					i += 2
					continue
				}
				if c == '"' {
					closed = true
					i++
					break
				}
				value.WriteByte(c)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("the value of %s has no closing quote", key)
			}
			if i < len(s) && s[i] != ' ' {
				return nil, fmt.Errorf("the value of %s goes on after its closing quote", key)
			}
		} else {
			for i < len(s) && s[i] != ' ' {
				if s[i] == '"' {
					return nil, fmt.Errorf("the value of %s has a quote in it; quote the whole value", key)
				}
				value.WriteByte(s[i])
				i++
			}
		}
		pairs = append(pairs, [2]string{key, value.String()})
	}
	return pairs, nil
}

// wire is the selector as the runner reads it.
func (s Selector) wire() map[string]any {
	m := map[string]any{}
	if s.Identifier != "" {
		m["identifier"] = s.Identifier
	}
	if s.Label != "" {
		m["label"] = s.Label
	}
	if s.Type != "" {
		m["type"] = s.Type
	}
	if s.Index != nil {
		m["index"] = *s.Index
	}
	return m
}
