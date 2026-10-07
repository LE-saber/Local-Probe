//go:build windows

package rootfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/readcore"
)

func TestWindowsUnicodeCaseAndCombiningPath(t *testing.T) {
	env := newTestSource(t, []string{"Sensitive/**"})
	actualPath := "中文/e\u0301-Ω.TXT"
	caseVariantPath := "中文/E\u0301-ω.txt"
	absolutePath := filepath.Join(env.dir, filepath.FromSlash(actualPath))
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolutePath, []byte("unicode-inside"), 0o600); err != nil {
		t.Fatal(err)
	}

	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: caseVariantPath})
	if err != nil {
		t.Fatalf("Windows case-insensitive Unicode open = %v", err)
	}
	data, err := readWindowsHandleForTest(handle)
	if err != nil {
		t.Fatal(err)
	}
	if data != "unicode-inside" {
		t.Fatalf("Unicode/combining path content = %q", data)
	}

	deniedActual := "Sensitive/秘密-Ω.txt"
	deniedAbsolute := filepath.Join(env.dir, filepath.FromSlash(deniedActual))
	if err := os.MkdirAll(filepath.Dir(deniedAbsolute), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deniedAbsolute, []byte("must-not-leak"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, deniedPath := range []string{
		"sensitive/秘密-ω.TXT",
		"SENSITIVE/秘密-Ω.txt",
	} {
		handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: deniedPath})
		if handle != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("case-variant deny path %q = handle %v, error %v", deniedPath, handle, err)
		}
	}

	boundSource, err := env.source.Bind(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := readcore.New(boundSource, readcore.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.ReadBatch(context.Background(), env.scope, []readcore.Request{{
		File:     readcore.FileRef{RootID: "workspace", Path: caseVariantPath},
		MaxBytes: 64,
	}})
	if err != nil || len(result.Items) != 1 || result.Items[0].Error != nil || result.Items[0].Content != "unicode-inside" {
		t.Fatalf("Engine Unicode/combining read = %#v, %v", result, err)
	}
	result, err = engine.ReadBatch(context.Background(), env.scope, []readcore.Request{{
		File:     readcore.FileRef{RootID: "workspace", Path: "sEnSiTiVe/秘密-ω.TXT"},
		MaxBytes: 64,
	}})
	if err != nil || len(result.Items) != 1 || result.Items[0].Error == nil || result.Items[0].Error.Code != "denied" || result.Items[0].Content != "" {
		t.Fatalf("Engine case-variant deny read = %#v, %v", result, err)
	}
}

func TestWindowsLongPathReadRemainsRootBound(t *testing.T) {
	env := newTestSource(t, nil)
	parts := make([]string, 0, 24)
	for i := 0; i < 24; i++ {
		parts = append(parts, fmt.Sprintf("long-segment-%02d", i))
	}
	relativePath := strings.Join(parts, "/") + "/payload.txt"
	absolutePath := filepath.Join(env.dir, filepath.FromSlash(relativePath))
	if len(absolutePath) <= 260 {
		t.Fatalf("long-path fixture is not longer than MAX_PATH: %d", len(absolutePath))
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o700); err != nil {
		t.Skipf("Windows filesystem does not support creating the >260-character fixture: %v", err)
	}
	if err := os.WriteFile(absolutePath, []byte("long-path-inside"), 0o600); err != nil {
		t.Skipf("Windows filesystem does not support writing the >260-character fixture: %v", err)
	}

	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: relativePath})
	if err != nil {
		t.Fatalf("Source long-path open = %v", err)
	}
	data, err := readWindowsHandleForTest(handle)
	if err != nil || data != "long-path-inside" {
		t.Fatalf("Source long-path read = %q, %v", data, err)
	}

	boundSource, err := env.source.Bind(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := readcore.New(boundSource, readcore.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.ReadBatch(context.Background(), env.scope, []readcore.Request{{
		File:     readcore.FileRef{RootID: "workspace", Path: relativePath},
		MaxBytes: 64,
	}})
	if err != nil || len(result.Items) != 1 || result.Items[0].Error != nil || result.Items[0].Content != "long-path-inside" {
		t.Fatalf("Engine long-path read = %#v, %v", result, err)
	}

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside-marker.txt")
	if err := os.WriteFile(outsideFile, []byte("outside-marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, escapePath := range []string{"../outside-marker.txt", filepath.ToSlash(outsideFile)} {
		handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: escapePath})
		if handle != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("long-path root escape %q = handle %v, error %v", escapePath, handle, err)
		}
	}

	linkPath := filepath.Join(env.dir, "long-path-escape")
	if err := os.Symlink(outsideFile, linkPath); err == nil {
		handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "long-path-escape"})
		if handle != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("long-path symlink escape = handle %v, error %v", handle, err)
		}
	} else {
		t.Logf("long-path symlink escape fixture unavailable: %v", err)
	}
}

