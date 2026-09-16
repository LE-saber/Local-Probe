package desktopadmin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/supervisor"
)

type testConfigOptions struct {
	label    string
	rules    bool
	disabled bool
}

func newTestStore(t *testing.T, options testConfigOptions) (*config.Store, string) {
	t.Helper()
	root, err := config.NewRoot("root", `C:\workspace`, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("profile", []string{"root"}, []string{"list_directory", "find_files"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection := config.NewConnection("connection", options.label, "profile", "credential", !options.disabled)
	credential := config.NewCredentialRef("credential", "dpapi")
	var cfg config.Config
	if options.rules {
		mode, err := commandprofile.NewDeveloperMode(true, []string{"connection"}, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
		if err != nil {
			t.Fatal(err)
		}
		command, err := commandprofile.New(commandprofile.Spec{
			ID:         "codex_version",
			Kind:       commandprofile.KindVersionProbe,
			Platform:   []commandprofile.Platform{commandprofile.PlatformWindows},
			Executable: `C:\Program Files\Codex\codex.exe`,
			Identity: commandprofile.IdentitySpec{
				RequireRegular: true,
				RejectReparse:  true,
				SHA256:         strings.Repeat("0", 64),
			},
			Variants:     []commandprofile.VariantSpec{{ID: "version", Exact: []string{"-v"}}},
			CWD:          commandprofile.CWDSpec{Kind: commandprofile.CWDPrivateEmpty},
			Limits:       commandprofile.LimitsSpec{WallTimeout: time.Second, StdoutBytes: 1024, StderrBytes: 1024, MaxProcesses: 1},
			Network:      commandprofile.NetworkSpec{Mode: commandprofile.NetworkDeny, RequireEnforcement: true},
			Confirmation: commandprofile.ConfirmationSpec{Mode: commandprofile.ConfirmationPerCall, LocalOnly: true},
			Result:       commandprofile.ResultSpec{Type: commandprofile.ResultVersion},
		})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err = config.NewWithCommandProfiles(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credential}, nil, mode, []commandprofile.Profile{command})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		cfg, err = config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credential})
		if err != nil {
			t.Fatal(err)
		}
	}
	store, err := config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return store, store.Snapshot().Revision()
}

type staticStatusSource struct {
	snapshots []supervisor.Snapshot
	panic     bool
}

func (s staticStatusSource) Snapshots() []supervisor.Snapshot {
	if s.panic {
		panic("status source must never escape")
	}
	return append([]supervisor.Snapshot(nil), s.snapshots...)
}

type actionSpy struct{ calls atomic.Int32 }

func (s *actionSpy) Start(context.Context, string) error {
	s.calls.Add(1)
	return nil
}

func (s *actionSpy) Stop(context.Context, string) error {
	s.calls.Add(1)
	return nil
}

func (s *actionSpy) Reconnect(context.Context, string) error {
	s.calls.Add(1)
	return nil
}

type diagnosticSource struct {
	records []DiagnosticRecord
	err     error
	panic   bool
}

func (s diagnosticSource) ReadDiagnostics(context.Context, int) ([]DiagnosticRecord, error) {
	if s.panic {
		panic("diagnostic source must never escape")
	}
	if s.err != nil {
		return nil, s.err
	}
	return append([]DiagnosticRecord(nil), s.records...), nil
}

func readySnapshot(revision string) supervisor.Snapshot {
	return supervisor.Snapshot{
		ID:        "connection",
		Revision:  revision,
		Transport: config.TransportLocal,
		LocalPort: 8788,
		State:     supervisor.StateReady,
		LastError: supervisor.ErrorNone,
	}
}

func TestNewRequiresConfigAndCapabilitiesStayFailClosed(t *testing.T) {
	if _, err := New(Dependencies{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New without config error = %v", err)
	}
	store, _ := newTestStore(t, testConfigOptions{})
	client, err := New(Dependencies{Config: store})
	if err != nil {
		t.Fatal(err)
	}
	caps := client.Capabilities()
	if caps.ProductionReady || !caps.Overview.Available || caps.ConnectionStatus.Available || caps.Diagnostics.Available || caps.ManagementActions.Available || !caps.DeveloperRules.Available {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
	if caps.ManagementActions.Reason != CapabilityProductionGate {
		t.Fatalf("management action reason = %q", caps.ManagementActions.Reason)
	}
	wire, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainAny(wire, "secret", "token", "key", "path") {
		t.Fatalf("capability wire contains sensitive label: %s", wire)
	}
}

func TestOverviewAndStatusesNeverExposeRootOrCredentialData(t *testing.T) {
	store, revision := newTestStore(t, testConfigOptions{label: `C:\private\token.txt`})
	status := staticStatusSource{snapshots: []supervisor.Snapshot{readySnapshot(revision)}}
	client, err := New(Dependencies{Config: store, Status: status})
	if err != nil {
		t.Fatal(err)
	}
	overview, err := client.GetOverview()
	if err != nil {
		t.Fatal(err)
	}
	if !overview.StatusKnown || overview.ReadyConnections != 1 || overview.ConnectionCount != 1 {
		t.Fatalf("unexpected overview: %+v", overview)
	}
	statuses, err := client.GetConnectionStatuses()
	if err != nil {
		t.Fatal(err)
	}
	if !statuses.StatusKnown || len(statuses.Entries) != 1 || statuses.Entries[0].State != StateReady || !statuses.Entries[0].LabelOmitted {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}
	overviewWire, err := json.Marshal(overview)
	if err != nil {
		t.Fatal(err)
	}
	statusWire, err := json.Marshal(statuses)
	if err != nil {
		t.Fatal(err)
	}
	for _, wire := range [][]byte{overviewWire, statusWire} {
		if bytesContainAny(wire, `C:\private\token.txt`, "credential", "dpapi", "secret") {
			t.Fatalf("wire contains local-only data: %s", wire)
		}
	}
}

func TestStatusProjectionFailsClosedOnRevisionOrSourceFailure(t *testing.T) {
	store, revision := newTestStore(t, testConfigOptions{label: "Desktop"})
	stale := readySnapshot("r999")
	client, err := New(Dependencies{Config: store, Status: staticStatusSource{snapshots: []supervisor.Snapshot{stale}}})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := client.GetConnectionStatuses()
	if err != nil {
		t.Fatal(err)
	}
	if statuses.StatusKnown || statuses.Entries[0].StatusKnown || statuses.Entries[0].State != StateUnavailable {
		t.Fatalf("stale status was not hidden: %+v", statuses)
	}
	overview, err := client.GetOverview()
	if err != nil || overview.StatusKnown || overview.ReadyConnections != 0 || overview.Capabilities.ConnectionStatus.Available != true {
		t.Fatalf("stale overview result = %+v, err=%v", overview, err)
	}
	panicClient, err := New(Dependencies{Config: store, Status: staticStatusSource{panic: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := panicClient.GetConnectionStatuses(); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("panic status source error = %v", err)
	}
	_ = revision
}

func TestRevisionValidationAcceptsStoreAndFileStoreFormats(t *testing.T) {
	for _, value := range []string{"r1", "r0", "r18446744073709551615", "sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("aA09", 16)} {
		if !validRevision(value) {
			t.Errorf("validRevision(%q) = false", value)
		}
	}
	for _, value := range []string{
		"",
		"r",
		"r1x",
		"r" + strings.Repeat("1", 21),
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 65),
		"SHA256:" + strings.Repeat("a", 64),
		"sha256:" + strings.Repeat("g", 64),
		"sha256:" + strings.Repeat("a", 63) + " ",
	} {
		if validRevision(value) {
			t.Errorf("validRevision(%q) = true", value)
		}
	}
}

func TestStatusProjectionRejectsMaliciousReadyCombinations(t *testing.T) {
	cases := []struct {
		name     string
		disabled bool
		mutate   func(*supervisor.Snapshot)
	}{
		{
			name:     "disabled ready",
			disabled: true,
		},
		{
			name:   "ready with error",
			mutate: func(snapshot *supervisor.Snapshot) { snapshot.LastError = supervisor.ErrorHealth },
		},
		{
			name:   "ready with retry deadline",
			mutate: func(snapshot *supervisor.Snapshot) { snapshot.NextRetryAt = time.Unix(200, 0) },
		},
		{
			name:   "ready with attempt",
			mutate: func(snapshot *supervisor.Snapshot) { snapshot.Attempt = 1 },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store, revision := newTestStore(t, testConfigOptions{label: "Status", disabled: testCase.disabled})
			snapshot := readySnapshot(revision)
			if testCase.mutate != nil {
				testCase.mutate(&snapshot)
			}
			client, err := New(Dependencies{Config: store, Status: staticStatusSource{snapshots: []supervisor.Snapshot{snapshot}}})
			if err != nil {
				t.Fatal(err)
			}
			statuses, err := client.GetConnectionStatuses()
			if err != nil {
				t.Fatal(err)
			}
			if statuses.StatusKnown || len(statuses.Entries) != 1 || statuses.Entries[0].State != StateUnavailable || statuses.Entries[0].StatusKnown {
				t.Fatalf("malicious status was projected: %+v", statuses)
			}
			overview, err := client.GetOverview()
			if err != nil || overview.StatusKnown || overview.ReadyConnections != 0 {
				t.Fatalf("malicious overview = %+v, err=%v", overview, err)
			}
		})
	}
}

func TestStatusSourceBoundIsFailClosed(t *testing.T) {
	store, _ := newTestStore(t, testConfigOptions{label: "Bound"})
	snapshots := make([]supervisor.Snapshot, MaxItems+1)
	client, err := New(Dependencies{Config: store, Status: staticStatusSource{snapshots: snapshots}})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := client.GetConnectionStatuses()
	if !errors.Is(err, ErrCapabilityUnavailable) || len(statuses.Entries) != 1 || statuses.Entries[0].State != StateUnavailable {
		t.Fatalf("oversized status source result=%+v err=%v", statuses, err)
	}
	overview, err := client.GetOverview()
	if !errors.Is(err, ErrCapabilityUnavailable) || overview.StatusKnown || overview.ReadyConnections != 0 {
		t.Fatalf("oversized overview=%+v err=%v", overview, err)
	}
}

func TestActionRequestStrictAndDispatchRemainsUnavailable(t *testing.T) {
	var request ActionRequest
	if err := json.Unmarshal([]byte(`{"action":"reconnect","connection_id":"connection"}`), &request); err != nil {
		t.Fatal(err)
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		`{"action":"start","connection_id":"connection","extra":true}`,
		`{"action":"start","action":"stop","connection_id":"connection"}`,
		`{"action":"start","connection_id":"connection"} {}`,
		`{"action":"start","connection_id":"C:\\private"}`,
	} {
		var invalid ActionRequest
		if err := json.Unmarshal([]byte(data), &invalid); err == nil {
			t.Errorf("request %s error = %v", data, err)
		}
	}
	store, _ := newTestStore(t, testConfigOptions{})
	spy := &actionSpy{}
	client, err := New(Dependencies{Config: store, Actions: spy})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.DispatchAction(context.Background(), request)
	if !errors.Is(err, ErrActionUnavailable) || result.Accepted || result.ErrorCode != ActionErrorCapabilityUnavailable || spy.calls.Load() != 0 {
		t.Fatalf("dispatch result=%+v err=%v calls=%d", result, err, spy.calls.Load())
	}
	wire, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainAny(wire, "command", "argv", "credential", "secret") {
		t.Fatalf("action wire contains forbidden data: %s", wire)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DispatchAction(context.Background(), request); !errors.Is(err, ErrClosed) {
		t.Fatalf("dispatch after client exit error = %v", err)
	}
	if spy.calls.Load() != 0 {
		t.Fatal("client exit or dispatch invoked lifecycle action")
	}
}

func TestDeveloperRulePreviewIsReadOnlyAndPathFree(t *testing.T) {
	store, _ := newTestStore(t, testConfigOptions{rules: true})
	client, err := New(Dependencies{Config: store})
	if err != nil {
		t.Fatal(err)
	}
	rules, err := client.GetDeveloperRules()
	if err != nil {
		t.Fatal(err)
	}
	if !rules.Enabled || rules.ProductionReady || len(rules.Rules) != 1 || rules.Rules[0].ID != "codex_version" {
		t.Fatalf("unexpected rule preview: %+v", rules)
	}
	wire, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainAny(wire, `C:\Program Files\Codex\codex.exe`, "-v", "sha256", "environment") {
		t.Fatalf("rule preview contains executable details: %s", wire)
	}
}

func TestDiagnosticsCopyIsBoundedAndRejectsSensitiveOrRawRecords(t *testing.T) {
	first, err := NewDiagnosticRecord(time.Unix(100, 0), DiagnosticMCP, DiagnosticInfo, DiagnosticSucceeded, "", "connection", "profile", "r1", 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewDiagnosticRecord(time.Unix(101, 0), DiagnosticError, DiagnosticErrorLvl, DiagnosticFailed, "unavailable", "connection", "profile", "r1", 3)
	if err != nil {
		t.Fatal(err)
	}
	copyResult, err := CopyDiagnostics([]DiagnosticRecord{first, second}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !copyResult.Truncated || len(copyResult.Entries) != 1 {
		t.Fatalf("unexpected diagnostic copy: %+v", copyResult)
	}
	wire, err := json.Marshal(copyResult)
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainAny(wire, "message", "path", "command", "output", "credential") || len(wire) > MaxWireBytes {
		t.Fatalf("diagnostic wire violates boundary: %s", wire)
	}
	if _, err := json.Marshal(first); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("local diagnostic record marshal error = %v", err)
	}
	if _, err := NewDiagnosticRecord(time.Now(), DiagnosticMCP, DiagnosticInfo, DiagnosticSucceeded, "api-key", "", "", "", 0); !errors.Is(err, ErrInvalidDiagnostics) {
		t.Fatalf("sensitive diagnostic code error = %v", err)
	}
	if _, err := CopyDiagnostics([]DiagnosticRecord{{}}, 1); !errors.Is(err, ErrInvalidDiagnostics) {
		t.Fatalf("invalid diagnostic source error = %v", err)
	}
	client, err := New(Dependencies{Config: funcConfigSource(t), Diagnostics: diagnosticSource{records: []DiagnosticRecord{first, second}}})
	if err != nil {
		t.Fatal(err)
	}
	fromClient, err := client.GetDiagnostics(context.Background())
	if err != nil || len(fromClient.Entries) != 2 {
		t.Fatalf("client diagnostics=%+v err=%v", fromClient, err)
	}
	failedClient, err := New(Dependencies{Config: funcConfigSource(t), Diagnostics: diagnosticSource{records: []DiagnosticRecord{{}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failedClient.GetDiagnostics(context.Background()); !errors.Is(err, ErrInvalidDiagnostics) {
		t.Fatalf("invalid diagnostics source error = %v", err)
	}
}

func TestClientConcurrentProjectionAndExit(t *testing.T) {
	store, revision := newTestStore(t, testConfigOptions{label: "Concurrent"})
	client, err := New(Dependencies{Config: store, Status: staticStatusSource{snapshots: []supervisor.Snapshot{readySnapshot(revision)}}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = client.Capabilities()
				_, _ = client.GetOverview()
				_, _ = client.GetConnectionStatuses()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = client.Close()
		}
	}()
	wg.Wait()
}

type configSourceFunc struct{ store *config.Store }

func (s configSourceFunc) Snapshot() config.Snapshot { return s.store.Snapshot() }

func funcConfigSource(t *testing.T) ConfigSource {
	t.Helper()
	store, _ := newTestStore(t, testConfigOptions{})
	return configSourceFunc{store: store}
}

func bytesContainAny(data []byte, values ...string) bool {
	text := string(data)
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}
