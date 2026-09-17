package main

import (
	"testing"

	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/previewui"
)

var (
	_ previewui.Connector           = previewConnector{}
	_ previewui.Reconnector         = previewConnector{}
	_ previewui.ProgressConnector   = previewConnector{}
	_ previewui.ProgressReconnector = previewConnector{}
)

func TestConnectionResultMapsStableStatusWithoutSensitiveFields(t *testing.T) {
	got := connectionResult(previewconnect.Status{
		Stage:           previewconnect.StageFailed,
		Code:            previewconnect.CodeTokenRejected,
		Message:         "must not cross the adapter",
		Remedy:          "must not cross the adapter",
		PublicHost:      "mcp.example.test",
		CredentialHint:  ".secrets/hidden.txt",
		TokenConfigured: true,
	})
	if got.Phase != previewui.ConnectionFailed || got.Code != previewui.ConnectionTokenRejected {
		t.Fatalf("mapped result = %+v", got)
	}
	if got.PublicHost != "mcp.example.test" || !got.TokenConfigured {
		t.Fatalf("safe public state missing: %+v", got)
	}
}

func TestConnectionCodeMapsActionableFailures(t *testing.T) {
	tests := map[previewconnect.Code]previewui.ConnectionCode{
		previewconnect.CodeTokenMissing:        previewui.ConnectionTokenMissing,
		previewconnect.CodeMCPBinaryMissing:    previewui.ConnectionMCPMissing,
		previewconnect.CodeCloudflaredMissing:  previewui.ConnectionCloudflaredMissing,
		previewconnect.CodeMCPPortInUse:        previewui.ConnectionPortInUse,
		previewconnect.CodeMetricsPortInUse:    previewui.ConnectionPortInUse,
		previewconnect.CodeEdgeUnreachable:     previewui.ConnectionEdgeUnreachable,
		previewconnect.CodeNoEdgeConnections:   previewui.ConnectionNoEdgeConnections,
		previewconnect.CodeAccessConfigInvalid: previewui.ConnectionAccessInvalid,
		previewconnect.CodeStopFailed:          previewui.ConnectionStopFailed,
	}
	for input, want := range tests {
		if got := connectionCode(input); got != want {
			t.Errorf("connectionCode(%q) = %q, want %q", input, got, want)
		}
	}
}
