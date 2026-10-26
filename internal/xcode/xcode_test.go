package xcode

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fake answers each command by its name and arguments, joined by spaces.
type fake map[string]string

func (f fake) output(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	out, ok := f[cmd]
	if !ok {
		return nil, errors.New("exit status 1")
	}
	return []byte(out), nil
}

func TestDetect(t *testing.T) {
	f := fake{
		"xcode-select -p":     "/Applications/Xcode.app/Contents/Developer\n",
		"xcodebuild -version": "Xcode 26.5\nBuild version 17E192\n",
		"xcrun --sdk iphonesimulator --show-sdk-version": "26.5\n",
	}
	got, err := Detect(context.Background(), f.output)
	if err != nil {
		t.Fatal(err)
	}
	want := Toolchain{
		DeveloperDir: "/Applications/Xcode.app/Contents/Developer",
		Version:      "Xcode 26.5 / Build version 17E192",
		SDK:          "26.5",
	}
	if got != want {
		t.Errorf("Detect = %+v, want %+v", got, want)
	}
}

func TestDetectErrors(t *testing.T) {
	tests := map[string]struct {
		f    fake
		want string
	}{
		"no Xcode": {
			f:    fake{},
			want: "needs Xcode, and xcode-select found none",
		},
		"Command Line Tools": {
			f:    fake{"xcode-select -p": "/Library/Developer/CommandLineTools\n"},
			want: "sudo xcode-select -s /Applications/Xcode.app",
		},
		"no simulator SDK": {
			f: fake{
				"xcode-select -p":     "/Applications/Xcode.app/Contents/Developer\n",
				"xcodebuild -version": "Xcode 26.5\nBuild version 17E192\n",
			},
			want: "xcodebuild -downloadPlatform iOS",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Detect(context.Background(), tt.f.output)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Detect = %v, want an error that says %q", err, tt.want)
			}
		})
	}
}
