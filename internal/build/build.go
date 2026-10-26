// Package build builds the XCUITest runner with the Xcode of the machine
// probe-ios runs on, and keeps the build in a cache, so that only the first
// step after the action or Xcode changes waits for it.
package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe-ios/internal/filelock"
	"github.com/mozership/probe-ios/internal/xcode"
)

// format changes when what a cache directory holds changes, so that a
// newer probe-ios does not read an older layout.
const format = "1"

// ready names the file written into a cache directory once its build has
// succeeded. It holds the path of the .xctestrun, relative to the
// directory.
const ready = "ready"

// tailLines is how much of the build log an error shows.
const tailLines = 30

// Exec runs name with args in dir, writing its output to out, and stops it
// once ctx is done. Tests replace it.
type Exec func(ctx context.Context, out io.Writer, dir, name string, args ...string) error

// Options are what Ensure builds the runner from.
type Options struct {
	// Root is the directory the builds are cached in, one directory each.
	Root string
	// Source is the Xcode project, and Project and Scheme name it in there.
	Source  fs.FS
	Project string
	Scheme  string
	// Toolchain is the Xcode the runner is built with.
	Toolchain xcode.Toolchain
	// Arch is the architecture of the simulators, arm64 or x86_64.
	Arch string
	// Log tells a person that the build has started, as it takes minutes.
	Log  hclog.Logger
	Exec Exec
}

// Runner is a runner built and ready to run.
type Runner struct {
	// Dir is the cache directory the runner is in.
	Dir string
	// XCTestRun is the file xcodebuild test-without-building runs it by.
	XCTestRun string
}

// Ensure returns the runner built from opts, building it first when the
// cache has none. Steps that start at once build it once: the others wait
// for that build and take it.
func Ensure(ctx context.Context, opts Options) (Runner, error) {
	key, err := Key(opts)
	if err != nil {
		return Runner{}, err
	}
	dir := filepath.Join(opts.Root, key)
	if r, ok := cached(dir); ok {
		return r, nil
	}
	if err := os.MkdirAll(opts.Root, 0o755); err != nil {
		return Runner{}, err
	}

	unlock, err := filelock.Lock(ctx, dir+".lock", func() {
		opts.Log.Warn("waiting for the iOS test runner being built by another step")
	})
	if err != nil {
		return Runner{}, fmt.Errorf("waiting for the iOS test runner being built by another step: %w", err)
	}
	defer unlock()
	// The step that held the lock may have built it in the meantime.
	if r, ok := cached(dir); ok {
		return r, nil
	}

	// The step's own timeout, beside its with, bounds the build; the
	// timeout in with bounds only the actions.
	opts.Log.Warn("building the iOS test runner; the first run takes a few minutes, so give the step timeout: 10m if it runs out",
		"xcode", opts.Toolchain.Version, "cache", dir)
	start := time.Now()
	r, err := build(ctx, opts, dir)
	if err != nil {
		return Runner{}, err
	}
	opts.Log.Debug("built the iOS test runner", "took", time.Since(start).Round(time.Second).String())
	return r, nil
}

// cached returns the runner in dir when a build has finished there.
func cached(dir string) (Runner, bool) {
	b, err := os.ReadFile(filepath.Join(dir, ready))
	if err != nil {
		return Runner{}, false
	}
	xctestrun := filepath.Join(dir, strings.TrimSpace(string(b)))
	if _, err := os.Stat(xctestrun); err != nil {
		return Runner{}, false
	}
	return Runner{Dir: dir, XCTestRun: xctestrun}, true
}

// build builds the runner in a directory of its own next to dir, and moves
// it to dir once it has succeeded, so that a build that fails or is stopped
// halfway leaves nothing that cached takes.
func build(ctx context.Context, opts Options, dir string) (Runner, error) {
	tmp, err := os.MkdirTemp(opts.Root, filepath.Base(dir)+".tmp-")
	if err != nil {
		return Runner{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	src := filepath.Join(tmp, "src")
	if err := os.CopyFS(src, opts.Source); err != nil {
		return Runner{}, fmt.Errorf("writing the runner's source: %w", err)
	}

	// The log is kept next to the builds, as the temporary directory is
	// removed, so that the error can point at it.
	logPath := filepath.Join(opts.Root, "last-build.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return Runner{}, err
	}
	derived := filepath.Join(tmp, "DerivedData")
	err = opts.Exec(ctx, logFile, src, "xcodebuild", "build-for-testing",
		"-project", opts.Project,
		"-scheme", opts.Scheme,
		"-sdk", "iphonesimulator",
		"-destination", "generic/platform=iOS Simulator",
		"-derivedDataPath", derived,
		"ARCHS="+opts.Arch,
		"ONLY_ACTIVE_ARCH=NO",
	)
	_ = logFile.Close()
	if err != nil {
		if ctx.Err() != nil {
			return Runner{}, fmt.Errorf("building the iOS test runner was stopped: %w", ctx.Err())
		}
		return Runner{}, fmt.Errorf("building the iOS test runner failed: %w\n%s\nThe whole log is %s", err, tail(logPath, tailLines), logPath)
	}

	matches, err := filepath.Glob(filepath.Join(derived, "Build", "Products", "*.xctestrun"))
	if err != nil || len(matches) == 0 {
		return Runner{}, fmt.Errorf("xcodebuild built the iOS test runner but wrote no .xctestrun; the log is %s", logPath)
	}
	rel, err := filepath.Rel(tmp, matches[0])
	if err != nil {
		return Runner{}, err
	}
	if err := os.RemoveAll(filepath.Join(tmp, "src")); err != nil {
		return Runner{}, err
	}
	if err := os.WriteFile(filepath.Join(tmp, ready), []byte(rel+"\n"), 0o644); err != nil {
		return Runner{}, err
	}
	// A directory left by a build that was stopped after its rename is
	// taken by cached, so one without ready is all that can be here.
	if err := os.RemoveAll(dir); err != nil {
		return Runner{}, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return Runner{}, err
	}
	return Runner{Dir: dir, XCTestRun: filepath.Join(dir, rel)}, nil
}

// Key is the name of the cache directory of the runner opts builds: it
// changes with the runner's source, the Xcode and its SDK, and the
// architecture, any of which needs a new build.
func Key(opts Options) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "format %s\nxcode %s\nsdk %s\narch %s\n", format, opts.Toolchain.Version, opts.Toolchain.SDK, opts.Arch)
	var files []string
	err := fs.WalkDir(opts.Source, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("reading the runner's source: %w", err)
	}
	sort.Strings(files)
	for _, p := range files {
		b, err := fs.ReadFile(opts.Source, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "file %s %d\n", path.Clean(p), len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// tail returns the last n lines of the file at p, or nothing when it cannot
// be read.
func tail(p string, n int) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ErrNoArch is returned by Arch for a machine whose simulators probe-ios
// does not know.
var ErrNoArch = errors.New("iOS simulators run only on arm64 and amd64 Macs")

// Arch returns the architecture the simulators of a Mac whose GOARCH is
// goarch run.
func Arch(goarch string) (string, error) {
	switch goarch {
	case "arm64":
		return "arm64", nil
	case "amd64":
		return "x86_64", nil
	default:
		return "", ErrNoArch
	}
}
