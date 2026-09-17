// Package previewui contains the deliberately small, local-only surface used
// by the Windows Preview window and tray icon.
//
// The package does not own a supervisor, credentials, policy, or a network
// listener.  A ViewModel supplies an already-sanitized snapshot and an
// already-sanitized diagnostic export.  Keeping that seam here means the
// native window remains independent from the eventual previewapp composition
// package.
package previewui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	SchemaVersion = "local-probe.preview.v1"
	Version       = "R9-preview"

	MaxItems       = 128
	MaxTextBytes   = 512
	MaxExportBytes = 256 << 10
)

var (
	ErrInvalidModel        = errors.New("invalid preview model")
	ErrUnavailable         = errors.New("preview capability unavailable")
	ErrClosed              = errors.New("preview UI is closed")
	ErrExportLimit         = errors.New("preview diagnostic export exceeds size limit")
	ErrAlreadyRunning      = errors.New("preview is already running")
	ErrUnsupportedPlatform = errors.New("preview is unsupported on this platform")
)

// RunOptions contains only local UI dependencies. In particular it has no
// listener, process, credential, or network setting. Connector is an
// optional, typed seam for the application-owned local lifecycle backend; the
// UI never accepts a command line or a shell callback.
type RunOptions struct {
	Model            ViewModel
	Connector        Connector
	WorkspaceManager WorkspaceManager
	Title            string
}

// ViewModel is the only dependency required by the native UI.  Implementers
// must return a bounded, secret-free snapshot.  Refresh may perform bounded
// local reads, but must not perform network or process I/O on the UI goroutine.
// ExportDiagnostics must return a fixed, sanitized support/diagnostic document
// and must not return raw config or audit files.
type ViewModel interface {
	Refresh(context.Context) (Snapshot, error)
	ExportDiagnostics(context.Context) ([]byte, error)
}

// Snapshot is the render model for the Preview window.  It intentionally
// contains counts, constrained identifiers, enums, and stable error codes
// only. It has no paths, URLs, commands, arguments, environment values,
// credentials, tunnel IDs, or raw error strings.
type Snapshot struct {
	SchemaVersion     string            `json:"schema_version"`
	GeneratedAt       time.Time         `json:"generated_at"`
	ProductionReady   bool              `json:"production_ready"`
	Unconfigured      bool              `json:"unconfigured"`
	Status            string            `json:"status"`
	ErrorCode         string            `json:"error_code,omitempty"`
	Overview          Overview          `json:"overview"`
	Connections       []Connection      `json:"connections"`
	DeveloperRules    DeveloperRules    `json:"developer_rules"`
	Audit             AuditStatus       `json:"audit"`
	Logs              []LogEntry        `json:"logs"`
	Diagnostics       []DiagnosticEntry `json:"diagnostics"`
	ManagementActions ActionCapability  `json:"management_actions"`
	Omitted           []string          `json:"omitted,omitempty"`
}

type Overview struct {
	ConfigRevision         string `json:"config_revision,omitempty"`
	RootCount              int    `json:"root_count"`
	ProfileCount           int    `json:"profile_count"`
	ConnectionCount        int    `json:"connection_count"`
	EnabledConnectionCount int    `json:"enabled_connection_count"`
	StatusKnown            bool   `json:"status_known"`
	ReadyConnections       int    `json:"ready_connections,omitempty"`
	DegradedConnections    int    `json:"degraded_connections,omitempty"`
	Truncated              bool   `json:"truncated"`
}

type Connection struct {
	ConnectionID     string `json:"connection_id"`
	Label            string `json:"label,omitempty"`
	LabelOmitted     bool   `json:"label_omitted,omitempty"`
	ProfileID        string `json:"profile_id"`
	Enabled          bool   `json:"enabled"`
	Transport        string `json:"transport"`
	TunnelConfigured bool   `json:"tunnel_configured"`
	StatusKnown      bool   `json:"status_known"`
	State            string `json:"state"`
	Attempt          int    `json:"attempt,omitempty"`
	LastError        string `json:"last_error,omitempty"`
}

