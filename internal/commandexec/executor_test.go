package commandexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/confirmation"
)

type captureAudit struct {
	events []string
	fail   string
}

func (r *captureAudit) RecordAdmission(audit.CommandContext) error {
	r.events = append(r.events, "admission")
	if r.fail == "admission" {
		return audit.ErrStorage
	}
	return nil
}

func (r *captureAudit) RecordStart(audit.CommandContext) error {
	r.events = append(r.events, "start")
	if r.fail == "start" {
		return audit.ErrStorage
	}
	return nil
}

func (r *captureAudit) RecordReject(audit.CommandContext, string) error {
	r.events = append(r.events, "reject")
	if r.fail == "reject" {
		return audit.ErrStorage
	}
	return nil
}

func executionProfile(t *testing.T) commandprofile.Profile {
	t.Helper()
	profile, err := commandprofile.New(commandprofile.Spec{
		ID:         "codex_version",
		Kind:       commandprofile.KindVersionProbe,
		Platform:   []commandprofile.Platform{commandprofile.PlatformWindows},
		Executable: `C:\Program Files\Codex\codex.exe`,
		Identity:   commandprofile.IdentitySpec{RequireRegular: true, RejectReparse: true, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Variants:   []commandprofile.VariantSpec{{ID: "short", Exact: []string{"-v"}}},
		CWD:        commandprofile.CWDSpec{Kind: commandprofile.CWDPrivateEmpty},
		Environment: commandprofile.EnvironmentSpec{
			Inherit: false,
		},
		Limits:  commandprofile.LimitsSpec{WallTimeout: time.Second, StdoutBytes: 4096, StderrBytes: 4096, MaxProcesses: 1, MaxChildren: 0},
		Network: commandprofile.NetworkSpec{Mode: commandprofile.NetworkDeny, RequireEnforcement: true},
		Confirmation: commandprofile.ConfirmationSpec{
			Mode: commandprofile.ConfirmationPerCall, LocalOnly: true,
		},
		Result: commandprofile.ResultSpec{Type: commandprofile.ResultVersion},
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func executionRequest(t *testing.T, manager *confirmation.Manager, revision string) Request {
	t.Helper()
	confirmationRequest := confirmation.Request{
		ConnectionID: "connection-a", ProfileID: "codex_version", ProfileRevision: revision,
		CommandID: "codex_version", VariantID: "short", RequestNonce: "nonce-a",
	}
	token, err := manager.Mint(confirmationRequest, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return Request{CommandID: "codex_version", VariantID: "short", RequestNonce: "nonce-a", Confirmation: token}
}

func executionMode(t *testing.T) commandprofile.DeveloperMode {
	t.Helper()
	mode, err := commandprofile.NewDeveloperMode(true, []string{"connection-a"}, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
	if err != nil {
		t.Fatal(err)
	}
	return mode
}

func revisionLease(current string) func(string) (func(), bool) {
	return func(expected string) (func(), bool) {
		if expected != current {
			return nil, false
		}
		return func() {}, true
	}
}

func TestExecutorFailsClosedWithoutNetworkCapabilityBeforeConsumingConfirmation(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	profile := executionProfile(t)
	binding, err := NewBinding("connection-a", "r1", profile, revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureAudit{}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	req := executionRequest(t, manager, "r1")
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("want ErrNotAdmitted, got %v", err)
	}
	if len(recorder.events) != 1 || recorder.events[0] != "reject" {
		t.Fatalf("audit events = %v, want [reject]", recorder.events)
	}
	confirmReq := confirmation.Request{ConnectionID: "connection-a", ProfileID: "codex_version", ProfileRevision: "r1", CommandID: "codex_version", VariantID: "short", RequestNonce: "nonce-a"}
	if err := manager.Consume(req.Confirmation, confirmReq); err != nil {
		t.Fatalf("network gate consumed confirmation: %v", err)
	}
}

func TestExecutorRejectsStaleBindingBeforeConfirmation(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	profile := executionProfile(t)
	binding, err := NewBinding("connection-a", "r1", profile, revisionLease("r2"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureAudit{}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	req := executionRequest(t, manager, "r1")
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("want ErrProfileChanged, got %v", err)
	}
	if len(recorder.events) != 1 || recorder.events[0] != "reject" {
		t.Fatalf("audit events = %v, want [reject]", recorder.events)
	}
}

func TestExecutorRejectsModelSelectorsOutsideBinding(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	profile := executionProfile(t)
	binding, err := NewBinding("connection-a", "r1", profile, revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureAudit{}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	req := executionRequest(t, manager, "r1")
	req.CommandID = "other_command"
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
	if len(recorder.events) != 1 || recorder.events[0] != "reject" {
		t.Fatalf("audit events = %v, want [reject]", recorder.events)
	}
}

func TestExecutorRequiresAuditRecorder(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding("connection-a", "r1", executionProfile(t), revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, nil); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("New without audit recorder = %v, want %v", err, ErrInvalidBinding)
	}
}

func TestExecutorAuditFailureStopsBeforeConfirmationAndRun(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	profile := executionProfile(t)
	binding, err := NewBinding("connection-a", "r1", profile, revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureAudit{fail: "admission"}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	executor.admit = func(commandprofile.Profile, commandprofile.DeveloperMode, string, commandprofile.EnforcementCapability) error {
		return nil
	}
	runs := 0
	executor.run = func(context.Context, commandprofile.Profile, commandprofile.Variant) (Result, error) {
		runs++
		return Result{}, nil
	}
	req := executionRequest(t, manager, "r1")
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrAuditFailed) {
		t.Fatalf("Execute audit failure = %v, want %v", err, ErrAuditFailed)
	}
	if runs != 0 {
		t.Fatalf("runner calls = %d, want 0", runs)
	}
	confirmReq := confirmation.Request{ConnectionID: "connection-a", ProfileID: "codex_version", ProfileRevision: "r1", CommandID: "codex_version", VariantID: "short", RequestNonce: "nonce-a"}
	if err := manager.Consume(req.Confirmation, confirmReq); err != nil {
		t.Fatalf("admission audit failure consumed confirmation: %v", err)
	}
}

func TestExecutorRecordsAdmissionAndStartBeforeRun(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	profile := executionProfile(t)
	binding, err := NewBinding("connection-a", "r1", profile, revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureAudit{}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	executor.admit = func(commandprofile.Profile, commandprofile.DeveloperMode, string, commandprofile.EnforcementCapability) error {
		return nil
	}
	executor.run = func(context.Context, commandprofile.Profile, commandprofile.Variant) (Result, error) {
		if len(recorder.events) != 2 || recorder.events[0] != "admission" || recorder.events[1] != "start" {
			t.Fatalf("events before run = %v, want [admission start]", recorder.events)
		}
		return Result{CommandID: "codex_version", VariantID: "short", Version: "1.2.3"}, nil
	}
	result, err := executor.Execute(context.Background(), executionRequest(t, manager, "r1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "1.2.3" {
		t.Fatalf("version = %q, want 1.2.3", result.Version)
	}
}

func TestExecutorStartAuditFailureStopsRun(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding("connection-a", "r1", executionProfile(t), revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureAudit{fail: "start"}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	executor.admit = func(commandprofile.Profile, commandprofile.DeveloperMode, string, commandprofile.EnforcementCapability) error {
		return nil
	}
	runs := 0
	executor.run = func(context.Context, commandprofile.Profile, commandprofile.Variant) (Result, error) {
		runs++
		return Result{}, nil
	}
	if _, err := executor.Execute(context.Background(), executionRequest(t, manager, "r1")); !errors.Is(err, ErrAuditFailed) {
		t.Fatalf("Execute start audit failure = %v, want %v", err, ErrAuditFailed)
	}
	if runs != 0 {
		t.Fatalf("runner calls = %d, want 0", runs)
	}
}

func TestExecutorRejectAuditFailureIsFailClosed(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding("connection-a", "r1", executionProfile(t), revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, &captureAudit{fail: "reject"})
	if err != nil {
		t.Fatal(err)
	}
	req := executionRequest(t, manager, "r1")
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrAuditFailed) {
		t.Fatalf("Execute reject audit failure = %v, want %v", err, ErrAuditFailed)
	}
	confirmReq := confirmation.Request{ConnectionID: "connection-a", ProfileID: "codex_version", ProfileRevision: "r1", CommandID: "codex_version", VariantID: "short", RequestNonce: "nonce-a"}
	if err := manager.Consume(req.Confirmation, confirmReq); err != nil {
		t.Fatalf("reject audit failure consumed confirmation: %v", err)
	}
}

func TestExecutorRejectsFixedCommandBeforeConfirmationAndRun(t *testing.T) {
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := commandprofile.New(commandprofile.Spec{
		ID: "fixed_command", Kind: commandprofile.KindFixedCommand, Platform: []commandprofile.Platform{commandprofile.PlatformWindows},
		Executable:   `C:\Tools\tool.exe`,
		Identity:     commandprofile.IdentitySpec{RequireRegular: true, RejectReparse: true, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		Variants:     []commandprofile.VariantSpec{{ID: "run", Exact: []string{"--status", "ready"}}},
		CWD:          commandprofile.CWDSpec{Kind: commandprofile.CWDPrivateEmpty},
		Environment:  commandprofile.EnvironmentSpec{Inherit: false},
		Limits:       commandprofile.LimitsSpec{WallTimeout: time.Second, StdoutBytes: 4096, StderrBytes: 4096, MaxProcesses: 1, MaxChildren: 0},
		Network:      commandprofile.NetworkSpec{Mode: commandprofile.NetworkDeny, RequireEnforcement: true},
		Confirmation: commandprofile.ConfirmationSpec{Mode: commandprofile.ConfirmationPerCall, LocalOnly: true},
		Result:       commandprofile.ResultSpec{Type: commandprofile.ResultExitStatus},
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding("connection-a", "r1", profile, revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureAudit{}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	executor.run = func(context.Context, commandprofile.Profile, commandprofile.Variant) (Result, error) {
		runs++
		return Result{}, nil
	}
	confirmationRequest := confirmation.Request{
		ConnectionID: "connection-a", ProfileID: "fixed_command", ProfileRevision: "r1",
		CommandID: "fixed_command", VariantID: "run", RequestNonce: "nonce-fixed",
	}
	token, err := manager.Mint(confirmationRequest, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{CommandID: "fixed_command", VariantID: "run", RequestNonce: "nonce-fixed", Confirmation: token}
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrNotAdmitted) || !strings.Contains(err.Error(), "unsupported_profile") {
		t.Fatalf("fixed command execution error = %v, want stable unsupported_profile", err)
	}
	if runs != 0 || len(recorder.events) != 1 || recorder.events[0] != "reject" {
		t.Fatalf("fixed command side effects: runs=%d events=%v", runs, recorder.events)
	}
	if err := manager.Consume(token, confirmationRequest); err != nil {
		t.Fatalf("fixed command rejection consumed confirmation: %v", err)
	}
}
