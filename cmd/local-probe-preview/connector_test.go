package main

import (
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
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
		Transport:       config.TransportOpenAIRuntime,
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
	if got.Transport != string(config.TransportOpenAIRuntime) {
		t.Fatalf("transport was not preserved: %+v", got)
	}
}

func TestConnectionCodeMapsActionableFailures(t *testing.T) {
	tests := map[previewconnect.Code]previewui.ConnectionCode{
		previewconnect.CodeTokenMissing:            previewui.ConnectionTokenMissing,
		previewconnect.CodeMCPBinaryMissing:        previewui.ConnectionMCPMissing,
		previewconnect.CodeCloudflaredMissing:      previewui.ConnectionCloudflaredMissing,
		previewconnect.CodeOpenAIClientMissing:     previewui.ConnectionOpenAIClientMissing,
		previewconnect.CodeOpenAIKeyMissing:        previewui.ConnectionOpenAIKeyMissing,
		previewconnect.CodeOpenAIKeyInvalid:        previewui.ConnectionOpenAIKeyInvalid,
		previewconnect.CodeOpenAIMCPTokenMissing:   previewui.ConnectionOpenAIMCPTokenMissing,
		previewconnect.CodeOpenAIMCPTokenEmpty:     previewui.ConnectionOpenAIMCPTokenEmpty,
		previewconnect.CodeOpenAIMCPTokenInvalid:   previewui.ConnectionOpenAIMCPTokenInvalid,
		previewconnect.CodeOpenAITunnelMissing:     previewui.ConnectionOpenAITunnelMissing,
		previewconnect.CodeOpenAITunnelInvalid:     previewui.ConnectionOpenAITunnelInvalid,
		previewconnect.CodeOpenAITunnelStartFailed: previewui.ConnectionOpenAITunnelStartFailed,
		previewconnect.CodeOpenAITunnelNotReady:    previewui.ConnectionOpenAITunnelNotReady,
		previewconnect.CodeOpenAIProfileInvalid:    previewui.ConnectionOpenAIProfileInvalid,
		previewconnect.CodeOpenAIAuthRejected:      previewui.ConnectionOpenAIAuthRejected,
		previewconnect.CodeOpenAIHealthURLInvalid:  previewui.ConnectionOpenAIHealthInvalid,
		previewconnect.CodeMCPPortInUse:            previewui.ConnectionPortInUse,
		previewconnect.CodeMetricsPortInUse:        previewui.ConnectionPortInUse,
		previewconnect.CodeEdgeUnreachable:         previewui.ConnectionEdgeUnreachable,
		previewconnect.CodeNoEdgeConnections:       previewui.ConnectionNoEdgeConnections,
		previewconnect.CodeAccessConfigInvalid:     previewui.ConnectionAccessInvalid,
		previewconnect.CodeStopFailed:              previewui.ConnectionStopFailed,
	}
	for input, want := range tests {
		if got := connectionCode(input); got != want {
			t.Errorf("connectionCode(%q) = %q, want %q", input, got, want)
		}
	}
}
