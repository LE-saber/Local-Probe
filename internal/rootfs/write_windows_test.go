package rootfs

import (
	"errors"
	"github.com/LE-saber/Local-Probe/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func newWritableTestSource(t *testing.T, deny []string) testSource {
	t.Helper()
	f := newTestSource(t, deny)
	cfg := f.store.Snapshot().Config()
	profiles := cfg.Profiles()
	for i := range profiles {
		profiles[i] = profiles[i].WithWriteAccess()
	}
	next, err := config.New(config.SchemaVersionV2, cfg.Roots(), profiles, cfg.Connections(), cfg.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Replace(next); err != nil {
		t.Fatal(err)
	}
	f.source.Close()
	f.source, err = New(f.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.source.Close() })
	f.bound, err = f.manager.BindAuthenticated("connection")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestWriteFileConflictBackupAndLinkedObjects(t *testing.T) {
	f := newWritableTestSource(t, []string{"*.key"})
	target := filepath.Join(f.dir, "edit.txt")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := f.source.FileVersion(f.bound, "workspace", "edit.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, []byte("user-edited"), 0600); err != nil {
		t.Fatal(err)
	}
	backed := false
	backup := func(b []byte) error { backed = true; return nil }
	if err = f.source.WriteFile(f.bound, "workspace", "edit.txt", v, []byte("remote"), backup); !errors.Is(err, ErrWriteConflict) || backed {
		t.Fatalf("conflict was not preserved: %v backup=%v", err, backed)
	}
	content, _ := os.ReadFile(target)
	if string(content) != "user-edited" {
		t.Fatal("conflict overwrote user's edits")
	}
	v, err = f.source.FileVersion(f.bound, "workspace", "edit.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.source.WriteFile(f.bound, "workspace", "edit.txt", v, []byte("updated"), func(b []byte) error {
		if string(b) != "user-edited" {
			t.Fatal("wrong before image")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(target)
	if string(content) != "updated" {
		t.Fatal("file was not written")
	}
	if err = f.source.WriteFile(f.bound, "workspace", "new.txt", "absent", []byte("new"), backup); err != nil {
		t.Fatal(err)
	}
	if err = f.source.WriteFile(f.bound, "workspace", "new.txt", "absent", []byte("clobbered"), backup); !errors.Is(err, ErrWriteConflict) {
		t.Fatal("exclusive creation clobbered existing file")
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err = os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(outside, filepath.Join(f.dir, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.source.FileVersion(f.bound, "workspace", "linked.txt"); err == nil {
		t.Fatal("hard linked file accepted")
	}
	if err = f.source.WriteFile(f.bound, "workspace", "../outside.txt", "absent", []byte("escape"), backup); err == nil {
		t.Fatal("traversal write accepted")
	}
	if err = f.source.WriteFile(f.bound, "workspace", "secret.key", "absent", []byte("escape"), backup); err == nil {
		t.Fatal("deny rule bypassed")
	}
	content, _ = os.ReadFile(outside)
	if string(content) != "private" {
		t.Fatal("outside marker was modified")
	}
}
func TestWriteFileBackupFailureDoesNotTouchFile(t *testing.T) {
	f := newWritableTestSource(t, nil)
	target := filepath.Join(f.dir, "file.txt")
	if err := os.WriteFile(target, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := f.source.FileVersion(f.bound, "workspace", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.source.WriteFile(f.bound, "workspace", "file.txt", v, []byte("changed"), func([]byte) error { return errors.New("backup unavailable") }); err == nil {
		t.Fatal("failed backup did not stop write")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "keep" {
		t.Fatal("failed backup still changed file")
	}
}

func TestReadOnlyScopeCannotWriteEvenThroughLocalCore(t *testing.T) {
	f := newTestSource(t, nil)
	called := false
	err := f.source.WriteFile(f.bound, "workspace", "not-created.txt", "absent", []byte("bad"), func([]byte) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("read-only profile admitted a file write")
	}
	if _, err = os.Stat(filepath.Join(f.dir, "not-created.txt")); !os.IsNotExist(err) {
		t.Fatal("read-only write created a file")
	}
}
