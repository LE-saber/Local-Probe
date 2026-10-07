package supportbundle

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func minimalPreviewDocument() map[string]any {
	return map[string]any{
		"schema_version":   PreviewSchemaVersion,
		"generated_at":     "2026-09-16T12:00:00Z",
		"production_ready": false,
		"unconfigured":     true,
		"status":           "unconfigured",
		"overview": map[string]any{
			"root_count": 0, "profile_count": 0, "connection_count": 0,
			"enabled_connection_count": 0, "status_known": false, "truncated": false,
		},
		"connections":        []any{},
		"developer_rules":    map[string]any{"enabled": false, "allowed_connection_ids": []any{}, "rules": []any{}, "available": false},
		"audit":              map[string]any{"available": false, "status": "unavailable", "records": 0, "corrupt": false, "truncated": false},
		"logs":               []any{},
		"diagnostics":        []any{},
		"management_actions": map[string]any{"available": false, "reason": "production_gate"},
		"omitted":            []any{"paths", "credentials"},
	}
}

func TestGenerateDocumentUsesFixedSchemaAnd64KiBLimit(t *testing.T) {
	data, err := GenerateDocument(minimalPreviewDocument())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || len(data) > MaxBytes {
		t.Fatalf("document size = %d", len(data))
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["schema_version"] != PreviewSchemaVersion {
		t.Fatalf("schema = %#v", decoded["schema_version"])
	}
	for _, unsafe := range []string{"path", "token", "secret", "argv", "environment"} {
		if strings.Contains(strings.ToLower(string(data)), `"`+unsafe+`"`) {
			t.Fatalf("document contains unsafe key %q: %s", unsafe, data)
		}
	}
}

func TestGenerateDocumentRejectsUnknownAndSensitiveValues(t *testing.T) {
	unknown := minimalPreviewDocument()
	unknown["future_field"] = "safe"
	if _, err := GenerateDocument(unknown); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown field error = %v", err)
	}
	sensitive := minimalPreviewDocument()
	sensitive["status"] = "Bearer secret-value"
	if _, err := GenerateDocument(sensitive); !errors.Is(err, ErrSensitiveData) {
		t.Fatalf("sensitive field error = %v", err)
	}
	path := minimalPreviewDocument()
	path["status"] = `C:\private\config.json`
	if _, err := GenerateDocument(path); !errors.Is(err, ErrSensitiveData) {
		t.Fatalf("path value error = %v", err)
	}
}

func TestWriteDocumentUsesAtomicNoOverwriteAndPrivateMode(t *testing.T) {
	directory := t.TempDir()
	data, err := GenerateDocument(minimalPreviewDocument())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "diagnostics.json")
	if err := WriteDocument(path, data); err != nil {
		t.Fatal(err)
	}
	if err := WriteDocument(path, data); !errors.Is(err, ErrTargetExists) {
		t.Fatalf("overwrite error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy temporary file remains: %v", err)
	}
}

func TestWriteDocumentRejectsRelativeAndNonJSONTargets(t *testing.T) {
	data, err := GenerateDocument(minimalPreviewDocument())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteDocument("relative.json", data); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("relative path error = %v", err)
	}
	if err := WriteDocument(filepath.Join(t.TempDir(), "diagnostics.txt"), data); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("extension error = %v", err)
	}
}
