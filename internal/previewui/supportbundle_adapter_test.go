package previewui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/supportbundle"
)

func TestBuildSupportBundleUsesFixed64KiBDocument(t *testing.T) {
	data, err := BuildSupportBundle(unconfiguredSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || len(data) > 64<<10 {
		t.Fatalf("support bundle size = %d", len(data))
	}
	if err := ValidateExport(data); err != nil {
		t.Fatalf("legacy UI boundary rejected unified bundle: %v", err)
	}
}

func TestWriteSupportBundleUsesNoOverwriteWriter(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "support.json")
	data, err := BuildSupportBundle(unconfiguredSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSupportBundle(path, data); err != nil {
		t.Fatal(err)
	}
	if err := WriteSupportBundle(path, data); !errors.Is(err, supportbundle.ErrTargetExists) {
		t.Fatalf("overwrite error = %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
		}
	}
}
