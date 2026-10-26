import SwiftUI

// The app the UI test bundle is hosted by. It does nothing itself: the test
// in ProbeRunnerUITests drives the app under test by its bundle identifier.
@main
struct ProbeRunnerApp: App {
  var body: some Scene {
    WindowGroup {
      Text("probe-ios runner")
    }
  }
}
