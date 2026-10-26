package runner

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
)

// fakeRunner stands in for the runner: a process of its own to stop, as
// xcodebuild is, and an HTTP server that answers as the runner does.
type fakeRunner struct {
	t      *testing.T
	starts atomic.Int32
	// exit has the process exit at once, as a runner that fails to start.
	exit bool
	// commands are the commands the server was sent, by name.
	commands chan string
	servers  []*http.Server
	// exitOnShutdown, when set, has the runner exit that long after it
	// takes the shutdown, as a runner ends its test once the command it is
	// running is over.
	exitOnShutdown time.Duration
}

func newFakeRunner(t *testing.T) *fakeRunner {
	f := &fakeRunner{t: t, commands: make(chan string, 16)}
	t.Cleanup(func() {
		for _, s := range f.servers {
			_ = s.Close()
		}
	})
	return f
}

func (f *fakeRunner) start(s Session, xctestrun, udid, test string) (int, <-chan struct{}, error) {
	f.starts.Add(1)
	if err := os.WriteFile(s.Log, []byte("Testing started\nsomething the runner wrote\n"), 0o644); err != nil {
		return 0, nil, err
	}
	script := "sleep 60"
	if f.exit {
		script = "exit 1"
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	f.t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	if !f.exit {
		f.serve(s, cmd.Process.Pid)
	}
	return cmd.Process.Pid, exited, nil
}

func (f *fakeRunner) serve(s Session, pid int) {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(s.Port))
	if err != nil {
		f.t.Fatal(err)
	}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, v map[string]any) {
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(tokenHeader) != s.Token {
			w.WriteHeader(http.StatusUnauthorized)
			reply(w, map[string]any{"ok": false, "error": "the token does not match"})
			return
		}
		reply(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/command", func(w http.ResponseWriter, r *http.Request) {
		var cmd map[string]any
		_ = json.NewDecoder(r.Body).Decode(&cmd)
		name, _ := cmd["cmd"].(string)
		f.commands <- name
		switch name {
		case "text":
			reply(w, map[string]any{"ok": true, "text": "General"})
		case "shutdown":
			reply(w, map[string]any{"ok": true})
			if f.exitOnShutdown > 0 {
				time.AfterFunc(f.exitOnShutdown, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
			}
		default:
			reply(w, map[string]any{"ok": false, "error": "no command named " + name})
		}
	})
	srv := &http.Server{Handler: mux}
	f.servers = append(f.servers, srv)
	go func() { _ = srv.Serve(l) }()
}

func testOptions(t *testing.T, f *fakeRunner) Options {
	return Options{
		Root:         t.TempDir(),
		RunID:        "run-1",
		UDID:         "AAAA",
		XCTestRun:    "/cache/ProbeRunner.xctestrun",
		Test:         "ProbeRunnerUITests/RunnerTests/testRunner",
		StartTimeout: 5 * time.Second,
		Log:          hclog.NewNullLogger(),
		Start:        f.start,
	}
}

func TestOpenStartsOnceForARun(t *testing.T) {
	f := newFakeRunner(t)
	opts := testOptions(t, f)

	c, release, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := c.Do(context.Background(), map[string]any{"cmd": "text"})
	if err != nil {
		t.Fatal(err)
	}
	if reply["text"] != "General" {
		t.Errorf("reply = %v", reply)
	}
	release()

	// The next step of the run takes the runner the first one started.
	_, release, err = Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if got := f.starts.Load(); got != 1 {
		t.Errorf("the runner was started %d times for one run, want once", got)
	}

	// Another run starts its own.
	opts.RunID = "run-2"
	_, release, err = Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if got := f.starts.Load(); got != 2 {
		t.Errorf("the runner was started %d times for two runs, want twice", got)
	}
}

func TestOpenStartsAgainWhenTheRunnerIsGone(t *testing.T) {
	f := newFakeRunner(t)
	opts := testOptions(t, f)
	_, release, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	release()
	for _, s := range f.servers {
		_ = s.Close()
	}

	c, release, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := c.Health(context.Background()); err != nil {
		t.Errorf("the new runner does not answer: %v", err)
	}
	if got := f.starts.Load(); got != 2 {
		t.Errorf("the runner was started %d times, want twice", got)
	}
}

func TestOpenRunnerThatExits(t *testing.T) {
	f := newFakeRunner(t)
	f.exit = true
	opts := testOptions(t, f)

	_, _, err := Open(context.Background(), opts)
	if err == nil {
		t.Fatal("Open of a runner that exits succeeded")
	}
	for _, want := range []string{"exited before it answered", "something the runner wrote", "AAAA.log"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(RunDir(opts.Root, opts.RunID), "AAAA.json")); !os.IsNotExist(err) {
		t.Errorf("the session of a runner that failed was kept: %v", err)
	}
}

func withShutdownWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := shutdownWait
	shutdownWait = d
	t.Cleanup(func() { shutdownWait = old })
}

func TestCleanup(t *testing.T) {
	// The runner takes the shutdown but does not exit, so it is stopped.
	withShutdownWait(t, 100*time.Millisecond)
	f := newFakeRunner(t)
	opts := testOptions(t, f)
	_, release, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s, err := readSession(filepath.Join(RunDir(opts.Root, opts.RunID), "AAAA.json"))
	if err != nil {
		t.Fatal(err)
	}

	if err := Cleanup(opts.Root, opts.RunID); err != nil {
		t.Fatal(err)
	}
	if alive(s.PID) {
		t.Error("the runner is still running after the cleanup")
	}
	select {
	case name := <-f.commands:
		if name != "shutdown" {
			t.Errorf("the cleanup sent %q, want shutdown", name)
		}
	default:
		t.Error("the cleanup did not ask the runner to shut down")
	}
	if _, err := os.Stat(RunDir(opts.Root, opts.RunID)); !os.IsNotExist(err) {
		t.Errorf("the run's directory was kept: %v", err)
	}

	// A cleanup of a run that left nothing, or was cleaned up already,
	// does not fail.
	if err := Cleanup(opts.Root, opts.RunID); err != nil {
		t.Errorf("a second cleanup = %v", err)
	}
}

func TestClientErrors(t *testing.T) {
	f := newFakeRunner(t)
	s := Session{Port: mustFreePort(t), Token: "secret"}
	f.serve(s, 0)

	if err := NewClient(s.Port, "wrong").Health(context.Background()); err == nil || !strings.Contains(err.Error(), "the token does not match") {
		t.Errorf("Health with a wrong token = %v", err)
	}
	_, err := NewClient(s.Port, s.Token).Do(context.Background(), map[string]any{"cmd": "fly"})
	if err == nil || err.Error() != "no command named fly" {
		t.Errorf("Do of an unknown command = %v, want what the runner said", err)
	}
}

func mustFreePort(t *testing.T) int {
	t.Helper()
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCleanupWaitsForTheRunnerToExit(t *testing.T) {
	withShutdownWait(t, 10*time.Second)
	f := newFakeRunner(t)
	f.exitOnShutdown = 500 * time.Millisecond
	opts := testOptions(t, f)
	_, release, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	release()
	s, err := readSession(filepath.Join(RunDir(opts.Root, opts.RunID), "AAAA.json"))
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := Cleanup(opts.Root, opts.RunID); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if alive(s.PID) {
		t.Error("the runner is still running after the cleanup")
	}
	// It waited for the runner to exit on its own, and no longer.
	if took < 500*time.Millisecond || took > 5*time.Second {
		t.Errorf("the cleanup took %s, want about the half second the runner took to exit", took)
	}
}
