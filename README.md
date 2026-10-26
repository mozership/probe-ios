# probe-ios

A [Probe](https://github.com/mozership/probe) action that drives an app on an iOS simulator: it launches the app, taps and types into it, waits for elements, reads their text and takes screenshots. It works through an XCUITest runner that it builds from source with the Mac's own Xcode, the first time a step needs it.

It runs on macOS alone, with Xcode, an iOS simulator runtime, and a booted simulator.

A workflow gives the action a name once, under `actions` at its top, and its steps use the name:

```yaml
actions:
  ios: github.com/mozership/probe-ios@<commit SHA>
```

Probe only takes a full 40-character commit SHA there. The notes of each [release](https://github.com/mozership/probe-ios/releases) start with the action and its commit to copy.

## Requirements

| What | How |
|------|-----|
| Xcode | From the App Store or developer.apple.com. `xcode-select -p` has to point at it, not at the Command Line Tools: `sudo xcode-select -s /Applications/Xcode.app` |
| An iOS simulator runtime | `xcodebuild -downloadPlatform iOS` |
| A booted simulator | `xcrun simctl boot "iPhone 17"`, or open Simulator.app |
| The app under test | Installed on the simulator: `xcrun simctl install booted path/to/MyApp.app`. A build for the simulator needs no signing |

The action does not boot the simulator or install the app. Do that in a step of its own, such as a `shell` step, so that a failed boot is not taken for a failed test.

## The first run

The first step that uses the action builds the runner, which takes a few minutes, and says so:

```
[WARN] ios: building the iOS test runner; the first run takes a few minutes, so give the step timeout: 10m if it runs out
[WARN]   cache: /Users/you/Library/Caches/probe-ios/runner/3f2a9c1d0b7e4a65
[WARN]   xcode: Xcode 26.5 / Build version 17E192
```

A step has 5 minutes by default, which a first build can run out of, so give the first step a longer `timeout` of its own, beside its `with`. Later steps take the runner from the cache. It is built again once the action or Xcode changes.

Steps that start at once build it once: the others wait for that build. A build that fails says why, and leaves its log in the cache directory as `last-build.log`.

The runner is started on the simulator by the first step of a run that uses it, which takes a few seconds more, and is kept for the run's other steps. It is stopped once the run is over.

A simulator runs one runner at a time, so two runs of Probe at once need a simulator each: a run that starts a runner on a simulator stops the runner another run has there. Steps of one run that use the same simulator take turns.

## Basic Syntax

```yaml
steps:
  - name: Open General
    uses: ios
    timeout: 15m
    with:
      app: com.apple.Preferences
      actions:
        - name: launch
          args: ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        - name: tap
          selector: com.apple.settings.general
        - name: wait
          selector: type=navigationbar identifier=General
        - name: text
          id: back
          selector: type=button identifier=BackButton
    test: res.code == 0 && res.results.back == "Settings"
```

The launch arguments above start the app in English whatever language the simulator is set to, so that the labels a selector names do not change with it.

## Parameters

These go directly under `with`.

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `app` | String | required | The bundle identifier of the app the actions drive |
| `actions` | List | required | The actions to take, in order. See [Actions](#actions) |
| `device` | String | the one booted simulator | The simulator, by its name or UDID. It has to be booted. When more than one is booted, it has to be given |
| `timeout` | Duration string or number of seconds | `30s` | Limit for the actions, from the first to the last. It does not count building the runner or starting it, which the step's own `timeout` bounds |

## Actions

Each entry of `actions` names its action in `name`:

| Action | Fields | What it does |
|--------|--------|--------------|
| `launch` | `args` | Launches the app afresh, ending it first if it runs, with `args` as its launch arguments |
| `activate` | | Brings the app to the foreground, launching it if it is not running |
| `terminate` | | Ends the app |
| `tap` | `selector` | Taps the element |
| `type` | `selector`, `value` | Taps the element and types `value` into it |
| `wait` | `selector` | Waits until the element exists |
| `text` | `selector`, `id` | Reads the element's label, or its value when it has no label, into `res.results` |
| `tree` | `id` | Reads the app's accessibility tree, as XCUITest describes it, into `res.results`. It shows what a selector can name: run a step with it under `-v` to see the tree |
| `screenshot` | `id` | Saves the screen as PNG, and puts its path in `res.filepaths` |

`id` is the key of the result. Without it the action's name is the key.

An action on an element waits for the element to appear, for what is left of `timeout`, and the step fails when it does not.

### Selectors

A selector names an element by key=value pairs, separated by spaces. A value with a space goes in double quotes.

| Key | Matches |
|-----|---------|
| `identifier` (or `id`) | The accessibility identifier, as `.accessibilityIdentifier("login")` in SwiftUI sets it |
| `label` | The accessibility label, which is mostly the text shown |
| `type` | The element type: `alert`, `any`, `button`, `cell`, `image`, `link`, `navigationbar`, `other`, `searchfield`, `securetextfield`, `slider`, `switch`, `tabbar`, `text`, `textfield`, `textview` |
| `index` | Which of the matching elements, from 0. Without it, the first |

```yaml
selector: login                          # identifier=login
selector: type=button label="Sign in"
selector: type=cell index=2
```

## Result

| Field | Description |
|-------|-------------|
| `res.code` | `0` when every action succeeded, `1` otherwise |
| `res.results` | What `text` and `tree` read, by key |
| `res.filepaths` | The files `screenshot` saved, by key |
| `res.device` | The UDID of the simulator the actions ran on |

## Cache

The runner is built in `~/Library/Caches/probe-ios`. `PROBE_IOS_CACHE_DIR` puts it elsewhere, such as in a directory a CI job caches.

## CI

On GitHub Actions, run the steps on a macOS runner, create the simulator on a runtime the image has, and boot it before Probe runs. [.github/workflows/test.yml](.github/workflows/test.yml) does this for the e2e tests of this repository.
