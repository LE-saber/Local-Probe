package commandexec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/confirmation"
)

func fixedTypedProfile(t *testing.T) commandprofile.Profile {
	t.Helper()
	profile, err := commandprofile.New(commandprofile.Spec{
		ID: "fixed_status", Kind: commandprofile.KindFixedCommand,
		Platform:   []commandprofile.Platform{commandprofile.PlatformWindows},
		Executable: `C:\Tools\status.exe`,
		Identity: commandprofile.IdentitySpec{
			RequireRegular: true, RejectReparse: true,
			SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		Slots: []commandprofile.SlotSpec{
			{ID: "mode", Kind: commandprofile.SlotEnum, EnumValues: []string{"brief", "full"}},
			{ID: "limit", Kind: commandprofile.SlotBoundedInteger, Min: 1, Max: 32},
			{ID: "path", Kind: commandprofile.SlotRootRelativePath, RootIDs: []string{"workspace"}},
		},
		Variants: []commandprofile.VariantSpec{{
			ID: "status",
			Template: []commandprofile.TemplateItemSpec{
				{Literal: "--mode"}, {SlotID: "mode"},
				{Literal: "--limit"}, {SlotID: "limit"},
				{Literal: "--path"}, {SlotID: "path"}, {SlotID: "path"},
			},
		}},
		CWD:         commandprofile.CWDSpec{Kind: commandprofile.CWDPrivateEmpty},
		Environment: commandprofile.EnvironmentSpec{Inherit: false},
		Limits: commandprofile.LimitsSpec{
			WallTimeout: time.Second, StdoutBytes: 4096, StderrBytes: 4096,
			MaxProcesses: 1, MaxChildren: 0,
		},
		Network:      commandprofile.NetworkSpec{Mode: commandprofile.NetworkDeny, RequireEnforcement: true},
		Confirmation: commandprofile.ConfirmationSpec{Mode: commandprofile.ConfirmationPerCall, LocalOnly: true},
		Result:       commandprofile.ResultSpec{Type: commandprofile.ResultExitStatus},
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func fixedTypedValues() map[string]commandprofile.SlotValue {
	return map[string]commandprofile.SlotValue{
		"mode":  {Text: "brief"},
		"limit": {Integer: 8},
		"path":  {RootID: "workspace", RelativePath: "src/main.go"},
	}
}

func fixedPathResolver(calls *int) commandprofile.PathResolver {
	return func(rootID, relative string) (string, error) {
		(*calls)++
		if rootID != "workspace" {
			return "", errors.New("unexpected root")
		}
		return `C:\Workspace\` + strings.ReplaceAll(relative, "/", `\`), nil
	}
}

func newFixedTypedExecutor(t *testing.T, profile commandprofile.Profile, revisionLease func(string) (func(), bool)) (*Executor, *confirmation.Manager, *captureAudit) {
	t.Helper()
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding("connection-a", "r1", profile, revisionLease)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &captureAudit{}
	executor, err := New(binding, executionMode(t), manager, commandprofile.EnforcementCapability{}, recorder)
	if err != nil {
		t.Fatal(err)
	}
	return executor, manager, recorder
}

func prepareFixedTyped(t *testing.T, executor *Executor) PreparedInput {
	t.Helper()
	calls := 0
	prepared, err := executor.Prepare(context.Background(), PrepareRequest{
		CommandID: "fixed_status", VariantID: "status",
		Values: fixedTypedValues(), Resolver: fixedPathResolver(&calls),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("path resolver calls = %d, want one per slot", calls)
	}
	return prepared
}

func TestPrepareFixedTypedInputIsImmutableAndLocalOnly(t *testing.T) {
	profile := fixedTypedProfile(t)
	executor, _, _ := newFixedTypedExecutor(t, profile, revisionLease("r1"))
	values := fixedTypedValues()
	calls := 0
	prepared, err := executor.Prepare(context.Background(), PrepareRequest{
		CommandID: "fixed_status", VariantID: "status", Values: values,
		Resolver: fixedPathResolver(&calls),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Valid() || prepared.ProfileRevision() != "r1" || prepared.CommandID() != "fixed_status" || prepared.VariantID() != "status" || prepared.Digest() == "" {
		t.Fatalf("prepared metadata invalid: %#v", prepared)
	}
	want := []string{"--mode", "brief", "--limit", "8", "--path", `C:\Workspace\src\main.go`, `C:\Workspace\src\main.go`}
	got := prepared.Argv()
	if len(got) != len(want) {
		t.Fatalf("argv length = %d, want %d (%v)", len(got), len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	got[1] = "tampered"
	values["mode"] = commandprofile.SlotValue{Text: "full"}
	values["path"] = commandprofile.SlotValue{RootID: "workspace", RelativePath: "other.go"}
	again := prepared.Argv()
	if again[1] != "brief" || again[5] != `C:\Workspace\src\main.go` || again[6] != `C:\Workspace\src\main.go` {
		t.Fatalf("prepared argv changed after caller mutation: %v", again)
	}
	if calls != 1 {
		t.Fatalf("resolver calls after prepared mutation = %d, want one", calls)
	}
	if _, err := json.Marshal(prepared); err == nil {
		t.Fatal("prepared input unexpectedly became JSON-serializable")
	}
	var forged PreparedInput
	if err := json.Unmarshal([]byte(`{}`), &forged); err == nil {
		t.Fatal("JSON forged a prepared input")
	}
}

func TestPrepareFixedTypedInputRejectsInvalidValuesBeforeReturning(t *testing.T) {
	profile := fixedTypedProfile(t)
	tests := []struct {
		name     string
		values   map[string]commandprofile.SlotValue
		resolver commandprofile.PathResolver
	}{
		{name: "missing_slot", values: map[string]commandprofile.SlotValue{"mode": {Text: "brief"}, "limit": {Integer: 8}}, resolver: fixedPathResolver(new(int))},
		{name: "extra_slot", values: map[string]commandprofile.SlotValue{"mode": {Text: "brief"}, "limit": {Integer: 8}, "path": {RootID: "workspace", RelativePath: "src/main.go"}, "extra": {Text: "x"}}, resolver: fixedPathResolver(new(int))},
		{name: "wrong_enum", values: map[string]commandprofile.SlotValue{"mode": {Text: "all"}, "limit": {Integer: 8}, "path": {RootID: "workspace", RelativePath: "src/main.go"}}, resolver: fixedPathResolver(new(int))},
		{name: "integer_out_of_range", values: map[string]commandprofile.SlotValue{"mode": {Text: "brief"}, "limit": {Integer: 33}, "path": {RootID: "workspace", RelativePath: "src/main.go"}}, resolver: fixedPathResolver(new(int))},
		{name: "resolver_required", values: fixedTypedValues()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor, _, _ := newFixedTypedExecutor(t, profile, revisionLease("r1"))
			prepared, err := executor.Prepare(context.Background(), PrepareRequest{CommandID: "fixed_status", VariantID: "status", Values: test.values, Resolver: test.resolver})
			if err == nil || prepared.Valid() {
				t.Fatalf("Prepare = %#v, %v; want invalid prepared and error", prepared, err)
			}
		})
	}
}

func TestPrepareRequiresTypedFixedProfileAndDeveloperMode(t *testing.T) {
	versionProfile := executionProfile(t)
	versionExecutor, _, _ := newFixedTypedExecutor(t, versionProfile, revisionLease("r1"))
	if _, err := versionExecutor.Prepare(context.Background(), PrepareRequest{CommandID: versionProfile.ID(), VariantID: "short"}); !errors.Is(err, ErrPrepareNotSupported) {
		t.Fatalf("version prepare error = %v, want ErrPrepareNotSupported", err)
	}

	profile := fixedTypedProfile(t)
	mode, err := commandprofile.NewDeveloperMode(false, []string{"connection-a"}, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := confirmation.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding("connection-a", "r1", profile, revisionLease("r1"))
	if err != nil {
		t.Fatal(err)
	}
	executor, err := New(binding, mode, manager, commandprofile.EnforcementCapability{}, &captureAudit{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Prepare(context.Background(), PrepareRequest{CommandID: "fixed_status", VariantID: "status", Values: fixedTypedValues(), Resolver: fixedPathResolver(new(int))}); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("disabled-mode prepare error = %v, want ErrNotAdmitted", err)
	}
}

func TestConfirmBindsPreparedDigestAndRevisionWithoutConsumingToken(t *testing.T) {
	profile := fixedTypedProfile(t)
	executor, manager, _ := newFixedTypedExecutor(t, profile, revisionLease("r1"))
	prepared := prepareFixedTyped(t, executor)
	capability, err := executor.Confirm(prepared, "nonce-fixed", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request, err := executor.BuildRequest(prepared, "nonce-fixed", capability)
	if err != nil {
		t.Fatal(err)
	}
	if request.Prepared.Digest() != prepared.Digest() || request.Prepared.ProfileRevision() != "r1" {
		t.Fatalf("request lost prepared binding: %#v", request)
	}
	base := confirmation.Request{
		ConnectionID: "connection-a", ProfileID: "fixed_status", ProfileRevision: "r1",
		CommandID: "fixed_status", VariantID: "status", RequestNonce: "nonce-fixed",
		ResolvedInputDigest: prepared.Digest(),
	}
	wrongDigest := base
	wrongDigest.ResolvedInputDigest = strings.Repeat("c", 64)
	if err := manager.Consume(capability, wrongDigest); !errors.Is(err, confirmation.ErrRequestMismatch) {
		t.Fatalf("wrong digest consume = %v, want ErrRequestMismatch", err)
	}
	wrongRevision := base
	wrongRevision.ProfileRevision = "r2"
	if err := manager.Consume(capability, wrongRevision); !errors.Is(err, confirmation.ErrRequestMismatch) {
		t.Fatalf("wrong revision consume = %v, want ErrRequestMismatch", err)
	}
	if err := manager.Consume(capability, base); err != nil {
		t.Fatalf("correct consume after rejected bindings = %v", err)
	}
	if request.Confirmation.String() == "" {
		t.Fatal("BuildRequest dropped confirmation capability")
	}
}

func TestConfirmRejectsRevisionChangeBeforeMinting(t *testing.T) {
	profile := fixedTypedProfile(t)
	current := "r1"
	executor, manager, _ := newFixedTypedExecutor(t, profile, func(expected string) (func(), bool) {
		if expected != current {
			return nil, false
		}
		return func() {}, true
	})
	prepared := prepareFixedTyped(t, executor)
	current = "r2"
	if _, err := executor.Confirm(prepared, "nonce-revision", time.Minute); !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("confirm after revision change = %v, want ErrProfileChanged", err)
	}
	// Confirm failed before minting, so there is no capability that can be
	// consumed. The manager remains usable for a fresh request after the
	// binding is restored.
	current = "r1"
	capability, err := executor.Confirm(prepared, "nonce-revision-fresh", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request := confirmation.Request{
		ConnectionID: "connection-a", ProfileID: "fixed_status", ProfileRevision: "r1",
		CommandID: "fixed_status", VariantID: "status", RequestNonce: "nonce-revision-fresh",
		ResolvedInputDigest: prepared.Digest(),
	}
	if err := manager.Consume(capability, request); err != nil {
		t.Fatalf("fresh confirmation after failed revision check = %v", err)
	}
}

func TestPrepareHoldsRevisionLeaseAcrossResolver(t *testing.T) {
	profile := fixedTypedProfile(t)
	held := false
	executor, _, _ := newFixedTypedExecutor(t, profile, func(expected string) (func(), bool) {
		if expected != "r1" {
			return nil, false
		}
		held = true
		return func() { held = false }, true
	})
	prepared, err := executor.Prepare(context.Background(), PrepareRequest{
		CommandID: "fixed_status", VariantID: "status", Values: fixedTypedValues(),
		Resolver: func(string, string) (string, error) {
			if !held {
				t.Fatal("revision lease was released before resolver")
			}
			return `C:\Workspace\src\main.go`, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Valid() || held {
		t.Fatalf("prepared=%v lease held after Prepare=%v", prepared.Valid(), held)
	}
}

func TestFixedExecuteRemainsFailClosedAndDoesNotConsumePreparedConfirmation(t *testing.T) {
	profile := fixedTypedProfile(t)
	executor, manager, recorder := newFixedTypedExecutor(t, profile, revisionLease("r1"))
	prepared := prepareFixedTyped(t, executor)
	request, err := executor.ConfirmRequest(prepared, "nonce-execute", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrNotAdmitted) || !strings.Contains(err.Error(), "unsupported_profile") {
		t.Fatalf("fixed execute error = %v, want stable unsupported_profile", err)
	}
	if len(recorder.events) != 1 || recorder.events[0] != "reject" {
		t.Fatalf("fixed execute audit events = %v, want [reject]", recorder.events)
	}
	confirmationRequest := confirmation.Request{
		ConnectionID: "connection-a", ProfileID: "fixed_status", ProfileRevision: "r1",
		CommandID: "fixed_status", VariantID: "status", RequestNonce: "nonce-execute",
		ResolvedInputDigest: prepared.Digest(),
	}
	if err := manager.Consume(request.Confirmation, confirmationRequest); err != nil {
		t.Fatalf("fail-closed fixed execute consumed confirmation: %v", err)
	}
}

func TestFixedExecuteRejectsForeignPreparedInputBeforeConfirmation(t *testing.T) {
	profile := fixedTypedProfile(t)
	executorA, managerA, _ := newFixedTypedExecutor(t, profile, revisionLease("r1"))
	executorB, _, recorderB := newFixedTypedExecutor(t, profile, revisionLease("r1"))
	prepared := prepareFixedTyped(t, executorA)
	request, err := executorA.ConfirmRequest(prepared, "nonce-foreign", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executorB.Execute(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("foreign prepared execute error = %v, want ErrInvalidRequest", err)
	}
	if len(recorderB.events) != 1 || recorderB.events[0] != "reject" {
		t.Fatalf("foreign prepared audit events = %v, want [reject]", recorderB.events)
	}
	confirmationRequest := confirmation.Request{
		ConnectionID: "connection-a", ProfileID: "fixed_status", ProfileRevision: "r1",
		CommandID: "fixed_status", VariantID: "status", RequestNonce: "nonce-foreign",
		ResolvedInputDigest: prepared.Digest(),
	}
	if err := managerA.Consume(request.Confirmation, confirmationRequest); err != nil {
		t.Fatalf("foreign prepared rejection consumed source confirmation: %v", err)
	}
}

func TestFixedExecuteRejectsRequestSelectorMutationBeforeConfirmation(t *testing.T) {
	profile := fixedTypedProfile(t)
	executor, manager, recorder := newFixedTypedExecutor(t, profile, revisionLease("r1"))
	prepared := prepareFixedTyped(t, executor)
	request, err := executor.ConfirmRequest(prepared, "nonce-selector", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	request.VariantID = "other"
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("mutated selector execute error = %v, want ErrInvalidRequest", err)
	}
	if len(recorder.events) != 1 || recorder.events[0] != "reject" {
		t.Fatalf("mutated selector audit events = %v, want [reject]", recorder.events)
	}
	confirmationRequest := confirmation.Request{
		ConnectionID: "connection-a", ProfileID: "fixed_status", ProfileRevision: "r1",
		CommandID: "fixed_status", VariantID: "status", RequestNonce: "nonce-selector",
		ResolvedInputDigest: prepared.Digest(),
	}
	if err := manager.Consume(request.Confirmation, confirmationRequest); err != nil {
		t.Fatalf("mutated selector rejection consumed source confirmation: %v", err)
	}
}
