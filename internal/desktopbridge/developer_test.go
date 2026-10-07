package desktopbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/previewconnect"
)

func developerProfileFixture() json.RawMessage {
	return json.RawMessage(`{"id":"tool_version","kind":"version_probe","platform":["windows"],"executable":"C:\\Tools\\tool.exe","identity":{"require_regular":true,"reject_reparse":true,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"argv":{"variants":[{"variant_id":"version","exact":["--version"]}]},"cwd":{"kind":"private_empty"},"env":{"inherit":false,"allow":[],"fixed":{}},"limits":{"wall_timeout_ms":2000,"stdout_bytes":8192,"stderr_bytes":4096,"max_processes":1,"max_children":0},"network":{"mode":"deny","require_enforcement":true},"confirmation":{"mode":"per_call","local_only":true},"result":{"type":"version","return_raw_output":false}}`)
}

func TestDeveloperPolicyProfilesPersistWithoutEnablingExecution(t *testing.T) {
	var diagnostic []byte
	f := newBridgeFixture(t, false, func(o *Options) {
		o.Export = func(_ context.Context, _ ExportKind, b []byte) error {
			diagnostic = append([]byte(nil), b...)
			return nil
		}
	})
	snap := f.service.snapshot()
	if snap.Desktop.Developer.Enabled || snap.Desktop.Developer.ExecutionAvailable {
		t.Fatal("default authority")
	}
	fake := &fakeBridgeController{status: previewconnect.Status{Stage: previewconnect.StageIdle}}
	f.service.mu.Lock()
	f.service.controller = fake
	f.service.mu.Unlock()
	profile := desktopWrite(t, f.service, "developer.saveProfile", map[string]any{"expected_revision": snap.Desktop.Revision, "id": "tool_version", "profile": developerProfileFixture()})
	if profile.Connection.Code != "" || len(profile.Desktop.Developer.Profiles) != 1 || profile.Desktop.Developer.Enabled || profile.Desktop.Developer.ExecutionAvailable {
		t.Fatalf("profile: %+v", profile)
	}
	fake.mu.Lock()
	stops, connects, reconnects := fake.stopCount, fake.connectCount, fake.reconnectCount
	fake.mu.Unlock()
	if stops != 1 || connects != 0 || reconnects != 0 {
		t.Fatal("configuration launched/reconnected")
	}
	allowed := []string{snap.Desktop.ActiveID}
	on := desktopWrite(t, f.service, "developer.configure", map[string]any{"expected_revision": profile.Desktop.Revision, "enabled": true, "allowed_connections": allowed})
	if on.Connection.Code != "" || !on.Desktop.Developer.Enabled || on.Desktop.Developer.ExecutionAvailable || on.Desktop.Developer.GateCode != "network_enforcement_required" {
		t.Fatal("mode confused with OS enforcement")
	}
	reopened, err := New(f.service.options)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !reopened.snapshot().Desktop.Developer.Enabled || len(reopened.snapshot().Desktop.Developer.Profiles) != 1 {
		t.Fatal("policy did not persist")
	}
	if response := callRPC(t, f.service, "export", "diagnostics.export", nil); !response.OK || len(diagnostic) == 0 {
		t.Fatal("diagnostic export")
	}
	if bytes.Contains(diagnostic, []byte("tool.exe")) || bytes.Contains(diagnostic, []byte(strings.Repeat("a", 64))) || bytes.Contains(diagnostic, []byte("--version")) {
		t.Fatal("diagnostics leaked command profile")
	}
	for _, method := range []string{"developer.run", "command.run", "run_probe"} {
		if callRPC(t, f.service, "no-run", method, map[string]any{"id": "tool_version"}).OK {
			t.Fatal("opened execution RPC")
		}
	}
	off := desktopWrite(t, f.service, "developer.configure", map[string]any{"expected_revision": on.Desktop.Revision, "enabled": false, "allowed_connections": []string{}})
	if off.Desktop.Developer.Enabled || len(off.Desktop.Developer.AllowedConnections) != 0 || len(off.Desktop.Developer.Profiles) != 1 {
		t.Fatal("disable did not revoke connections/retain templates")
	}
	removed := desktopWrite(t, f.service, "developer.removeProfile", map[string]any{"expected_revision": off.Desktop.Revision, "id": "tool_version"})
	if removed.Connection.Code != "" || len(removed.Desktop.Developer.Profiles) != 0 {
		t.Fatal("remove failed")
	}
}

