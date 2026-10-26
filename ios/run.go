package ios

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"time"
)

// commander sends a command to the runner and returns its reply.
type commander interface {
	Do(ctx context.Context, cmd map[string]any) (map[string]any, error)
}

// Res is what a step returns in res.
type Res struct {
	// Code is 0 when every action succeeded, and 1 otherwise.
	Code int `map:"code"`
	// Results are what text and tree returned, by the key of each.
	Results map[string]string `map:"results"`
	// FilePaths are the files screenshot wrote, by the key of each.
	FilePaths map[string]string `map:"filepaths"`
	// Device is the UDID of the simulator the actions ran on.
	Device string `map:"device"`
}

// Result is the result of a step.
type Result struct {
	Req    Req           `map:"req"`
	Res    Res           `map:"res"`
	RT     time.Duration `map:"rt"`
	Status int           `map:"status"`
}

// runActions takes the actions of req in order, through c, and stops at the
// first that fails. Each action that waits for an element waits for what
// is left of the step's timeout.
func runActions(ctx context.Context, c commander, req *Req, res *Res) error {
	actx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	for i, a := range req.Actions {
		err := actx.Err()
		if err != nil {
			err = fmt.Errorf("the step's timeout ran out: %w", err)
		} else {
			err = runAction(ctx, actx, c, req.App, a, res)
		}
		if err != nil {
			return fmt.Errorf("actions[%d] %s: %w", i, a.Name, err)
		}
	}
	return nil
}

// replyGrace is how long past the deadline of the actions an action on an
// element waits for the runner to answer. The runner stops waiting for the
// element before the deadline, but XCUITest first waits for the app to be
// idle, which took 45 seconds on a simulator just booted in CI, and its
// answer says why the action failed.
var replyGrace = time.Minute

// runAction takes one action. actx bounds the actions of the step; ctx is
// the step's, which an action on an element waits on a little longer.
func runAction(ctx, actx context.Context, c commander, app string, a *Action, res *Res) error {
	cmd := map[string]any{"cmd": a.Name, "app": app}
	dctx := actx
	if kinds[a.Name].selector {
		cmd["selector"] = a.selector.wire()
		cmd["timeout"] = remaining(actx)
		if d, ok := actx.Deadline(); ok {
			var cancel context.CancelFunc
			dctx, cancel = context.WithDeadline(ctx, d.Add(replyGrace))
			defer cancel()
		}
	}
	if a.Name == "type" {
		cmd["text"] = a.Value
	}
	if a.Name == "launch" {
		cmd["args"] = a.Args
		if a.Args == nil {
			cmd["args"] = []string{}
		}
	}
	reply, err := c.Do(dctx, cmd)
	if err != nil {
		if dctx.Err() != nil {
			return fmt.Errorf("the step's timeout ran out: %w", err)
		}
		return err
	}

	switch a.Name {
	case "text":
		res.Results[a.key()] = str(reply["text"])
	case "tree":
		res.Results[a.key()] = str(reply["tree"])
	case "screenshot":
		png, err := base64.StdEncoding.DecodeString(str(reply["png"]))
		if err != nil {
			return fmt.Errorf("the runner sent a screenshot that is not base64: %w", err)
		}
		path, err := writeTemp("probe-ios-screenshot-*.png", png)
		if err != nil {
			return err
		}
		res.FilePaths[a.key()] = path
	}
	return nil
}

// remaining is the seconds left before ctx's deadline, less a moment for
// the reply to come back, for the runner to wait for an element.
func remaining(ctx context.Context) float64 {
	d, ok := ctx.Deadline()
	if !ok {
		return DefaultTimeout.Seconds()
	}
	left := time.Until(d) - 500*time.Millisecond
	return math.Max(left.Seconds(), 0)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func writeTemp(pattern string, b []byte) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}
