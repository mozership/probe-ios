// Package runnersrc holds the source of the XCUITest runner, an Xcode
// project that probe-ios builds on the machine it runs on, with that
// machine's Xcode, the first time a step needs it.
package runnersrc

import "embed"

// FS is the Xcode project: ProbeRunner.xcodeproj, the host app in
// ProbeRunner and the runner itself in ProbeRunnerUITests.
//
//go:embed ProbeRunner.xcodeproj ProbeRunner ProbeRunnerUITests
var FS embed.FS

const (
	// Project is the Xcode project in FS.
	Project = "ProbeRunner.xcodeproj"
	// Scheme builds the runner for testing.
	Scheme = "ProbeRunner"
	// Test is the one test that serves the commands, for -only-testing.
	Test = "ProbeRunnerUITests/RunnerTests/testRunner"
)
