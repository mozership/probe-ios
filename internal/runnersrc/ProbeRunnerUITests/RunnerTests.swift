import XCTest

// RunnerTests is not a test: its one method serves the commands of
// probe-ios over HTTP until it is told to shut down, or its xcodebuild is
// stopped. probe-ios passes the port and the token through xcodebuild as
// TEST_RUNNER_PROBE_IOS_PORT and TEST_RUNNER_PROBE_IOS_TOKEN, which
// xcodebuild hands to this process without the TEST_RUNNER_ prefix.
final class RunnerTests: XCTestCase {
  override func setUp() {
    super.setUp()
    // A command that fails records an XCTest failure, such as a tap on an
    // element that went away. The runner goes on serving after it, and the
    // command reports the failure in its reply.
    continueAfterFailure = true
  }

  @MainActor
  func testRunner() throws {
    let env = ProcessInfo.processInfo.environment
    guard let rawPort = env["PROBE_IOS_PORT"], let port = UInt16(rawPort), port > 0 else {
      XCTFail("PROBE_IOS_PORT is not set to a port")
      return
    }
    guard let token = env["PROBE_IOS_TOKEN"], !token.isEmpty else {
      XCTFail("PROBE_IOS_TOKEN is not set")
      return
    }
    let server = try Server(port: port, token: token, commands: Commands())
    server.start()
    // The commands run on the main actor, which this loop keeps serving.
    while !server.stopped {
      _ = RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.5))
    }
  }
}
