package previewui

import (
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/desktopadmin"
	"github.com/LE-saber/Local-Probe/internal/previewapp"
)

func TestConvertPreviewAppModelKeepsOnlyUIProjection(t *testing.T) {
	model := previewapp.ViewModel{
		SchemaVersion: previewapp.SchemaVersion,
		ConfigState:   previewapp.ConfigConfigured,
		Overview: previewapp.Overview{
			ConfigRevision: "sha256:" + strings.Repeat("a", 64),
			RootCount:      1, ProfileCount: 1, ConnectionCount: 1,
			EnabledConnectionCount: 1,
		},
		Connections: previewapp.ConnectionList{Entries: []previewapp.Connection{{
			ConnectionID: "connection", Label: "Desktop", ProfileID: "profile",
			Enabled: true, Transport: "local", State: desktopadmin.StateUnavailable,
		}}},
		Rules: previewapp.RuleSet{Entries: []previewapp.Rule{{ID: "version", Kind: commandprofile.KindVersionProbe}}},
		Logs: previewapp.LogView{Available: true, Entries: []previewapp.LogEntry{{
			Timestamp: time.Unix(1, 0), Component: "MCP", Type: "mcp.list", Severity: "info", Outcome: "succeeded",
		}}},
		Diagnostics: previewapp.DiagnosticView{Available: true, Entries: []desktopadmin.Diagnostic{{
			Timestamp: time.Unix(2, 0), Component: desktopadmin.DiagnosticMCP, Severity: desktopadmin.DiagnosticWarn,
			Outcome: desktopadmin.DiagnosticFailed, ErrorCode: "unavailable",
		}}},
	}
	converted := convertPreviewAppModel(model)
	if converted.Status != string(previewapp.ConfigConfigured) || converted.Overview.ConnectionCount != 1 || len(converted.Connections) != 1 || len(converted.Logs) != 1 || len(converted.Diagnostics) != 1 {
		t.Fatalf("converted model = %+v", converted)
	}
	if converted.ManagementActions.Available || converted.ManagementActions.Reason != "production_gate" {
		t.Fatalf("action capability = %+v", converted.ManagementActions)
	}
	if len(converted.Omitted) == 0 {
		t.Fatal("conversion must declare omitted sensitive categories")
	}
	if !strings.Contains(Render(converted, SectionConnections), "Desktop") {
		t.Fatal("safe label unexpectedly omitted")
	}
}
