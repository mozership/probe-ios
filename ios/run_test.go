package ios

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeRunner answers commands as the runner would, and keeps them.
type fakeRunner struct {
	cmds    []map[string]any
	replies map[string]map[string]any
	fail    map[string]string
	// delay is how long the runner takes over a command, as XCUITest can
	// on a slow machine.
	delay map[string]time.Duration
}

func (f *fakeRunner) Do(ctx context.Context, cmd map[string]any) (map[string]any, error) {
	f.cmds = append(f.cmds, cmd)
	name := cmd["cmd"].(string)
	if d, ok := f.delay[name]; ok {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("Post \"http://127.0.0.1:1/command\": %w", ctx.Err())
		case <-time.After(d):
		}
	}
	if msg, ok := f.fail[name]; ok {
		return nil, errors.New(msg)
	}
	if r, ok := f.replies[name]; ok {
		return r, nil
	}
	return map[string]any{"ok": true}, nil
}

func newRes() *Res {
	return &Res{Results: map[string]string{}, FilePaths: map[string]string{}}
}

func TestRunActions(t *testing.T) {
	png := []byte("\x89PNG fake")
	f := &fakeRunner{replies: map[string]map[string]any{
		"text":       {"ok": true, "text": "General"},
		"tree":       {"ok": true, "tree": "Application, label: 'Settings'"},
		"screenshot": {"ok": true, "png": base64.StdEncoding.EncodeToString(png)},
	}}
	req, err := parseReq(map[string]any{
		"app": "com.apple.Preferences",
		"actions": []any{
			map[string]any{"name": "launch"},
			map[string]any{"name": "tap", "selector": "label=General"},
			map[string]any{"name": "type", "selector": "type=searchfield", "value": "Wi-Fi"},
			map[string]any{"name": "text", "id": "title", "selector": "type=navigationbar"},
			map[string]any{"name": "tree"},
			map[string]any{"name": "screenshot", "id": "shot"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := newRes()
	if err := runActions(context.Background(), f, req, res); err != nil {
		t.Fatal(err)
	}

	if got := f.cmds[0]; !reflect.DeepEqual(got, map[string]any{"cmd": "launch", "app": "com.apple.Preferences", "args": []string{}}) {
		t.Errorf("launch sent %v", got)
	}
	tap := f.cmds[1]
	if !reflect.DeepEqual(tap["selector"], map[string]any{"label": "General"}) {
		t.Errorf("tap sent the selector %v", tap["selector"])
	}
	if timeout, _ := tap["timeout"].(float64); timeout <= 0 || timeout > DefaultTimeout.Seconds() {
		t.Errorf("tap waits for %v seconds, want what is left of the timeout", tap["timeout"])
	}
	if got := f.cmds[2]["text"]; got != "Wi-Fi" {
		t.Errorf("type sent the text %v", got)
	}

	if got := res.Results["title"]; got != "General" {
		t.Errorf("results.title = %q", got)
	}
	if got := res.Results["tree"]; !strings.Contains(got, "Settings") {
		t.Errorf("results.tree = %q", got)
	}
	path := res.FilePaths["shot"]
	t.Cleanup(func() { _ = os.Remove(path) })
	b, err := os.ReadFile(path)
	if err != nil || string(b) != string(png) {
		t.Errorf("the screenshot at %q holds %q, %v", path, b, err)
	}
}

func TestRunActionsStopsAtTheFirstFailure(t *testing.T) {
	f := &fakeRunner{fail: map[string]string{"tap": "no element matches label=Nope within 4.5s"}}
	req, err := parseReq(map[string]any{
		"app": "com.example",
		"actions": []any{
			map[string]any{"name": "launch"},
			map[string]any{"name": "tap", "selector": "label=Nope"},
			map[string]any{"name": "terminate"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = runActions(context.Background(), f, req, newRes())
	if err == nil || err.Error() != "actions[1] tap: no element matches label=Nope within 4.5s" {
		t.Errorf("runActions = %v", err)
	}
	if len(f.cmds) != 2 {
		t.Errorf("%d commands were sent, want the two up to the failure", len(f.cmds))
	}
}

func TestRemaining(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if got := remaining(ctx); got <= 9 || got > 9.5 {
		t.Errorf("remaining = %v, want a little under 9.5", got)
	}
	expired, cancel2 := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel2()
	time.Sleep(5 * time.Millisecond)
	if got := remaining(expired); got != 0 {
		t.Errorf("remaining after the deadline = %v, want 0", got)
	}
}

func withReplyGrace(t *testing.T, d time.Duration) {
	t.Helper()
	old := replyGrace
	replyGrace = d
	t.Cleanup(func() { replyGrace = old })
}

func parse(t *testing.T, timeout string, actions ...map[string]any) *Req {
	t.Helper()
	list := make([]any, len(actions))
	for i, a := range actions {
		list[i] = a
	}
	req, err := parseReq(map[string]any{"app": "com.example", "timeout": timeout, "actions": list})
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRunnerAnswerAfterTheDeadline(t *testing.T) {
	withReplyGrace(t, time.Second)
	f := &fakeRunner{
		delay: map[string]time.Duration{"tap": 150 * time.Millisecond},
		fail:  map[string]string{"tap": "no element matches label=Nope within 0.0s"},
	}
	err := runActions(context.Background(), f, parse(t, "50ms", map[string]any{"name": "tap", "selector": "label=Nope"}), newRes())
	if err == nil || err.Error() != "actions[0] tap: no element matches label=Nope within 0.0s" {
		t.Errorf("runActions = %v, want the runner's answer, which came after the deadline", err)
	}
}

func TestRunnerThatDoesNotAnswer(t *testing.T) {
	withReplyGrace(t, 50*time.Millisecond)
	f := &fakeRunner{delay: map[string]time.Duration{"tap": time.Minute}}
	start := time.Now()
	err := runActions(context.Background(), f, parse(t, "50ms", map[string]any{"name": "tap", "selector": "label=Nope"}), newRes())
	if err == nil || !strings.HasPrefix(err.Error(), "actions[0] tap: the step's timeout ran out: ") {
		t.Errorf("runActions = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the action waited for the runner past the grace")
	}
}

func TestNoActionAfterTheDeadline(t *testing.T) {
	withReplyGrace(t, time.Second)
	f := &fakeRunner{delay: map[string]time.Duration{"wait": 150 * time.Millisecond}}
	err := runActions(context.Background(), f, parse(t, "50ms",
		map[string]any{"name": "wait", "selector": "label=Slow"},
		map[string]any{"name": "terminate"},
	), newRes())
	if err == nil || err.Error() != "actions[1] terminate: the step's timeout ran out: context deadline exceeded" {
		t.Errorf("runActions = %v", err)
	}
	if len(f.cmds) != 1 {
		t.Errorf("%d commands were sent, want none after the deadline", len(f.cmds))
	}
}

func TestOnlyActionsOnElementsGetTheGrace(t *testing.T) {
	withReplyGrace(t, time.Minute)
	f := &fakeRunner{delay: map[string]time.Duration{"launch": time.Minute}}
	start := time.Now()
	err := runActions(context.Background(), f, parse(t, "50ms", map[string]any{"name": "launch"}), newRes())
	if err == nil || !strings.HasPrefix(err.Error(), "actions[0] launch: the step's timeout ran out: ") {
		t.Errorf("runActions = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("launch waited past the deadline of the actions")
	}
}

func TestStoppingTheStepEndsTheGrace(t *testing.T) {
	withReplyGrace(t, time.Minute)
	f := &fakeRunner{delay: map[string]time.Duration{"tap": time.Minute}}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := runActions(ctx, f, parse(t, "50ms", map[string]any{"name": "tap", "selector": "label=Nope"}), newRes())
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("runActions = %v, want the step's stop", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("a stopped step waited out the grace")
	}
}
