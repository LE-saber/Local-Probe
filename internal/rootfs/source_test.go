package rootfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
)

type testSource struct {
	dir     string
	store   *config.Store
	manager *policy.Manager
	bound   policy.BoundScope
	scope   readcore.Scope
	source  *Source
}

func newTestSource(t *testing.T, denyPatterns []string) testSource {
	t.Helper()
	dir := t.TempDir()
	root, err := config.NewRoot("workspace", dir, denyPatterns)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("read", []string{"workspace"}, []string{"read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection := config.NewConnection("connection", "test", "read", "credential", true)
	credential := config.NewCredentialRef("credential", "runtime")
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credential})
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := policy.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.BindAuthenticated("connection")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := bound.Scope()
	if err != nil {
		t.Fatal(err)
	}
	source, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return testSource{dir: dir, store: store, manager: manager, bound: bound, scope: scope, source: source}
}

func TestSourceReadsRegularFileAndProducesWeakMetadata(t *testing.T) {
	env := newTestSource(t, nil)
	filePath := filepath.Join(env.dir, "hello.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "hello.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	metadata, err := handle.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Size != 5 || metadata.Version.Strength != "metadata" || len(metadata.Version.Token) != len("m1.")+64 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	buf := make([]byte, 5)
	if n, err := handle.ReadAt(buf, 0); err != nil || n != len(buf) || string(buf) != "hello" {
		t.Fatalf("ReadAt() = %d, %v, %q", n, err, buf)
	}
	if _, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "missing.txt"}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestSourceEnforcesDenyAndRevocationOnEveryHandleOperation(t *testing.T) {
	env := newTestSource(t, []string{".env"})
	if err := os.WriteFile(filepath.Join(env.dir, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: ".env"}); !errors.Is(err, ErrDenied) || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("denied file error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(env.dir, "live.txt"), []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "live.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if _, err := env.store.Replace(env.store.Snapshot().Config()); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Metadata(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked Metadata() error = %v", err)
	}
	if _, err := handle.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked ReadAt() error = %v", err)
	}
	if _, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "live.txt"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked Open() error = %v", err)
	}
}

func TestSourceRejectsDirectoriesAndLinks(t *testing.T) {
	env := newTestSource(t, nil)
	if err := os.Mkdir(filepath.Join(env.dir, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "directory"}); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("directory error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(env.dir, "target.txt"), []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(env.dir, "inside-link.txt")
	if err := os.Symlink("target.txt", linkPath); err != nil {
		t.Skipf("symlink test requires Windows Developer Mode/admin or filesystem support: %v", err)
	}
	if _, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "inside-link.txt"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("in-root symlink error = %v", err)
	}

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideLink := filepath.Join(env.dir, "outside-link.txt")
	if err := os.Symlink(filepath.Join(outside, "outside.txt"), outsideLink); err != nil {
		t.Skipf("outside symlink test requires Windows Developer Mode/admin or filesystem support: %v", err)
	}
	if _, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "outside-link.txt"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("out-of-root symlink error = %v", err)
	}
}

func TestSourceDirectoryMapsToStableEngineError(t *testing.T) {
	env := newTestSource(t, nil)
	if err := os.Mkdir(filepath.Join(env.dir, "directory"), 0o700); err != nil {
		t.Fatal(err)
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
		File: readcore.FileRef{RootID: "workspace", Path: "directory"}, MaxBytes: 32,
	}})
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("directory Engine read = %#v, %v", result, err)
	}
	item := result.Items[0]
	if item.Error == nil || item.Error.Code != "unsupported_type" || item.Error.Message != "file type is not supported" {
		t.Fatalf("directory Engine error = %#v", item.Error)
	}
}

func TestSourceMetadataChangesAfterFileMutation(t *testing.T) {
	env := newTestSource(t, nil)
	filePath := filepath.Join(env.dir, "versioned.txt")
	if err := os.WriteFile(filePath, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "versioned.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	first, err := handle.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("changed-size"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := handle.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Version.Token == second.Version.Token || second.Size != int64(len("changed-size")) {
		t.Fatalf("metadata token did not change: first=%#v second=%#v", first, second)
	}
}

func TestSourceAndHandleCloseAreIdempotent(t *testing.T) {
	env := newTestSource(t, nil)
	if err := os.WriteFile(filepath.Join(env.dir, "closed.txt"), []byte("closed"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "closed.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Metadata(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed Metadata() error = %v", err)
	}
	if _, err := handle.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed ReadAt() error = %v", err)
	}
	if err := env.source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := env.source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "closed.txt"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed Open() error = %v", err)
	}
}

