//go:build windows

package environment

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalCandidatePathAcceptsForwardSlashWindowsForm(t *testing.T) {
	got, err := canonicalCandidatePath(`C:/Local-Probe/tool.exe`)
	if err != nil {
		t.Fatalf("canonicalCandidatePath: %v", err)
	}
	if got != `C:\Local-Probe\tool.exe` {
		t.Fatalf("canonical path = %q", got)
	}
}

func TestDiscoverToolsAcceptsForwardSlashWindowsCandidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.exe")
	if err := os.WriteFile(path, []byte("candidate"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	results, err := DiscoverTools([]ToolSpec{{
		ID:             "tool",
		CandidateFiles: []string{filepath.ToSlash(path)},
	}}, DiscoveryOptions{})
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(results) != 1 || !results[0].Exists || results[0].CandidateCount != 1 {
		t.Fatalf("forward-slash candidate result = %#v", results)
	}
}

func TestDiscoverToolsRejectsWindowsUnsafePathForms(t *testing.T) {
	for _, path := range []string{`relative-tool.exe`, `\\server\share\tool.exe`, `\\?\C:\tool.exe`, `\\.\C:\tool.exe`, `C:\tool.exe:secret`} {
		_, err := DiscoverTools([]ToolSpec{{ID: "tool", CandidateFiles: []string{path}}}, DiscoveryOptions{})
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("path %q error = %v, want invalid input", path, err)
		}
	}
}