func TestWindowsRejectsSpecialPathsBeforeSourceOpen(t *testing.T) {
	env := newTestSource(t, nil)
	if err := os.WriteFile(filepath.Join(env.dir, "regular.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"regular.txt:secret",
		"regular.txt::$DATA",
		"C:regular.txt",
		`C:\regular.txt`,
		`\\server\share\regular.txt`,
		`\\?\C:\regular.txt`,
		`\\.\NUL`,
		"CON",
		"CON.txt",
		"prn.log",
		"AUX",
		"NUL",
		"COM1",
		"LPT9.txt",
		"CLOCK$",
	}
	adsPath := filepath.Join(env.dir, "regular.txt:secret")
	if err := os.WriteFile(adsPath, []byte("ads-must-not-leak"), 0o600); err == nil {
		t.Log("created an ADS fixture; Source must still reject it before opening")
	} else {
		t.Logf("ADS fixture unavailable on this filesystem: %v", err)
	}

	closedRoot, err := os.OpenRoot(env.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := closedRoot.Close(); err != nil {
		t.Fatal(err)
	}
	// A closed root makes any accidental Lstat/Open observable as ErrClosed.
	// Invalid Windows spellings must be denied before the OS opener is reached.
	probe := &Source{roots: map[string]*os.Root{"workspace": closedRoot}, revision: env.bound.Revision()}
	for _, path := range paths {
		handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: path})
		if handle != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("special Source path %q = handle %v, error %v", path, handle, err)
		}
		handle, err = probe.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: path})
		if handle != nil || !errors.Is(err, ErrDenied) {
			t.Fatalf("special path %q reached closed root: handle %v, error %v", path, handle, err)
		}
	}

	boundSource, err := env.source.Bind(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	countingSource := &windowsCountingSource{Source: boundSource}
	engine, err := readcore.New(countingSource, readcore.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	requests := make([]readcore.Request, len(paths))
	for i, path := range paths {
		requests[i] = readcore.Request{File: readcore.FileRef{RootID: "workspace", Path: path}, MaxBytes: 64}
	}
	result, err := engine.ReadBatch(context.Background(), env.scope, requests)
	if err != nil || len(result.Items) != len(paths) {
		t.Fatalf("Engine special-path batch = %#v, %v", result, err)
	}
	if got := countingSource.opens.Load(); got != 0 {
		t.Fatalf("Engine touched Source opener for invalid paths %d time(s)", got)
	}
	for i, item := range result.Items {
		if item.Error == nil || item.Error.Code != "invalid_request" || item.Content != "" || item.BytesRead != 0 {
			t.Fatalf("Engine special path %q result = %#v", paths[i], item)
		}
	}
}

func TestWindowsRejectsSymlinkAndJunctionSwapRace(t *testing.T) {
	env := newTestSource(t, nil)
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside-file-marker.txt")
	if err := os.WriteFile(outsideFile, []byte("OUTSIDE-FILE-MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(outside, "outside-dir")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideDirMarker := filepath.Join(outsideDir, "marker.txt")
	if err := os.WriteFile(outsideDirMarker, []byte("OUTSIDE-DIR-MARKER"), 0o600); err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(env.dir, "swap-file")
	if err := os.Symlink(outsideFile, filePath); err != nil {
		t.Skipf("symlink race test requires Windows symlink privilege/support: %v", err)
	}
	dirPath := filepath.Join(env.dir, "swap-dir")
	if err := createWindowsJunction(dirPath, outsideDir); err != nil {
		t.Skipf("junction race test requires Windows junction support: %v", err)
	}

	const (
		transitions = 128
		readers     = 8
		readsEach   = 256
		minSwaps    = 8
	)
	start := make(chan struct{})
	stop := make(chan struct{})
	var stopOnce sync.Once
	bad := make(chan string, 1)
	reportBad := func(format string, args ...any) {
		stopOnce.Do(func() {
			bad <- fmt.Sprintf(format, args...)
			close(stop)
		})
	}
	var fileSwaps atomic.Int64
	var dirSwaps atomic.Int64
	var fileReads atomic.Int64
	var dirReads atomic.Int64
	var overlapReads atomic.Int64
	var writerActive atomic.Int64
	writerReady := make(chan struct{})
	var writerReadyOnce sync.Once
	markWriterActive := func() {
		if writerActive.Add(1) == 1 {
			writerReadyOnce.Do(func() { close(writerReady) })
		}
	}
	markWriterDone := func() { writerActive.Add(-1) }
	var allowedFileFailures atomic.Int64
	var allowedDirFailures atomic.Int64
	recordTransitionFailure := func(kind string, sequence int, err error, allowed *atomic.Int64) bool {
		if isAllowedWindowsTransitionError(err) {
			allowed.Add(1)
			return true
		}
		reportBad("%s transition %d failed: %v", kind, sequence, err)
		return false
	}
	var wg sync.WaitGroup

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			<-writerReady
			for j := 0; j < readsEach; j++ {
				select {
				case <-stop:
					return
				default:
				}
				overlapped := writerActive.Load() > 0
				if j%2 == 0 {
					handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "swap-file"})
					if err != nil {
						continue
					}
					data, readErr := readWindowsHandleForTest(handle)
					if readErr != nil {
						reportBad("swap-file read error: %v", readErr)
						return
					}
					if data != "INSIDE-FILE-MARKER" {
						reportBad("swap-file read outside or unexpected content: %q", data)
						return
					}
					fileReads.Add(1)
					if overlapped {
						overlapReads.Add(1)
					}
					continue
				}

				handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "swap-dir/marker.txt"})
				if err != nil {
					continue
				}
				data, readErr := readWindowsHandleForTest(handle)
				if readErr != nil {
					reportBad("swap-dir read error: %v", readErr)
					return
				}
				if data != "INSIDE-DIR-MARKER" {
					reportBad("swap-dir read outside or unexpected content: %q", data)
					return
				}
				dirReads.Add(1)
				if overlapped {
					overlapReads.Add(1)
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		markWriterActive()
		defer markWriterDone()
		for i := 0; i < transitions; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := swapWindowsFileToRegular(filePath, []byte("INSIDE-FILE-MARKER"), i); err != nil {
				if !recordTransitionFailure("file regular", i, err, &allowedFileFailures) {
					return
				}
			} else {
				fileSwaps.Add(1)
			}
			if err := swapWindowsFileToSymlink(filePath, outsideFile, transitions+i); err != nil {
				if !recordTransitionFailure("file symlink", transitions+i, err, &allowedFileFailures) {
					return
				}
			} else {
				fileSwaps.Add(1)
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		markWriterActive()
		defer markWriterDone()
		for i := 0; i < transitions; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := swapWindowsDirectoryToInside(dirPath, []byte("INSIDE-DIR-MARKER"), i); err != nil {
				if !recordTransitionFailure("directory inside", i, err, &allowedDirFailures) {
					return
				}
				// A failed inside transition must not be paired with a junction
				// transition: the path may still have a backup/junction state.
				continue
			} else {
				dirSwaps.Add(1)
			}
			if err := swapWindowsDirectoryToJunction(dirPath, outsideDir, transitions+i); err != nil {
				if !recordTransitionFailure("directory junction", transitions+i, err, &allowedDirFailures) {
					return
				}
			} else {
				dirSwaps.Add(1)
			}
		}
	}()

	close(start)
	wg.Wait()
	if data, err := os.ReadFile(outsideFile); err != nil {
		t.Fatalf("outside file marker read = %v", err)
	} else if string(data) != "OUTSIDE-FILE-MARKER" {
		t.Fatalf("outside file marker changed to %q", data)
	}
	if data, err := os.ReadFile(outsideDirMarker); err != nil {
		t.Fatalf("outside directory marker read = %v", err)
	} else if string(data) != "OUTSIDE-DIR-MARKER" {
		t.Fatalf("outside directory marker changed to %q", data)
	}
	select {
	case failure := <-bad:
		t.Fatal(failure)
	default:
	}
	if fileSwaps.Load() < minSwaps || dirSwaps.Load() < minSwaps {
		t.Fatalf("swap race made too few successful transitions: file=%d dir=%d", fileSwaps.Load(), dirSwaps.Load())
	}
	if fileReads.Load() == 0 || dirReads.Load() == 0 {
		t.Fatalf("swap race did not observe safe regular states: file reads=%d dir reads=%d", fileReads.Load(), dirReads.Load())
	}
	if overlapReads.Load() == 0 {
		t.Fatalf("swap race did not observe a read overlapping an active writer")
	}
	if failures := allowedFileFailures.Load(); failures > 0 {
		t.Logf("file writer skipped %d allowed sharing/lock/access failures", failures)
	}
	if failures := allowedDirFailures.Load(); failures > 0 {
		t.Logf("directory writer skipped %d allowed sharing/lock/access failures", failures)
	}
}

