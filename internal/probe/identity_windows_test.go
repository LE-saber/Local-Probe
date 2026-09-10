//go:build windows

package probe

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsRejectsPathAliasesAndRemoteForms(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, executableName("target"))
	data, err := os.ReadFile(helperPath("fake-node"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	for _, unsafePath := range []string{
		`relative.exe`,
		`\\server\share\tool.exe`,
		`\\?\C:\tool.exe`,
		`\\.\C:\tool.exe`,
		`C:\tool.exe:secret`,
		`C:tool.exe`,
		`C:\Windows\.\tool.exe`,
		`C:\Windows\..\tool.exe`,
	} {
		if _, err := AuditExecutable(ToolNode, unsafePath); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q: want ErrInvalidInput, got %v", unsafePath, err)
		}
	}
	if _, err := AuditExecutable(ToolNode, directory); !errors.Is(err, ErrRejected) {
		t.Fatalf("directory: want ErrRejected, got %v", err)
	}
	if _, err := AuditExecutable(ToolNode, path); err != nil {
		t.Fatalf("local regular file unexpectedly rejected: %v", err)
	}
}

func TestWindowsRejectsSymlinkExecutable(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, executableName("target"))
	link := filepath.Join(directory, executableName("link"))
	data, err := os.ReadFile(helperPath("fake-node"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	if _, err := AuditExecutable(ToolNode, link); !errors.Is(err, ErrRejected) {
		t.Fatalf("symlink: want ErrRejected, got %v", err)
	}
}
