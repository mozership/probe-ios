import Foundation
import Network

// Server answers HTTP/1.1 on 127.0.0.1 at the port probe-ios chose. A
// simulator shares the Mac's network, so the port is the Mac's own.
//
//   GET  /health   {"ok": true}
//   POST /command  a command for Commands, as a JSON object
//
// Each request carries the token in X-Probe-Token, so that another process
// on the Mac cannot drive the simulator through the port. One request is
// served per connection.
final class Server {
  private let listener: NWListener
  private let token: String
  private let commands: Commands
  private let queue = DispatchQueue(label: "probe-ios.server")

  // stopped is set by the shutdown command, as soon as it comes, and read
  // by the loop of the test on the main actor.
  private let lock = NSLock()
  private var stopRequested = false
  var stopped: Bool {
    lock.lock()
    defer { lock.unlock() }
    return stopRequested
  }

  init(port: UInt16, token: String, commands: Commands) throws {
    guard let nwPort = NWEndpoint.Port(rawValue: port) else {
      throw Failure("\(port) is not a port")
    }
    let parameters = NWParameters.tcp
    parameters.allowLocalEndpointReuse = true
    parameters.requiredLocalEndpoint = NWEndpoint.hostPort(host: "127.0.0.1", port: nwPort)
    listener = try NWListener(using: parameters)
    self.token = token
    self.commands = commands
  }

  func start() {
    listener.newConnectionHandler = { [weak self] connection in
      self?.accept(connection)
    }
    listener.stateUpdateHandler = { state in
      if case .failed(let error) = state {
        NSLog("probe-ios runner: the listener failed: \(error)")
      }
    }
    listener.start(queue: queue)
  }

  private func accept(_ connection: NWConnection) {
    connection.start(queue: queue)
    receive(connection, buffer: Data())
  }

  private func receive(_ connection: NWConnection, buffer: Data) {
    connection.receive(minimumIncompleteLength: 1, maximumLength: 1 << 20) {
      [weak self] data, _, isComplete, error in
      guard let self else {
        connection.cancel()
        return
      }
      var buffer = buffer
      if let data {
        buffer.append(data)
      }
      switch HTTPRequest.parse(buffer) {
      case .complete(let request):
        self.serve(request, on: connection)
      case .malformed(let reason):
        self.respond(connection, status: 400, body: ["ok": false, "error": reason])
      case .incomplete:
        if isComplete || error != nil {
          connection.cancel()
        } else {
          self.receive(connection, buffer: buffer)
        }
      }
    }
  }

  private func serve(_ request: HTTPRequest, on connection: NWConnection) {
    guard request.headers["x-probe-token"] == token else {
      respond(connection, status: 401, body: ["ok": false, "error": "the token does not match"])
      return
    }
    switch (request.method, request.path) {
    case ("GET", "/health"):
      respond(connection, status: 200, body: ["ok": true])
    case ("POST", "/command") where Server.isShutdown(request.body):
      // The shutdown is answered here, not on the main actor, where a
      // command can keep XCUITest busy for long on a slow machine. The test
      // ends once that command is over.
      lock.lock()
      stopRequested = true
      lock.unlock()
      respond(connection, status: 200, body: ["ok": true])
    case ("POST", "/command"):
      Task { @MainActor in
        let reply = self.commands.handle(request.body)
        self.respond(connection, status: 200, body: reply.body)
      }
    default:
      respond(connection, status: 404, body: ["ok": false, "error": "no such path"])
    }
  }

  private func respond(_ connection: NWConnection, status: Int, body: [String: Any]) {
    let payload =
      (try? JSONSerialization.data(withJSONObject: body))
      ?? Data(#"{"ok":false,"error":"the reply is not JSON"}"#.utf8)
    var head = "HTTP/1.1 \(status) \(Server.reason(status))\r\n"
    head += "Content-Type: application/json\r\n"
    head += "Content-Length: \(payload.count)\r\n"
    head += "Connection: close\r\n\r\n"
    var data = Data(head.utf8)
    data.append(payload)
    connection.send(
      content: data,
      completion: .contentProcessed { _ in
        connection.cancel()
      })
  }

  private static func isShutdown(_ body: Data) -> Bool {
    guard let object = try? JSONSerialization.jsonObject(with: body),
      let command = object as? [String: Any]
    else {
      return false
    }
    return command["cmd"] as? String == "shutdown"
  }

  private static func reason(_ status: Int) -> String {
    switch status {
    case 200: return "OK"
    case 400: return "Bad Request"
    case 401: return "Unauthorized"
    case 404: return "Not Found"
    default: return "Error"
    }
  }
}

// HTTPRequest is a request read off a connection: enough of HTTP/1.1 for
// the client of probe-ios, which sends a Content-Length with every body.
struct HTTPRequest {
  enum Parse {
    case complete(HTTPRequest)
    case incomplete
    case malformed(String)
  }

  let method: String
  let path: String
  // headers are keyed by their names in lower case.
  let headers: [String: String]
  let body: Data

  static func parse(_ buffer: Data) -> Parse {
    let separator = Data("\r\n\r\n".utf8)
    guard let end = buffer.range(of: separator) else {
      return buffer.count > 64 * 1024 ? .malformed("the header is too long") : .incomplete
    }
    guard let head = String(data: buffer[buffer.startIndex..<end.lowerBound], encoding: .utf8) else {
      return .malformed("the header is not UTF-8")
    }
    let lines = head.components(separatedBy: "\r\n")
    let requestLine = lines[0].split(separator: " ")
    guard requestLine.count == 3 else {
      return .malformed("the request line is not METHOD PATH VERSION")
    }
    var headers: [String: String] = [:]
    for line in lines.dropFirst() {
      guard let colon = line.firstIndex(of: ":") else {
        return .malformed("a header has no colon")
      }
      let name = line[..<colon].trimmingCharacters(in: .whitespaces).lowercased()
      let value = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
      headers[name] = value
    }
    let length = Int(headers["content-length"] ?? "0") ?? -1
    guard length >= 0 else {
      return .malformed("Content-Length is not a number")
    }
    let bodyStart = end.upperBound
    guard buffer.count - (bodyStart - buffer.startIndex) >= length else {
      return .incomplete
    }
    let body = buffer[bodyStart..<(bodyStart + length)]
    return .complete(
      HTTPRequest(
        method: String(requestLine[0]), path: String(requestLine[1]), headers: headers,
        body: Data(body)))
  }
}

// Failure is an error whose message is what the reply says.
struct Failure: Error {
  let message: String

  init(_ message: String) {
    self.message = message
  }
}
