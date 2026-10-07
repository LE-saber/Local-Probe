//go:build !windows

package rootfs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/commandpath"
	"github.com/LE-saber/Local-Probe/internal/policy"
)

func TestCommandPathResolverIsExplicitlyUnsupportedOutsideWindows(t *testing.T) {
	env := newTestSource(t, nil)
	if err := os.WriteFile(filepath.Join(env.dir, "input.txt"), []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver, err := env.source.BindCommandPath(env.bound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(resolver); !errors.Is(err, commandpath.ErrLocalOnly) {
		t.Fatalf("resolver JSON error = %v, want ErrLocalOnly", err)
	}
	if _, err := resolver.Resolve(context.Background(), env.bound, "workspace", "input.txt"); !errors.Is(err, commandpath.ErrUnsupported) {
		t.Fatalf("non-Windows resolve = %v, want ErrUnsupported", err)
	}
}

func TestNilCommandPathResolverFailsClosedOutsideWindows(t *testing.T) {
	var resolver *commandPathResolver
	if _, err := resolver.Resolve(context.Background(), policy.BoundScope{}, "workspace", "input.txt"); !errors.Is(err, commandpath.ErrUnavailable) {
		t.Fatalf("nil resolver error = %v, want ErrUnavailable", err)
	}
}
