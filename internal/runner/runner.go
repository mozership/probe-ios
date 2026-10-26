// Package runner starts the XCUITest runner on a simulator and keeps it for
// the steps of a run. Each step runs in a process of its own, which exits
// once the step is done, so the runner outlives it, and the run's cleanup
// stops it.
//
// The runners of a run are kept under Root/<run>/, by the UDID of their
// simulators:
//
//	<udid>.lock  held by the step using the simulator
//	<udid>.json  the Session of the runner
//	<udid>.log   what its xcodebuild writes
package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe-ios/internal/filelock"
)

const (
	// DefaultStartTimeout bounds how long a runner takes to answer once it
	// is started: xcodebuild installs it on the simulator and launches it,
	// which takes longer the first time.
	DefaultStartTimeout = 3 * time.Minute
	// poll is how often a starting runner is asked whether it is up.
	poll = 500 * time.Millisecond
	// stopWait is how long a runner is given to exit at each stage of Stop.
	stopWait = 5 * time.Second
	// tailLines is how much of a runner's log an error shows.
	tailLines = 20
)

// Session is a runner started for a run, as its file keeps it.
type Session struct {
	// PID leads the process group of the runner's xcodebuild.
	PID   int    `json:"pid"`
	Port  int    `json:"port"`
	Token string `json:"token"`
	Log   string `json:"log"`
}

// Start starts the runner of s and returns the PID of the process that
// leads its group, and a channel closed once that process exits. Tests
// replace it.
type Start func(s Session, xctestrun, udid, test string) (pid int, exited <-chan struct{}, err error)

// Options are what Open starts or finds a runner by.
type Options struct {
	// Root is the directory the runners of every run are kept under.
	Root string
	// RunID names the run, which keeps one runner per simulator.
	RunID string
	// UDID is the simulator the runner runs on.
	UDID string
	// XCTestRun and Test are what xcodebuild test-without-building runs.
	XCTestRun string
	Test      string
	// StartTimeout bounds how long a new runner takes to answer; zero is
	// DefaultStartTimeout.
	StartTimeout time.Duration
	Log          hclog.Logger
	// Start is nil for the runner xcodebuild starts.
	Start Start
}

// Open returns a client of the run's runner on the simulator, starting one
// when the run has none or the one it had has gone. It holds the simulator
// for the step until release is called, so that the steps of the run that
// use one simulator take turns.
func Open(ctx context.Context, opts Options) (c *Client, release func(), err error) {
	dir := RunDir(opts.Root, opts.RunID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	base := filepath.Join(dir, opts.UDID)
	unlock, err := filelock.Lock(ctx, base+".lock", func() {
		opts.Log.Debug("waiting for another step to be done with the simulator", "udid", opts.UDID)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("waiting for another step to be done with the simulator: %w", err)
	}
	defer func() {
		if err != nil {
			unlock()
		}
	}()

	if s, err := readSession(base + ".json"); err == nil {
		c := NewClient(s.Port, s.Token)
		if c.Health(ctx) == nil {
			return c, unlock, nil
		}
		opts.Log.Debug("the runner of the run is gone; starting another", "pid", s.PID)
		stop(s)
	}

	c, err = start(ctx, opts, base)
	if err != nil {
		return nil, nil, err
	}
	return c, unlock, nil
}

func start(ctx context.Context, opts Options, base string) (*Client, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	s := Session{Port: port, Token: token, Log: base + ".log"}
	startFn := opts.Start
	if startFn == nil {
		startFn = xcodebuild
	}
	opts.Log.Debug("starting the iOS test runner", "udid", opts.UDID, "port", port, "log", s.Log)
	pid, exited, err := startFn(s, opts.XCTestRun, opts.UDID, opts.Test)
	if err != nil {
		return nil, fmt.Errorf("starting the iOS test runner: %w", err)
	}
	s.PID = pid
	// The session is written before the runner answers, so that the run's
	// cleanup stops it even when the step is stopped while it starts.
	if err := writeSession(base+".json", s); err != nil {
		stop(s)
		return nil, err
	}

	timeout := opts.StartTimeout
	if timeout == 0 {
		timeout = DefaultStartTimeout
	}
	c := NewClient(port, token)
	if err := await(ctx, c, exited, timeout); err != nil {
		stop(s)
		_ = os.Remove(base + ".json")
		return nil, fmt.Errorf("%w\n%s\nThe whole log is %s", err, tail(s.Log, tailLines), s.Log)
	}
	return c, nil
}

// await waits until the runner answers, its process exits, timeout runs
// out or ctx is done.
func await(ctx context.Context, c *Client, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		hctx, cancel := context.WithTimeout(ctx, poll)
		err := c.Health(hctx)
		cancel()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the step was stopped while the iOS test runner started: %w", ctx.Err())
		case <-exited:
			return errors.New("the iOS test runner exited before it answered")
		case <-deadline.C:
			return fmt.Errorf("the iOS test runner did not answer within %s", timeout)
		case <-time.After(poll):
		}
	}
}

