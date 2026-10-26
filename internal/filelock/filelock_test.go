package filelock

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	unlock, err := Lock(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}

	// flock locks are held by open files, so a second open file of this
	// process waits as another process would.
	waited := make(chan struct{})
	got := make(chan error)
	go func() {
		unlock2, err := Lock(context.Background(), path, func() { close(waited) })
		if err == nil {
			unlock2()
		}
		got <- err
	}()

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("onWait was not called while the lock was held")
	}
	unlock()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("Lock after unlock = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the lock was not taken once it was released")
	}
}

func TestLockCanceled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	unlock, err := Lock(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, path, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock on a held lock = %v, want the deadline", err)
	}
}
