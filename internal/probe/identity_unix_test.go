//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package probe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixRejectsSymlinkAndNonRegularExecutable(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	link := filepath.Join(directory, "link")
	data, err := os.ReadFile(helperPath("fake-node"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := AuditExecutable(ToolNode, link); !errors.Is(err, ErrRejected) {
		t.Fatalf("symlink: want ErrRejected, got %v", err)
	}
	if _, err := AuditExecutable(ToolNode, directory); !errors.Is(err, ErrRejected) {
		t.Fatalf("directory: want ErrRejected, got %v", err)
	}
}
