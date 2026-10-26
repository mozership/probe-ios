// Package device picks the iOS simulator a step runs on.
package device

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mozership/probe-ios/internal/xcode"
)

// Booted is the state simctl gives a simulator that is running.
const Booted = "Booted"

// Device is a simulator simctl lists.
type Device struct {
	UDID  string
	Name  string
	State string
	// Runtime is simctl's identifier of its iOS, such as
	// com.apple.CoreSimulator.SimRuntime.iOS-26-5.
	Runtime string
}

// List returns the available iOS simulators, sorted by name.
func List(ctx context.Context, out xcode.Output) ([]Device, error) {
	b, err := out(ctx, "xcrun", "simctl", "list", "devices", "available", "--json")
	if err != nil {
		return nil, fmt.Errorf("listing the simulators: %w", err)
	}
	return parse(b)
}

func parse(b []byte) ([]Device, error) {
	var list struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("reading the simulators simctl listed: %w", err)
	}
	var devices []Device
	for runtime, ds := range list.Devices {
		if !strings.Contains(runtime, ".SimRuntime.iOS-") {
			continue
		}
		for _, d := range ds {
			devices = append(devices, Device{UDID: d.UDID, Name: d.Name, State: d.State, Runtime: runtime})
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Name != devices[j].Name {
			return devices[i].Name < devices[j].Name
		}
		return devices[i].UDID < devices[j].UDID
	})
	return devices, nil
}

// Resolve picks the simulator want names, by its UDID or its name, from
// devices. An empty want picks the one booted simulator. The simulator has
// to be booted: probe-ios leaves booting it, and installing the app, to a
// step of its own, so that a failed boot is not taken for a failed test.
func Resolve(devices []Device, want string) (Device, error) {
	if want == "" {
		var booted []Device
		for _, d := range devices {
			if d.State == Booted {
				booted = append(booted, d)
			}
		}
		switch len(booted) {
		case 0:
			return Device{}, fmt.Errorf("no iOS simulator is booted: boot one, as with xcrun simctl boot \"iPhone 17\", or name one in device")
		case 1:
			return booted[0], nil
		default:
			return Device{}, fmt.Errorf("%d iOS simulators are booted (%s): name the one to use in device", len(booted), names(booted))
		}
	}

	var matched []Device
	for _, d := range devices {
		if d.UDID == want || d.Name == want {
			matched = append(matched, d)
		}
	}
	if len(matched) == 0 {
		return Device{}, fmt.Errorf("no available iOS simulator is named %q or has that UDID", want)
	}
	var booted []Device
	for _, d := range matched {
		if d.State == Booted {
			booted = append(booted, d)
		}
	}
	switch len(booted) {
	case 0:
		return Device{}, fmt.Errorf("the iOS simulator %q is not booted: boot it with xcrun simctl boot %s", want, matched[0].UDID)
	case 1:
		return booted[0], nil
	default:
		return Device{}, fmt.Errorf("%d booted iOS simulators are named %q (%s): name one by its UDID in device", len(booted), want, udids(booted))
	}
}

func names(ds []Device) string {
	s := make([]string, len(ds))
	for i, d := range ds {
		s[i] = d.Name + " " + d.UDID
	}
	return strings.Join(s, ", ")
}

func udids(ds []Device) string {
	s := make([]string, len(ds))
	for i, d := range ds {
		s[i] = d.UDID
	}
	return strings.Join(s, ", ")
}
