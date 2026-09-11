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

func TestWindowsRejectsWrapperAndNonPEExecutables(t *testing.T) {
	directory := t.TempDir()
	data, err := os.ReadFile(helperPath("fake-node"))
	if err != nil {
		t.Fatal(err)
	}
	for _, extension := range []string{".cmd", ".bat", ".ps1", ".lnk", ".url", ".txt"} {
		path := filepath.Join(directory, "candidate"+extension)
		if err := os.WriteFile(path, data, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := AuditExecutable(ToolNode, path); !errors.Is(err, ErrRejected) {
			t.Errorf("%s: want ErrRejected, got %v", extension, err)
		}
	}
}

func TestWindowsExecutionGuardBlocksReplacementBeforeCreateProcess(t *testing.T) {
	path := copyExecutable(t, helperPath("fake-node"), executableName("guarded"))
	descriptor, err := AuditExecutable(ToolNode, path)
	if err != nil {
		t.Fatal(err)
	}
	swappedPath := filepath.Join(filepath.Dir(path), "guarded-swap.exe")
	guard, err := openWindowsExecutionGuard(path, descriptor.identity)
	if err != nil {
		t.Fatal(err)
	}
	renameErr := os.Rename(path, swappedPath)
	writeErr := os.WriteFile(path, []byte("replacement"), 0600)
	deleteErr := os.Remove(path)
	guard.close()
	if renameErr == nil {
		t.Fatal("replacement rename succeeded while launch guard was held")
	}
	if writeErr == nil {
		t.Fatal("replacement write succeeded while launch guard was held")
	}
	if deleteErr == nil {
		t.Fatal("replacement delete succeeded while launch guard was held")
	}
}
