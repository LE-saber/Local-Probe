package desktopbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

type bridgeFixture struct {
	service    *Service
	store      *config.FileStore
	configPath string
	rootPath   string
	rootID     string
}

func newBridgeFixture(t *testing.T, sharedProfile bool, options func(*Options)) bridgeFixture {
	t.Helper()
	base := t.TempDir()
	rootPath := filepath.Join(base, "workspace")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := config.NewRoot("root-a", rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("profile-a", []string{root.ID()}, []string{"read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	credential := config.NewCredentialRef("credential-a", "runtime")
	connections := []config.Connection{config.NewConnection(workspaceadmin.DefaultConnectionID, "Local", profile.ID(), credential.ID(), true)}
	if sharedProfile {
		connections = append(connections, config.NewConnection("second-connection", "Second", profile.ID(), credential.ID(), true))
	}
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, connections, []config.CredentialRef{credential})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(base, "trusted-config", "local-probe.json")
	store, err := config.NewFileStoreWithOptions(config.FileStoreOptions{Path: configPath, CreateParent: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	serviceOptions := Options{ConfigPath: configPath, RepoRoot: base, Transport: string(config.TransportOpenAIRuntime)}
	if options != nil {
		options(&serviceOptions)
	}
	service, err := New(serviceOptions)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close bridge: %v", err)
		}
	})
	return bridgeFixture{service: service, store: store, configPath: configPath, rootPath: rootPath, rootID: root.ID()}
}

func callRPC(t *testing.T, service *Service, id, method string, params any) rpcResponse {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	request, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	var response rpcResponse
	if err := json.Unmarshal([]byte(service.Handle(string(request))), &response); err != nil {
		t.Fatalf("decode %s response: %v", method, err)
	}
	return response
}

func waitBridgeIdle(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		service.mu.Lock()
		busy := service.busy
		service.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("bridge operation did not finish")
}

func TestNewMissingConfigProjectsUnconfiguredAndDoesNotCreateAuthorization(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "new-parent", "local-probe.json")
	service, err := New(Options{ConfigPath: configPath, RepoRoot: base, Transport: string(config.TransportOpenAIRuntime)})
	if err != nil {
		t.Fatalf("New without config: %v", err)
	}
	defer service.Close()
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("New unexpectedly created configuration: stat err=%v", err)
	}
	response := callRPC(t, service, "first", "snapshot", nil)
	if !response.OK || response.Data == nil || response.Data.View.Status != "unconfigured" || response.Data.Workspace.ErrorCode != "config_missing" || response.Data.Workspace.Available {
		t.Fatalf("missing config snapshot = %+v", response)
	}
	if len(response.Data.Workspace.Roots) != 0 {
		t.Fatalf("missing config authorized roots: %+v", response.Data.Workspace.Roots)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot created a configuration: stat err=%v", err)
	}
}

func TestUnknownSchemaRemainsInvalidAndCannotBeOverwrittenAsEmptyScope(t *testing.T) {
	base := t.TempDir()
	configPath := filepath.Join(base, "local-probe.json")
	badConfig := []byte(`{"schema_version":"future.schema"}`)
	if err := os.WriteFile(configPath, badConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{ConfigPath: configPath, RepoRoot: base})
	if err != nil {
		t.Fatalf("New with invalid config should still render: %v", err)
	}
	defer service.Close()
	snapshot := callRPC(t, service, "bad", "snapshot", nil)
	if !snapshot.OK || snapshot.Data == nil || snapshot.Data.View.Status != "config_invalid" || snapshot.Data.Workspace.ErrorCode != "config_invalid" || snapshot.Data.Workspace.Available {
		t.Fatalf("invalid config snapshot = %+v", snapshot)
	}
	mutation := callRPC(t, service, "write", "workspace.remove", map[string]any{"expected_revision": "anything", "root_ids": []string{"root-a"}})
	if !mutation.OK {
		t.Fatalf("invalid config mutation was not accepted asynchronously: %+v", mutation)
	}
	waitBridgeIdle(t, service)
	if got := service.snapshot().Connection.Code; got != "config_invalid" {
		t.Fatalf("invalid config mutation code = %q, want config_invalid", got)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, badConfig) {
		t.Fatalf("invalid config was rewritten: %q", after)
	}
}

