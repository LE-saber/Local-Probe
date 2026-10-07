package config

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileStoreRequiresExplicitPathOrConstrainedParent(t *testing.T) {
	tests := []struct {
		name string
		opts FileStoreOptions
	}{
		{name: "empty", opts: FileStoreOptions{}},
		{name: "relative", opts: FileStoreOptions{Path: "config.json"}},
		{name: "path and file name", opts: FileStoreOptions{Path: filepath.Join(t.TempDir(), "config.json"), FileName: "other.json"}},
		{name: "parent without name", opts: FileStoreOptions{ParentDir: t.TempDir()}},
		{name: "relative parent", opts: FileStoreOptions{ParentDir: "config", FileName: "config.json"}},
		{name: "traversal child", opts: FileStoreOptions{ParentDir: t.TempDir(), FileName: ".." + string(filepath.Separator) + "other.json"}},
		{name: "absolute child", opts: FileStoreOptions{ParentDir: t.TempDir(), FileName: filepath.Join(t.TempDir(), "other.json")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewFileStoreWithOptions(test.opts); err == nil || !errors.Is(err, ErrFileStorePath) {
				t.Fatalf("NewFileStoreWithOptions(%+v) error = %v, want ErrFileStorePath", test.opts, err)
			}
		})
	}

	parent := t.TempDir()
	store, err := NewFileStoreIn(parent, "settings.json")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(parent, "settings.json")
	if store.Path() != want || store.BackupPath() != want+".bak" {
		t.Fatalf("store paths = %q/%q, want %q/%q", store.Path(), store.BackupPath(), want, want+".bak")
	}
	if _, err := NewFileStoreWithOptions(FileStoreOptions{
		Path:      filepath.Join(parent, "nested", "settings.json"),
		ParentDir: parent,
	}); err == nil || !errors.Is(err, ErrFileStorePath) {
		t.Fatalf("accepted path outside direct parent: %v", err)
	}
}

func TestFileStoreSaveLoadCASBackupAndExplicitRestore(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "config.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Parse([]byte(strings.Replace(validJSON, `"enabled": true`, `"enabled": false`, 1)))
	if err != nil {
		t.Fatal(err)
	}

	created, err := store.SaveIfRevision("", first)
	if err != nil {
		t.Fatalf("initial CAS save failed: %v", err)
	}
	if !strings.HasPrefix(created.Revision(), "sha256:") {
		t.Fatalf("revision = %q, want sha256 digest", created.Revision())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision() != created.Revision() || !configsEqual(loaded.Config(), first) {
		t.Fatalf("loaded snapshot differs: revision=%q config_equal=%v", loaded.Revision(), configsEqual(loaded.Config(), first))
	}

	updated, err := store.SaveIfRevision(created.Revision(), second)
	if err != nil {
		t.Fatalf("CAS update failed: %v", err)
	}
	if updated.Revision() == created.Revision() {
		t.Fatal("CAS update did not advance revision")
	}
	backupData, err := os.ReadFile(store.BackupPath())
	if err != nil {
		t.Fatalf("backup was not written: %v", err)
	}
	backupConfig, err := Parse(backupData)
	if err != nil || !configsEqual(backupConfig, first) {
		t.Fatalf("backup is not previous config: parse=%v equal=%v", err, configsEqual(backupConfig, first))
	}

	if _, err := store.SaveIfRevision(created.Revision(), first); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale CAS accepted: %v", err)
	}
	current, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !configsEqual(current.Config(), second) {
		t.Fatal("stale CAS changed current config")
	}
	if _, err := store.SaveIfRevision("", first); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("empty create CAS replaced existing config: %v", err)
	}

	restored, err := store.RestoreBackupIfRevision(updated.Revision())
	if err != nil {
		t.Fatalf("explicit backup restore failed: %v", err)
	}
	if !configsEqual(restored.Config(), first) || restored.Revision() != created.Revision() {
		t.Fatalf("restored snapshot mismatch: revision=%q config_equal=%v", restored.Revision(), configsEqual(restored.Config(), first))
	}
	newBackup, err := os.ReadFile(store.BackupPath())
	if err != nil {
		t.Fatal(err)
	}
	newBackupConfig, err := Parse(newBackup)
	if err != nil || !configsEqual(newBackupConfig, first) {
		t.Fatalf("restore changed recovery source: parse=%v equal=%v", err, configsEqual(newBackupConfig, first))
	}
	if _, err := store.RestoreBackupIfRevision(updated.Revision()); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("restore accepted stale current revision: %v", err)
	}
}

