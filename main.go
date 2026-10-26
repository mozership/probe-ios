// Command probe-ios serves a Probe action that drives an app on an iOS
// simulator. Probe starts it for a step that uses
// github.com/mozership/probe-ios@<commit>.
package main

import "github.com/mozership/probe-ios/ios"

// version is set at build time.
var version = "dev"

func main() {
	ios.Serve()
}
