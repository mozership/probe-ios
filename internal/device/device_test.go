package device

import (
	"context"
	"strings"
	"testing"
)

const listed = `{
  "devices": {
    "com.apple.CoreSimulator.SimRuntime.iOS-26-5": [
      {"udid": "AAAA", "name": "iPhone 17", "state": "Booted", "isAvailable": true},
      {"udid": "BBBB", "name": "iPhone 17 Pro", "state": "Shutdown", "isAvailable": true}
    ],
    "com.apple.CoreSimulator.SimRuntime.iOS-18-6": [
      {"udid": "CCCC", "name": "iPhone 17", "state": "Shutdown", "isAvailable": true}
    ],
    "com.apple.CoreSimulator.SimRuntime.watchOS-26-0": [
      {"udid": "DDDD", "name": "Apple Watch", "state": "Booted", "isAvailable": true}
    ]
  }
}`

func TestList(t *testing.T) {
	out := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if got := name + " " + strings.Join(args, " "); got != "xcrun simctl list devices available --json" {
			t.Errorf("ran %q", got)
		}
		return []byte(listed), nil
	}
	devices, err := List(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range devices {
		got = append(got, d.UDID)
	}
	// The watch is left out, and the rest are sorted by name and UDID.
	if want := "AAAA CCCC BBBB"; strings.Join(got, " ") != want {
		t.Errorf("List = %v, want %s", got, want)
	}
}

func TestResolve(t *testing.T) {
	devices, err := parse([]byte(listed))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		devices []Device
		want    string
		udid    string
		wantErr string
	}{
		{name: "the one booted", devices: devices, udid: "AAAA"},
		{name: "by name, the booted one of two", devices: devices, want: "iPhone 17", udid: "AAAA"},
		{name: "by UDID", devices: devices, want: "AAAA", udid: "AAAA"},
		{name: "not booted", devices: devices, want: "iPhone 17 Pro", wantErr: "xcrun simctl boot BBBB"},
		{name: "unknown", devices: devices, want: "iPad", wantErr: `no available iOS simulator is named "iPad"`},
		{name: "none booted", devices: []Device{{UDID: "BBBB", Name: "iPhone 17 Pro", State: "Shutdown"}}, wantErr: "no iOS simulator is booted"},
		{
			name: "two booted",
			devices: []Device{
				{UDID: "AAAA", Name: "iPhone 17", State: Booted},
				{UDID: "EEEE", Name: "iPad Air", State: Booted},
			},
			wantErr: "2 iOS simulators are booted",
		},
		{
			name: "two booted of a name",
			devices: []Device{
				{UDID: "AAAA", Name: "iPhone 17", State: Booted},
				{UDID: "EEEE", Name: "iPhone 17", State: Booted},
			},
			want:    "iPhone 17",
			wantErr: "name one by its UDID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.devices, tt.want)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve = %+v, %v, want an error that says %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.UDID != tt.udid {
				t.Errorf("Resolve = %s, want %s", got.UDID, tt.udid)
			}
		})
	}
}