func TestRPCRejectsUnknownMethodFieldsAndOversizedRequests(t *testing.T) {
	fixture := newBridgeFixture(t, false, nil)
	unknown := callRPC(t, fixture.service, "u", "arbitrary.shell", nil)
	if unknown.OK || unknown.Error == nil || unknown.Error.Code != "unknown_method" {
		t.Fatalf("unknown method response = %+v", unknown)
	}
	unknownField := fixture.service.Handle(`{"id":"x","method":"snapshot","params":{"path":"C:\\\\secret"}}`)
	if !strings.Contains(unknownField, `"code":"invalid_params"`) || strings.Contains(unknownField, "secret") {
		t.Fatalf("unknown field response was not safe: %s", unknownField)
	}
	oversized := fixture.service.Handle(strings.Repeat("x", maxRequestBytes+1))
	if len(oversized) > 512 || !strings.Contains(oversized, `"code":"invalid_request"`) {
		t.Fatalf("oversized request response = %s", oversized)
	}
}

func TestTrustedConfigPathIsExactlyTheControllerMCPConfig(t *testing.T) {
	fixture := newBridgeFixture(t, false, nil)
	controller, err := fixture.service.newController(config.TransportOpenAIRuntime)
	if err != nil {
		t.Fatal(err)
	}
	previewController, ok := controller.(*previewconnect.Controller)
	if !ok {
		t.Fatalf("controller type = %T", controller)
	}
	if got := previewController.Options().MCPConfig; got != fixture.configPath {
		t.Fatalf("MCP config path = %q, trusted path = %q", got, fixture.configPath)
	}
}

func TestPickerReturnsSuggestionsWithoutAuthorizingThem(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "picked")
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := newBridgeFixture(t, false, func(options *Options) {
		options.PickFolders = func(context.Context) ([]string, error) { return []string{selected}, nil }
	})
	before, err := fixture.service.workspaces.List(context.Background(), workspaceadmin.DefaultConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	response := callRPC(t, fixture.service, "pick", "workspace.pick", nil)
	if !response.OK || response.Data == nil || len(response.Data.Paths) != 1 || response.Data.Paths[0] != selected {
		t.Fatalf("picker response = %+v", response)
	}
	after, err := fixture.service.workspaces.List(context.Background(), workspaceadmin.DefaultConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision || len(before.Roots) != len(after.Roots) {
		t.Fatalf("picker changed authorization: before=%+v after=%+v", before, after)
	}
}

func TestExportsUseOnlySanitizedPathFreePreviewProjection(t *testing.T) {
	var captured map[ExportKind][]byte
	capturedDeadline := make(map[ExportKind]bool)
	fixture := newBridgeFixture(t, false, func(options *Options) {
		captured = make(map[ExportKind][]byte)
		options.Export = func(ctx context.Context, kind ExportKind, data []byte) error {
			deadline, ok := ctx.Deadline()
			capturedDeadline[kind] = ok && deadline.After(time.Now())
			captured[kind] = append([]byte(nil), data...)
			return nil
		}
	})
	for _, test := range []struct {
		method string
		kind   ExportKind
	}{{"logs.export", ExportLogs}, {"diagnostics.export", ExportDiagnostics}} {
		response := callRPC(t, fixture.service, "export", test.method, nil)
		if !response.OK || response.Data == nil {
			t.Fatalf("%s failed: %+v", test.method, response)
		}
		payload := captured[test.kind]
		if len(payload) == 0 || len(payload) > 256<<10 {
			t.Fatalf("%s payload length = %d", test.method, len(payload))
		}
		if !capturedDeadline[test.kind] {
			t.Fatalf("%s exporter did not receive a live deadline", test.method)
		}
		if bytes.Contains(payload, []byte(fixture.rootPath)) || bytes.Contains(payload, []byte("credential-a")) {
			t.Fatalf("%s included a forbidden value: %s", test.method, payload)
		}
	}
}

type fakeBridgeController struct {
	mu sync.Mutex

	status         previewconnect.Status
	stopCount      int
	connectCount   int
	reconnectCount int
	connectStarted chan struct{}
	connectRelease chan struct{}
	stopHook       func() error
	reconnectHook  func(context.Context) (previewconnect.Status, error)
}

func (c *fakeBridgeController) Connect(ctx context.Context, progress previewconnect.ProgressFunc) (previewconnect.Status, error) {
	c.mu.Lock()
	c.connectCount++
	started, release := c.connectStarted, c.connectRelease
	c.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release // Intentionally ignore ctx to simulate a late controller result.
	}
	status := readyFakeStatus()
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
	if progress != nil {
		progress(status)
	}
	return status, nil
}