func TestFileStoreMissingAndMalformedData(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("missing Load error = %v, want ErrConfigNotFound", err)
	}
	if _, err := store.RestoreBackup(); !errors.Is(err, ErrBackupNotFound) {
		t.Fatalf("missing RestoreBackup error = %v, want ErrBackupNotFound", err)
	}

	path := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"secret-config-marker"}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err = NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "secret-config-marker") {
		t.Fatalf("malformed Load error = %v, want safe ErrInvalid without contents", err)
	}
	first, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveIfRevision("sha256:does-not-match", first); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed CAS error = %v, want ErrInvalid", err)
	}
}

func TestFileStoreCreateParentAndPermissions(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "config", "nested")
	path := filepath.Join(parent, "settings.json")
	store, err := NewFileStoreWithOptions(FileStoreOptions{Path: path, CreateParent: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		t.Fatalf("created parent = %v, stat error = %v", info, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(parent); err != nil {
			t.Fatal(err)
		} else if got := info.Mode().Perm(); got&0077 != 0 {
			t.Fatalf("created parent permissions = %o, expected no group/other bits", got)
		}
		if info, err := os.Stat(path); err != nil {
			t.Fatal(err)
		} else if got := info.Mode().Perm(); got&0077 != 0 {
			t.Fatalf("config permissions = %o, expected no group/other bits", got)
		}
	}
}

func TestFileStoreAtomicCrashPointsLeaveSafeState(t *testing.T) {
	baseConfig, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	nextConfig, err := Parse([]byte(strings.Replace(validJSON, `"enabled": true`, `"enabled": false`, 1)))
	if err != nil {
		t.Fatal(err)
	}

	for _, failure := range []string{"create", "write", "sync", "chmod", "close", "backup-replace", "config-replace", "dir-sync"} {
		t.Run(failure, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "config")
			fake := newFakeFileStoreFS()
			fake.addDir(parent)
			path := filepath.Join(parent, "settings.json")
			store, err := newFileStoreWithFS(FileStoreOptions{Path: path}, fake)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Save(baseConfig); err != nil {
				t.Fatalf("seed save failed: %v", err)
			}
			fake.resetFailures()
			fake.failAt(failure)
			if _, err := store.Save(nextConfig); err == nil {
				t.Fatalf("save succeeded at injected %s failure", failure)
			} else if !errors.Is(err, ErrFileStoreIO) {
				t.Fatalf("injected %s error = %v, want ErrFileStoreIO", failure, err)
			} else if strings.Contains(err.Error(), "settings.json") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("injected %s error leaked path/content: %v", failure, err)
			}
			got, exists := fake.bytes(path)
			if !exists {
				t.Fatalf("current file disappeared after %s failure", failure)
			}
			parsed, err := Parse(got)
			if err != nil || !configsEqual(parsed, baseConfig) {
				t.Fatalf("current file changed after %s failure: parse=%v equal=%v", failure, err, configsEqual(parsed, baseConfig))
			}
			if fake.tempCount() != 0 {
				t.Fatalf("temporary files leaked after %s failure: %d", failure, fake.tempCount())
			}
		})
	}
}

func TestFileStoreRejectsSymlinkAndNeverFollowsIt(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "config")
	fake := newFakeFileStoreFS()
	fake.addDir(parent)
	path := filepath.Join(parent, "settings.json")
	fake.addSymlink(path)
	store, err := newFileStoreWithFS(FileStoreOptions{Path: path}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !errors.Is(err, ErrFileStorePath) {
		t.Fatalf("symlink Load error = %v, want ErrFileStorePath", err)
	}
	cfg, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(cfg); err == nil || !errors.Is(err, ErrFileStorePath) {
		t.Fatalf("symlink Save error = %v, want ErrFileStorePath", err)
	}
	if fake.replaceCount.Load() != 0 {
		t.Fatal("symlink path reached replacement")
	}
}

