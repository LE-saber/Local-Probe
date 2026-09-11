package commandexec

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/confirmation"
)

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
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{})
	if err != nil {
		t.Fatal(err)
	}
	req := executionRequest(t, manager, "r1")
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("want ErrNotAdmitted, got %v", err)
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
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{})
	if err != nil {
		t.Fatal(err)
	}
	req := executionRequest(t, manager, "r1")
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("want ErrProfileChanged, got %v", err)
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
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{})
	if err != nil {
		t.Fatal(err)
	}
	req := executionRequest(t, manager, "r1")
	req.CommandID = "other_command"
	if _, err := executor.Execute(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest, got %v", err)
	}
}
