package build

import (
	"context"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// Command runs name in a process group of its own and stops the whole
// group once ctx is done, as xcodebuild leaves compilers running otherwise.
func Command(ctx context.Context, out io.Writer, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}