func TestDeveloperInvalidProfileAndStalePolicyLeaveConfigurationUntouched(t *testing.T) {
	f := newBridgeFixture(t, false, nil)
	snap := f.service.snapshot()
	fake := &fakeBridgeController{}
	f.service.mu.Lock()
	f.service.controller = fake
	f.service.mu.Unlock()
	before, _ := os.ReadFile(f.configPath)
	invalid := []string{
		strings.Replace(string(developerProfileFixture()), `"require_enforcement":true`, `"require_enforcement":false`, 1),
		strings.Replace(string(developerProfileFixture()), `"inherit":false`, `"inherit":true`, 1),
		strings.Replace(string(developerProfileFixture()), `["--version"]`, `["-c","evil"]`, 1),
		strings.Replace(string(developerProfileFixture()), `"sha256":"`, `"certified":true,"sha256":"`, 1),
		strings.Replace(string(developerProfileFixture()), `"id":"tool_version"`, `"id":"tool_version","id":"tool_version"`, 1),
	}
	for _, raw := range invalid {
		result := desktopWrite(t, f.service, "developer.saveProfile", map[string]any{"expected_revision": snap.Desktop.Revision, "id": "tool_version", "profile": json.RawMessage(raw)})
		if result.Connection.Code != "config_invalid" {
			t.Fatalf("accepted invalid profile: %s", result.Connection.Code)
		}
	}
	for _, allowed := range [][]string{{"unknown"}, {snap.Desktop.ActiveID, snap.Desktop.ActiveID}} {
		result := desktopWrite(t, f.service, "developer.configure", map[string]any{"expected_revision": snap.Desktop.Revision, "enabled": true, "allowed_connections": allowed})
		if result.Connection.Code != "config_invalid" {
			t.Fatal("invalid connection grant")
		}
	}
	stale := desktopWrite(t, f.service, "developer.configure", map[string]any{"expected_revision": "stale", "enabled": true, "allowed_connections": []string{snap.Desktop.ActiveID}})
	if stale.Connection.Code != "revision_conflict" {
		t.Fatal("stale grant")
	}
	for _, params := range []map[string]any{
		{"expected_revision": snap.Desktop.Revision, "enabled": true, "allowed_connections": []string{}},
		{"expected_revision": snap.Desktop.Revision, "enabled": false, "allowed_connections": []string{snap.Desktop.ActiveID}},
		{"expected_revision": snap.Desktop.Revision, "enabled": true, "allowed_connections": []string{snap.Desktop.ActiveID}, "execution_available": true},
	} {
		if callRPC(t, f.service, "invalid", "developer.configure", params).OK {
			t.Fatal("invalid fields accepted")
		}
	}
	after, _ := os.ReadFile(f.configPath)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected writes changed config")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.stopCount != 0 {
		t.Fatal("invalid write stopped controller")
	}
}

func TestDeveloperStopFailureRetainsOldAuthority(t *testing.T) {
	f := newBridgeFixture(t, false, nil)
	snap := f.service.snapshot()
	fake := &fakeBridgeController{stopHook: func() error { return errors.New("cannot stop") }}
	f.service.mu.Lock()
	f.service.controller = fake
	f.service.mu.Unlock()
	result := desktopWrite(t, f.service, "developer.configure", map[string]any{"expected_revision": snap.Desktop.Revision, "enabled": true, "allowed_connections": []string{snap.Desktop.ActiveID}})
	if result.Connection.Code != "stop_failed" || result.Desktop.Revision != snap.Desktop.Revision || result.Desktop.Developer.Enabled {
		t.Fatal("failed stop applied authority")
	}
	fake.mu.Lock()
	fake.stopHook = nil
	fake.mu.Unlock()
}

func TestDeveloperRemovingConnectionRevokesGrantAndRestoreDropsProfiles(t *testing.T) {
	var backup []byte
	f := newBridgeFixture(t, false, func(o *Options) {
		o.SaveBackup = func(_ context.Context, b []byte) error { backup = append([]byte(nil), b...); return nil }
		o.LoadBackup = func(context.Context) ([]byte, error) { return backup, nil }
	})
	s := f.service.snapshot()
	s = desktopWrite(t, f.service, "developer.saveProfile", map[string]any{"expected_revision": s.Desktop.Revision, "id": "tool_version", "profile": developerProfileFixture()})
	s = desktopWrite(t, f.service, "developer.configure", map[string]any{"expected_revision": s.Desktop.Revision, "enabled": true, "allowed_connections": []string{s.Desktop.ActiveID}})
	s = desktopWrite(t, f.service, "settings.backup", map[string]any{"expected_revision": s.Desktop.Revision})
	removed := desktopWrite(t, f.service, "connections.remove", map[string]any{"expected_revision": s.Desktop.Revision, "id": s.Desktop.ActiveID})
	if removed.Connection.Code != "" || removed.Desktop.Developer.Enabled || len(removed.Desktop.Developer.AllowedConnections) != 0 {
		t.Fatal("connection removal retained developer grant")
	}
	picked := callRPC(t, f.service, "pick", "settings.pickBackup", nil)
	if !picked.OK || picked.Data.BackupSelection == nil {
		t.Fatal("selection")
	}
	restored := desktopWrite(t, f.service, "settings.restore", map[string]any{"expected_revision": picked.Data.BackupSelection.Revision, "selection_id": picked.Data.BackupSelection.ID})
	if restored.Connection.Code != "" || restored.Desktop.Developer.Enabled || len(restored.Desktop.Developer.Profiles) != 0 {
		t.Fatal("restored command authority")
	}
}
