// Package xcode finds the Xcode that builds and runs the runner, and says
// what to do when there is none that can.
package xcode

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Output runs a command and returns what it wrote to stdout. Tests replace
// it.
type Output func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs the command on this machine.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

// Toolchain is the Xcode a runner is built with. Its fields are part of the
// key the build is cached by, so that a new Xcode builds the runner again.
type Toolchain struct {
	// DeveloperDir is what xcode-select points at.
	DeveloperDir string
	// Version is what xcodebuild -version says, such as
	// "Xcode 26.5 / Build version 17E192".
	Version string
	// SDK is the version of the iOS simulator SDK, such as "26.5".
	SDK string
}

// Detect finds the Xcode that xcode-select points at. A machine without
// one, or one whose xcode-select points at the Command Line Tools, which
// cannot build a UI test bundle, gets an error that says what to run.
func Detect(ctx context.Context, out Output) (Toolchain, error) {
	dir, err := out(ctx, "xcode-select", "-p")
	if err != nil {
		return Toolchain{}, fmt.Errorf("probe-ios needs Xcode, and xcode-select found none: %w", err)
	}
	t := Toolchain{DeveloperDir: strings.TrimSpace(string(dir))}
	if strings.Contains(t.DeveloperDir, "CommandLineTools") {
		return Toolchain{}, fmt.Errorf("probe-ios needs Xcode, but xcode-select points at the Command Line Tools (%s): install Xcode and run sudo xcode-select -s /Applications/Xcode.app", t.DeveloperDir)
	}

	version, err := out(ctx, "xcodebuild", "-version")
	if err != nil {
		return Toolchain{}, fmt.Errorf("running xcodebuild -version: %w", err)
	}
	t.Version = strings.Join(strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(version)), "\n", " / ")), " ")

	sdk, err := out(ctx, "xcrun", "--sdk", "iphonesimulator", "--show-sdk-version")
	if err != nil {
		return Toolchain{}, fmt.Errorf("%s has no iOS simulator SDK; install it with xcodebuild -downloadPlatform iOS: %w", t.Version, err)
	}
	t.SDK = strings.TrimSpace(string(sdk))
	return t, nil
}
