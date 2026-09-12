//go:build windows

package rootfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/commandpath"
)

func TestCommandPathBindingBindsStrongIdentityAndIsLocalOnly(t *testing.T) {
	env := newTestSource(t, nil)
	path := filepath.Join(env.dir, "input.txt")
	if err := os.WriteFile(path, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := env.source.BindCommandPath(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(resolver); !errors.Is(err, commandpath.ErrLocalOnly) {
		t.Fatalf("resolver JSON error = %v, want ErrLocalOnly", err)
	}
	binding, err := resolver.Resolve(context.Background(), env.bound, "workspace", "input.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close()
	if _, err := json.Marshal(binding); !errors.Is(err, commandpath.ErrLocalOnly) {
		t.Fatalf("binding JSON error = %v, want ErrLocalOnly", err)
	}
	commitment := binding.Commitment()
	if commitment == ([32]byte{}) {
		t.Fatal("binding returned an empty identity commitment")
	}
	commitmentText := string(commitment[:])
	if strings.Contains(commitmentText, env.dir) || strings.Contains(commitmentText, path) || strings.Contains(commitmentText, "input.txt") {
		t.Fatal("identity commitment contains a clear-text path component")
	}
	preview, err := binding.PreviewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.ToLower(preview), `\\?\`) {
		t.Fatalf("preview token %q is not the normalized handle path", preview)
	}
	if err := binding.Revalidate(context.Background()); err != nil {
		t.Fatalf("initial revalidation = %v", err)
	}
}

func TestCommandPathWithinRootHandlesDriveRootAndSiblingPrefix(t *testing.T) {
	root := `\\?\C:\`
	if !withinCommandRoot(root, `\\?\C:\child.txt`) {
		t.Fatal("drive-root child was rejected")
	}
	for _, candidate := range []string{
		`\\?\C:\`,
		`\\?\D:\child.txt`,
	} {
		if withinCommandRoot(root, candidate) {
			t.Fatalf("candidate %q was accepted outside drive-root child scope", candidate)
		}
	}
	if !withinCommandRoot(`\\?\C:\project`, `\\?\C:\project\child.txt`) {
		t.Fatal("nested child was rejected")
	}
	if withinCommandRoot(`\\?\C:\project`, `\\?\C:\project-old\child.txt`) {
		t.Fatal("sibling sharing a textual prefix was accepted")
	}
}

func TestCommandPathDecodeRejectsNonLocalOrMalformedFinalPaths(t *testing.T) {
	valid := []uint16{'\\', '\\', '?', '\\', 'C', ':', '\\', 'f', 'i', 'l', 'e'}
	if path, ok := decodeFinalCommandPath(valid); !ok || !strings.EqualFold(path, `\\?\C:\file`) {
		t.Fatalf("valid final path decode = %q, %v", path, ok)
	}
	for name, encoded := range map[string][]uint16{
		"unc":       {'\\', '\\', '?', '\\', 'U', 'N', 'C', '\\', 's', '\\', 'f'},
		"volume":    {'\\', '\\', '?', '\\', 'V', 'o', 'l', 'u', 'm', 'e', '{', 'x', '}', '\\', 'f'},
		"ads":       {'\\', '\\', '?', '\\', 'C', ':', '\\', 'f', ':', 's'},
		"surrogate": {'\\', '\\', '?', '\\', 'C', ':', '\\', 0xd800},
		"control":   {'\\', '\\', '?', '\\', 'C', ':', '\\', 0x001f},
	} {
		t.Run(name, func(t *testing.T) {
			if path, ok := decodeFinalCommandPath(encoded); ok || path != "" {
				t.Fatalf("malformed final path decoded as %q, %v", path, ok)
			}
		})
	}
}

func TestCommandPathRejectsRemoteDriveVolume(t *testing.T) {
	foundRemote := false
	for drive := 'A'; drive <= 'Z'; drive++ {
		rootPath := fmt.Sprintf(`%c:\`, drive)
		driveType, err := windowsDriveType(rootPath)
		if err != nil || driveType != driveRemote {
			continue
		}
		foundRemote = true
		finalPath := fmt.Sprintf(`\\?\%c:\`, drive)
		if !errors.Is(requireFixedCommandVolume(finalPath), commandpath.ErrUnsupported) {
			t.Fatalf("remote drive %s was not rejected by command-path volume guard", rootPath)
		}
	}
	if !foundRemote {
		t.Skip("no mapped remote drive is available for GetDriveTypeW integration test")
	}
}

func TestCommandPathBindingRejectsInvalidPolicyAndNonRegularObjects(t *testing.T) {
	env := newTestSource(t, []string{"secret.txt"})
	if err := os.WriteFile(filepath.Join(env.dir, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(env.dir, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver, err := env.source.BindCommandPath(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"secret.txt", "directory", "../escape", `bad\name`, "C:/absolute"} {
		if binding, err := resolver.Resolve(context.Background(), env.bound, "workspace", path); err == nil {
			_ = binding.Close()
			t.Fatalf("Resolve(%q) unexpectedly succeeded", path)
		}
	}
	if binding, err := resolver.Resolve(context.Background(), env.bound, "unknown", "secret.txt"); err == nil {
		_ = binding.Close()
		t.Fatal("unknown root unexpectedly resolved")
	}
}

func TestCommandPathBindingRejectsHardlinkAndReparse(t *testing.T) {
	env := newTestSource(t, nil)
	target := filepath.Join(env.dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(env.dir, "hardlink.txt")
	if err := os.Link(target, link); err != nil {
		t.Skipf("hardlink unavailable: %v", err)
	}
	resolver, err := env.source.BindCommandPath(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	if binding, err := resolver.Resolve(context.Background(), env.bound, "workspace", "target.txt"); err == nil {
		_ = binding.Close()
		t.Fatal("hardlinked file unexpectedly resolved")
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	sym := filepath.Join(env.dir, "symlink.txt")
	if err := os.Symlink(outside, sym); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if binding, err := resolver.Resolve(context.Background(), env.bound, "workspace", "symlink.txt"); err == nil {
		_ = binding.Close()
		t.Fatal("symlink unexpectedly resolved")
	}
}

func TestCommandPathBindingRevalidationDetectsReplacementAndRevocation(t *testing.T) {
	env := newTestSource(t, nil)
	path := filepath.Join(env.dir, "replace.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := env.source.BindCommandPath(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := resolver.Resolve(context.Background(), env.bound, "workspace", "replace.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Skipf("replacement unavailable: %v", err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := binding.Revalidate(context.Background()); !errors.Is(err, commandpath.ErrIdentityChanged) {
		t.Fatalf("replacement revalidation = %v, want ErrIdentityChanged", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}

	path = filepath.Join(env.dir, "revoked.txt")
	if err := os.WriteFile(path, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding, err = resolver.Resolve(context.Background(), env.bound, "workspace", "revoked.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.Replace(env.store.Snapshot().Config()); err != nil {
		t.Fatal(err)
	}
	if err := binding.Revalidate(context.Background()); !errors.Is(err, commandpath.ErrDenied) {
		t.Fatalf("revoked revalidation = %v, want ErrDenied", err)
	}
	_ = binding.Close()
	if _, err := resolver.Resolve(context.Background(), env.bound, "workspace", "revoked.txt"); !errors.Is(err, commandpath.ErrDenied) {
		t.Fatalf("revoked resolve = %v, want ErrDenied", err)
	}
}

func TestCommandPathBindingCloseIsIdempotentAndRevokesBinding(t *testing.T) {
	env := newTestSource(t, nil)
	path := filepath.Join(env.dir, "concurrent.txt")
	if err := os.WriteFile(path, []byte("concurrent"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := env.source.BindCommandPath(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := resolver.Resolve(context.Background(), env.bound, "workspace", "concurrent.txt")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				err := binding.Revalidate(context.Background())
				if err != nil && !errors.Is(err, commandpath.ErrClosed) && !errors.Is(err, commandpath.ErrUnavailable) && !errors.Is(err, commandpath.ErrDenied) && !errors.Is(err, commandpath.ErrIdentityChanged) {
					t.Errorf("concurrent revalidation = %v", err)
				}
			}
		}()
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := binding.PreviewToken(); !errors.Is(err, commandpath.ErrClosed) {
		t.Fatalf("closed preview token error = %v, want ErrClosed", err)
	}
	if got := binding.Commitment(); got != ([32]byte{}) {
		t.Fatal("closed binding retained identity commitment")
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
	if err := binding.Revalidate(context.Background()); !errors.Is(err, commandpath.ErrClosed) {
		t.Fatalf("revalidation after idempotent Close = %v, want ErrClosed", err)
	}
}
