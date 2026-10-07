//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package rootfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/readcore"
)

func TestSourceRejectsUnixFIFO(t *testing.T) {
	env := newTestSource(t, nil)
	fifoPath := filepath.Join(env.dir, "pipe")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("FIFO is unavailable on this filesystem: %v", err)
	}
	// Keep a non-blocking descriptor open so this remains safe if an opener
	// reaches the FIFO before the regular-file check in a future refactor.
	fifo, err := os.OpenFile(fifoPath, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Skipf("FIFO cannot be opened for the fixture: %v", err)
	}
	t.Cleanup(func() { _ = fifo.Close() })

	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "pipe"})
	if handle != nil {
		_ = handle.Close()
		t.Fatalf("FIFO was opened as a readable file: %v", err)
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("FIFO error = %v, want unsupported filesystem object", err)
	}
}

func TestSourceRejectsUnixSocket(t *testing.T) {
	env := newTestSource(t, nil)
	socketPath := filepath.Join(env.dir, "listener.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("Unix socket is unavailable on this filesystem: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	})

	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "listener.sock"})
	if handle != nil {
		_ = handle.Close()
		t.Fatalf("Unix socket was opened as a readable file: %v", err)
	}
	if !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("Unix socket error = %v, want unsupported filesystem object", err)
	}
}

func TestSourceRejectsUnixSymlinkSwapEscape(t *testing.T) {
	env := newTestSource(t, nil)
	insideContent := "INSIDE-SYMLINK-SWAP-MARKER"
	outsideContent := "OUTSIDE-SYMLINK-SWAP-MARKER"
	if err := os.WriteFile(filepath.Join(env.dir, "inside-marker.txt"), []byte(insideContent), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside-marker.txt")
	if err := os.WriteFile(outsidePath, []byte(outsideContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Verify the actual test filesystem supports the link operation before
	// starting concurrent transitions.
	probeLink := filepath.Join(env.dir, "symlink-probe")
	if err := os.Symlink(outsidePath, probeLink); err != nil {
		t.Skipf("symlink is unavailable on this filesystem: %v", err)
	}
	if err := os.Remove(probeLink); err != nil {
		t.Fatal(err)
	}

	swapPath := filepath.Join(env.dir, "swap.txt")
	if err := os.WriteFile(swapPath, []byte(insideContent), 0o600); err != nil {
		t.Fatal(err)
	}
	stagePath := swapPath + ".stage"
	backupPath := swapPath + ".backup"
	t.Cleanup(func() {
		_ = os.Remove(stagePath)
		_ = os.Remove(backupPath)
		_ = os.Remove(swapPath)
	})

	boundSource, err := env.source.Bind(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := readcore.New(boundSource, readcore.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}

	const (
		transitions = 128
		readers     = 4
		readsEach   = 256
		minSwaps    = 16
	)
	start := make(chan struct{})
	stop := make(chan struct{})
	var stopOnce sync.Once
	failures := make(chan string, 1)
	reportFailure := func(format string, args ...any) {
		stopOnce.Do(func() {
			failures <- fmt.Sprintf(format, args...)
			close(stop)
		})
	}
	var regularSwaps atomic.Int64
	var symlinkSwaps atomic.Int64
	var writerActive atomic.Bool
	var overlappingReadAttempts atomic.Int64
	var insideReads atomic.Int64
	var safeRejections atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < readsEach; j++ {
				select {
				case <-stop:
					return
				default:
				}
				if writerActive.Load() {
					overlappingReadAttempts.Add(1)
				}
				result, readErr := engine.ReadBatch(context.Background(), env.scope, []readcore.Request{{
					File:     readcore.FileRef{RootID: "workspace", Path: "swap.txt"},
					MaxBytes: len(insideContent),
				}})
				if readErr != nil {
					reportFailure("symlink swap batch error: %v", readErr)
					return
				}
				if len(result.Items) != 1 {
					reportFailure("symlink swap returned %d items", len(result.Items))
					return
				}
				item := result.Items[0]
				if item.Error != nil {
					switch item.Error.Code {
					case "denied", "not_found", "stale_version", "unavailable", "unsupported_type":
						// A concurrent observation of a non-regular object is a
						// safe rejection too; only successful content is security
						// relevant for this escape check.
						safeRejections.Add(1)
						continue
					default:
						reportFailure("symlink swap returned unexpected error: %#v", item.Error)
						return
					}
				}
				if item.Content != insideContent {
					reportFailure("symlink swap read outside or unexpected content: %q", item.Content)
					return
				}
				insideReads.Add(1)
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < transitions; i++ {
			select {
			case <-stop:
				return
			default:
			}
			writerActive.Store(true)
			regularErr := swapUnixPathToRegular(swapPath, stagePath, backupPath, []byte(insideContent))
			if regularErr == nil {
				regularSwaps.Add(1)
			}
			symlinkErr := error(nil)
			if regularErr == nil {
				symlinkErr = swapUnixPathToSymlink(swapPath, stagePath, backupPath, outsidePath)
				if symlinkErr == nil {
					symlinkSwaps.Add(1)
				}
			}
			writerActive.Store(false)
			if regularErr != nil {
				reportFailure("regular transition %d failed: %v", i, regularErr)
				return
			}
			if symlinkErr != nil {
				reportFailure("symlink transition %d failed: %v", i, symlinkErr)
				return
			}
		}
	}()

	close(start)
	wg.Wait()
	select {
	case failure := <-failures:
		t.Fatal(failure)
	default:
	}
	if regularSwaps.Load() < minSwaps || symlinkSwaps.Load() < minSwaps {
		t.Fatalf("symlink swap made too few transitions: regular=%d symlink=%d", regularSwaps.Load(), symlinkSwaps.Load())
	}
	t.Logf("symlink swap observations: regular=%d symlink=%d inside_reads=%d safe_rejections=%d overlapping_read_attempts=%d", regularSwaps.Load(), symlinkSwaps.Load(), insideReads.Load(), safeRejections.Load(), overlappingReadAttempts.Load())
	if insideReads.Load() == 0 {
		t.Fatalf("symlink swap did not observe a successful inside read")
	}
	if safeRejections.Load() == 0 {
		t.Fatalf("symlink swap did not observe a safe rejection")
	}
	if overlappingReadAttempts.Load() == 0 {
		t.Fatalf("symlink swap did not observe an overlapping read attempt")
	}
	outsideAfter, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatalf("outside marker after symlink swap = %v", err)
	}
	if string(outsideAfter) != outsideContent {
		t.Fatalf("outside marker changed during symlink swap: %q", outsideAfter)
	}
}

func swapUnixPathToRegular(path, stage, backup string, content []byte) error {
	if err := os.Remove(stage); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(stage, content, 0o600); err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.Remove(stage)
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		_ = os.Remove(stage)
		return err
	}
	if err := os.Rename(stage, path); err != nil {
		_ = os.Remove(stage)
		_ = os.Rename(backup, path)
		return err
	}
	return os.Remove(backup)
}

func swapUnixPathToSymlink(path, stage, backup, target string) error {
	if err := os.Remove(stage); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, stage); err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = os.Remove(stage)
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		_ = os.Remove(stage)
		return err
	}
	if err := os.Rename(stage, path); err != nil {
		_ = os.Remove(stage)
		_ = os.Rename(backup, path)
		return err
	}
	return os.Remove(backup)
}
