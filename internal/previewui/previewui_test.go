package previewui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderUnconfiguredIsExplicitAndBounded(t *testing.T) {
	text := Render(unconfiguredSnapshot(), SectionOverview)
	for _, want := range []string{"Status: UNCONFIGURED", "production_gate", "read-only"} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(want)) {
			t.Fatalf("render missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "credentials") == false {
		t.Fatalf("render must state omitted sensitive categories")
	}
}

func TestSanitizeSnapshotOmitsUnsafePresentationFields(t *testing.T) {
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion,
		Status:        "configured\nsecret",
		Overview:      Overview{ConfigRevision: `C:\private\config.json`, RootCount: -4, ConnectionCount: 9999},
		Connections: []Connection{{
			ConnectionID: "conn/one",
			ProfileID:    "profile",
			Label:        "C:\\token",
			Transport:    "bad",
			State:        "ready\nraw",
			LastError:    "Bearer abcdefghijklmnop",
		}},
		ManagementActions: ActionCapability{Available: true, Reason: "custom"},
	}
	text := Render(snapshot, SectionOverview)
	for _, forbidden := range []string{`C:\private\config.json`, "token", "Bearer", "ready\nraw", "conn/one"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unsafe value %q leaked into render: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "production_gate") {
		t.Fatalf("management action reason must remain production_gate")
	}
}

func TestValidateAndScrubExport(t *testing.T) {
	valid, err := marshalExport(unconfiguredSnapshot())
	if err != nil {
		t.Fatalf("marshal valid export: %v", err)
	}
	if err := ValidateExport(valid); err != nil {
		t.Fatalf("validate valid export: %v", err)
	}
	for _, value := range []string{
		`{"schema_version":"local-probe.preview.v1","path":"C:\\secret"}`,
		`{"schema_version":"local-probe.preview.v1","config_revision":"C:\\secret"}`,
		`{"schema_version":"local-probe.preview.v1","status":"Bearer abcdefghijklmnop"}`,
		`{"schema_version":"local-probe.preview.v1","future_field":"value"}`,
	} {
		if err := ValidateExport([]byte(value)); err == nil {
			t.Fatalf("unsafe export unexpectedly accepted: %s", value)
		}
	}
}

func TestSaveExportRefusesOverwriteAndUnsafeDestination(t *testing.T) {
	dir := t.TempDir()
	data, err := marshalExport(unconfiguredSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "diagnostics.json")
	if err := SaveExport(path, data); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := SaveExport(path, data); !errors.Is(err, ErrExportExists) {
		t.Fatalf("overwrite error = %v, want ErrExportExists", err)
	}
	if err := SaveExport(filepath.Join(dir, "diagnostics.txt"), data); !errors.Is(err, ErrExportPath) {
		t.Fatalf("extension error = %v, want ErrExportPath", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("saved export missing: info=%+v err=%v", info, err)
	}
}

func TestUnconfiguredModelHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (UnconfiguredModel{}).Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh error = %v", err)
	}
	if _, err := (UnconfiguredModel{}).ExportDiagnostics(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("export error = %v", err)
	}
}
