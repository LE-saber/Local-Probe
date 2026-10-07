//go:build windows

package workspaceadmin

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsMutationMutexOwnsPinnedThreadUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	locker := newMutationLocker(path)
	release, err := locker.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	thread := windows.GetCurrentThreadId()
	for i := 0; i < 100; i++ {
		runtime.Gosched()
		if windows.GetCurrentThreadId() != thread {
			t.Fatal("mutex owner migrated OS thread")
		}
	}
	blocked := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
		defer cancel()
		unlock, err := newMutationLocker(path).acquire(ctx)
		if err == nil {
			unlock()
		}
		blocked <- err
	}()
	if err := <-blocked; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("held mutex was not exclusive: %v", err)
	}
	release()
	release() // balanced, idempotent ReleaseMutex and UnlockOSThread
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		unlock, err := newMutationLocker(path).acquire(ctx)
		if err == nil {
			unlock()
		}
		blocked <- err
	}()
	if err := <-blocked; err != nil {
		t.Fatalf("mutex release left abandoned/stuck owner: %v", err)
	}
}