type DeveloperRules struct {
	Enabled              bool            `json:"enabled"`
	AllowedConnectionIDs []string        `json:"allowed_connection_ids"`
	Rules                []DeveloperRule `json:"rules"`
	Available            bool            `json:"available"`
}

type DeveloperRule struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	VariantIDs []string `json:"variant_ids"`
	SlotKinds  []string `json:"slot_kinds"`
}

type AuditStatus struct {
	Available bool   `json:"available"`
	Status    string `json:"status"`
	Records   int    `json:"records"`
	Dropped   uint64 `json:"dropped,omitempty"`
	Corrupt   bool   `json:"corrupt"`
	Truncated bool   `json:"truncated"`
}

type LogEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	Component    string    `json:"component"`
	EventType    string    `json:"event_type"`
	Severity     string    `json:"severity"`
	Outcome      string    `json:"outcome"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ConnectionID string    `json:"connection_id,omitempty"`
	ProfileID    string    `json:"profile_id,omitempty"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
}

type DiagnosticEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	Component    string    `json:"component"`
	Severity     string    `json:"severity"`
	Outcome      string    `json:"outcome"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ConnectionID string    `json:"connection_id,omitempty"`
	ProfileID    string    `json:"profile_id,omitempty"`
	DurationMS   int64     `json:"duration_ms,omitempty"`
}

type ActionCapability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// Section identifies one of the bounded views in the native window.
type Section int

const (
	SectionOverview Section = iota
	SectionConnections
	SectionWorkspaceAccess
	SectionDeveloperRules
	SectionLogs
	SectionAbout
)

func (s Section) valid() bool { return s >= SectionOverview && s <= SectionAbout }

// UnconfiguredModel keeps the executable useful before a config or a future
// previewapp adapter is available.  It never attempts to infer state.
type UnconfiguredModel struct{}

func (UnconfiguredModel) Refresh(ctx context.Context) (Snapshot, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
	}
	return unconfiguredSnapshot(), nil
}

func (UnconfiguredModel) ExportDiagnostics(ctx context.Context) ([]byte, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return marshalExport(unconfiguredSnapshot())
}

func unconfiguredSnapshot() Snapshot {
	return Snapshot{
		SchemaVersion:     SchemaVersion,
		GeneratedAt:       time.Now().UTC(),
		ProductionReady:   false,
		Unconfigured:      true,
		Status:            "unconfigured",
		ManagementActions: ActionCapability{Available: false, Reason: "production_gate"},
		Audit:             AuditStatus{Available: false, Status: "unavailable"},
		Omitted:           []string{"paths", "credentials", "network_endpoints", "process_details"},
	}
}

// Render returns a bounded textual representation suitable for a read-only
// native EDIT control.  The renderer is intentionally deterministic and
// performs a second presentation-layer sanitization even when the model has
// already validated its fields.
func Render(snapshot Snapshot, section Section) string {
	if !section.valid() {
		section = SectionOverview
	}
	snapshot = sanitizeSnapshot(snapshot)
	switch section {
	case SectionOverview:
		return renderOverview(snapshot)
	case SectionConnections:
		return renderConnections(snapshot)
	case SectionWorkspaceAccess:
		return renderWorkspaceAccess()
	case SectionDeveloperRules:
		return renderDeveloperRules(snapshot)
	case SectionLogs:
		return renderLogs(snapshot)
	case SectionAbout:
		return renderAbout(snapshot)
	default:
		return renderOverview(snapshot)
	}
}

func renderWorkspaceAccess() string {
	var b strings.Builder
	b.WriteString("Workspace Access\r\n=================\r\n\r\n")
	b.WriteString("This page is managed by the local Preview workspace manager.\r\n")
	b.WriteString("Folder paths are intentionally not included in snapshots or diagnostic exports.\r\n")
	b.WriteString("Use the Windows workspace page to add, select, or remove authorized folders.\r\n")
	return b.String()
}

func renderOverview(s Snapshot) string {
	var b strings.Builder
	b.WriteString("Local-Probe Preview\r\n")
	b.WriteString("===================\r\n\r\n")
	if s.Unconfigured {
		b.WriteString("Status: UNCONFIGURED\r\n")
		b.WriteString("No local configuration is available. The Preview remains read-only.\r\n")
	} else {
		fmt.Fprintf(&b, "Status: %s\r\n", displayAtom(s.Status, "unknown"))
		if s.ErrorCode != "" {
			fmt.Fprintf(&b, "Error: %s\r\n", displayAtom(s.ErrorCode, "unavailable"))
		}
	}
	fmt.Fprintf(&b, "Configuration revision: %s\r\n", displayAtom(s.Overview.ConfigRevision, "unavailable"))
	fmt.Fprintf(&b, "Roots: %d    Profiles: %d    Connections: %d    Enabled: %d\r\n", s.Overview.RootCount, s.Overview.ProfileCount, s.Overview.ConnectionCount, s.Overview.EnabledConnectionCount)
	if s.Overview.StatusKnown {
		fmt.Fprintf(&b, "Ready: %d    Degraded: %d\r\n", s.Overview.ReadyConnections, s.Overview.DegradedConnections)
	} else {
		b.WriteString("Connection runtime status: unavailable\r\n")
	}
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "Audit: %s (%d records)", displayAtom(s.Audit.Status, "unavailable"), s.Audit.Records)
	if s.Audit.Corrupt {
		b.WriteString("; corrupt records skipped")
	}
	if s.Audit.Truncated || s.Overview.Truncated {
		b.WriteString("; bounded/truncated")
	}
	b.WriteString("\r\n\r\n")
	b.WriteString("Lifecycle actions: DISABLED (production_gate)\r\n")
	b.WriteString("Start / Stop / Reconnect are visible for preview only and do not control MCP or Tunnel.\r\n")
	b.WriteString("Omitted: paths, credentials, network endpoints, process details, command lines, output, and file contents.\r\n")
	return b.String()
}

func renderConnections(s Snapshot) string {
	var b strings.Builder
	b.WriteString("Connections\r\n===========\r\n\r\n")
	if len(s.Connections) == 0 {
		b.WriteString("No configured connections.\r\n")
		return b.String()
	}
	for i, c := range s.Connections {
		fmt.Fprintf(&b, "%d. %s\r\n", i+1, displayLabel(c.Label, c.LabelOmitted, c.ConnectionID))
		fmt.Fprintf(&b, "   id=%s  profile=%s  enabled=%t  transport=%s\r\n", displayAtom(c.ConnectionID, "omitted"), displayAtom(c.ProfileID, "omitted"), c.Enabled, displayAtom(c.Transport, "unknown"))
		if c.StatusKnown {
			fmt.Fprintf(&b, "   state=%s  attempt=%d", displayAtom(c.State, "unknown"), c.Attempt)
			if c.LastError != "" {
				fmt.Fprintf(&b, "  error=%s", displayAtom(c.LastError, "unavailable"))
			}
			b.WriteString("\r\n")
		} else {
			b.WriteString("   runtime status=unavailable\r\n")
		}
	}
	return b.String()
}

func renderDeveloperRules(s Snapshot) string {
	var b strings.Builder
	b.WriteString("Developer Rules\r\n================\r\n\r\n")
	if !s.DeveloperRules.Available {
		b.WriteString("Developer rule projection: unavailable\r\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Developer mode enabled: %t\r\n", s.DeveloperRules.Enabled)
	fmt.Fprintf(&b, "Allowed connections: %s\r\n", joinAtoms(s.DeveloperRules.AllowedConnectionIDs, "none"))
	if len(s.DeveloperRules.Rules) == 0 {
		b.WriteString("Rules: none\r\n")
		return b.String()
	}
	b.WriteString("Rules:\r\n")
	for _, rule := range s.DeveloperRules.Rules {
		fmt.Fprintf(&b, "- %s (%s)\r\n", displayAtom(rule.ID, "omitted"), displayAtom(rule.Kind, "unknown"))
		fmt.Fprintf(&b, "  variants=%s  slots=%s\r\n", joinAtoms(rule.VariantIDs, "none"), joinAtoms(rule.SlotKinds, "none"))
	}
	b.WriteString("\r\nThis view is informational; it cannot change policy or enable commands.\r\n")
	return b.String()
}

func renderLogs(s Snapshot) string {
	var b strings.Builder
	b.WriteString("Logs / Diagnostics\r\n===================\r\n\r\n")
	fmt.Fprintf(&b, "Audit status: %s", displayAtom(s.Audit.Status, "unavailable"))
	if s.Audit.Corrupt {
		b.WriteString("; corrupt records skipped")
	}
	if s.Audit.Truncated {
		b.WriteString("; view bounded")
	}
	b.WriteString("\r\n\r\n")
	if len(s.Logs) == 0 {
		b.WriteString("No sanitized audit records are available.\r\n")
	} else {
		for _, entry := range s.Logs {
			fmt.Fprintf(&b, "%s  %s/%s  %s  %s", entry.Timestamp.Format(time.RFC3339), displayAtom(entry.Component, "unknown"), displayAtom(entry.EventType, "unknown"), displayAtom(entry.Severity, "unknown"), displayAtom(entry.Outcome, "unknown"))
			if entry.ErrorCode != "" {
				fmt.Fprintf(&b, "  error=%s", displayAtom(entry.ErrorCode, "unavailable"))
			}
			if entry.ConnectionID != "" {
				fmt.Fprintf(&b, "  connection=%s", displayAtom(entry.ConnectionID, "omitted"))
			}
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\r\nDiagnostics are typed, bounded, and message-free; raw paths, commands, output, and credentials are omitted.\r\n")
	return b.String()
}

func renderAbout(s Snapshot) string {
	var b strings.Builder
	b.WriteString("About\r\n=====\r\n\r\n")
	fmt.Fprintf(&b, "Product: Local-Probe\r\nPreview: %s\r\nSchema: %s\r\n", Version, SchemaVersion)
	fmt.Fprintf(&b, "Production-ready lifecycle controls: %t\r\n", s.ProductionReady)
	b.WriteString("\r\nThis is a local, read-only Windows Preview. Closing the window hides it to the tray; Exit only exits Preview and leaves MCP/Tunnel ownership untouched.\r\n")
	b.WriteString("\r\nOmitted by design: filesystem paths, endpoints, tunnel identifiers, credentials, process details, command lines, environment values, file contents, and raw error messages.\r\n")
	return b.String()
}

func sanitizeSnapshot(s Snapshot) Snapshot {
	if s.SchemaVersion != SchemaVersion {
		s.SchemaVersion = SchemaVersion
	}
	if s.GeneratedAt.IsZero() {
		s.GeneratedAt = time.Now().UTC()
	} else {
		s.GeneratedAt = s.GeneratedAt.UTC()
	}
	s.Status = sanitizeAtom(s.Status, 64)
	s.ErrorCode = sanitizeErrorCode(s.ErrorCode)
	s.Overview = sanitizeOverview(s.Overview)
	s.Connections = sanitizeConnections(s.Connections)
	s.DeveloperRules = sanitizeDeveloperRules(s.DeveloperRules)
	s.Audit.Status = sanitizeAtom(s.Audit.Status, 64)
	if len(s.Logs) > MaxItems {
		s.Logs = append([]LogEntry(nil), s.Logs[:MaxItems]...)
		s.Audit.Truncated = true
	}
	if len(s.Diagnostics) > MaxItems {
		s.Diagnostics = append([]DiagnosticEntry(nil), s.Diagnostics[:MaxItems]...)
		s.Audit.Truncated = true
	}
	for i := range s.Logs {
		s.Logs[i] = sanitizeLogEntry(s.Logs[i])
	}
	for i := range s.Diagnostics {
		s.Diagnostics[i] = sanitizeDiagnostic(s.Diagnostics[i])
	}
	s.ManagementActions = ActionCapability{Available: false, Reason: "production_gate"}
	s.Omitted = sanitizeAtoms(s.Omitted, MaxItems)
	return s
}

func sanitizeOverview(o Overview) Overview {
	o.ConfigRevision = sanitizeRevision(o.ConfigRevision)
	for _, p := range []*int{&o.RootCount, &o.ProfileCount, &o.ConnectionCount, &o.EnabledConnectionCount, &o.ReadyConnections, &o.DegradedConnections} {
		if *p < 0 {
			*p = 0
		}
		if *p > MaxItems {
			*p = MaxItems
		}
	}
	return o
}

func sanitizeConnections(values []Connection) []Connection {
	if len(values) > MaxItems {
		values = values[:MaxItems]
	}
	out := make([]Connection, len(values))
	for i, value := range values {
		value.ConnectionID = sanitizeIdentifier(value.ConnectionID)
		value.ProfileID = sanitizeIdentifier(value.ProfileID)
		value.Label = sanitizeLabel(value.Label)
		if value.Label == "" && value.LabelOmitted == false && value.ConnectionID == "omitted" {
			value.LabelOmitted = true
		}
		value.Transport = sanitizeTransport(value.Transport)
		value.State = sanitizeState(value.State)
		value.LastError = sanitizeStatusError(value.LastError)
		if value.Attempt < 0 {
			value.Attempt = 0
		}
		if value.Attempt > 1<<20 {
			value.Attempt = 1 << 20
		}
		out[i] = value
	}
	return out
}

func sanitizeDeveloperRules(r DeveloperRules) DeveloperRules {
	if len(r.AllowedConnectionIDs) > MaxItems {
		r.AllowedConnectionIDs = r.AllowedConnectionIDs[:MaxItems]
	}
	r.AllowedConnectionIDs = sanitizeIdentifiers(r.AllowedConnectionIDs)
	if len(r.Rules) > MaxItems {
		r.Rules = r.Rules[:MaxItems]
	}
	for i := range r.Rules {
		r.Rules[i].ID = sanitizeIdentifier(r.Rules[i].ID)
		r.Rules[i].Kind = sanitizeAtom(r.Rules[i].Kind, 64)
		if len(r.Rules[i].VariantIDs) > MaxItems {
			r.Rules[i].VariantIDs = r.Rules[i].VariantIDs[:MaxItems]
		}
		if len(r.Rules[i].SlotKinds) > MaxItems {
			r.Rules[i].SlotKinds = r.Rules[i].SlotKinds[:MaxItems]
		}
		r.Rules[i].VariantIDs = sanitizeIdentifiers(r.Rules[i].VariantIDs)
		r.Rules[i].SlotKinds = sanitizeAtoms(r.Rules[i].SlotKinds, MaxItems)
	}
	return r
}

func sanitizeLogEntry(e LogEntry) LogEntry {
	e.Timestamp = e.Timestamp.UTC()
	e.Component = sanitizeAtom(e.Component, 64)
	e.EventType = sanitizeAtom(e.EventType, 128)
	e.Severity = sanitizeAtom(e.Severity, 32)
	e.Outcome = sanitizeAtom(e.Outcome, 32)
	e.ErrorCode = sanitizeErrorCode(e.ErrorCode)
	e.ConnectionID = sanitizeOptionalIdentifier(e.ConnectionID)
	e.ProfileID = sanitizeOptionalIdentifier(e.ProfileID)
	if e.DurationMS < 0 || e.DurationMS > 24*60*60*1000 {
		e.DurationMS = 0
	}
	return e
}

func sanitizeDiagnostic(e DiagnosticEntry) DiagnosticEntry {
	value := sanitizeLogEntry(LogEntry{Timestamp: e.Timestamp, Component: e.Component, EventType: "diagnostic", Severity: e.Severity, Outcome: e.Outcome, ErrorCode: e.ErrorCode, ConnectionID: e.ConnectionID, ProfileID: e.ProfileID, DurationMS: e.DurationMS})
	e.Timestamp, e.Component, e.Severity, e.Outcome = value.Timestamp, value.Component, value.Severity, value.Outcome
	e.ErrorCode, e.ConnectionID, e.ProfileID, e.DurationMS = value.ErrorCode, value.ConnectionID, value.ProfileID, value.DurationMS
	return e
}

func sanitizeIdentifier(v string) string {
	if v == "" {
		return "omitted"
	}
	if len(v) > 128 {
		return "omitted"
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "omitted"
		}
	}
	return v
}

func sanitizeOptionalIdentifier(v string) string {
	if v == "" {
		return ""
	}
	result := sanitizeIdentifier(v)
	if result == "omitted" {
		return ""
	}
	return result
}

func sanitizeIdentifiers(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if safe := sanitizeOptionalIdentifier(value); safe != "" {
			out = append(out, safe)
		}
	}
	return out
}

func sanitizeAtoms(values []string, max int) []string {
	if len(values) > max {
		values = values[:max]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if safe := sanitizeAtom(value, 128); safe != "" {
			out = append(out, safe)
		}
	}
	return out
}

func sanitizeAtom(v string, max int) string {
	if v == "" || len(v) > max || !utf8.ValidString(v) {
		return ""
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return ""
		}
	}
	return v
}

func sanitizeLabel(v string) string {
	if v == "" || len(v) > 128 || !utf8.ValidString(v) {
		return ""
	}
	for _, r := range v {
		if unicode.IsControl(r) || strings.ContainsRune(`/\\:`, r) {
			return ""
		}
	}
	return v
}

func sanitizeRevision(v string) string {
	if v == "" {
		return ""
	}
	if len(v) >= 2 && len(v) <= 21 && v[0] == 'r' {
		for _, r := range v[1:] {
			if r < '0' || r > '9' {
				return ""
			}
		}
		return v
	}
	if len(v) == len("sha256:")+64 && strings.HasPrefix(v, "sha256:") {
		for _, r := range v[len("sha256:"):] {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return ""
			}
		}
		return v
	}
	return ""
}

func sanitizeTransport(v string) string {
	switch v {
	case "local", "openai_runtime", "cloudflare_named":
		return v
	default:
		return "unknown"
	}
}

func sanitizeState(v string) string {
	if v == "" {
		return "unavailable"
	}
	return sanitizeAtom(v, 64)
}

func sanitizeStatusError(v string) string {
	if v == "" {
		return ""
	}
	return sanitizeErrorCode(v)
}

func sanitizeErrorCode(v string) string {
	if v == "" {
		return ""
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return "unavailable"
		}
	}
	if len(v) > 64 {
		return "unavailable"
	}
	return v
}

func displayAtom(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func displayLabel(label string, omitted bool, id string) string {
	if label != "" && !omitted {
		return label
	}
	if id != "" {
		return "(label omitted) " + displayAtom(sanitizeIdentifier(id), "connection")
	}
	return "(label omitted)"
}

func joinAtoms(values []string, fallback string) string {
	if len(values) == 0 {
		return fallback
	}
	return strings.Join(values, ", ")
}

func marshalExport(snapshot Snapshot) ([]byte, error) {
	value := sanitizeSnapshot(snapshot)
	value.SchemaVersion = SchemaVersion
	value.Omitted = append(value.Omitted, "raw_config", "raw_audit_lines")
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return ValidateAndScrubExport(data)
}

// ValidateExport is a small boundary check for adapters supplied by
// previewapp.  It rejects empty/non-JSON/oversized payloads; the UI never
// displays adapter error text.
func ValidateExport(data []byte) error {
	_, err := ValidateAndScrubExport(data)
	return err
}
