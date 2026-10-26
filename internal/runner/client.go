package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// tokenHeader carries the token the runner was started with, which it
// checks on every request.
const tokenHeader = "X-Probe-Token"

// Client sends commands to a runner, which serves them on 127.0.0.1.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient returns a client of the runner on port that was started with
// token.
func NewClient(port int, token string) *Client {
	return &Client{base: "http://127.0.0.1:" + strconv.Itoa(port), token: token, http: &http.Client{}}
}

// Health returns nil once the runner answers.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return err
	}
	_, err = c.send(req)
	return err
}

// Do sends a command, such as {"cmd": "tap", ...}, and returns the reply. A
// reply whose ok is not true is an error that says what the runner said.
func (c *Client) Do(ctx context.Context, cmd map[string]any) (map[string]any, error) {
	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/command", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.send(req)
}

func (c *Client) send(req *http.Request) (map[string]any, error) {
	req.Header.Set(tokenHeader, c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var reply map[string]any
	if err := json.Unmarshal(b, &reply); err != nil {
		return nil, fmt.Errorf("the runner answered %s with what is not JSON: %q", res.Status, b)
	}
	if ok, _ := reply["ok"].(bool); !ok {
		msg, _ := reply["error"].(string)
		if msg == "" {
			msg = "the runner answered " + res.Status
		}
		return nil, errors.New(msg)
	}
	return reply, nil
}
