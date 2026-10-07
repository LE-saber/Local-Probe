package desktopbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureBackup(t *testing.T, f bridgeFixture) []byte {
	t.Helper()
	cfg, err := f.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeBackup(cfg.Config(), time.Date(2026, 10, 6, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func pickFixtureBackup(t *testing.T, f bridgeFixture) BackupSelection {
	t.Helper()
	response := callRPC(t, f.service, "pick", "settings.pickBackup", nil)
	if !response.OK || response.Data.BackupSelection == nil {
		t.Fatalf("pick failed: %+v", response.Error)
	}
	return *response.Data.BackupSelection
}

func TestBackupFormatRoundTripAndInvalidDocuments(t *testing.T) {
	f := newBridgeFixture(t, false, nil)
	data := fixtureBackup(t, f)
	parsed, created, err := decodeBackup(data)
	if err != nil || len(parsed.Roots()) != 1 || created.Year() != 2026 {
		t.Fatalf("round trip: %v", err)
	}
	var spaced bytes.Buffer
	if err := json.Indent(&spaced, data, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeBackup(spaced.Bytes()); err != nil {
		t.Fatal("whitespace broke canonical checksum")
	}
	cfg, _ := f.store.Load()
	raw, _ := cfg.Config().MarshalJSON()
	invalid := map[string][]byte{
		"empty": nil, "raw-config": raw, "truncated": data[:len(data)-2], "trailing": append(append([]byte(nil), data...), []byte(`{}`)...),
		"schema":           bytes.Replace(data, []byte(backupSchema), []byte("local-probe.backup.v99"), 1),
		"tampered":         bytes.Replace(data, []byte(`"label":"Local"`), []byte(`"label":"Changed"`), 1),
		"unknown":          append([]byte(`{"secret":"must-not-import",`), data[1:]...),
		"duplicate":        append([]byte(`{"schema_version":"local-probe.backup.v1",`), data[1:]...),
		"nested-duplicate": bytes.Replace(data, []byte(`"enabled":true`), []byte(`"enabled":true,"enabled":true`), 1),
		"missing-time":     bytes.Replace(data, []byte(`"2026-10-06T01:00:00Z"`), []byte(`null`), 1),
		"oversized":        bytes.Repeat([]byte{' '}, MaxBackupBytes+1),
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeBackup(value); err == nil {
				t.Fatal("accepted invalid document")
			}
		})
	}
}

func TestBackupSelectedFilesUseBoundedReadAndNeverOverwrite(t *testing.T) {
	f := newBridgeFixture(t, false, nil)
	data := fixtureBackup(t, f)
	dir := t.TempDir()
	path := filepath.Join(dir, "我的配置"+BackupExtension)
	if err := SaveBackupFile(path, data); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBackupFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("saved backup: %v", err)
	}
	if err := SaveBackupFile(path, data); err == nil {
		t.Fatal("overwrote existing backup")
	}
	if existing, _ := os.ReadFile(path); !bytes.Equal(existing, data) {
		t.Fatal("overwrite changed bytes")
	}
	for _, bad := range []string{"relative.lpbackup", filepath.Join(dir, "diagnostics.json"), filepath.Join(dir, "bad.lpbackup:stream"), filepath.Join(dir, "missing", "backup.lpbackup")} {
		if SaveBackupFile(bad, data) == nil {
			t.Fatalf("accepted bad path %q", bad)
		}
	}
	if err := SaveBackupFile(filepath.Join(dir, "broken.lpbackup"), []byte(`{}`)); err == nil {
		t.Fatal("saved invalid data")
	}
	directory := filepath.Join(dir, "directory.lpbackup")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBackupFile(directory); err == nil {
		t.Fatal("read directory")
	}
	large := filepath.Join(dir, "large.lpbackup")
	if err := os.WriteFile(large, bytes.Repeat([]byte{' '}, MaxBackupBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBackupFile(large); err == nil {
		t.Fatal("read oversized data")
	}
	link := filepath.Join(dir, "link.lpbackup")
	if os.Symlink(path, link) == nil {
		if _, err := ReadBackupFile(link); err == nil {
			t.Fatal("followed symbolic link")
		}
	}
}

func TestBackupBaseNameGetsFullExtensionExactlyOnce(t *testing.T) {
	for _, base := range []string{"备份", "备份.v2", "my backup.json"} {
		path := filepath.Join(t.TempDir(), base)
		if got := AppendBackupExtension(path); got != path+BackupExtension {
			t.Fatalf("suffix mismatch %q", got)
		}
	}
	for _, extension := range []string{".lpbackup", ".LPBACKUP"} {
		path := filepath.Join(t.TempDir(), "my backup"+extension)
		if got := AppendBackupExtension(path); got != path {
			t.Fatalf("duplicated suffix %q", got)
		}
	}
}

func TestBackupInvalidCancelledAndUnavailableLeaveConfigurationAndControllerAlone(t *testing.T) {
	for _, scenario := range []string{"invalid", "cancelled", "unavailable", "io-error"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBridgeFixture(t, false, func(o *Options) {
				switch scenario {
				case "invalid":
					o.LoadBackup = func(context.Context) ([]byte, error) { return []byte(`{"schema_version":"unknown"}`), nil }
				case "cancelled":
					o.LoadBackup = func(context.Context) ([]byte, error) { return nil, ErrBackupCancelled }
				case "io-error":
					o.LoadBackup = func(context.Context) ([]byte, error) { return nil, errors.New("private file path must not appear") }
				}
			})
			fake := &fakeBridgeController{status: readyFakeStatus()}
			installFakeController(f, fake)
			before, _ := os.ReadFile(f.configPath)
			response := callRPC(t, f.service, "pick", "settings.pickBackup", nil)
			if scenario == "cancelled" {
				if !response.OK || response.Data.BackupSelection != nil {
					t.Fatal("cancel opened restore")
				}
			} else if response.OK {
				t.Fatal("invalid selection accepted")
			}
			after, _ := os.ReadFile(f.configPath)
			if !bytes.Equal(before, after) {
				t.Fatal("picker changed configuration")
			}
			fake.mu.Lock()
			stops := fake.stopCount
			fake.mu.Unlock()
			if stops != 0 {
				t.Fatal("picker stopped live controller")
			}
			if strings.Contains(f.service.Handle(`{"id":"bad","method":"settings.pickBackup","params":{"path":"C:\\private"}}`), "private file path") {
				t.Fatal("leaked callback error")
			}
			if callRPC(t, f.service, "bad", "settings.pickBackup", map[string]any{"path": f.configPath}).OK {
				t.Fatal("RPC chose backup path")
			}
		})
	}
}

func TestBackupRestoreIsOneUseExpiresAndBindsRevision(t *testing.T) {
	var data []byte
	f := newBridgeFixture(t, false, func(o *Options) {
		o.LoadBackup = func(context.Context) ([]byte, error) { return append([]byte(nil), data...), nil }
	})
	data = fixtureBackup(t, f)
	selection := pickFixtureBackup(t, f)
	f.service.mu.Lock()
	f.service.backupCandidate.expires = time.Now().Add(-time.Second)
	f.service.mu.Unlock()
	expired := desktopWrite(t, f.service, "settings.restore", map[string]any{"expected_revision": selection.Revision, "selection_id": selection.ID})
	if expired.Connection.Code != "backup_selection_expired" {
		t.Fatal("expired selection restored")
	}
	selection = pickFixtureBackup(t, f)
	renamed := desktopWrite(t, f.service, "workspace.update", map[string]any{"expected_revision": selection.Revision, "root_id": f.rootID, "display_name": "New"})
	conflict := desktopWrite(t, f.service, "settings.restore", map[string]any{"expected_revision": selection.Revision, "selection_id": selection.ID})
	if conflict.Connection.Code != "revision_conflict" || conflict.Desktop.Revision != renamed.Desktop.Revision {
		t.Fatal("stale selection restored")
	}
	selection = pickFixtureBackup(t, f)
	// Changing the file after selection cannot change what the user confirmed.
	data = []byte(`invalid replacement`)
	restored := desktopWrite(t, f.service, "settings.restore", map[string]any{"expected_revision": selection.Revision, "selection_id": selection.ID})
	if restored.Connection.Code != "" || restored.Workspace.Roots[0].DisplayName == "New" {
		t.Fatal("selected bytes not retained")
	}
	replay := desktopWrite(t, f.service, "settings.restore", map[string]any{"expected_revision": restored.Desktop.Revision, "selection_id": selection.ID})
	if replay.Connection.Code != "backup_selection_expired" {
		t.Fatal("selection reused")
	}
}

func TestBackupRestoreStopsBeforeWriteAndPreservesConfigOnStopFailure(t *testing.T) {
	var data []byte
	f := newBridgeFixture(t, false, func(o *Options) { o.LoadBackup = func(context.Context) ([]byte, error) { return data, nil } })
	data = fixtureBackup(t, f)
	fake := &fakeBridgeController{status: readyFakeStatus(), stopHook: func() error { return errors.New("stop failed") }}
	installFakeController(f, fake)
	selection := pickFixtureBackup(t, f)
	before, _ := os.ReadFile(f.configPath)
	result := desktopWrite(t, f.service, "settings.restore", map[string]any{"expected_revision": selection.Revision, "selection_id": selection.ID})
	after, _ := os.ReadFile(f.configPath)
	if result.Connection.Code != "stop_failed" || !bytes.Equal(before, after) {
		t.Fatal("failed stop changed configuration")
	}
	fake.mu.Lock()
	fake.stopHook = nil
	fake.mu.Unlock()
}

func TestBackupSaveCancelledAndFailedDoNotChangeConfiguration(t *testing.T) {
	for _, scenario := range []string{"cancelled", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBridgeFixture(t, false, func(o *Options) {
				o.SaveBackup = func(context.Context, []byte) error {
					if scenario == "cancelled" {
						return ErrBackupCancelled
					}
					return errors.New("disk write failed")
				}
			})
			before, _ := os.ReadFile(f.configPath)
			snap := f.service.snapshot()
			after := desktopWrite(t, f.service, "settings.backup", map[string]any{"expected_revision": snap.Desktop.Revision})
			if scenario == "cancelled" && after.Connection.Code != "cancelled" || scenario == "failed" && after.Connection.Code != "backup_failed" {
				t.Fatal("wrong backup outcome")
			}
			current, _ := os.ReadFile(f.configPath)
			if !bytes.Equal(before, current) {
				t.Fatal("failed backup changed config")
			}
			if _, err := os.Stat(f.configPath + ".desktop-backup.json"); !os.IsNotExist(err) {
				t.Fatal("wrote hidden fallback backup")
			}
		})
	}
}

func TestBackupRestoreCanInitializeAnUnconfiguredDesktop(t *testing.T) {
	f := newBridgeFixture(t, false, nil)
	data := fixtureBackup(t, f)
	base := t.TempDir()
	s, err := New(Options{RepoRoot: base, ConfigPath: filepath.Join(base, "config", "local-probe.json"), LoadBackup: func(context.Context) ([]byte, error) { return data, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	picked := callRPC(t, s, "pick", "settings.pickBackup", nil)
	if !picked.OK || picked.Data.BackupSelection == nil || picked.Data.BackupSelection.Revision != "" {
		t.Fatal("first-run selection failed")
	}
	result := desktopWrite(t, s, "settings.restore", map[string]any{"expected_revision": "", "selection_id": picked.Data.BackupSelection.ID})
	if !result.Workspace.Available || result.Connection.Code != "" || result.Workspace.Roots[0].Enabled {
		t.Fatal("first-run restore failed or enabled root")
	}
}
