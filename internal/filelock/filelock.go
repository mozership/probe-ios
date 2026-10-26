// Package filelock takes locks on files that other processes of probe-ios
// take too, such as the steps of one run that start at once.
package filelock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// poll is how often a lock another process holds is tried again.
const poll = 200 * time.Millisecond

// Lock takes an exclusive lock on the file at path, creating it, and
// returns the function that releases it. When another process holds the
// lock, Lock calls onWait once, if it is not nil, and waits until the lock
// is free or ctx is done. The lock goes with the process too, so one that
// is killed leaves none behind.
func Lock(ctx context.Context, path string, onWait func()) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	waited := false
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if !waited && onWait != nil {
			onWait()
		}
		waited = true
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}
