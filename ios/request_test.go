package ios

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func intp(n int) *int { return &n }

func TestParseSelector(t *testing.T) {
	tests := []struct {
		in   string
		want Selector
	}{
		{in: "login", want: Selector{Identifier: "login"}},
		{in: "identifier=login", want: Selector{Identifier: "login"}},
		{in: "id=login", want: Selector{Identifier: "login"}},
		{in: `type=button label="Sign in"`, want: Selector{Type: "button", Label: "Sign in"}},
		{in: "type=Cell index=2", want: Selector{Type: "cell", Index: intp(2)}},
		{in: `label="say \"hi\" \\ bye"`, want: Selector{Label: `say "hi" \ bye`}},
		{in: `  label=General   type=text  `, want: Selector{Label: "General", Type: "text"}},
		{in: `label=""`, want: Selector{}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseSelector(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseSelector = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseSelectorErrors(t *testing.T) {
	tests := map[string]string{
		"Sign in":           "not key=value pairs nor an identifier",
		"type=knob":         `no element type is named "knob"`,
		"index=-1":          "index must be a number from 0",
		"index=first":       "index must be a number from 0",
		"color=red":         `no key is named "color"`,
		"id=a identifier=b": "gives identifier twice",
		`label="Sign in`:    "no closing quote",
		`label="Sign"in`:    "goes on after its closing quote",
		`label=Sign"in"`:    "quote the whole value",
		"type=button =x":    `"=x" is not key=value`,
		"type=button label": `"label" is not key=value`,
		`"type"=button`:     `"\"type\"" is not key=value`,
	}
	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			_, err := ParseSelector(in)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("ParseSelector(%q) = %v, want an error that says %q", in, err, want)
			}
		})
	}
}

func TestParseReq(t *testing.T) {
	req, err := parseReq(map[string]any{
		"app":     "com.apple.Preferences",
		"device":  "iPhone 17",
		"timeout": "1m",
		"actions": []any{
			map[string]any{"name": "launch", "args": []any{"-flag"}},
			map[string]any{"name": "tap", "selector": `label=General`},
			map[string]any{"name": "text", "id": "title", "selector": "type=navigationbar index=0"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.App != "com.apple.Preferences" || req.Device != "iPhone 17" || req.Timeout != time.Minute {
		t.Errorf("parseReq = %+v", req)
	}
	if got := req.Actions[0].Args; !reflect.DeepEqual(got, []string{"-flag"}) {
		t.Errorf("args = %v", got)
	}
	if got := req.Actions[1].selector; got.Label != "General" {
		t.Errorf("the selector was not parsed: %+v", got)
	}
	if got := req.Actions[2].key(); got != "title" {
		t.Errorf("key = %q, want the id", got)
	}

	def, err := parseReq(map[string]any{"app": "a", "actions": []any{map[string]any{"name": "launch"}}})
	if err != nil {
		t.Fatal(err)
	}
	if def.Timeout != DefaultTimeout {
		t.Errorf("the timeout is %s without one given, want %s", def.Timeout, DefaultTimeout)
	}
}

func TestParseReqErrors(t *testing.T) {
	action := func(a map[string]any) map[string]any {
		return map[string]any{"app": "com.example", "actions": []any{a}}
	}
	tests := []struct {
		name string
		with map[string]any
		want string
	}{
		{name: "no app", with: map[string]any{"actions": []any{map[string]any{"name": "launch"}}}, want: "app is required"},
		{name: "no actions", with: map[string]any{"app": "com.example"}, want: "actions is required"},
		{name: "bad timeout", with: map[string]any{"app": "a", "timeout": "soon", "actions": []any{map[string]any{"name": "launch"}}}, want: "timeout"},
		{name: "unknown action", with: action(map[string]any{"name": "swipe"}), want: `actions[0]: no action is named "swipe"`},
		{name: "tap without selector", with: action(map[string]any{"name": "tap"}), want: "tap needs a selector"},
		{name: "launch with selector", with: action(map[string]any{"name": "launch", "selector": "x"}), want: "launch takes no selector"},
		{name: "type without value", with: action(map[string]any{"name": "type", "selector": "x"}), want: "type needs a value"},
		{name: "tap with value", with: action(map[string]any{"name": "tap", "selector": "x", "value": "y"}), want: "tap takes no value"},
		{name: "tap with args", with: action(map[string]any{"name": "tap", "selector": "x", "args": []any{"-a"}}), want: "tap takes no args"},
		{name: "tap with id", with: action(map[string]any{"name": "tap", "selector": "x", "id": "t"}), want: "returns nothing for id"},
		{name: "bad selector", with: action(map[string]any{"name": "tap", "selector": "type=knob"}), want: "tap: selector"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseReq(tt.with)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parseReq = %v, want an error that says %q", err, tt.want)
			}
		})
	}
}

func TestParams(t *testing.T) {
	want := []string{"app", "device", "actions", "timeout"}
	if got := Params(); !reflect.DeepEqual(got, want) {
		t.Errorf("Params = %v, want %v", got, want)
	}
}
