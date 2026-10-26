import XCTest

// Reply is what a command answers: a JSON object with ok, and error when it
// failed.
struct Reply {
  var body: [String: Any]
  var shutdown = false

  static func success(_ fields: [String: Any] = [:]) -> Reply {
    var body = fields
    body["ok"] = true
    return Reply(body: body)
  }

  static func failure(_ message: String) -> Reply {
    Reply(body: ["ok": false, "error": message])
  }
}

// Commands runs the commands probe-ios sends, each a JSON object:
//
//   {"cmd": "launch", "app": "com.example.App", "args": ["-flag"]}
//   {"cmd": "activate" | "terminate", "app": "..."}
//   {"cmd": "tap" | "wait" | "text", "app": "...", "selector": {...}, "timeout": 5}
//   {"cmd": "type", "app": "...", "selector": {...}, "text": "...", "timeout": 5}
//   {"cmd": "screenshot"}
//   {"cmd": "tree", "app": "..."}
//   {"cmd": "shutdown"}
//
// A selector is {"identifier", "label", "type", "index"}, every key
// optional: the element of the type with that accessibility identifier and
// label, the index-th of them from 0, or the first.
@MainActor
final class Commands {
  // types are the element types a selector names, as probe-ios checks them.
  static let types: [String: XCUIElement.ElementType] = [
    "any": .any,
    "alert": .alert,
    "button": .button,
    "cell": .cell,
    "image": .image,
    "link": .link,
    "navigationbar": .navigationBar,
    "other": .other,
    "searchfield": .searchField,
    "securetextfield": .secureTextField,
    "slider": .slider,
    "switch": .switch,
    "tabbar": .tabBar,
    "text": .staticText,
    "textfield": .textField,
    "textview": .textView,
  ]

  func handle(_ body: Data) -> Reply {
    guard let object = try? JSONSerialization.jsonObject(with: body),
      let command = object as? [String: Any],
      let name = command["cmd"] as? String
    else {
      return .failure("the command is not a JSON object with cmd")
    }
    do {
      return try run(name, command)
    } catch let failure as Failure {
      return .failure(failure.message)
    } catch {
      return .failure("\(error)")
    }
  }

  private func run(_ name: String, _ command: [String: Any]) throws -> Reply {
    switch name {
    case "launch":
      let app = try application(command)
      app.launchArguments = command["args"] as? [String] ?? []
      app.launch()
      return .success()
    case "activate":
      try application(command).activate()
      return .success()
    case "terminate":
      try application(command).terminate()
      return .success()
    case "tap":
      try element(command).tap()
      return .success()
    case "type":
      guard let text = command["text"] as? String else {
        throw Failure("type needs text")
      }
      let field = try element(command)
      field.tap()
      field.typeText(text)
      return .success()
    case "wait":
      _ = try element(command)
      return .success()
    case "text":
      return .success(["text": Commands.text(of: try element(command))])
    case "screenshot":
      let png = XCUIScreen.main.screenshot().pngRepresentation
      return .success(["png": png.base64EncodedString()])
    case "tree":
      return .success(["tree": try application(command).debugDescription])
    case "shutdown":
      var reply = Reply.success()
      reply.shutdown = true
      return reply
    default:
      throw Failure("no command named \(name)")
    }
  }

  private func application(_ command: [String: Any]) throws -> XCUIApplication {
    guard let bundleID = command["app"] as? String, !bundleID.isEmpty else {
      throw Failure("the command names no app")
    }
    return XCUIApplication(bundleIdentifier: bundleID)
  }

  // element waits for the element the command's selector names, for its
  // timeout in seconds, and fails if none appears by then.
  private func element(_ command: [String: Any]) throws -> XCUIElement {
    let app = try application(command)
    guard let selector = command["selector"] as? [String: Any] else {
      throw Failure("the command has no selector")
    }
    let typeName = selector["type"] as? String ?? "any"
    guard let type = Commands.types[typeName] else {
      throw Failure("no element type named \(typeName)")
    }
    var query = app.descendants(matching: type)
    var predicates: [NSPredicate] = []
    if let identifier = selector["identifier"] as? String {
      predicates.append(NSPredicate(format: "identifier == %@", identifier))
    }
    if let label = selector["label"] as? String {
      predicates.append(NSPredicate(format: "label == %@", label))
    }
    if !predicates.isEmpty {
      query = query.matching(NSCompoundPredicate(andPredicateWithSubpredicates: predicates))
    }
    let element: XCUIElement
    if let index = selector["index"] as? Int {
      element = query.element(boundBy: index)
    } else {
      element = query.firstMatch
    }
    let timeout = command["timeout"] as? Double ?? 5
    guard element.waitForExistence(timeout: timeout) else {
      throw Failure("no element matches \(Commands.describe(selector)) within \(timeout)s")
    }
    return element
  }

  // text is what an element shows: its label, or its value when it has no
  // label, as a text field has.
  private static func text(of element: XCUIElement) -> String {
    if !element.label.isEmpty {
      return element.label
    }
    if let value = element.value as? String {
      return value
    }
    return ""
  }

  private static func describe(_ selector: [String: Any]) -> String {
    selector.keys.sorted().map { "\($0)=\(selector[$0] ?? "")" }.joined(separator: " ")
  }
}