type windowsCountingSource struct {
	readcore.Source
	opens atomic.Int64
}

func (s *windowsCountingSource) Open(ctx context.Context, scope readcore.Scope, ref readcore.FileRef) (readcore.Handle, error) {
	s.opens.Add(1)
	return s.Source.Open(ctx, scope, ref)
}

func readWindowsHandleForTest(handle readcore.Handle) (string, error) {
	defer handle.Close()
	buf := make([]byte, 256)
	n, err := handle.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return string(buf[:n]), nil
}

func swapWindowsFileToRegular(path string, content []byte, sequence int) error {
	stage := fmt.Sprintf("%s.stage-%03d", path, sequence)
	backup := fmt.Sprintf("%s.backup-%03d", path, sequence)
	if err := os.WriteFile(stage, content, 0o600); err != nil {
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
	_ = os.Remove(backup)
	return nil
}

func swapWindowsFileToSymlink(path, target string, sequence int) error {
	stage := fmt.Sprintf("%s.stage-%03d", path, sequence)
	backup := fmt.Sprintf("%s.backup-%03d", path, sequence)
	if err := os.Symlink(target, stage); err != nil {
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
	_ = os.Remove(backup)
	return nil
}

func swapWindowsDirectoryToInside(path string, content []byte, sequence int) error {
	stage := fmt.Sprintf("%s.stage-%03d", path, sequence)
	backup := fmt.Sprintf("%s.backup-%03d", path, sequence)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "marker.txt"), content, 0o600); err != nil {
		_ = cleanupWindowsDirectoryBackup(stage)
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		_ = cleanupWindowsDirectoryBackup(stage)
		return err
	}
	if err := os.Rename(stage, path); err != nil {
		_ = cleanupWindowsDirectoryBackup(stage)
		_ = os.Rename(backup, path)
		return err
	}
	return cleanupWindowsDirectoryBackup(backup)
}

