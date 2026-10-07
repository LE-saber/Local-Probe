package previewapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/desktopadmin"
	"github.com/LE-saber/Local-Probe/internal/supervisor"
)

type fakeAuditReader struct {
	snapshot AuditSnapshot
	err      error
	calls    atomic.Int32
	max      atomic.Int32
}

type panickingStatusSource struct{}

func (panickingStatusSource) Snapshots() []supervisor.Snapshot { panic("status source failure") }

func (r *fakeAuditReader) Read(ctx context.Context, maxItems int) (AuditSnapshot, error) {
	r.calls.Add(1)
	r.max.Store(int32(maxItems))
	if err := ctx.Err(); err != nil {
		return AuditSnapshot{}, err
	}
	if r.err != nil {
		return AuditSnapshot{}, r.err
	}
	return r.snapshot, nil
}

func testConfig(t *testing.T) config.Config {
	t.Helper()
	root, err := config.NewRoot("root", `C:\private\root`, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("profile", []string{"root"}, []string{"server_info", "ping"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection := config.NewConnection("connection", `C:\private\token.txt`, "profile", "credential", true)
	credential := config.NewCredentialRef("credential", "dpapi")
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credential})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeConfig(t *testing.T, path string) (config.Snapshot, *config.FileStore) {
	t.Helper()
	store, err := config.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Save(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, store
}

func newTestApp(t *testing.T, reader AuditReader) (*App, string, *config.FileStore) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "local-probe.json")
	auditDir := filepath.Join(dir, "audit")
	snapshot, store := writeConfig(t, configPath)
	app, err := New(Options{ConfigPath: configPath, AuditDir: auditDir, AuditReader: reader})
	if err != nil {
		t.Fatal(err)
	}
	return app, snapshot.Revision(), store
}

func hasWarning(model ViewModel, code string) bool {
	for _, warning := range model.Warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestNewMissingConfigIsExplicitlyUnconfigured(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "missing.json")
	auditDir := filepath.Join(dir, "audit")
	app, err := New(Options{ConfigPath: configPath, AuditDir: auditDir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	model := app.Model()
	if model.ConfigState != ConfigUnconfigured || model.Overview.ConfigRevision != "" {
		t.Fatalf("missing config model = %+v", model)
	}
	if model.Overview.StatusKnown || model.Capabilities.ConnectionStatus.Reason != desktopadmin.CapabilityUnavailableReason {
		t.Fatalf("missing status availability = %+v", model)
	}
	if !hasWarning(model, WarningConfigNotFound) || !hasWarning(model, WarningManagementProductionGate) || !hasWarning(model, WarningStatusUnavailable) {
		t.Fatalf("missing config warnings = %+v", model.Warnings)
	}
	wire, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), configPath) || strings.Contains(string(wire), auditDir) {
		t.Fatalf("model exposed option path: %s", wire)
	}
}

func TestRefreshUsesFileStoreSHA256RevisionAndSafeProjection(t *testing.T) {
	app, revision, _ := newTestApp(t, nil)
	defer app.Close()

	model := app.Model()
	if model.ConfigState != ConfigConfigured || model.Overview.ConfigRevision != revision {
		t.Fatalf("configured model = %+v, revision=%q", model, revision)
	}
	if !strings.HasPrefix(model.Overview.ConfigRevision, "sha256:") {
		t.Fatalf("revision = %q, want sha256", model.Overview.ConfigRevision)
	}
	if model.Overview.RootCount != 1 || model.Overview.ProfileCount != 1 || model.Overview.ConnectionCount != 1 || model.Overview.EnabledConnectionCount != 1 {
		t.Fatalf("overview = %+v", model.Overview)
	}
	if model.Overview.StatusKnown || model.Connections.StatusKnown || len(model.Connections.Entries) != 1 || !model.Connections.Entries[0].LabelOmitted {
		t.Fatalf("connection projection = %+v", model.Connections)
	}
	if model.Capabilities.ConnectionStatus.Reason != desktopadmin.CapabilityUnavailableReason || model.Capabilities.ManagementActions.Reason != desktopadmin.CapabilityProductionGate {
		t.Fatalf("capabilities = %+v", model.Capabilities)
	}
	wire, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`C:\private\root`, `C:\private\token.txt`, "credential", "dpapi"} {
		if strings.Contains(string(wire), marker) {
			t.Fatalf("safe model contains %q: %s", marker, wire)
		}
	}
}

