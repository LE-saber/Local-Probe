package desktopbridge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/previewconnect"
)

func desktopWrite(t *testing.T, s *Service, method string, params any) Snapshot {
	t.Helper()
	response := callRPC(t, s, "write", method, params)
	if !response.OK {
		t.Fatalf("%s rejected: %+v", method, response.Error)
	}
	waitBridgeIdle(t, s)
	return s.snapshot()
}
func TestDesktopHistoryPersistsIndependentScopesAndSelection(t *testing.T) {
	f := newBridgeFixture(t, false, nil)
	original := f.service.snapshot()
	added := desktopWrite(t, f.service, "connections.save", map[string]any{"expected_revision": original.Desktop.Revision, "id": "", "name": "Second", "transport": "openai_runtime", "port": 8891})
	if added.Connection.Code != "" || len(added.Desktop.Connections) != 2 || len(added.Workspace.Roots) != 0 || added.Desktop.ActiveID == original.Desktop.ActiveID {
		t.Fatalf("new scope: %+v", added)
	}
	secondID := added.Desktop.ActiveID
	selected := desktopWrite(t, f.service, "connections.select", map[string]any{"expected_revision": added.Desktop.Revision, "id": original.Desktop.ActiveID})
	if len(selected.Workspace.Roots) != 1 || selected.Workspace.Roots[0].ID != f.rootID {
		t.Fatal("selection mixed scopes")
	}
	again := desktopWrite(t, f.service, "connections.select", map[string]any{"expected_revision": selected.Desktop.Revision, "id": secondID})
	next, err := New(f.service.options)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	after := next.snapshot()
	if after.Desktop.ActiveID != secondID || after.Connection.Transport != "openai_runtime" || len(after.Workspace.Roots) != 0 || len(after.Desktop.Events) < 3 {
		t.Fatalf("restart lost history: %+v", after)
	}
	controller, err := next.newController(config.TransportOpenAIRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if controller.(*previewconnect.Controller).Options().ConnectionID != secondID || controller.(*previewconnect.Controller).Options().MCPListenAddr != "127.0.0.1:8891" {
		t.Fatal("controller did not bind saved configuration")
	}
	conflict := desktopWrite(t, f.service, "connections.remove", map[string]any{"expected_revision": original.Desktop.Revision, "id": secondID})
	if conflict.Connection.Code != "revision_conflict" || len(conflict.Desktop.Connections) != 2 || conflict.Desktop.Revision != again.Desktop.Revision {
		t.Fatal("stale request changed history")
	}
}
func TestDesktopFirstRunRequiresExplicitSaveAndDoesNotGenerateSecrets(t *testing.T) {
	base := t.TempDir()
	s, err := New(Options{ConfigPath: filepath.Join(base, "config", "local-probe.json"), RepoRoot: base})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap := desktopWrite(t, s, "connections.save", map[string]any{"expected_revision": "", "id": "", "name": "My connection", "transport": "openai_runtime", "port": 8787})
	if snap.Connection.Code != "" || !snap.Workspace.Available || len(snap.Workspace.Roots) != 0 || len(snap.Desktop.Connections) != 1 {
		t.Fatalf("first save: %+v", snap)
	}
	start := desktopWrite(t, s, "connection.connect", map[string]any{})
	if start.Connection.Code != "workspace_scope_empty" || s.controller != nil {
		t.Fatal("empty scope started a controller")
	}
	cfg, err := s.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	data, err := cfg.Config().MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "api_key") || len(cfg.Config().Roots()) != 0 {
		t.Fatal("first run manufactured authority or secrets")
	}
}
func TestDesktopBackupRestorePausesRootsAndKeepsPreviousConfig(t *testing.T) {
	var backup []byte
	f := newBridgeFixture(t, false, func(o *Options) {
		o.SaveBackup = func(_ context.Context, data []byte) error { backup = append([]byte(nil), data...); return nil }
		o.LoadBackup = func(_ context.Context) ([]byte, error) { return append([]byte(nil), backup...), nil }
	})
	snap := f.service.snapshot()
	backed := desktopWrite(t, f.service, "settings.backup", map[string]any{"expected_revision": snap.Desktop.Revision})
	if len(backup) == 0 || backed.Connection.Code != "" {
		t.Fatal("backup unavailable")
	}
	renamed := desktopWrite(t, f.service, "workspace.update", map[string]any{"expected_revision": backed.Workspace.Revision, "root_id": f.rootID, "display_name": "Changed"})
	selection := callRPC(t, f.service, "pick", "settings.pickBackup", nil)
	if !selection.OK || selection.Data.BackupSelection == nil {
		t.Fatal("no selected backup")
	}
	if selection.Data.BackupSelection.Roots != 1 || selection.Data.BackupSelection.Revision != renamed.Desktop.Revision {
		t.Fatal("incorrect backup preview")
	}
	restored := desktopWrite(t, f.service, "settings.restore", map[string]any{"expected_revision": renamed.Desktop.Revision, "selection_id": selection.Data.BackupSelection.ID})
	if restored.Connection.Code != "" || len(restored.Workspace.Roots) != 1 || restored.Workspace.Roots[0].Enabled || restored.Connection.MCPReady || restored.Connection.TunnelReady {
		t.Fatalf("unsafe restore: %+v", restored)
	}
	cfg, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Config().DeveloperMode().Enabled() || len(cfg.Config().CommandProfiles()) != 0 {
		t.Fatal("restore retained commands")
	}
	if _, err := os.Stat(f.configPath + ".bak"); err != nil {
		t.Fatal("previous config not recoverable")
	}
	// A paused folder swapped for a missing path must not be silently enabled.
	if err := os.Rename(f.rootPath, f.rootPath+"-moved"); err != nil {
		t.Fatal(err)
	}
	change := desktopWrite(t, f.service, "workspace.update", map[string]any{"expected_revision": restored.Workspace.Revision, "root_id": f.rootID, "enabled": true})
	if change.Connection.Code != "path_missing" || change.Workspace.Roots[0].Enabled {
		t.Fatal("restored missing root became authorized")
	}
}
func TestDesktopScopeEventsCaptureIDsWithoutPaths(t *testing.T) {
	f := newBridgeFixture(t, false, nil)
	snap := f.service.snapshot()
	after := desktopWrite(t, f.service, "workspace.update", map[string]any{"expected_revision": snap.Workspace.Revision, "root_id": f.rootID, "display_name": "Renamed"})
	if len(after.Desktop.Events) != 1 || len(after.Desktop.Events[0].RootIDs) != 1 || after.Desktop.Events[0].RootIDs[0] != f.rootID {
		t.Fatal("missing event-time root scope")
	}
	data, err := os.ReadFile(f.configPath + ".desktop-events.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), f.rootPath) {
		t.Fatal("management history leaked paths")
	}
	captured := []byte(nil)
	f.service.options.Export = func(_ context.Context, _ ExportKind, data []byte) error {
		captured = append([]byte(nil), data...)
		return nil
	}
	response := callRPC(t, f.service, "export", "logs.export", nil)
	if !response.OK || !strings.Contains(string(captured), "management_events") || strings.Contains(string(captured), f.rootPath) {
		t.Fatalf("management export: %+v", response)
	}
}
