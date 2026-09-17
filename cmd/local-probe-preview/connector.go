package main

import (
	"context"

	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/previewui"
)

// previewConnector is the deliberately small composition seam between the
// native UI and the fixed Preview lifecycle controller. It translates only
// stable enums and never forwards paths, child output, or credential data.
type previewConnector struct {
	controller *previewconnect.Controller
}

func (c previewConnector) Connect(ctx context.Context) previewui.ConnectionResult {
	status, _ := c.controller.Connect(ctx, nil)
	return connectionResult(status)
}

func (c previewConnector) Reconnect(ctx context.Context) previewui.ConnectionResult {
	status, _ := c.controller.Reconnect(ctx, nil)
	return connectionResult(status)
}

func (c previewConnector) ConnectWithProgress(ctx context.Context, progress func(previewui.ConnectionProgress)) previewui.ConnectionResult {
	status, _ := c.controller.Connect(ctx, func(status previewconnect.Status) {
		if progress != nil {
			progress(connectionProgress(status))
		}
	})
	return connectionResult(status)
}

func (c previewConnector) ReconnectWithProgress(ctx context.Context, progress func(previewui.ConnectionProgress)) previewui.ConnectionResult {
	status, _ := c.controller.Reconnect(ctx, func(status previewconnect.Status) {
		if progress != nil {
			progress(connectionProgress(status))
		}
	})
	return connectionResult(status)
}

func connectionResult(status previewconnect.Status) previewui.ConnectionResult {
	return previewui.ConnectionResult{
		Phase:           connectionPhase(status.Stage),
		Code:            connectionCode(status.Code),
		PublicHost:      status.PublicHost,
		TokenConfigured: status.TokenConfigured,
	}
}

func connectionProgress(status previewconnect.Status) previewui.ConnectionProgress {
	result := connectionResult(status)
	return previewui.ConnectionProgress{
		Phase:           result.Phase,
		Code:            result.Code,
		PublicHost:      result.PublicHost,
		TokenConfigured: result.TokenConfigured,
	}
}

func connectionPhase(stage previewconnect.Stage) previewui.ConnectionPhase {
	switch stage {
	case previewconnect.StageValidating:
		return previewui.ConnectionPreflight
	case previewconnect.StageStartingMCP, previewconnect.StageWaitingMCP:
		return previewui.ConnectionStartingMCP
	case previewconnect.StageStartingTunnel:
		return previewui.ConnectionStartingTunnel
	case previewconnect.StageWaitingTunnel:
		return previewui.ConnectionVerifying
	case previewconnect.StageReady:
		return previewui.ConnectionReady
	case previewconnect.StageFailed, previewconnect.StageCancelled:
		return previewui.ConnectionFailed
	default:
		return previewui.ConnectionIdle
	}
}

func connectionCode(code previewconnect.Code) previewui.ConnectionCode {
	switch code {
	case previewconnect.CodeNone:
		return ""
	case previewconnect.CodeTokenMissing:
		return previewui.ConnectionTokenMissing
	case previewconnect.CodeTokenEmpty:
		return previewui.ConnectionTokenEmpty
	case previewconnect.CodeTokenInvalidFormat:
		return previewui.ConnectionTokenInvalidFormat
	case previewconnect.CodeTokenRejected:
		return previewui.ConnectionTokenRejected
	case previewconnect.CodeCloudflaredMissing:
		return previewui.ConnectionCloudflaredMissing
	case previewconnect.CodeMCPBinaryMissing:
		return previewui.ConnectionMCPMissing
	case previewconnect.CodeConfigMissing:
		return previewui.ConnectionConfigMissing
	case previewconnect.CodeConfigInvalid, previewconnect.CodeTunnelConfigInvalid:
		return previewui.ConnectionConfigInvalid
	case previewconnect.CodeAccessConfigInvalid:
		return previewui.ConnectionAccessInvalid
	case previewconnect.CodeMCPPortInUse, previewconnect.CodeMetricsPortInUse:
		return previewui.ConnectionPortInUse
	case previewconnect.CodeMCPStartFailed:
		return previewui.ConnectionOriginStartFailed
	case previewconnect.CodeMCPNotReady, previewconnect.CodeOriginUnavailable:
		return previewui.ConnectionOriginNotReady
	case previewconnect.CodeEdgeUnreachable:
		return previewui.ConnectionEdgeUnreachable
	case previewconnect.CodeNoEdgeConnections:
		return previewui.ConnectionNoEdgeConnections
	case previewconnect.CodeTunnelStartFailed, previewconnect.CodeTunnelNotReady:
		return previewui.ConnectionTunnelNotReady
	case previewconnect.CodeCancelled:
		return previewui.ConnectionTimeout
	case previewconnect.CodeStopFailed:
		return previewui.ConnectionStopFailed
	default:
		return previewui.ConnectionUnknown
	}
}