// xcodebuild starts the runner with xcodebuild test-without-building, in a
// session of its own so that it outlives the step's process.
func xcodebuild(s Session, xctestrun, udid, test string) (int, <-chan struct{}, error) {
	log, err := os.Create(s.Log)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = log.Close() }()
	// The test results go beside the log, in the run's directory that
	// Cleanup removes, not in ~/Library/Developer/Xcode/DerivedData, where
	// each runner would leave a directory of its own.
	data := filepath.Join(filepath.Dir(s.Log), udid+".derived")
	cmd := exec.Command("xcodebuild", "test-without-building",
		"-xctestrun", xctestrun,
		"-destination", "id="+udid,
		"-only-testing:"+test,
		"-derivedDataPath", data,
	)
	// xcodebuild hands the variables prefixed with TEST_RUNNER_ to the test
	// without the prefix.
	cmd.Env = append(os.Environ(),
		"TEST_RUNNER_PROBE_IOS_PORT="+strconv.Itoa(s.Port),
		"TEST_RUNNER_PROBE_IOS_TOKEN="+s.Token,
	)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	return cmd.Process.Pid, exited, nil
}

// Cleanup stops the runners of the run and removes what was kept of them.
// It does not fail for a runner that is gone already.
func Cleanup(root, runID string) error {
	dir := RunDir(root, runID)
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, f := range files {
		if s, err := readSession(f); err == nil {
			shutdown(s)
			stop(s)
		}
	}
	return os.RemoveAll(dir)
}

// shutdown asks the runner to end its test, which has xcodebuild exit on
// its own.
func shutdown(s Session) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := NewClient(s.Port, s.Token).Do(ctx, map[string]any{"cmd": "shutdown"}); err == nil {
		waitGone(s.PID, stopWait)
	}
}

// stop stops the process group of the runner: SIGTERM first, then SIGKILL
// for one that has not exited.
func stop(s Session) {
	if s.PID <= 0 || !alive(s.PID) {
		return
	}
	_ = syscall.Kill(-s.PID, syscall.SIGTERM)
	if waitGone(s.PID, stopWait) {
		return
	}
	_ = syscall.Kill(-s.PID, syscall.SIGKILL)
	waitGone(s.PID, stopWait)
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitGone reports whether the process exits within d.
func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !alive(pid)
}

// RunDir is the directory the runners of the run are kept in. The run's ID
// is hashed, so that it names a directory whatever it holds.
func RunDir(root, runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(root, hex.EncodeToString(sum[:])[:16])
}

func readSession(path string) (Session, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Session{}, err
	}
	var s Session
	if err := json.Unmarshal(b, &s); err != nil {
		return Session{}, err
	}
	return s, nil
}

func writeSession(path string, s Session) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// freePort returns a port of 127.0.0.1 nothing listens on.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