func TestSourceRejectsRelativeAndWindowsUNCRoots(t *testing.T) {
	dir := t.TempDir()
	root, err := config.NewRoot("workspace", "relative-root", nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("read", []string{"workspace"}, []string{"read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{config.NewConnection("connection", "test", "read", "credential", true)}, []config.CredentialRef{config.NewCredentialRef("credential", "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(store); !errors.Is(err, ErrInvalidRoot) {
		t.Fatalf("relative root error = %v", err)
	}

	if runtime.GOOS != "windows" {
		return
	}
	uncRoot, err := config.NewRoot("workspace", `\\server\share`, nil)
	if err != nil {
		t.Fatal(err)
	}
	uncCfg, err := config.New(config.SchemaVersionV1, []config.Root{uncRoot}, []config.Profile{profile}, []config.Connection{config.NewConnection("connection", "test", "read", "credential", true)}, []config.CredentialRef{config.NewCredentialRef("credential", "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	uncStore, err := config.NewStore(uncCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(uncStore); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("UNC root error = %v", err)
	}
	_ = dir
}

func TestSourceRejectsNewBoundAgainstOldRootRevision(t *testing.T) {
	env := newTestSource(t, nil)
	if err := os.WriteFile(filepath.Join(env.dir, "same.txt"), []byte("old root"), 0o600); err != nil {
		t.Fatal(err)
	}
	newDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(newDir, "same.txt"), []byte("new root"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := config.NewRoot("workspace", newDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("read", []string{"workspace"}, []string{"read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{config.NewConnection("connection", "test", "read", "credential", true)}, []config.CredentialRef{config.NewCredentialRef("credential", "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Replace(next); err != nil {
		t.Fatal(err)
	}
	newBound, err := env.manager.BindAuthenticated("connection")
	if err != nil {
		t.Fatal(err)
	}
	ref := readcore.FileRef{RootID: "workspace", Path: "same.txt"}
	if _, err := env.source.Open(context.Background(), newBound, ref); !errors.Is(err, ErrDenied) {
		t.Fatalf("old Source opened a root ID reused by a new revision: %v", err)
	}
	if _, err := env.source.Bind(newBound); !errors.Is(err, ErrDenied) {
		t.Fatalf("old Source.Bind accepted a new revision: %v", err)
	}
}

func TestBoundSourceRunsEngineAndRevokesOldHandle(t *testing.T) {
	env := newTestSource(t, nil)
	if err := os.WriteFile(filepath.Join(env.dir, "engine.txt"), []byte("engine"), 0o600); err != nil {
		t.Fatal(err)
	}
	boundSource, err := env.source.Bind(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := readcore.New(boundSource, readcore.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	request := readcore.Request{File: readcore.FileRef{RootID: "workspace", Path: "engine.txt"}, MaxBytes: 32}
	result, err := engine.ReadBatch(context.Background(), env.scope, []readcore.Request{request})
	if err != nil || len(result.Items) != 1 || result.Items[0].Error != nil || result.Items[0].Content != "engine" {
		t.Fatalf("initial Engine read = %#v, %v", result, err)
	}

	handle, err := env.source.Open(context.Background(), env.bound, request.File)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Replace(env.store.Snapshot().Config()); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Metadata(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatalf("old handle Metadata after Replace = %v", err)
	}
	if _, err := handle.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("old handle ReadAt after Replace = %v", err)
	}
	result, err = engine.ReadBatch(context.Background(), env.scope, []readcore.Request{request})
	if err != nil || len(result.Items) != 1 || result.Items[0].Error == nil || result.Items[0].Error.Code != "denied" {
		t.Fatalf("Engine read after Replace = %#v, %v", result, err)
	}
}

func TestSourceCloseConcurrentOpenDoesNotPanic(t *testing.T) {
	env := newTestSource(t, nil)
	if err := os.WriteFile(filepath.Join(env.dir, "concurrent.txt"), []byte("concurrent"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := readcore.FileRef{RootID: "workspace", Path: "concurrent.txt"}
	var wg sync.WaitGroup
	var handles atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				handle, err := env.source.Open(context.Background(), env.bound, ref)
				if err != nil {
					continue
				}
				handles.Add(1)
				_ = handle.Close()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = env.source.Close()
	}()
	wg.Wait()
	if handles.Load() == 0 {
		t.Log("Source.Close won the race before any Open completed")
	}
	if _, err := env.source.Open(context.Background(), env.bound, ref); !errors.Is(err, ErrClosed) {
		t.Fatalf("Open after concurrent Close = %v", err)
	}
}
