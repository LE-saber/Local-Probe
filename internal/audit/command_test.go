package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const testIdentityDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type commandCapture struct {
	mu       sync.Mutex
	normal   []Event
	security []Event
}

func (c *commandCapture) Emit(event Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.normal = append(c.normal, event)
	return nil
}

func (c *commandCapture) EmitSecurity(event Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.security = append(c.security, event)
	return nil
}

func (c *commandCapture) snapshot() (normal, security []Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.normal...), append([]Event(nil), c.security...)
}

func testCommandContext() CommandContext {
	return CommandContext{
		ConnectionID: "connection-1", ProfileID: "profile-1", ProfileRevision: "revision-1",
		CommandID: "codex-version", VariantID: "short", IdentityDigest: testIdentityDigest,
		Network: NetworkEnforcementVerified,
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestCommandRecorderUsesFixedSafeEvents(t *testing.T) {
	capture := &commandCapture{}
	recorder := NewCommandRecorder(capture)
	ctx := testCommandContext()
	if err := recorder.RecordAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordStart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordResult(ctx, CommandResult{
		ExitCode: int64Pointer(0), DurationMS: 21, StdoutBytes: 17, StderrBytes: 3,
		ErrorCode: "Bearer secret-command-output-123456",
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordReject(CommandContext{
		ConnectionID: "connection-1", ProfileID: "profile-1", ProfileRevision: "revision-1",
		CommandID: "codex-version", VariantID: "short", Network: NetworkEnforcementUnavailable,
	}, "C:\\private\\secret\\argv --token Bearer secret-command-output-123456"); err != nil {
		t.Fatal(err)
	}

	normal, security := capture.snapshot()
	if len(normal) != 3 || len(security) != 1 {
		t.Fatalf("events = normal %d/security %d, want 3/1", len(normal), len(security))
	}
	if normal[0].EventType != EventCommandAdmission || normal[0].Action != CommandActionAdmission || normal[0].Outcome != OutcomeSucceeded {
		t.Fatalf("admission = %#v", normal[0])
	}
	if normal[1].EventType != EventCommandStart || normal[1].Action != CommandActionStart || normal[1].Outcome != OutcomeStarted {
		t.Fatalf("start = %#v", normal[1])
	}
	if normal[2].EventType != EventCommandResult || normal[2].Action != CommandActionResult || normal[2].CommandBytes != (CommandBytes{Stdout: 17, Stderr: 3}) {
		t.Fatalf("result = %#v", normal[2])
	}
	if normal[2].Budget != (Budget{}) {
		t.Fatalf("command output counts polluted MCP budget: %#v", normal[2].Budget)
	}
	if normal[2].ErrorCode != "" {
		t.Fatalf("successful result copied error code: %#v", normal[2])
	}
	if security[0].EventType != EventCommandReject || security[0].Class != ClassSecurity || security[0].Outcome != OutcomeRejected {
		t.Fatalf("reject = %#v", security[0])
	}
	if security[0].ErrorCode != "unavailable" {
		t.Fatalf("unknown reject reason = %q, want unavailable", security[0].ErrorCode)
	}
}

func TestCommandRecorderWritesOnlyAllowlistedFields(t *testing.T) {
	dir := t.TempDir()
	sink, err := New(testConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewCommandRecorder(sink)
	ctx := testCommandContext()
	if err := recorder.RecordAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordStart(ctx); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordResult(ctx, CommandResult{
		ExitCode: int64Pointer(7), DurationMS: 9, StdoutBytes: 101, StderrBytes: 11,
		ErrorCode: "child_exit",
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordReject(CommandContext{
		ConnectionID: "connection-1", ProfileID: "profile-1", ProfileRevision: "revision-1",
		CommandID: "codex-version", VariantID: "short", Network: NetworkEnforcementNotChecked,
	}, "denied"); err != nil {
		t.Fatal(err)
	}
	if err := sink.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		`C:\\secret\\tool.exe`, `--token`, `Bearer`, `"stdout":`, `"stderr":`, `"argv":`, `"env":`, `"query":`, `"request_body":`,
	} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("audit contains forbidden material %q: %s", forbidden, data)
		}
	}
	for _, required := range []string{`"command_id":"codex-version"`, `"variant_id":"short"`, `"identity_digest":"` + testIdentityDigest + `"`, `"exit_code":7`, `"network_enforcement":"verified"`, `"stdout_bytes":101`, `"stderr_bytes":11`} {
		if !bytes.Contains(data, []byte(required)) {
			t.Fatalf("audit is missing allowlisted field %q: %s", required, data)
		}
	}
	var admissionFound bool
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var wire wireEvent
		if err := json.Unmarshal(line, &wire); err != nil {
			t.Fatal(err)
		}
		if oneOf(wire.Type, EventCommandAdmission, EventCommandStart, EventCommandResult, EventCommandReject) && wire.Schema != SchemaVersionV2 {
			t.Fatalf("new command event schema = %q, want %q", wire.Schema, SchemaVersionV2)
		}
		if wire.Type == EventCommandAdmission {
			admissionFound = true
			if wire.CommandID != "codex-version" || wire.IdentityDigest != testIdentityDigest {
				t.Fatalf("wire admission = %#v", wire)
			}
		}
		if wire.Type == EventCommandResult && wire.Budget.LimitBytes != 0 {
			t.Fatalf("command result set common Budget.LimitBytes: %#v", wire.Budget)
		}
	}
	if !admissionFound {
		t.Fatalf("command admission was not written: %s", data)
	}
}

func TestCommandRecorderRejectIsSynchronousAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.QueueSize = 1
	sink, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	recorder := NewCommandRecorder(sink)
	ctx := testCommandContext()
	if err := recorder.RecordAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	// A security reject bypasses the asynchronous queue. It is available on
	// disk immediately even while normal events are still queued.
	if err := recorder.RecordReject(ctx, "denied"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"type":"command.reject"`)) {
		t.Fatalf("synchronous reject missing: %s", data)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCommandRecorderRejectsUnsafeInputAndBounds(t *testing.T) {
	recorder := NewCommandRecorder(&commandCapture{})
	ctx := testCommandContext()
	for name, mutate := range map[string]func(*CommandContext){
		"absolute command path": func(value *CommandContext) { value.CommandID = `C:\\Windows\\tool.exe` },
		"invalid digest":        func(value *CommandContext) { value.IdentityDigest = "not-a-digest" },
		"network not verified admission": func(value *CommandContext) {
			value.Network = NetworkEnforcementUnavailable
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := ctx
			mutate(&value)
			if err := recorder.RecordAdmission(value); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error = %v, want ErrInvalidEvent", err)
			}
		})
	}
	secret := ctx
	secret.CommandID = "api-key"
	if err := recorder.RecordAdmission(secret); !errors.Is(err, ErrSensitiveField) {
		t.Fatalf("secret command id error = %v, want ErrSensitiveField", err)
	}
	if err := recorder.RecordResult(ctx, CommandResult{ExitCode: int64Pointer(0), StdoutBytes: maxCounter, StderrBytes: 1}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("byte overflow error = %v, want ErrInvalidEvent", err)
	}
	if err := recorder.RecordResult(ctx, CommandResult{TimedOut: true, DurationMS: maxDuration + 1}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("duration overflow error = %v, want ErrInvalidEvent", err)
	}
	if err := recorder.RecordResult(ctx, CommandResult{TimedOut: true, ExitCode: int64Pointer(1)}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("timeout with exit error = %v, want ErrInvalidEvent", err)
	}
}

func TestCommandRecorderRejectsInvalidSelectorWithoutDroppingSecurityAudit(t *testing.T) {
	capture := &commandCapture{}
	recorder := NewCommandRecorder(capture)
	err := recorder.RecordReject(CommandContext{
		ConnectionID:    `C:\\secret\\connection`,
		ProfileID:       "Bearer secret-profile-123456",
		ProfileRevision: "revision-1",
		CommandID:       `C:\\Windows\\tool.exe`,
		VariantID:       "api-key",
		IdentityDigest:  "not-a-digest",
		Network:         NetworkEnforcement("leaked-network-state"),
	}, "denied")
	if err != nil {
		t.Fatalf("RecordReject() = %v, want synchronous audit success", err)
	}
	_, security := capture.snapshot()
	if len(security) != 1 {
		t.Fatalf("security events = %d, want 1", len(security))
	}
	event := security[0]
	if event.ErrorCode != "invalid_request" || event.CommandID != "" || event.VariantID != "" || event.IdentityDigest != "" || event.NetworkEnforcement != NetworkEnforcementNotChecked {
		t.Fatalf("sanitized rejection = %#v", event)
	}
	for _, value := range []string{event.ConnectionID, event.ProfileID, event.ProfileRevision, event.CommandID, event.VariantID, event.IdentityDigest, string(event.NetworkEnforcement), event.ErrorCode} {
		if value == `C:\\secret\\connection` || value == "Bearer secret-profile-123456" || value == `C:\\Windows\\tool.exe` || value == "api-key" || value == "not-a-digest" || value == "leaked-network-state" {
			t.Fatalf("raw rejected selector reached audit: %q", value)
		}
	}
}

func TestValidateCommandEventStateCombinations(t *testing.T) {
	base := func(kind EventType) Event {
		return Event{
			EventID: "event-1", CorrelationID: "event-1", Class: ClassNormal,
			Component: ComponentCommand, EventType: kind, Action: commandActionForEvent(kind),
			Severity: SeverityInfo, Outcome: OutcomeSucceeded,
			ConnectionID: "connection-1", ProfileID: "profile-1", ProfileRevision: "revision-1",
			CommandID: "command-1", VariantID: "variant-1", IdentityDigest: testIdentityDigest,
			NetworkEnforcement: NetworkEnforcementVerified, Timestamp: time.Unix(1, 0),
		}
	}
	result := base(EventCommandResult)
	result.ExitCode = int64Pointer(0)
	cases := map[string]struct {
		makeEvent func() Event
		valid     bool
	}{
		"admission canonical": {makeEvent: func() Event { return base(EventCommandAdmission) }, valid: true},
		"admission unavailable": {makeEvent: func() Event {
			event := base(EventCommandAdmission)
			event.NetworkEnforcement = NetworkEnforcementUnavailable
			return event
		}},
		"admission wrong outcome": {makeEvent: func() Event {
			event := base(EventCommandAdmission)
			event.Outcome = OutcomeStarted
			return event
		}},
		"admission path-like connection": {makeEvent: func() Event {
			event := base(EventCommandAdmission)
			event.ConnectionID = `C:\\temp\\connection`
			return event
		}},
		"start canonical": {makeEvent: func() Event {
			event := base(EventCommandStart)
			event.Outcome = OutcomeStarted
			return event
		}, valid: true},
		"start wrong severity": {makeEvent: func() Event {
			event := base(EventCommandStart)
			event.Outcome = OutcomeStarted
			event.Severity = SeverityWarn
			return event
		}},
		"result success canonical": {makeEvent: func() Event { return result }, valid: true},
		"result success unavailable": {makeEvent: func() Event {
			event := result
			event.NetworkEnforcement = NetworkEnforcementUnavailable
			return event
		}},
		"result timeout canonical": {makeEvent: func() Event {
			event := base(EventCommandResult)
			event.Outcome = OutcomeFailed
			event.ErrorCode = "deadline_exceeded"
			event.TimedOut = true
			return event
		}, valid: true},
		"result timeout with exit": {makeEvent: func() Event {
			event := base(EventCommandResult)
			event.Outcome = OutcomeFailed
			event.ErrorCode = "deadline_exceeded"
			event.TimedOut = true
			event.ExitCode = int64Pointer(1)
			return event
		}},
		"result nonzero canonical": {makeEvent: func() Event {
			event := base(EventCommandResult)
			event.Outcome = OutcomeFailed
			event.ErrorCode = "child_exit"
			event.ExitCode = int64Pointer(1)
			return event
		}, valid: true},
		"result nonzero missing error": {makeEvent: func() Event {
			event := base(EventCommandResult)
			event.Outcome = OutcomeFailed
			event.ExitCode = int64Pointer(1)
			return event
		}},
		"result nonzero deadline": {makeEvent: func() Event {
			event := base(EventCommandResult)
			event.Outcome = OutcomeFailed
			event.ErrorCode = "deadline_exceeded"
			event.ExitCode = int64Pointer(1)
			return event
		}},
		"reject canonical": {makeEvent: func() Event {
			event := base(EventCommandReject)
			event.Class = ClassSecurity
			event.Severity = SeverityWarn
			event.Outcome = OutcomeRejected
			event.ErrorCode = "denied"
			event.IdentityDigest = ""
			event.CommandID = ""
			event.VariantID = ""
			event.NetworkEnforcement = NetworkEnforcementNotChecked
			return event
		}, valid: true},
		"reject info": {makeEvent: func() Event {
			event := base(EventCommandReject)
			event.Class = ClassSecurity
			event.Outcome = OutcomeRejected
			event.ErrorCode = "denied"
			event.IdentityDigest = ""
			event.CommandID = ""
			event.VariantID = ""
			event.NetworkEnforcement = NetworkEnforcementNotChecked
			event.Severity = SeverityInfo
			return event
		}},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			err := validateEvent(testCase.makeEvent())
			if testCase.valid && err != nil {
				t.Fatalf("validateEvent() = %v, want nil", err)
			}
			if !testCase.valid && !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("validateEvent() = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestCommandRecorderDowngradesUnverifiedResultAndRecordsTimeout(t *testing.T) {
	capture := &commandCapture{}
	recorder := NewCommandRecorder(capture)
	ctx := testCommandContext()
	ctx.Network = NetworkEnforcementUnavailable
	if err := recorder.RecordResult(ctx, CommandResult{ExitCode: int64Pointer(0), StdoutBytes: 4}); err != nil {
		t.Fatal(err)
	}
	ctx.Network = NetworkEnforcementVerified
	if err := recorder.RecordResult(ctx, CommandResult{TimedOut: true, DurationMS: 100}); err != nil {
		t.Fatal(err)
	}
	normal, _ := capture.snapshot()
	if len(normal) != 2 {
		t.Fatalf("normal events = %d, want 2", len(normal))
	}
	if normal[0].Outcome != OutcomeDegraded || normal[0].ErrorCode != "unavailable" || normal[0].NetworkEnforcement != NetworkEnforcementUnavailable {
		t.Fatalf("unverified result = %#v", normal[0])
	}
	if !normal[1].TimedOut || normal[1].ExitCode != nil || normal[1].Outcome != OutcomeFailed || normal[1].ErrorCode != "deadline_exceeded" {
		t.Fatalf("timeout result = %#v", normal[1])
	}
}

func TestCommandRecorderRecordsNoExitTerminalOutcomes(t *testing.T) {
	capture := &commandCapture{}
	recorder := NewCommandRecorder(capture)
	ctx := testCommandContext()
	cases := []struct {
		name       string
		result     CommandResult
		wantCode   string
		wantCancel bool
	}{
		{name: "cancelled", result: CommandResult{Cancelled: true, ErrorCode: "cancelled", DurationMS: 4}, wantCode: "cancelled", wantCancel: true},
		{name: "output limit", result: CommandResult{ErrorCode: "output_limit", StdoutBytes: 64}, wantCode: "output_limit"},
		{name: "identity", result: CommandResult{ErrorCode: "identity_changed"}, wantCode: "identity_changed"},
		{name: "start unavailable", result: CommandResult{ErrorCode: "unavailable"}, wantCode: "unavailable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := recorder.RecordResult(ctx, test.result); err != nil {
				t.Fatalf("RecordResult() = %v", err)
			}
			normal, _ := capture.snapshot()
			event := normal[len(normal)-1]
			if event.ExitCode != nil || event.Outcome != OutcomeFailed || event.ErrorCode != test.wantCode || event.Cancelled != test.wantCancel {
				t.Fatalf("event = %#v", event)
			}
		})
	}
}

func TestCommandRecorderRejectsAmbiguousTerminalOutcomes(t *testing.T) {
	recorder := NewCommandRecorder(&commandCapture{})
	ctx := testCommandContext()
	cases := []CommandResult{
		{TimedOut: true, Cancelled: true},
		{Cancelled: true, ExitCode: int64Pointer(1)},
		{TimedOut: true, ErrorCode: "cancelled"},
		{Cancelled: true, ErrorCode: "output_limit"},
		{ErrorCode: "deadline_exceeded"},
		{ErrorCode: "cancelled"},
		{ErrorCode: "child_exit"},
		{ExitCode: int64Pointer(0), ErrorCode: "output_limit"},
		{},
	}
	for i, result := range cases {
		if err := recorder.RecordResult(ctx, result); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("case %d: RecordResult() = %v, want ErrInvalidEvent", i, err)
		}
	}
}

func TestCommandRecorderDoesNotCopyNoExitErrorText(t *testing.T) {
	capture := &commandCapture{}
	recorder := NewCommandRecorder(capture)
	secret := `C:\private\secret\tool.exe --token Bearer output-secret-123456`
	if err := recorder.RecordResult(testCommandContext(), CommandResult{ErrorCode: secret}); err != nil {
		t.Fatalf("RecordResult() = %v", err)
	}
	normal, _ := capture.snapshot()
	if len(normal) != 1 || normal[0].ExitCode != nil || normal[0].ErrorCode != "unavailable" {
		t.Fatalf("sanitized result = %#v", normal)
	}
}

func TestCommandRecorderAcceptsProbeOutcomeErrorCodesWithoutExit(t *testing.T) {
	recorder := NewCommandRecorder(&commandCapture{})
	codes := []string{
		"invalid_input", "not_found", "rejected_executable", "identity_changed",
		"output_limit", "invalid_output",
		"hash_mismatch", "hash_limit", "unsupported_platform", "unavailable",
	}
	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			if err := recorder.RecordResult(testCommandContext(), CommandResult{ErrorCode: code}); err != nil {
				t.Fatalf("RecordResult(%q) = %v", code, err)
			}
		})
	}
	if err := recorder.RecordResult(testCommandContext(), CommandResult{ExitCode: int64Pointer(0), ErrorCode: "invalid_output"}); err != nil {
		t.Fatalf("successful process with invalid output = %v", err)
	}
}

func TestLegacyCommandExitWireValueIsPreserved(t *testing.T) {
	dir := t.TempDir()
	sink, err := New(testConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	legacy := Event{
		Component: ComponentCommand, EventType: EventCommandExit, Action: CommandActionExit,
		Severity: SeverityInfo, Outcome: OutcomeSucceeded,
	}
	if err := sink.Emit(legacy); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"type":"command.exit"`)) || bytes.Contains(data, []byte(`"type":"command.result"`)) {
		t.Fatalf("legacy wire type changed: %s", data)
	}
	if !bytes.Contains(data, []byte(`"schema":"local-probe.audit.v2"`)) {
		t.Fatalf("legacy event did not use current v2 schema: %s", data)
	}
}

func TestCommandRecorderConcurrent(t *testing.T) {
	capture := &commandCapture{}
	recorder := NewCommandRecorder(capture)
	ctx := testCommandContext()
	const workers = 12
	const iterations = 80
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if err := recorder.RecordAdmission(ctx); err != nil {
					t.Errorf("admission: %v", err)
					return
				}
				if err := recorder.RecordStart(ctx); err != nil {
					t.Errorf("start: %v", err)
					return
				}
				if err := recorder.RecordResult(ctx, CommandResult{ExitCode: int64Pointer(0)}); err != nil {
					t.Errorf("result: %v", err)
					return
				}
				if err := recorder.RecordReject(CommandContext{
					ConnectionID: ctx.ConnectionID, ProfileID: ctx.ProfileID, ProfileRevision: ctx.ProfileRevision,
					CommandID: ctx.CommandID, VariantID: ctx.VariantID, Network: NetworkEnforcementNotChecked,
				}, "denied"); err != nil {
					t.Errorf("reject: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	normal, security := capture.snapshot()
	if len(normal) != workers*iterations*3 || len(security) != workers*iterations {
		t.Fatalf("concurrent event count = normal %d/security %d", len(normal), len(security))
	}
}