func swapWindowsDirectoryToJunction(path, target string, sequence int) error {
	stage := fmt.Sprintf("%s.stage-%03d", path, sequence)
	backup := fmt.Sprintf("%s.backup-%03d", path, sequence)
	if err := createWindowsJunction(stage, target); err != nil {
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
	return cleanupWindowsDirectoryBackup(backup)
}

// cleanupWindowsDirectoryBackup never traverses a reparse-point backup. A
// junction or symlink is removed as one directory entry; only an ordinary
// directory gets its known test marker removed before the directory itself.
func cleanupWindowsDirectoryBackup(backup string) error {
	info, err := os.Lstat(backup)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || fileInfoReparse(info) {
		return os.Remove(backup)
	}
	if !info.IsDir() {
		return fmt.Errorf("unexpected non-directory backup %q", backup)
	}
	marker := filepath.Join(backup, "marker.txt")
	if _, err := os.Lstat(marker); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Remove(backup)
}

const (
	// syscall exposes ERROR_ACCESS_DENIED but not these two Win32 values.
	windowsErrorSharingViolation syscall.Errno = 32
	windowsErrorLockViolation    syscall.Errno = 33
	windowsErrorBusy             syscall.Errno = 170
)

func isAllowedWindowsTransitionError(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windowsErrorSharingViolation) ||
		errors.Is(err, windowsErrorLockViolation) ||
		errors.Is(err, windowsErrorBusy)
}
