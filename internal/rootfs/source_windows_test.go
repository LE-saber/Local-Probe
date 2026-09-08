//go:build windows

package rootfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/readcore"
)

func TestWindowsHandleIdentityFields(t *testing.T) {
	env := newTestSource(t, nil)
	filePath := filepath.Join(env.dir, "native-identity.txt")
	if err := os.WriteFile(filePath, []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	handle, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "native-identity.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	fileHandle, ok := handle.(*fileHandle)
	if !ok {
		t.Fatalf("unexpected handle type %T", handle)
	}
	info, err := fileHandle.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	native, err := nativeMetadata(fileHandle.file, info)
	if err != nil {
		t.Fatal(err)
	}
	if !native.hasIdentity || native.volume == 0 || native.fileID == 0 {
		t.Fatalf("missing Windows file identity: %#v", native)
	}
	if !native.hasLinks || native.links != 1 {
		t.Fatalf("unexpected Windows link count: %#v", native)
	}
	if native.attributes == 0 || native.reparse {
		t.Fatalf("unexpected Windows file attributes: %#v", native)
	}
	metadata, err := handle.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(metadata.Version.Token, "m1.") || strings.Contains(metadata.Version.Token, env.dir) {
		t.Fatalf("identity token is malformed or leaks root path: %q", metadata.Version.Token)
	}
}

func TestWindowsRejectsJunctionReparse(t *testing.T) {
	env := newTestSource(t, nil)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "outside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(env.dir, "junction")
	if err := createWindowsJunction(junction, target); err != nil {
		t.Skipf("junction test requires Windows junction privilege and a supported filesystem: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(junction) })
	if _, err := env.source.Open(context.Background(), env.bound, readcore.FileRef{RootID: "workspace", Path: "junction/outside.txt"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("junction/reparse path error = %v", err)
	}
}

func TestWindowsRejectsRemoteMappedRoot(t *testing.T) {
	foundRemote := false
	for drive := 'A'; drive <= 'Z'; drive++ {
		rootPath := fmt.Sprintf(`%c:\`, drive)
		driveType, err := windowsDriveType(rootPath)
		if err != nil {
			continue
		}
		if driveType != driveRemote {
			continue
		}
		foundRemote = true
		if _, err := validateRootPath(rootPath); !errors.Is(err, ErrUnsupportedType) {
			t.Fatalf("remote drive %s was accepted: %v", rootPath, err)
		}
	}
	if !foundRemote {
		t.Skip("no mapped remote drive is available for GetDriveTypeW integration test")
	}
}

func createWindowsJunction(link, target string) error {
	output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
