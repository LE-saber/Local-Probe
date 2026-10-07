package environment

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGetEnvironmentIsCoarseAndStable(t *testing.T) {
	got := GetEnvironment()
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version = %q", got.SchemaVersion)
	}
	if got.OS != runtime.GOOS || got.Arch != runtime.GOARCH {
		t.Fatalf("environment = %#v", got)
	}
	if len(got.Capabilities) == 0 {
		t.Fatal("capabilities are empty")
	}
	for _, capability := range got.Capabilities {
		if capability == "local_diagnostics_opt_in" {
			t.Fatal("get_environment advertised a local-only diagnostics capability")
		}
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal environment: %v", err)
	}
	if strings.Contains(string(encoded), "HOME") || strings.Contains(string(encoded), "USERPROFILE") {
		t.Fatalf("environment exposed process details: %s", encoded)
	}
}

func TestDiscoverToolsDefaultsToPathFreeResults(t *testing.T) {
	dir := t.TempDir()
	toolPath := filepath.Join(dir, "tool")
	if err := os.WriteFile(toolPath, []byte("not executed"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	results, err := DiscoverTools([]ToolSpec{{
		ID:             "tool",
		CandidateFiles: []string{toolPath},
	}}, DiscoveryOptions{})
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(results) != 1 || results[0].ApprovedLogicalID != "tool" || !results[0].Exists || results[0].CandidateCount != 1 {
		t.Fatalf("results = %#v", results)
	}
	if len(results[0].CandidatePaths) != 0 || results[0].DiagnosticsNotice != "" {
		t.Fatalf("default result disclosed diagnostics: %#v", results[0])
	}
	encoded, err := json.Marshal(results)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(encoded), toolPath) {
		t.Fatalf("default result leaked candidate path: %s", encoded)
	}
}

func TestDiscoverToolsLocalDiagnosticsIsExplicit(t *testing.T) {
	dir := t.TempDir()
	toolPath := filepath.Join(dir, "tool")
	if err := os.WriteFile(toolPath, []byte("diagnostic candidate"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}

	results, err := DiscoverTools([]ToolSpec{{
		ID:             "tool",
		CandidateFiles: []string{toolPath},
	}}, DiscoveryOptions{LocalDiagnostics: true})
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(results) != 1 || len(results[0].CandidatePaths) != 1 || results[0].CandidatePaths[0] != toolPath {
		t.Fatalf("diagnostics = %#v", results)
	}
	if results[0].DiagnosticsNotice != DiagnosticsNotice {
		t.Fatalf("diagnostics notice = %q", results[0].DiagnosticsNotice)
	}
}

func TestDiscoverToolsDeduplicatesCandidatesAndDoesNotRecurse(t *testing.T) {
	dir := t.TempDir()
	toolPath := filepath.Join(dir, "tool")
	if err := os.WriteFile(toolPath, []byte("candidate"), 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "tool"), []byte("nested candidate"), 0o600); err != nil {
		t.Fatalf("write nested candidate: %v", err)
	}

	results, err := DiscoverTools([]ToolSpec{{
		ID:             "tool",
		CandidateFiles: []string{toolPath, toolPath},
		CandidateDirs:  []string{dir},
	}}, DiscoveryOptions{LocalDiagnostics: true})
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(results) != 1 || results[0].CandidateCount != 1 || len(results[0].CandidatePaths) != 1 || results[0].CandidatePaths[0] != toolPath {
		t.Fatalf("deduplicated results = %#v", results)
	}
}

func TestDiscoverToolsMissingCandidateIsNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-tool")
	results, err := DiscoverTools([]ToolSpec{{
		ID:             "missing_tool",
		CandidateFiles: []string{missing},
	}}, DiscoveryOptions{})
	if err != nil {
		t.Fatalf("DiscoverTools: %v", err)
	}
	if len(results) != 1 || results[0].Exists || results[0].CandidateCount != 0 {
		t.Fatalf("missing result = %#v", results)
	}
}

func TestDiscoverToolsRejectsInvalidCandidatesWithoutPathLeak(t *testing.T) {
	relative := "relative-tool"
	_, err := DiscoverTools([]ToolSpec{{
		ID:             "tool",
		CandidateFiles: []string{relative},
	}}, DiscoveryOptions{})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("relative candidate error = %v", err)
	}
	if strings.Contains(err.Error(), relative) {
		t.Fatalf("error leaked candidate path: %v", err)
	}

	duplicateIDPath := filepath.Join(t.TempDir(), "second")
	_, err = DiscoverTools([]ToolSpec{{ID: "same"}, {ID: "same", CandidateFiles: []string{duplicateIDPath}}}, DiscoveryOptions{})
	if !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate ID error = %v", err)
	}
}

func TestDiscoverToolsRejectsNonRegularExplicitCandidate(t *testing.T) {
	directory := t.TempDir()
	_, err := DiscoverTools([]ToolSpec{{
		ID:             "tool",
		CandidateFiles: []string{directory},
	}}, DiscoveryOptions{})
	if !errors.Is(err, ErrRejectedCandidate) {
		t.Fatalf("directory candidate error = %v", err)
	}
	if strings.Contains(err.Error(), directory) {
		t.Fatalf("error leaked candidate path: %v", err)
	}
}
