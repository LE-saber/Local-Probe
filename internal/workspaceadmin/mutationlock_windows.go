//go:build windows

package workspaceadmin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

type mutationLocker interface {
	// The release function must be called by the acquiring goroutine.
	acquire(context.Context) (func(), error)
}

// windowsMutationLocker uses a Local\ named mutex derived from the explicit
// configuration path. The path is never placed in the kernel object name.
// This closes the Preview's cross-process load→rebuild→CAS window; FileStore
// itself remains intentionally non-production and does not claim this gate.
type windowsMutationLocker struct {
	name string
}

func newMutationLocker(path string) mutationLocker {
	key := strings.ToLower(filepath.Clean(path))
	digest := sha256.Sum256([]byte(key))
	return &windowsMutationLocker{name: `Local\LocalProbeWorkspace-` + hex.EncodeToString(digest[:])}
}

func (l *windowsMutationLocker) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// A Win32 mutex is owned by an OS thread, not by a Go goroutine. Pin the
	// acquiring goroutine until ReleaseMutex; otherwise a migration can fail
	// release or let a different goroutine re-enter the same thread's mutex.
	runtime.LockOSThread()
	owned := false
	defer func() {
		if !owned {
			runtime.UnlockOSThread()
		}
	}()
	name, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return nil, errors.New("workspace mutex name is invalid")
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	if handle == 0 {
		return nil, errors.New("workspace mutex is unavailable")
	}
	closeHandle := func() { _ = windows.CloseHandle(handle) }
	for {
		select {
		case <-ctx.Done():
			closeHandle()
			return nil, ctx.Err()
		default:
		}
		status, waitErr := windows.WaitForSingleObject(handle, 25)
		if waitErr != nil {
			closeHandle()
			return nil, waitErr
		}
		switch status {
		case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
			owned = true
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = windows.ReleaseMutex(handle)
					closeHandle()
					runtime.UnlockOSThread()
				})
			}, nil
		case uint32(windows.WAIT_TIMEOUT):
			continue
		default:
			closeHandle()
			return nil, errors.New("workspace mutex wait failed")
		}
	}
}
