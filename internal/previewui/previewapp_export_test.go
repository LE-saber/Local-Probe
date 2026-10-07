package previewui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/previewapp"
)

func TestPreviewAppExportReturnsRefreshErrorInsteadOfStaleDocument(t *testing.T) {
	directory := t.TempDir()
	app, err := previewapp.New(previewapp.Options{
		ConfigPath: filepath.Join(directory, "missing.json"),
		AuditDir:   filepath.Join(directory, "audit"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	model := NewPreviewAppModel(app)
	if _, err := model.ExportDiagnostics(context.Background()); !errors.Is(err, previewapp.ErrConfigUnavailable) {
		t.Fatalf("export error = %v, want latest refresh error", err)
	}
}
