package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe-ios/internal/xcode"
)

// fakeXcodebuild stands in for xcodebuild build-for-testing: it writes an
// .xctestrun under the -derivedDataPath it is given, as xcodebuild does.
type fakeXcodebuild struct {
	calls atomic.Int32
	fail  bool
	// gate, when not nil, holds each build until it is closed.
	gate chan struct{}
}

func (f *fakeXcodebuild) exec(ctx context.Context, out io.Writer, dir, name string, args ...string) error {
	f.calls.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	if _, err := os.Stat(filepath.Join(dir, "ProbeRunner.xcodeproj", "project.pbxproj")); err != nil {
		return fmt.Errorf("the source was not written to the build's directory: %w", err)
	}
	if f.fail {
		fmt.Fprintln(out, "error: something went wrong")
		return errors.New("exit status 65")
	}
	derived := args[slices.Index(args, "-derivedDataPath")+1]
	products := filepath.Join(derived, "Build", "Products")
	if err := os.MkdirAll(products, 0o755); err != nil {
		return err
	}
	fmt.Fprintln(out, "** TEST BUILD SUCCEEDED **")
	return os.WriteFile(filepath.Join(products, "ProbeRunner_iphonesimulator26.5-arm64.xctestrun"), []byte("plist"), 0o644)
}

func options(t *testing.T, f *fakeXcodebuild, log *bytes.Buffer) Options {
	t.Helper()
	return Options{
		Root: t.TempDir(),
		Source: fstest.MapFS{
			"ProbeRunner.xcodeproj/project.pbxproj": {Data: []byte("project")},
			"ProbeRunnerUITests/RunnerTests.swift":  {Data: []byte("tests")},
		},
		Project:   "ProbeRunner.xcodeproj",
		Scheme:    "ProbeRunner",
		Toolchain: xcode.Toolchain{Version: "Xcode 26.5 / Build version 17E192", SDK: "26.5"},
		Arch:      "arm64",
		Log:       hclog.New(&hclog.LoggerOptions{Output: log, Level: hclog.Debug}),
		Exec:      f.exec,
	}
}

func TestEnsureBuildsOnceAndCaches(t *testing.T) {
	f := &fakeXcodebuild{}
	var log bytes.Buffer
	opts := options(t, f, &log)

	r, err := Ensure(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.XCTestRun, ".xctestrun") {
		t.Errorf("XCTestRun = %q, want an .xctestrun", r.XCTestRun)
	}
	if _, err := os.Stat(r.XCTestRun); err != nil {
		t.Errorf("the .xctestrun is not in the cache: %v", err)
	}
	if !strings.Contains(log.String(), "building the iOS test runner; the first run takes a few minutes") {
		t.Errorf("the first build was not announced; the log is:\n%s", log.String())
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "src")); !os.IsNotExist(err) {
		t.Errorf("the source was left in the cache: %v", err)
	}

	log.Reset()
	again, err := Ensure(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if again != r {
		t.Errorf("Ensure from the cache = %+v, want %+v", again, r)
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("xcodebuild ran %d times, want once", got)
	}
	if log.Len() != 0 {
		t.Errorf("a cached runner logged:\n%s", log.String())
	}
}

func TestEnsureBuildsAgainForAnotherXcode(t *testing.T) {
	f := &fakeXcodebuild{}
	var log bytes.Buffer
	opts := options(t, f, &log)
	first, err := Ensure(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}

	opts.Toolchain.Version = "Xcode 27.0 / Build version 27A266a"
	second, err := Ensure(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Dir == second.Dir {
		t.Error("a new Xcode took the runner the old one built")
	}
	if got := f.calls.Load(); got != 2 {
		t.Errorf("xcodebuild ran %d times, want twice", got)
	}
}

func TestEnsureFailure(t *testing.T) {
	f := &fakeXcodebuild{fail: true}
	var log bytes.Buffer
	opts := options(t, f, &log)

	_, err := Ensure(context.Background(), opts)
	if err == nil {
		t.Fatal("Ensure of a failing build succeeded")
	}
	for _, want := range []string{"building the iOS test runner failed", "error: something went wrong", "last-build.log"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
	entries, _ := os.ReadDir(opts.Root)
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("a failed build left %s in the cache", e.Name())
		}
	}

	// A later step builds again rather than taking the failure.
	f.fail = false
	if _, err := Ensure(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureConcurrentStepsBuildOnce(t *testing.T) {
	f := &fakeXcodebuild{gate: make(chan struct{})}
	var log bytes.Buffer
	var logMu sync.Mutex
	opts := options(t, f, &log)
	opts.Log = hclog.New(&hclog.LoggerOptions{Output: &lockedWriter{w: &log, mu: &logMu}, Level: hclog.Debug})

	const steps = 3
	errs := make(chan error, steps)
	for range steps {
		go func() {
			_, err := Ensure(context.Background(), opts)
			errs <- err
		}()
	}
	// Let the others reach the lock before the build finishes.
	for {
		logMu.Lock()
		waiting := strings.Count(log.String(), "waiting for the iOS test runner")
		logMu.Unlock()
		if waiting == steps-1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(f.gate)
	for range steps {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := f.calls.Load(); got != 1 {
		t.Errorf("xcodebuild ran %d times for %d steps at once, want once", got, steps)
	}
}

func TestEnsureStopped(t *testing.T) {
	f := &fakeXcodebuild{}
	var log bytes.Buffer
	opts := options(t, f, &log)
	ctx, cancel := context.WithCancel(context.Background())
	opts.Exec = func(ctx context.Context, out io.Writer, dir, name string, args ...string) error {
		cancel()
		return ctx.Err()
	}
	_, err := Ensure(ctx, opts)
	if err == nil || !strings.Contains(err.Error(), "was stopped") {
		t.Errorf("Ensure of a stopped build = %v, want one that says it was stopped", err)
	}
}

func TestKey(t *testing.T) {
	var log bytes.Buffer
	base := options(t, &fakeXcodebuild{}, &log)
	key := func(o Options) string {
		k, err := Key(o)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	k := key(base)
	if key(base) != k {
		t.Error("the key is not the same for the same options")
	}

	changed := map[string]func(o *Options){
		"source": func(o *Options) {
			o.Source = fstest.MapFS{
				"ProbeRunner.xcodeproj/project.pbxproj": {Data: []byte("project")},
				"ProbeRunnerUITests/RunnerTests.swift":  {Data: []byte("tests changed")},
			}
		},
		"Xcode": func(o *Options) { o.Toolchain.Version = "Xcode 27.0" },
		"SDK":   func(o *Options) { o.Toolchain.SDK = "27.0" },
		"arch":  func(o *Options) { o.Arch = "x86_64" },
	}
	for name, change := range changed {
		o := base
		change(&o)
		if key(o) == k {
			t.Errorf("the key does not change with the %s", name)
		}
	}
}

func TestArch(t *testing.T) {
	for goarch, want := range map[string]string{"arm64": "arm64", "amd64": "x86_64"} {
		if got, err := Arch(goarch); err != nil || got != want {
			t.Errorf("Arch(%q) = %q, %v, want %q", goarch, got, err, want)
		}
	}
	if _, err := Arch("386"); !errors.Is(err, ErrNoArch) {
		t.Errorf("Arch(386) = %v, want ErrNoArch", err)
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