func (c *fakeBridgeController) Reconnect(ctx context.Context, progress previewconnect.ProgressFunc) (previewconnect.Status, error) {
	c.mu.Lock()
	c.reconnectCount++
	hook := c.reconnectHook
	c.mu.Unlock()
	if hook != nil {
		status, err := hook(ctx)
		c.mu.Lock()
		c.status = status
		c.mu.Unlock()
		if progress != nil {
			progress(status)
		}
		return status, err
	}
	status := readyFakeStatus()
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
	if progress != nil {
		progress(status)
	}
	return status, nil
}

func (c *fakeBridgeController) Stop() error {
	c.mu.Lock()
	c.stopCount++
	stopCount := c.stopCount
	hook := c.stopHook
	c.status = previewconnect.Status{Stage: previewconnect.StageIdle, Transport: config.TransportOpenAIRuntime, Message: "已停止。"}
	c.mu.Unlock()
	if hook != nil && stopCount == 1 {
		return hook()
	}
	return nil
}

func (c *fakeBridgeController) Status() previewconnect.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func readyFakeStatus() previewconnect.Status {
	return previewconnect.Status{Stage: previewconnect.StageReady, Transport: config.TransportOpenAIRuntime, MCPReady: true, TunnelReady: true, Message: "本机检查通过。", Remedy: "等待后续状态刷新。", UpdatedAt: time.Now().UTC()}
}

func installFakeController(fixture bridgeFixture, controller *fakeBridgeController) {
	fixture.service.mu.Lock()
	fixture.service.controller = controller
	fixture.service.controllerFactory = func(config.ConnectionTransport) (lifecycleController, error) { return controller, nil }
	fixture.service.status = controller.Status()
	fixture.service.statusKnown = true
	fixture.service.mu.Unlock()
}

func TestCancelInvalidatesLateConnectionResultAndStopsOwnedController(t *testing.T) {
	fixture := newBridgeFixture(t, false, nil)
	fake := &fakeBridgeController{connectStarted: make(chan struct{}, 1), connectRelease: make(chan struct{})}
	installFakeController(fixture, fake)
	first := callRPC(t, fixture.service, "connect", "connection.connect", nil)
	if !first.OK || first.Data == nil || !first.Data.Connection.Busy {
		t.Fatalf("connect did not start asynchronously: %+v", first)
	}
	select {
	case <-fake.connectStarted:
	case <-time.After(time.Second):
		t.Fatal("connect worker did not enter fake controller")
	}
	second := callRPC(t, fixture.service, "again", "connection.connect", nil)
	if second.OK || second.Error == nil || second.Error.Code != "busy" {
		t.Fatalf("second click was not busy: %+v", second)
	}
	cancelled := callRPC(t, fixture.service, "cancel", "connection.cancel", nil)
	if !cancelled.OK || cancelled.Data == nil || cancelled.Data.Connection.Generation <= first.Data.Connection.Generation || cancelled.Data.Connection.Stage != string(previewconnect.StageStopping) {
		t.Fatalf("cancel did not invalidate generation: %+v", cancelled)
	}
	close(fake.connectRelease)
	waitBridgeIdle(t, fixture.service)
	final := fixture.service.snapshot().Connection
	if final.Stage == string(previewconnect.StageReady) || final.MCPReady || final.TunnelReady || final.Code != "cancelled" {
		t.Fatalf("late ready result replaced cancellation: %+v", final)
	}
	fake.mu.Lock()
	stops := fake.stopCount
	fake.mu.Unlock()
	if stops != 1 {
		t.Fatalf("owned controller stop count = %d, want 1", stops)
	}
}

