package auditreader

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/desktopadmin"
)

func testWireEvent(ts, id string) wireEvent {
	return wireEvent{
		TS: ts, Event: id, Schema: audit.SchemaVersionV2, Instance: "instance-1", Correlation: id,
		Component: audit.ComponentMCP, Type: audit.EventMCPCall, Action: "tools/call", Class: "normal",
		Severity: audit.SeverityInfo, Outcome: audit.OutcomeSucceeded, DurationMS: 7,
		Connection: "connection-1", Profile: "profile-1", Revision: "revision-1",
		Budget: audit.Budget{WireInBytes: 10, WireOutBytes: 20, LogicalReadBytes: 4, ReturnedBytes: 4, LimitBytes: 100},
	}
}

func writeAuditLines(t *testing.T, directory string, names []string, lines ...string) {
	t.Helper()
	path := filepath.Join(directory, names[0])
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range names[1:] {
		if err := os.WriteFile(filepath.Join(directory, name), []byte{}, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func marshalTestEvent(t *testing.T, event wireEvent) string {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReadSanitizesAllowlistedFields(t *testing.T) {
	directory := t.TempDir()
	event := testWireEvent(time.Date(2026, 9, 16, 12, 0, 0, 123, time.UTC).Format(time.RFC3339Nano), "event-1")
	event.Bytes = &audit.CommandBytes{Stdout: 17, Stderr: 3}
	// Bytes on a non-command event is intentionally invalid.  Use a command
	// event for the positive projection below.
	event.Component, event.Type, event.Action = audit.ComponentCommand, audit.EventCommandResult, audit.CommandActionResult
	event.ExitCode = int64Pointer(0)
	line := marshalTestEvent(t, event)
	writeAuditLines(t, directory, []string{ActiveFileName}, line)

	reader, err := New(directory, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || report.Summary.RecordsParsed != 1 || report.Summary.RecordsReturned != 1 {
		t.Fatalf("report = %#v", report)
	}
	entry := report.Entries[0]
	if entry.Component != audit.ComponentCommand || entry.Type != audit.EventCommandResult || entry.Command == nil || entry.Command.StdoutBytes != 17 || entry.Command.StderrBytes != 3 || entry.Budget.WireInBytes != 10 {
		t.Fatalf("entry = %#v", entry)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"instance", "correlation", "action", "command_id", "identity_digest", "exit_code", "argv", "env", "stdout\"", "stderr\""} {
		if strings.Contains(string(data), `"`+forbidden+`"`) {
			t.Fatalf("sanitized report contains forbidden key %q: %s", forbidden, data)
		}
	}
}

func TestReadCountsCorruptUnknownAndTruncatedLines(t *testing.T) {
	directory := t.TempDir()
	valid := marshalTestEvent(t, testWireEvent("2026-09-16T12:00:00Z", "event-valid"))
	unknown := `{"ts":"2026-09-16T12:00:01Z","event":"event-secret","schema":"local-probe.audit.v2","instance":"instance-1","correlation":"event-secret","component":"MCP","type":"mcp.call","class":"normal","severity":"info","outcome":"succeeded","duration_ms":0,"budget":{"wire_in_bytes":0,"wire_out_bytes":0,"logical_read_bytes":0,"returned_bytes":0,"limit_bytes":0},"token":"Bearer do-not-return"}`
	if err := os.WriteFile(filepath.Join(directory, ActiveFileName), []byte(valid+"\n"+unknown+"\n{"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := New(directory, Limits{MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.RecordsReturned != 1 || report.Summary.UnknownFieldLines != 1 || report.Summary.CorruptLines != 2 {
		t.Fatalf("summary = %#v", report.Summary)
	}
	if report.Summary.TruncatedLines != 0 {
		t.Fatalf("ordinary malformed final line reported truncated: %#v", report.Summary)
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "Bearer") || strings.Contains(string(data), "do-not-return") {
		t.Fatalf("sensitive unknown field escaped: %s", data)
	}
}

func TestReadEnforcesByteLineRecordAndFileLimits(t *testing.T) {
	directory := t.TempDir()
	first := marshalTestEvent(t, testWireEvent("2026-09-16T12:00:00Z", "event-a"))
	second := marshalTestEvent(t, testWireEvent("2026-09-16T12:00:01Z", "event-b"))
	if err := os.WriteFile(filepath.Join(directory, ActiveFileName), []byte(first+"\n"+second+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "audit-00000000000000000001.jsonl"), []byte(first+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := New(directory, Limits{MaxFiles: 1, MaxRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || !report.Summary.RecordLimitReached || !report.Summary.FileLimitReached || report.Summary.FilesSkipped != 1 {
		t.Fatalf("limited report = %#v", report)
	}

	shortReader, err := New(directory, Limits{MaxBytes: int64(len(first) / 2), MaxRecords: 8})
	if err != nil {
		t.Fatal(err)
	}
	short, err := shortReader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !short.Summary.ByteLimitReached || short.Summary.TruncatedLines == 0 || short.Summary.RecordsReturned != 0 {
		t.Fatalf("byte-limited report = %#v", short.Summary)
	}

	lineReader, err := New(directory, Limits{MaxLineBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	lineReport, err := lineReader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lineReport.Summary.TruncatedLines == 0 || lineReport.Summary.RecordsReturned != 0 {
		t.Fatalf("line-limited report = %#v", lineReport.Summary)
	}
}

func TestNewRejectsRelativeAndSymlinkDirectoriesAndFiles(t *testing.T) {
	if _, err := New("relative", Limits{}); !errors.Is(err, ErrInvalidDirectory) {
		t.Fatalf("relative directory error = %v", err)
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := New(link, Limits{}); !errors.Is(err, ErrInvalidDirectory) {
		t.Fatalf("symlink directory error = %v", err)
	}

	directory := filepath.Join(parent, "audit")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside.jsonl")
	if err := os.WriteFile(outside, []byte(marshalTestEvent(t, testWireEvent("2026-09-16T12:00:00Z", "event-outside"))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, ActiveFileName)); err != nil {
		t.Skipf("file symlink creation unavailable: %v", err)
	}
	reader, err := New(directory, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reader.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.FilesRejected != 1 || len(report.Entries) != 0 {
		t.Fatalf("symlink file report = %#v", report)
	}
}

func TestReadDiagnosticsUsesDesktopAdminAdapter(t *testing.T) {
	directory := t.TempDir()
	line := marshalTestEvent(t, testWireEvent("2026-09-16T12:00:00Z", "event-diagnostic"))
	writeAuditLines(t, directory, []string{ActiveFileName}, line)
	reader, err := New(directory, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	records, err := reader.ReadDiagnostics(context.Background(), 1)
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	if _, err := json.Marshal(records[0]); !errors.Is(err, desktopadmin.ErrLocalOnly) {
		t.Fatalf("marshal local record error = %v", err)
	}
}

func TestReadWithMaxItemsAppliesRecordLimitBeforeScan(t *testing.T) {
	directory := t.TempDir()
	first := marshalTestEvent(t, testWireEvent("2026-09-16T12:00:00Z", "event-first"))
	second := marshalTestEvent(t, testWireEvent("2026-09-16T12:00:01Z", "event-second"))
	writeAuditLines(t, directory, []string{ActiveFileName}, first+"\n"+second)
	reader, err := New(directory, Limits{MaxRecords: desktopadmin.MaxItems})
	if err != nil {
		t.Fatal(err)
	}
	report, err := reader.ReadWithMaxItems(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 1 || report.Summary.RecordsReturned != 1 || !report.Summary.RecordLimitReached {
		t.Fatalf("bounded report = %#v", report)
	}
}

func int64Pointer(value int64) *int64 { return &value }
