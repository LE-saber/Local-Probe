package audit

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func testConfig(dir string) Config {
	return Config{Directory: dir, FilePrefix: "audit", MaxFileBytes: 4096, MaxFiles: 4, QueueSize: 8, InstanceID: "instance-test"}
}

func testEvent(id string) Event {
	return Event{EventID: id, CorrelationID: "corr-1", ParentID: "parent-1", Component: ComponentMCP, EventType: EventMCPCall, Action: "tools/call", Severity: SeverityInfo, Outcome: OutcomeSucceeded, ConnectionID: "connection-1", ProfileID: "profile-1", ProfileRevision: "rev-1", DurationMS: 12, Budget: Budget{WireInBytes: 10, WireOutBytes: 20, LogicalReadBytes: 4, ReturnedBytes: 4, LimitBytes: 100}}
}

func readLines(t *testing.T, dir string) [][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "audit*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var lines [][]byte
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		for scanner.Scan() {
			lines = append(lines, append([]byte(nil), scanner.Bytes()...))
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return lines
}

func TestJSONLWireSchemaAndCorrelation(t *testing.T) {
	dir := t.TempDir()
	sink, err := New(testConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	event := testEvent("")
	event.CorrelationID = ""
	if err := sink.Emit(event); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, dir)
	if len(lines) != 1 {
		t.Fatalf("got %d JSONL records, want 1", len(lines))
	}
	decoder := json.NewDecoder(bytes.NewReader(lines[0]))
	decoder.DisallowUnknownFields()
	var got wireEvent
	if err := decoder.Decode(&got); err != nil {
		t.Fatalf("decode wire event: %v", err)
	}
	if got.Event == "" || got.Event != got.Correlation || got.Schema != SchemaVersion || got.Instance != "instance-test" || got.Component != ComponentMCP || got.Type != EventMCPCall || got.Action != "tools/call" {
		t.Fatalf("identity/type fields = %#v", got)
	}
	if got.Budget.WireInBytes != 10 || got.Budget.LimitBytes != 100 {
		t.Fatalf("budget = %#v", got.Budget)
	}
	for _, forbidden := range []string{"event_version", "event_id", "timestamp", "instance_id", "correlation_id", "parent_id", "event_type", "connection_id", "profile_id", "profile_revision", "query", "body", "argv", "env", "stdout", "stderr"} {
		if bytes.Contains(lines[0], []byte(`"`+forbidden+`"`)) {
			t.Fatalf("wire record contains forbidden key %q: %s", forbidden, lines[0])
		}
	}
}

func TestSecurityWriteThroughAndNormalQueueRejectsSecurity(t *testing.T) {
	dir := t.TempDir()
	sink, err := New(testConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	security := testEvent("security-1")
	security.Class, security.Component, security.EventType, security.Outcome = ClassSecurity, ComponentAuth, EventAuthReject, OutcomeRejected
	if err := sink.Emit(Event{Class: ClassSecurity, Component: ComponentAuth, EventType: EventAuthReject, Severity: SeverityWarn, Outcome: OutcomeRejected}); !errors.Is(err, ErrSecurityDelivery) {
		t.Fatalf("ordinary security event error = %v", err)
	}
	if err := sink.EmitSecurity(security); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"event":"security-1"`)) {
		t.Fatalf("security event was not write-through: %s", data)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSensitiveInputRejectedAndNeverWritten(t *testing.T) {
	dir := t.TempDir()
	sink, err := New(testConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	secret := "Bearer sentinel-secret-value-123456"
	event := testEvent("secret-event")
	event.ConnectionID = secret
	if err := sink.Emit(event); !errors.Is(err, ErrSensitiveField) {
		t.Fatalf("secret error = %v, want ErrSensitiveField", err)
	}
	if got := Redact(secret); got != "[REDACTED]" {
		t.Fatalf("Redact(secret) = %q", got)
	}
	if err := sink.Emit(testEvent("safe-event")); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("sentinel-secret-value-123456")) || bytes.Contains(data, []byte("Bearer")) {
		t.Fatalf("secret reached disk: %s", data)
	}
}

func TestRotationAndRetention(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.MaxFileBytes, cfg.MaxFiles, cfg.QueueSize = 700, 2, 32
	sink, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	for i := 0; i < 8; i++ {
		if err := sink.Emit(testEvent("rotation-" + string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 || len(paths) > cfg.MaxFiles-1 {
		t.Fatalf("archives = %v", paths)
	}
	if got := len(readLines(t, dir)); got == 0 || got > 8 {
		t.Fatalf("retained %d records", got)
	}
}

func TestQueueDropDegradedAndCloseDrains(t *testing.T) {
	dir := t.TempDir()
	cfg := testConfig(dir)
	cfg.QueueSize = 1
	sink, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	sink.beforeWrite = func() {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	if err := sink.Emit(testEvent("drop-a")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not reach backpressure gate")
	}
	if err := sink.Emit(testEvent("drop-b")); err != nil {
		t.Fatal(err)
	}
	if err := sink.Emit(testEvent("drop-c")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third event error = %v", err)
	}
	stats := sink.Stats()
	if stats.Dropped != 1 || !stats.Degraded || stats.Accepted != 2 {
		t.Fatalf("stats before release = %#v", stats)
	}
	close(release)
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(readLines(t, dir)); got != 2 {
		t.Fatalf("close drained %d records", got)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
	if err := sink.Emit(testEvent("after-close")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Emit after Close() = %v", err)
	}
	if err := sink.Flush(context.Background()); err != nil {
		t.Fatalf("Flush after Close() = %v", err)
	}
}

func TestConfigHardLimits(t *testing.T) {
	dir := t.TempDir()
	for name, cfg := range map[string]Config{
		"file":  {Directory: dir, MaxFileBytes: MaxFileBytes + 1, MaxFiles: 1, QueueSize: 1},
		"files": {Directory: dir, MaxFileBytes: 1, MaxFiles: MaxFiles + 1, QueueSize: 1},
		"queue": {Directory: dir, MaxFileBytes: 1, MaxFiles: 1, QueueSize: MaxQueueSize + 1},
	} {
		if _, err := New(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s config error = %v", name, err)
		}
	}
	if strings.Contains(Redact("normal-value"), "REDACTED") {
		t.Fatal("Redact changed a non-sensitive value")
	}
}