func TestStatusSourceFailureIsUnavailableNotProductionGate(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "local-probe.json")
	auditDir := filepath.Join(dir, "audit")
	_, _ = writeConfig(t, configPath)
	app, err := New(Options{ConfigPath: configPath, AuditDir: auditDir, Status: panickingStatusSource{}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	model := app.Model()
	if model.Overview.StatusKnown || model.Connections.StatusKnown {
		t.Fatalf("failed status source was treated as known: %+v", model)
	}
	if model.Capabilities.ConnectionStatus.Reason != desktopadmin.CapabilityUnavailableReason {
		t.Fatalf("status capability = %+v, want unavailable", model.Capabilities.ConnectionStatus)
	}
	if hasWarning(model, WarningStatusProductionGate) || !hasWarning(model, WarningStatusUnavailable) {
		t.Fatalf("status warnings = %+v", model.Warnings)
	}
	if model.Capabilities.ManagementActions.Reason != desktopadmin.CapabilityProductionGate {
		t.Fatalf("management action capability = %+v", model.Capabilities.ManagementActions)
	}
}

func TestInvalidRefreshClearsPreviousSnapshot(t *testing.T) {
	app, revision, store := newTestApp(t, nil)
	defer app.Close()
	if err := os.WriteFile(store.Path(), []byte(`{"schema_version":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	model, err := app.Refresh(context.Background())
	if !errors.Is(err, ErrConfigInvalid) {
		t.Fatalf("Refresh error = %v, want ErrConfigInvalid", err)
	}
	if model.ConfigState != ConfigInvalid || model.Overview.ConfigRevision != "" || model.Overview.ConnectionCount != 0 {
		t.Fatalf("invalid model retained old snapshot: %+v", model)
	}
	if !hasWarning(model, WarningConfigInvalid) {
		t.Fatalf("invalid warnings = %+v", model.Warnings)
	}
	if strings.Contains(string(mustJSON(t, model)), revision) {
		t.Fatal("invalid model exposed previous revision")
	}
}

func TestAuditReaderIsBoundedAndDiagnosticsAreTyped(t *testing.T) {
	logs := make([]LogEntry, 0, MaxItems+9)
	for i := 0; i < MaxItems+9; i++ {
		entry, err := NewLogEntry(time.Unix(int64(i+1), 0), "MCP", "mcp.call", "info", "succeeded", "", "connection", "profile", "sha256:"+strings.Repeat("a", 64), 1)
		if err != nil {
			t.Fatal(err)
		}
		logs = append(logs, entry)
	}
	record, err := desktopadmin.NewDiagnosticRecord(time.Unix(10, 0), desktopadmin.DiagnosticMCP, desktopadmin.DiagnosticInfo, desktopadmin.DiagnosticSucceeded, "", "connection", "profile", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	records := make([]desktopadmin.DiagnosticRecord, 0, MaxItems+2)
	for i := 0; i < MaxItems+2; i++ {
		records = append(records, record)
	}
	reader := &fakeAuditReader{snapshot: AuditSnapshot{Logs: logs, Diagnostics: records}}
	app, _, _ := newTestApp(t, reader)
	defer app.Close()

	model := app.Model()
	if !model.Logs.Available || len(model.Logs.Entries) != MaxItems || !model.Logs.Truncated {
		t.Fatalf("logs = %+v", model.Logs)
	}
	if !model.Diagnostics.Available || len(model.Diagnostics.Entries) != MaxItems || !model.Diagnostics.Truncated {
		t.Fatalf("diagnostics = %+v", model.Diagnostics)
	}
	if reader.max.Load() != MaxItems {
		t.Fatalf("AuditReader max = %d, want %d", reader.max.Load(), MaxItems)
	}
	if model.Capabilities.Diagnostics.Available != true {
		t.Fatalf("diagnostics capability = %+v", model.Capabilities.Diagnostics)
	}
	if strings.Contains(string(mustJSON(t, model)), "message") {
		t.Fatal("model contains untyped diagnostic message")
	}
}

func TestDefaultAuditReaderUsesExplicitAuditDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "local-probe.json")
	auditDir := filepath.Join(dir, "audit")
	_, _ = writeConfig(t, configPath)
	sink, err := audit.New(audit.Config{Directory: auditDir, InstanceID: "preview-test"})
	if err != nil {
		t.Fatal(err)
	}
	err = sink.Emit(audit.Event{
		EventID: "event", CorrelationID: "correlation", Component: audit.ComponentMCP,
		EventType: audit.EventMCPList, Severity: audit.SeverityInfo,
		Outcome: audit.OutcomeSucceeded, Timestamp: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	app, err := New(Options{ConfigPath: configPath, AuditDir: auditDir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	model := app.Model()
	if !model.Logs.Available || len(model.Logs.Entries) != 1 {
		t.Fatalf("default audit reader model = %+v", model.Logs)
	}
	if !model.Diagnostics.Available || len(model.Diagnostics.Entries) != 1 {
		t.Fatalf("default diagnostics model = %+v", model.Diagnostics)
	}
}

func TestDefaultAuditReaderOmitsSHARevisionFromTypedDiagnostics(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "local-probe.json")
	auditDir := filepath.Join(dir, "audit")
	_, _ = writeConfig(t, configPath)
	if err := os.MkdirAll(auditDir, 0700); err != nil {
		t.Fatal(err)
	}
	line := `{"ts":"2026-09-16T12:00:00Z","event":"event-sha","schema":"local-probe.audit.v2","instance":"instance-1","correlation":"event-sha","component":"MCP","type":"mcp.call","class":"normal","severity":"info","outcome":"succeeded","duration_ms":0,"connection":"connection-1","profile":"profile-1","revision":"sha256:` + strings.Repeat("a", 64) + `","budget":{"wire_in_bytes":0,"wire_out_bytes":0,"logical_read_bytes":0,"returned_bytes":0,"limit_bytes":0}}
`
	if err := os.WriteFile(filepath.Join(auditDir, "audit.jsonl"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	app, err := New(Options{ConfigPath: configPath, AuditDir: auditDir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	model := app.Model()
	if !model.Logs.Available || !model.Diagnostics.Available {
		t.Fatalf("sha revision made audit unavailable: %+v", model)
	}
	if encoded := string(mustJSON(t, model.Diagnostics)); strings.Contains(encoded, "sha256:") {
		t.Fatalf("typed diagnostics leaked SHA revision: %s", encoded)
	}
}

func TestDefaultAuditReaderIsRetriedWhenDirectoryAppears(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "local-probe.json")
	auditDir := filepath.Join(dir, "audit")
	_, store := writeConfig(t, configPath)
	app, err := New(Options{ConfigPath: configPath, AuditDir: auditDir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.Model().Logs.Available {
		t.Fatal("missing audit directory unexpectedly available")
	}
	if err := os.MkdirAll(auditDir, 0700); err != nil {
		t.Fatal(err)
	}
	sink, err := audit.New(audit.Config{Directory: auditDir, InstanceID: "preview-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh after audit directory creation: %v", err)
	}
	if !app.Model().Logs.Available {
		t.Fatal("audit reader was not retried")
	}
	_ = store
}

func TestInvalidAuditRecordFailsClosedWithoutDroppingConfig(t *testing.T) {
	reader := &fakeAuditReader{snapshot: AuditSnapshot{Logs: []LogEntry{{Timestamp: time.Now(), Component: "MCP", Type: "not.valid", Severity: "info", Outcome: "succeeded"}}}}
	app, revision, _ := newTestApp(t, reader)
	defer app.Close()

	model := app.Model()
	if model.ConfigState != ConfigConfigured || model.Overview.ConfigRevision != revision {
		t.Fatalf("config was lost after invalid audit: %+v", model)
	}
	if model.Logs.Available || !hasWarning(model, WarningAuditUnavailable) {
		t.Fatalf("invalid audit model = %+v", model)
	}
}

func TestRefreshCancellationDoesNotReplaceModel(t *testing.T) {
	app, revision, _ := newTestApp(t, nil)
	defer app.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	model, err := app.Refresh(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Refresh error = %v", err)
	}
	if model.Overview.ConfigRevision != revision || app.Model().Overview.ConfigRevision != revision {
		t.Fatalf("cancelled Refresh changed model = %+v", model)
	}
}

func TestRefreshAndModelAreThreadSafe(t *testing.T) {
	app, _, _ := newTestApp(t, nil)
	defer app.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _ = app.Refresh(context.Background())
				_ = app.Model()
			}
		}()
	}
	wg.Wait()
	if app.Model().ConfigState != ConfigConfigured {
		t.Fatal("concurrent refresh lost configured model")
	}
}

func TestLifecycleActionsAreTypedProductionGateErrors(t *testing.T) {
	app, _, _ := newTestApp(t, nil)
	defer app.Close()
	for _, test := range []struct {
		action Action
		call   func() error
	}{
		{ActionStart, func() error { return app.Start(context.Background(), "connection") }},
		{ActionStop, func() error { return app.Stop(context.Background(), "connection") }},
		{ActionReconnect, func() error { return app.Reconnect(context.Background(), "connection") }},
	} {
		err := test.call()
		if !errors.Is(err, ErrProductionGate) {
			t.Fatalf("%s error = %v, want production gate", test.action, err)
		}
		var gate *ProductionGateError
		if !errors.As(err, &gate) || gate.Action != test.action || gate.Code != "production_gate" {
			t.Fatalf("%s typed error = %#v", test.action, err)
		}
	}
}

func TestCloseOnlyClosesPreview(t *testing.T) {
	app, _, _ := newTestApp(t, nil)
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Refresh(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Refresh after Close error = %v", err)
	}
	if err := app.Start(context.Background(), "connection"); !errors.Is(err, ErrProductionGate) {
		t.Fatalf("Start after Close error = %v", err)
	}
}

func TestOptionsRequireAbsolutePaths(t *testing.T) {
	if _, err := New(Options{ConfigPath: "relative.json", AuditDir: t.TempDir()}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("relative config error = %v", err)
	}
	if _, err := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.json"), AuditDir: "relative-audit"}); !errors.Is(err, ErrInvalidOptions) {
		t.Fatalf("relative audit error = %v", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