func TestFileStoreUsesSingleWriterLockAcrossInstances(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "config")
	fake := newFakeFileStoreFS()
	fake.addDir(parent)
	path := filepath.Join(parent, "settings.json")
	storeA, err := newFileStoreWithFS(FileStoreOptions{Path: path}, fake)
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := newFileStoreWithFS(FileStoreOptions{Path: strings.ToUpper(path)}, fake)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	fake.replaceDelay = 20 * time.Millisecond
	var failures atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var saveErr error
			if i%2 == 0 {
				_, saveErr = storeA.Save(cfg)
			} else {
				_, saveErr = storeB.Save(cfg)
			}
			if saveErr != nil {
				failures.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := failures.Load(); got != 0 {
		t.Fatalf("single-writer saves failed: %d", got)
	}
	if got := fake.maxActiveReplace.Load(); got > 1 {
		t.Fatalf("concurrent replacements = %d, want process-local single writer", got)
	}
}

func TestFileStoreLocksPrimaryAndBackupAliasesTogether(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "config")
	fake := newFakeFileStoreFS()
	fake.addDir(parent)
	primaryPath := filepath.Join(parent, "settings.json")
	backupPath := primaryPath + ".bak"
	primary, err := newFileStoreWithFS(FileStoreOptions{Path: primaryPath}, fake)
	if err != nil {
		t.Fatal(err)
	}
	backupAsPrimary, err := newFileStoreWithFS(FileStoreOptions{Path: backupPath}, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(primary.locks) == 0 || len(backupAsPrimary.locks) == 0 {
		t.Fatal("alias stores did not receive resource locks")
	}
	shared := false
	for _, left := range primary.locks {
		for _, right := range backupAsPrimary.locks {
			if left == right {
				shared = true
			}
		}
	}
	if !shared {
		t.Fatal("primary and backup aliases do not share a process-local lock")
	}

	cfg, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	fake.replaceDelay = 20 * time.Millisecond
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var saveErr error
			if i == 0 {
				_, saveErr = primary.Save(cfg)
			} else {
				_, saveErr = backupAsPrimary.Save(cfg)
			}
			if saveErr != nil {
				failures.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("alias saves failed: %d", failures.Load())
	}
	if got := fake.maxActiveReplace.Load(); got > 1 {
		t.Fatalf("primary/backup alias replacements overlapped: %d", got)
	}
}

