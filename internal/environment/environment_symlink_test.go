package environment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverToolsRejectsSymlinkCandidates(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "tool")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err := DiscoverTools([]ToolSpec{{
		ID:             "tool",
		CandidateFiles: []string{link},
	}}, DiscoveryOptions{})
	if !errors.Is(err, ErrRejectedCandidate) {
		t.Fatalf("symlink candidate error = %v", err)
	}
	if strings.Contains(err.Error(), link) || strings.Contains(err.Error(), target) {
		t.Fatalf("symlink error leaked path: %v", err)
	}
}

func TestDiscoverToolsRejectsSymlinkAncestor(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	linkDir := filepath.Join(dir, "link")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("mkdir real directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "tool"), []byte("target"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err := DiscoverTools([]ToolSpec{{
		ID:            "tool",
		CandidateDirs: []string{linkDir},
	}}, DiscoveryOptions{})
	if !errors.Is(err, ErrRejectedCandidate) {
		t.Fatalf("symlink ancestor error = %v", err)
	}
}