func TestSharedProfileScopeChangeIsRefusedButLabelChangeIsAllowed(t *testing.T) {
	fixture := newBridgeFixture(t, true, nil)
	fake := &fakeBridgeController{status: readyFakeStatus()}
	installFakeController(fixture, fake)
	current, err := fixture.service.workspaces.List(context.Background(), workspaceadmin.DefaultConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	paused := false
	response := callRPC(t, fixture.service, "pause", "workspace.update", map[string]any{
		"expected_revision": current.Revision, "root_id": fixture.rootID, "enabled": paused,
	})
	if !response.OK {
		t.Fatalf("mutation request should be accepted asynchronously: %+v", response)
	}
	waitBridgeIdle(t, fixture.service)
	projection := fixture.service.snapshot()
	if projection.Connection.Code != "profile_shared" || projection.Connection.Saved == nil || *projection.Connection.Saved || projection.Connection.Applied == nil || *projection.Connection.Applied {
		t.Fatalf("shared profile mutation projection = %+v", projection.Connection)
	}
	stored, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := stored.Config().Profile("profile-a")
	if len(profile.RootIDs()) != 1 || profile.RootIDs()[0] != fixture.rootID {
		t.Fatalf("shared profile scope changed: %v", profile.RootIDs())
	}
	fake.mu.Lock()
	stops := fake.stopCount
	fake.mu.Unlock()
	if stops != 0 {
		t.Fatalf("shared profile rejection stopped controller %d times", stops)
	}

	name := "Shared label"
	labelChange := callRPC(t, fixture.service, "rename", "workspace.update", map[string]any{
		"expected_revision": stored.Revision(), "root_id": fixture.rootID, "display_name": name,
	})
	if !labelChange.OK {
		t.Fatalf("shared profile label change failed: %+v", labelChange)
	}
	waitBridgeIdle(t, fixture.service)
	after := fixture.service.snapshot()
	if after.Connection.Code != "" || len(after.Workspace.Roots) != 1 || after.Workspace.Roots[0].DisplayName != name || !after.Workspace.Roots[0].Enabled {
		t.Fatalf("shared profile label result = %+v / %+v", after.Connection, after.Workspace.Roots)
	}
	fake.mu.Lock()
	stops = fake.stopCount
	fake.mu.Unlock()
	if stops != 0 {
		t.Fatalf("label-only update stopped controller %d times", stops)
	}
}

func TestPausingLastWorkspaceStopsOldScopeAndDoesNotReconnectEmptyScope(t *testing.T) {
	fixture := newBridgeFixture(t, false, nil)
	fake := &fakeBridgeController{status: readyFakeStatus()}
	fake.stopHook = func() error {
		loaded, err := fixture.store.Load()
		if err != nil {
			return err
		}
		profile, _ := loaded.Config().Profile("profile-a")
		if len(profile.RootIDs()) != 1 {
			return errors.New("scope changed before old controller stopped")
		}
		return nil
	}
	installFakeController(fixture, fake)
	current, err := fixture.service.workspaces.List(context.Background(), workspaceadmin.DefaultConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	paused := false
	response := callRPC(t, fixture.service, "pause", "workspace.update", map[string]any{
		"expected_revision": current.Revision, "root_id": fixture.rootID, "enabled": paused,
	})
	if !response.OK {
		t.Fatalf("pause request failed: %+v", response)
	}
	waitBridgeIdle(t, fixture.service)
	result := fixture.service.snapshot()
	if result.Connection.Code != "workspace_scope_empty" || result.Connection.Stage != string(previewconnect.StageIdle) || result.Connection.MCPReady || result.Connection.TunnelReady ||
		result.Connection.Saved == nil || !*result.Connection.Saved || result.Connection.Applied == nil || *result.Connection.Applied || result.Workspace.Roots[0].Enabled {
		t.Fatalf("pause result = %+v / %+v", result.Connection, result.Workspace.Roots)
	}
	fake.mu.Lock()
	stops, reconnects := fake.stopCount, fake.reconnectCount
	fake.mu.Unlock()
	if stops != 1 || reconnects != 0 {
		t.Fatalf("pause coordination stop=%d reconnect=%d", stops, reconnects)
	}
}

func TestStaleWorkspaceRevisionDoesNotStopController(t *testing.T) {
	fixture := newBridgeFixture(t, false, nil)
	fake := &fakeBridgeController{status: readyFakeStatus()}
	installFakeController(fixture, fake)
	response := callRPC(t, fixture.service, "stale", "workspace.remove", map[string]any{"expected_revision": "old-revision", "root_ids": []string{fixture.rootID}})
	if !response.OK {
		t.Fatalf("workspace operation should be asynchronous: %+v", response)
	}
	waitBridgeIdle(t, fixture.service)
	state := fixture.service.snapshot()
	if state.Connection.Code != string(workspaceadmin.CodeRevisionConflict) || state.Connection.Saved == nil || *state.Connection.Saved {
		t.Fatalf("stale revision result = %+v", state.Connection)
	}
	fake.mu.Lock()
	stops := fake.stopCount
	fake.mu.Unlock()
	if stops != 0 {
		t.Fatalf("stale revision stopped controller %d times", stops)
	}
}

func TestFailedPostSaveReconnectReportsSavedButNotApplied(t *testing.T) {
	fixture := newBridgeFixture(t, false, nil)
	addSecondWorkspaceRoot(t, fixture)
	fake := &fakeBridgeController{status: readyFakeStatus()}
	fake.reconnectHook = func(context.Context) (previewconnect.Status, error) {
		return previewconnect.Status{Stage: previewconnect.StageFailed, Code: previewconnect.CodeMCPNotReady, Message: "本机 MCP 未就绪。", Remedy: "检查后重试。"}, errors.New("private test error")
	}
	installFakeController(fixture, fake)
	current, err := fixture.service.workspaces.List(context.Background(), workspaceadmin.DefaultConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	paused := false
	response := callRPC(t, fixture.service, "pause", "workspace.update", map[string]any{"expected_revision": current.Revision, "root_id": fixture.rootID, "enabled": paused})
	if !response.OK {
		t.Fatalf("pause request failed: %+v", response)
	}
	waitBridgeIdle(t, fixture.service)
	state := fixture.service.snapshot().Connection
	if state.Code != "reconnect_not_ready" || state.Saved == nil || !*state.Saved || state.Applied == nil || *state.Applied || !strings.Contains(state.Message, "配置已保存") {
		t.Fatalf("post-save reconnect failure = %+v", state)
	}
}

func addSecondWorkspaceRoot(t *testing.T, fixture bridgeFixture) {
	t.Helper()
	extraPath := filepath.Join(filepath.Dir(fixture.rootPath), "workspace-extra")
	if err := os.Mkdir(extraPath, 0o700); err != nil {
		t.Fatal(err)
	}
	extra, err := config.NewRoot("root-b", extraPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := snapshot.Config()
	profile, ok := cfg.Profile("profile-a")
	if !ok {
		t.Fatal("fixture profile missing")
	}
	firstMetadata, err := config.NewWorkspaceRootMetadata(fixture.rootID, "Workspace A", true)
	if err != nil {
		t.Fatal(err)
	}
	secondMetadata, err := config.NewWorkspaceRootMetadata(extra.ID(), "Workspace B", true)
	if err != nil {
		t.Fatal(err)
	}
	updatedProfile, err := config.NewProfileWithWorkspaceRoots(profile.ID(), []string{fixture.rootID, extra.ID()}, profile.Tools(), profile.DenyPatterns(), profile.IgnorePatterns(), []config.WorkspaceRootMetadata{firstMetadata, secondMetadata})
	if err != nil {
		t.Fatal(err)
	}
	profiles := cfg.Profiles()
	profiles[0] = updatedProfile
	roots := append(cfg.Roots(), extra)
	updatedConfig, err := config.New(cfg.SchemaVersion(), roots, profiles, cfg.Connections(), cfg.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.SaveIfRevision(snapshot.Revision(), updatedConfig); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRetriesOwnedControllerAfterFirstStopFailure(t *testing.T) {
	fixture := newBridgeFixture(t, false, nil)
	fake := &fakeBridgeController{status: readyFakeStatus()}
	fake.stopHook = func() error { return errors.New("private stop failure") }
	installFakeController(fixture, fake)
	if err := fixture.service.Close(); !errors.Is(err, ErrCloseStopFailed) {
		t.Fatalf("first Close error = %v, want ErrCloseStopFailed", err)
	}
	if err := fixture.service.Close(); err != nil {
		t.Fatalf("second Close did not retry cleanup: %v", err)
	}
	fake.mu.Lock()
	stops := fake.stopCount
	fake.mu.Unlock()
	if stops != 2 {
		t.Fatalf("owned controller Stop calls = %d, want retry count 2", stops)
	}
}

func TestConcurrentCloseSerializesOwnedControllerCleanup(t *testing.T) {
	fixture := newBridgeFixture(t, false, nil)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fake := &fakeBridgeController{status: readyFakeStatus()}
	fake.stopHook = func() error {
		started <- struct{}{}
		<-release
		return nil
	}
	installFakeController(fixture, fake)
	results := make(chan error, 2)
	go func() { results <- fixture.service.Close() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first Close did not enter Stop")
	}
	go func() { results <- fixture.service.Close() }()
	select {
	case err := <-results:
		t.Fatalf("concurrent Close returned before first cleanup completed: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("Close %d failed: %v", i+1, err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not return")
		}
	}
	fake.mu.Lock()
	stops := fake.stopCount
	fake.mu.Unlock()
	if stops != 1 {
		t.Fatalf("concurrent Close called Stop %d times, want 1", stops)
	}
}
