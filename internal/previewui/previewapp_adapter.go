package previewui

import (
	"context"
	"errors"

	"github.com/LE-saber/Local-Probe/internal/previewapp"
)

// PreviewAppModel adapts the composition-level previewapp.ViewModel to the
// deliberately smaller native-UI contract in this package.  The adapter is
// the only place where the GUI knows about previewapp's richer model; window
// code continues to render path-free, bounded values.
type PreviewAppModel struct {
	app *previewapp.App
}

func NewPreviewAppModel(app *previewapp.App) *PreviewAppModel {
	return &PreviewAppModel{app: app}
}

func (m *PreviewAppModel) Refresh(ctx context.Context) (Snapshot, error) {
	if m == nil || m.app == nil {
		return Snapshot{}, ErrUnavailable
	}
	model, err := m.app.Refresh(ctx)
	converted := convertPreviewAppModel(model)
	// Missing/invalid config is a renderable state, not a reason for the
	// window to retain an older snapshot. Cancellation and close remain hard
	// errors so a worker cannot commit stale data after shutdown.
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, previewapp.ErrClosed)) {
		return converted, err
	}
	return converted, nil
}

func (m *PreviewAppModel) ExportDiagnostics(ctx context.Context) ([]byte, error) {
	if m == nil || m.app == nil {
		return nil, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Export from a freshly loaded model.  A background audit/config change
	// must not be silently omitted just because the last paint snapshot was
	// older; any refresh error is returned to the UI for a stable failure
	// message instead of exporting stale data.
	model, err := m.app.Refresh(ctx)
	if err != nil {
		return nil, err
	}
	return ExportSupportBundle(ctx, convertPreviewAppModel(model))
}

func convertPreviewAppModel(model previewapp.ViewModel) Snapshot {
	snapshot := Snapshot{
		SchemaVersion:   SchemaVersion,
		ProductionReady: model.About.ProductionReady,
		Unconfigured:    model.ConfigState == previewapp.ConfigUnconfigured,
		Status:          string(model.ConfigState),
		Overview: Overview{
			ConfigRevision:         model.Overview.ConfigRevision,
			RootCount:              model.Overview.RootCount,
			ProfileCount:           model.Overview.ProfileCount,
			ConnectionCount:        model.Overview.ConnectionCount,
			EnabledConnectionCount: model.Overview.EnabledConnectionCount,
			StatusKnown:            model.Overview.StatusKnown,
			ReadyConnections:       model.Overview.ReadyConnections,
			DegradedConnections:    model.Overview.DegradedConnections,
			Truncated:              model.Overview.Truncated,
		},
		Audit: AuditStatus{
			Available: model.Logs.Available || model.Diagnostics.Available,
			Status:    "unavailable",
			Records:   len(model.Logs.Entries),
			Truncated: model.Logs.Truncated || model.Diagnostics.Truncated,
		},
		ManagementActions: ActionCapability{Available: false, Reason: "production_gate"},
		Omitted:           []string{"paths", "credentials", "network_endpoints", "process_details", "raw_config", "raw_audit_lines"},
	}
	if snapshot.Audit.Available {
		snapshot.Audit.Status = "available"
		if snapshot.Audit.Records == 0 {
			snapshot.Audit.Status = "empty"
		}
	}
	for _, warning := range model.Warnings {
		if snapshot.ErrorCode == "" && renderableWarning(warning.Code) {
			snapshot.ErrorCode = warning.Code
		}
	}
	if model.ConfigState == previewapp.ConfigInvalid && snapshot.ErrorCode == "" {
		snapshot.ErrorCode = "config_invalid"
	}
	for _, value := range model.Connections.Entries {
		snapshot.Connections = append(snapshot.Connections, Connection{
			ConnectionID:     value.ConnectionID,
			Label:            value.Label,
			LabelOmitted:     value.LabelOmitted,
			ProfileID:        value.ProfileID,
			Enabled:          value.Enabled,
			Transport:        string(value.Transport),
			TunnelConfigured: value.TunnelConfigured,
			StatusKnown:      value.StatusKnown,
			State:            string(value.State),
			Attempt:          value.Attempt,
			LastError:        string(value.LastError),
		})
	}
	snapshot.DeveloperRules = DeveloperRules{Available: model.Capabilities.DeveloperRules.Available, Enabled: model.Rules.Enabled, AllowedConnectionIDs: append([]string(nil), model.Rules.AllowedConnectionIDs...)}
	for _, value := range model.Rules.Entries {
		rule := DeveloperRule{ID: value.ID, Kind: string(value.Kind)}
		rule.VariantIDs = append(rule.VariantIDs, value.VariantIDs...)
		for _, slot := range value.SlotKinds {
			rule.SlotKinds = append(rule.SlotKinds, string(slot))
		}
		snapshot.DeveloperRules.Rules = append(snapshot.DeveloperRules.Rules, rule)
	}
	for _, value := range model.Logs.Entries {
		snapshot.Logs = append(snapshot.Logs, LogEntry{Timestamp: value.Timestamp, Component: value.Component, EventType: value.Type, Severity: value.Severity, Outcome: value.Outcome, ErrorCode: value.ErrorCode, ConnectionID: value.ConnectionID, ProfileID: value.ProfileID, DurationMS: value.DurationMS})
	}
	for _, value := range model.Diagnostics.Entries {
		snapshot.Diagnostics = append(snapshot.Diagnostics, DiagnosticEntry{Timestamp: value.Timestamp, Component: string(value.Component), Severity: string(value.Severity), Outcome: string(value.Outcome), ErrorCode: value.ErrorCode, ConnectionID: value.ConnectionID, ProfileID: value.ProfileID, DurationMS: value.DurationMS})
	}
	return sanitizeSnapshot(snapshot)
}

func renderableWarning(code string) bool {
	switch code {
	case previewapp.WarningConfigNotFound, previewapp.WarningConfigInvalid, previewapp.WarningConfigUnavailable,
		previewapp.WarningAuditUnavailable, previewapp.WarningDiagnosticsUnavailable, previewapp.WarningDiagnosticsInvalid,
		previewapp.WarningOverviewUnavailable:
		return true
	default:
		return false
	}
}