func TestFileStoreRestoreKeepsOriginalBackupWhenPostReplaceSyncFails(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "config")
	fake := newFakeFileStoreFS()
	fake.addDir(parent)
	path := filepath.Join(parent, "settings.json")
	store, err := newFileStoreWithFS(FileStoreOptions{Path: path}, fake)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Parse([]byte(strings.Replace(validJSON, `"enabled": true`, `"enabled": false`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	originalBackup, ok := fake.bytes(store.BackupPath())
	if !ok {
		t.Fatal("seed backup is missing")
	}
	fake.failAt("dir-sync")
	if _, err := store.RestoreBackup(); err == nil || !errors.Is(err, ErrFileStoreIO) {
		t.Fatalf("restore with post-replace sync failure = %v, want ErrFileStoreIO", err)
	}
	current, ok := fake.bytes(path)
	if !ok {
		t.Fatal("restore removed current file")
	}
	currentConfig, err := Parse(current)
	if err != nil || !configsEqual(currentConfig, first) {
		t.Fatalf("restore step one did not install backup: parse=%v equal=%v", err, configsEqual(currentConfig, first))
	}
	retainedBackup, ok := fake.bytes(store.BackupPath())
	if !ok || !bytes.Equal(retainedBackup, originalBackup) {
		t.Fatalf("original backup was not retained after post-replace sync failure")
	}
	if fake.tempCount() != 0 {
		t.Fatalf("restore leaked temporary files: %d", fake.tempCount())
	}

	fake.resetFailures()
	retried, err := store.RestoreBackup()
	if err != nil {
		t.Fatalf("restore retry failed: %v", err)
	}
	if !configsEqual(retried.Config(), first) {
		t.Fatal("restore retry returned unexpected config")
	}
}

func TestFileStoreProductionGateIsHardDisabled(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if store.ProductionReady() || store.Capability().ProductionReady {
		t.Fatal("FileStore advertised production readiness")
	}
	if !errors.Is(store.RequireProductionReady(), ErrUnsafeForProduction) {
		t.Fatal("production gate did not return ErrUnsafeForProduction")
	}
}

func configsEqual(left, right Config) bool {
	leftJSON, leftErr := left.MarshalJSON()
	rightJSON, rightErr := right.MarshalJSON()
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

type fakeFileStoreFS struct {
	mu               sync.Mutex
	files            map[string]*fakeStoreNode
	dirs             map[string]fs.FileMode
	failure          string
	failureUsed      bool
	replaceCount     atomic.Int32
	activeReplace    atomic.Int32
	maxActiveReplace atomic.Int32
	replaceDelay     time.Duration
}

type fakeStoreNode struct {
	data    []byte
	mode    fs.FileMode
	symlink bool
}

func newFakeFileStoreFS() *fakeFileStoreFS {
	return &fakeFileStoreFS{
		files: make(map[string]*fakeStoreNode),
		dirs:  make(map[string]fs.FileMode),
	}
}

func (f *fakeFileStoreFS) addDir(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[fakeStorePath(name)] = 0700 | fs.ModeDir
}

func (f *fakeFileStoreFS) addSymlink(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[fakeStorePath(name)] = &fakeStoreNode{mode: fs.ModeSymlink, symlink: true}
}

func (f *fakeFileStoreFS) failAt(operation string) {
	f.mu.Lock()
	f.failure = operation
	f.failureUsed = false
	f.mu.Unlock()
}

func (f *fakeFileStoreFS) resetFailures() {
	f.mu.Lock()
	f.failure = ""
	f.failureUsed = false
	f.mu.Unlock()
}

func (f *fakeFileStoreFS) shouldFail(operation string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failure == operation && !f.failureUsed {
		f.failureUsed = true
		return true
	}
	return false
}

func (f *fakeFileStoreFS) OpenFile(name string, flag int, perm fs.FileMode) (fileStoreFile, error) {
	clean := fakeStorePath(name)
	if flag&os.O_CREATE != 0 && f.shouldFail("create") {
		return nil, fs.ErrPermission
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	node, exists := f.files[clean]
	if flag&os.O_CREATE != 0 {
		if flag&os.O_EXCL != 0 && exists {
			return nil, fs.ErrExist
		}
		if !exists {
			node = &fakeStoreNode{mode: perm}
			f.files[clean] = node
		}
		return &fakeStoreFileHandle{owner: f, path: clean, writable: true, buf: bytes.NewBuffer(nil)}, nil
	}
	if !exists {
		return nil, fs.ErrNotExist
	}
	if node.symlink {
		return nil, fs.ErrInvalid
	}
	return &fakeStoreFileHandle{owner: f, path: clean, buf: bytes.NewBuffer(append([]byte(nil), node.data...))}, nil
}

func (f *fakeFileStoreFS) Lstat(name string) (fs.FileInfo, error) {
	clean := fakeStorePath(name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if mode, ok := f.dirs[clean]; ok {
		return fakeStoreFileInfo{name: filepath.Base(clean), mode: mode}, nil
	}
	if node, ok := f.files[clean]; ok {
		return fakeStoreFileInfo{name: filepath.Base(clean), mode: node.mode}, nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeFileStoreFS) MkdirAll(name string, perm fs.FileMode) error {
	if f.shouldFail("mkdir") {
		return fs.ErrPermission
	}
	clean := fakeStorePath(name)
	f.mu.Lock()
	f.dirs[clean] = perm | fs.ModeDir
	f.mu.Unlock()
	return nil
}

func (f *fakeFileStoreFS) Remove(name string) error {
	clean := fakeStorePath(name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.files[clean]; !ok {
		return fs.ErrNotExist
	}
	delete(f.files, clean)
	return nil
}

func (f *fakeFileStoreFS) Replace(oldPath, newPath string) error {
	if strings.HasSuffix(newPath, ".bak") && f.shouldFail("backup-replace") {
		return fs.ErrPermission
	}
	if !strings.HasSuffix(newPath, ".bak") && f.shouldFail("config-replace") {
		return fs.ErrPermission
	}
	active := f.activeReplace.Add(1)
	for {
		current := f.maxActiveReplace.Load()
		if active <= current || f.maxActiveReplace.CompareAndSwap(current, active) {
			break
		}
	}
	defer f.activeReplace.Add(-1)
	if f.replaceDelay > 0 {
		time.Sleep(f.replaceDelay)
	}
	f.replaceCount.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.files[fakeStorePath(oldPath)]
	if !ok {
		return fs.ErrNotExist
	}
	f.files[fakeStorePath(newPath)] = &fakeStoreNode{data: append([]byte(nil), node.data...), mode: node.mode}
	delete(f.files, fakeStorePath(oldPath))
	return nil
}

func (f *fakeFileStoreFS) SyncDir(string) error {
	if f.shouldFail("dir-sync") {
		return fs.ErrPermission
	}
	return nil
}

func (f *fakeFileStoreFS) bytes(name string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.files[fakeStorePath(name)]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), node.data...), true
}

func (f *fakeFileStoreFS) tempCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for name := range f.files {
		if strings.Contains(filepath.Base(name), ".tmp-") || strings.Contains(filepath.Base(name), ".backup-") || strings.Contains(filepath.Base(name), ".config-") {
			count++
		}
	}
	return count
}

func fakeStorePath(name string) string {
	clean := filepath.Clean(name)
	if runtime.GOOS == "windows" {
		return strings.ToLower(clean)
	}
	return clean
}

type fakeStoreFileHandle struct {
	owner    *fakeFileStoreFS
	path     string
	writable bool
	buf      *bytes.Buffer
	closed   bool
}

func (f *fakeStoreFileHandle) Read(p []byte) (int, error) {
	if f.writable {
		return 0, errors.New("not readable")
	}
	return f.buf.Read(p)
}

func (f *fakeStoreFileHandle) Write(p []byte) (int, error) {
	if !f.writable {
		return 0, errors.New("not writable")
	}
	if f.owner.shouldFail("write") {
		return 0, errors.New("injected write failure")
	}
	return f.buf.Write(p)
}

func (f *fakeStoreFileHandle) Sync() error {
	if f.owner.shouldFail("sync") {
		return errors.New("injected sync failure")
	}
	return nil
}

func (f *fakeStoreFileHandle) Chmod(mode fs.FileMode) error {
	if f.owner.shouldFail("chmod") {
		return errors.New("injected chmod failure")
	}
	f.owner.mu.Lock()
	if node := f.owner.files[f.path]; node != nil {
		node.mode = mode
	}
	f.owner.mu.Unlock()
	return nil
}

func (f *fakeStoreFileHandle) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	if f.owner.shouldFail("close") {
		return errors.New("injected close failure")
	}
	if f.writable {
		f.owner.mu.Lock()
		if node := f.owner.files[f.path]; node != nil {
			node.data = append([]byte(nil), f.buf.Bytes()...)
		}
		f.owner.mu.Unlock()
	}
	return nil
}

type fakeStoreFileInfo struct {
	name string
	mode fs.FileMode
}

func (f fakeStoreFileInfo) Name() string       { return f.name }
func (f fakeStoreFileInfo) Size() int64        { return 0 }
func (f fakeStoreFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeStoreFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeStoreFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeStoreFileInfo) Sys() any           { return nil }

var _ io.Reader = (*fakeStoreFileHandle)(nil)
var _ io.Writer = (*fakeStoreFileHandle)(nil)
var _ fs.FileInfo = fakeStoreFileInfo{}
