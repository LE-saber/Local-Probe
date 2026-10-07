// Package previewapp contains the local, read-only data model used by the
// future desktop Preview UI.
//
// The package deliberately does not listen on a socket, start a process,
// resolve a credential, or manage a tunnel.  It is a small composition layer
// around the existing desktopadmin projection.  Lifecycle methods remain
// fail-closed until a production supervisor/runtime adapter is available.
package previewapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/LE-saber/Local-Probe/internal/auditreader"
	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/desktopadmin"
)

const (
	// SchemaVersion identifies the pure-data Preview model.  It is separate
	// from the desktopadmin wire contract so a UI can evolve independently.
	SchemaVersion = "local-probe.preview.v1"

	// MaxItems is the largest collection a Preview refresh or model can hold.
	MaxItems = desktopadmin.MaxItems

	defaultConfigFileName = "local-probe.json"
	defaultAuditDirName   = "audit"
	defaultProductName    = "Local-Probe Preview"
	defaultProductVersion = "preview"
)

var (
	ErrInvalidOptions    = errors.New("invalid preview options")
	ErrClosed            = errors.New("preview app is closed")
	ErrConfigUnavailable = errors.New("preview configuration is unavailable")
	ErrConfigInvalid     = errors.New("preview configuration is invalid")
	ErrAuditUnavailable  = errors.New("preview audit is unavailable")
	ErrAuditInvalid      = errors.New("preview audit record is invalid")
)

// ConfigState is the only configuration state exposed by the Preview model.
// An unsuccessful refresh clears the cached snapshot, so an older valid
// snapshot is never presented as the current configuration.
type ConfigState string

const (
	ConfigUnconfigured ConfigState = "unconfigured"
	ConfigConfigured   ConfigState = "configured"
	ConfigInvalid      ConfigState = "config_invalid"
)

// Action is the deliberately tiny lifecycle vocabulary understood by the
// Preview.  These actions do not carry a command, path, argv, or credential.
type Action string

const (
	ActionStart     Action = "start"
	ActionStop      Action = "stop"
	ActionReconnect Action = "reconnect"
)

// ProductionGateError is returned by every lifecycle method.  It is typed so
// a UI can disable the corresponding controls without parsing text.
type ProductionGateError struct {
	Action Action
	Code   string
}

func (e *ProductionGateError) Error() string {
	if e == nil {
		return "preview lifecycle action is unavailable behind the production gate"
	}
	return "preview lifecycle action is unavailable behind the production gate"
}

// Is lets callers use errors.Is(err, ErrProductionGate) while retaining the
// concrete action in an errors.As result.
func (e *ProductionGateError) Is(target error) bool {
	_, ok := target.(*ProductionGateError)
	return ok
}

// ErrProductionGate is the stable sentinel for the unavailable lifecycle
// capability.  Returned errors include the requested Action.
var ErrProductionGate = &ProductionGateError{Code: "production_gate"}

// Warning is a bounded, path-free model warning.  It intentionally has no
// free-form detail field: filesystem and implementation errors must not leak
// into the GUI model.
type Warning struct {
	Code string `json:"code"`
}

const (
	WarningConfigNotFound    = "config_not_found"
	WarningConfigInvalid     = "config_invalid"
	WarningConfigUnavailable = "config_unavailable"
	// WarningStatusProductionGate is retained for wire compatibility with
	// early Preview builds.  Runtime status is never a production gate: when
	// the trusted StatusSource is absent or fails, the model reports the
	// capability as unavailable.  Only lifecycle management actions use the
	// production gate.
	WarningStatusProductionGate      = "connection_status_production_gate"
	WarningStatusUnavailable         = "connection_status_unavailable"
	WarningManagementProductionGate  = "management_actions_production_gate"
	WarningDeveloperRulesUnavailable = "developer_rules_unavailable"
	WarningAuditUnavailable          = "audit_unavailable"
	WarningDiagnosticsUnavailable    = "diagnostics_unavailable"
	WarningDiagnosticsInvalid        = "diagnostics_invalid"
	WarningOverviewUnavailable       = "overview_unavailable"
)

var validWarningCodes = map[string]struct{}{
	WarningConfigNotFound:            {},
	WarningConfigInvalid:             {},
	WarningConfigUnavailable:         {},
	WarningStatusProductionGate:      {},
	WarningStatusUnavailable:         {},
	WarningManagementProductionGate:  {},
	WarningDeveloperRulesUnavailable: {},
	WarningAuditUnavailable:          {},
	WarningDiagnosticsUnavailable:    {},
	WarningDiagnosticsInvalid:        {},
	WarningOverviewUnavailable:       {},
}

// About is fixed product metadata.  It contains no resolved option path.
type About struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	SchemaVersion   string `json:"schema_version"`
	ProductionReady bool   `json:"production_ready"`
}

// Overview is the safe summary rendered by the GUI.  ConfigRevision is the
// SHA-256 revision produced by config.FileStore.Load; it is not a path or a
// credential reference.
type Overview struct {
	ConfigRevision         string `json:"config_revision,omitempty"`
	RootCount              int    `json:"root_count"`
	ProfileCount           int    `json:"profile_count"`
	ConnectionCount        int    `json:"connection_count"`
	EnabledConnectionCount int    `json:"enabled_connection_count"`
	StatusKnown            bool   `json:"status_known"`
	ReadyConnections       int    `json:"ready_connections,omitempty"`
	DegradedConnections    int    `json:"degraded_connections,omitempty"`
	ProductionReady        bool   `json:"production_ready"`
	Truncated              bool   `json:"truncated"`
}

// Connection is a path-free copy of desktopadmin.ConnectionStatus.
type Connection struct {
	ConnectionID     string                      `json:"connection_id"`
	Label            string                      `json:"label,omitempty"`
	LabelOmitted     bool                        `json:"label_omitted,omitempty"`
	ProfileID        string                      `json:"profile_id"`
	Enabled          bool                        `json:"enabled"`
	Transport        config.ConnectionTransport  `json:"transport"`
	TunnelConfigured bool                        `json:"tunnel_configured"`
	StatusKnown      bool                        `json:"status_known"`
	State            desktopadmin.LifecycleState `json:"state"`
	Attempt          int                         `json:"attempt,omitempty"`
	NextRetryAt      *time.Time                  `json:"next_retry_at,omitempty"`
	LastError        desktopadmin.StatusError    `json:"last_error,omitempty"`
}

// ConnectionList is the bounded connection projection.
type ConnectionList struct {
	StatusKnown bool         `json:"status_known"`
	Entries     []Connection `json:"entries"`
	Truncated   bool         `json:"truncated"`
}

// Rule is the safe developer-rule summary.  It excludes executable paths,
// argv literals, slot values, environment values, and root paths.
type Rule struct {
	ID         string                    `json:"id"`
	Kind       commandprofile.Kind       `json:"kind"`
	VariantIDs []string                  `json:"variant_ids"`
	SlotKinds  []commandprofile.SlotKind `json:"slot_kinds"`
}

// RuleSet is the bounded developer-mode projection.
type RuleSet struct {
	Enabled              bool     `json:"enabled"`
	AllowedConnectionIDs []string `json:"allowed_connection_ids"`
	Entries              []Rule   `json:"entries"`
	Truncated            bool     `json:"truncated"`
}

// LogEntry is the safe subset of one audit event accepted from an
// AuditReader.  There is intentionally no event ID, correlation ID, command,
// argv, environment, endpoint, path, request body, or output field.
type LogEntry struct {
	Timestamp    time.Time `json:"timestamp"`
	Component    string    `json:"component"`
	Type         string    `json:"type"`
	Severity     string    `json:"severity"`
	Outcome      string    `json:"outcome"`
	ErrorCode    string    `json:"error_code,omitempty"`
	ConnectionID string    `json:"connection_id,omitempty"`
	ProfileID    string    `json:"profile_id,omitempty"`
	Revision     string    `json:"revision,omitempty"`
	DurationMS   int64     `json:"duration_ms"`
}

// NewLogEntry is the narrow construction path for adapters around the future
// internal/auditreader package.  It validates the fixed audit vocabularies
// before a record can enter the Preview model.
func NewLogEntry(timestamp time.Time, component, eventType, severity, outcome, errorCode, connectionID, profileID, revision string, durationMS int64) (LogEntry, error) {
	entry := LogEntry{
		Timestamp: timestamp.UTC(), Component: component, Type: eventType,
		Severity: severity, Outcome: outcome, ErrorCode: errorCode,
		ConnectionID: connectionID, ProfileID: profileID, Revision: revision,
		DurationMS: durationMS,
	}
	if err := validateLogEntry(entry); err != nil {
		return LogEntry{}, ErrAuditInvalid
	}
	return entry, nil
}

// AuditSnapshot is the narrow adapter result expected from a future
// internal/auditreader implementation.  Both collections are bounded again
// by App even if an adapter violates the requested limit.
type AuditSnapshot struct {
	Logs                 []LogEntry
	Diagnostics          []desktopadmin.DiagnosticRecord
	LogsTruncated        bool
	DiagnosticsTruncated bool
}

// AuditReader supplies already parsed, safe audit data.  It must not expose
// raw JSON, paths, credentials, command lines, or process output.  The
// Preview passes MaxItems and performs a second validation before rendering.
type AuditReader interface {
	Read(context.Context, int) (AuditSnapshot, error)
}

// ViewModel is a pure-data, bounded snapshot for a GUI or tray adapter.
// Resolved ConfigPath and AuditDir are deliberately absent.
type ViewModel struct {
	SchemaVersion string                    `json:"schema_version"`
	ConfigState   ConfigState               `json:"config_state"`
	Overview      Overview                  `json:"overview"`
	Connections   ConnectionList            `json:"connections"`
	Rules         RuleSet                   `json:"rules"`
	Logs          LogView                   `json:"logs"`
	Diagnostics   DiagnosticView            `json:"diagnostics"`
	About         About                     `json:"about"`
	Capabilities  desktopadmin.Capabilities `json:"capabilities"`
	Warnings      []Warning                 `json:"warnings"`
}

// LogView is the bounded audit-log projection.
type LogView struct {
	Available bool       `json:"available"`
	Entries   []LogEntry `json:"entries"`
	Truncated bool       `json:"truncated"`
}

// DiagnosticView is the bounded typed-diagnostic projection.
type DiagnosticView struct {
	Available bool                      `json:"available"`
	Entries   []desktopadmin.Diagnostic `json:"entries"`
	Truncated bool                      `json:"truncated"`
}

// Options selects explicit local inputs for a Preview.  Empty paths resolve
// to the per-user Local-Probe directory.  The resolved values are retained
// internally only and never copied into ViewModel.
type Options struct {
	ConfigPath  string
	AuditDir    string
	AuditReader AuditReader
	Status      desktopadmin.StatusSource
}

// App is a thread-safe, local-only Preview application model.
type App struct {
	store         *config.FileStore
	cache         *configCache
	admin         *desktopadmin.Client
	audit         AuditReader
	diagnostics   *diagnosticCache
	status        desktopadmin.StatusSource
	configPath    string
	auditDir      string
	auditExplicit bool

	refreshMu sync.Mutex
	mu        sync.RWMutex
	closed    bool
	model     ViewModel
}

// New creates a Preview.  A missing or malformed configuration is not a
// constructor failure: the returned app starts with an explicit
// unconfigured/config_invalid model and can be refreshed after the user fixes
// the file.  Invalid option paths are constructor errors.
func New(options Options) (*App, error) {
	resolved, err := resolveOptions(options)
	if err != nil {
		return nil, err
	}
	store, err := config.NewFileStore(resolved.configPath)
	if err != nil {
		return nil, ErrInvalidOptions
	}

	cache := &configCache{}
	auditReader := options.AuditReader
	if auditReader == nil {
		// The file reader is an optional read-only adapter.  A missing audit
		// directory leaves diagnostics unavailable; it is not a Preview
		// construction failure and no directory is created here.
		if reader, readerErr := newFileAuditReader(resolved.auditDir); readerErr == nil {
			auditReader = reader
		}
	}
	diagnostics := &diagnosticCache{}
	deps := desktopadmin.Dependencies{Config: cache, Status: options.Status}
	deps.Diagnostics = diagnostics
	admin, err := desktopadmin.New(deps)
	if err != nil {
		return nil, ErrInvalidOptions
	}

	app := &App{
		store: store, cache: cache, admin: admin,
		audit: auditReader, diagnostics: diagnostics,
		status: options.Status, configPath: resolved.configPath,
		auditDir: resolved.auditDir, auditExplicit: options.AuditReader != nil,
	}
	app.model = app.baseModel(ConfigUnconfigured)
	// Initial loading is best effort.  The model contains the precise stable
	// state while New remains usable for a GUI with a missing config file.
	_, _ = app.Refresh(context.Background())
	return app, nil
}

// Refresh loads one complete FileStore snapshot and rebuilds the data model.
// The context is checked before and after the filesystem operation and before
// committing a replacement.  Concurrent refreshes are serialized; callers
// always receive a deep copy of the model.
func (a *App) Refresh(ctx context.Context) (ViewModel, error) {
	if a == nil {
		return ViewModel{}, ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	if err := a.ensureOpen(); err != nil {
		return a.Model(), err
	}
	if err := ctx.Err(); err != nil {
		return a.Model(), err
	}
	a.ensureAuditReader()

	previous, previousOK := a.cache.current()
	snapshot, err := a.store.Load()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return a.Model(), ctxErr
		}
		a.cache.clear()
		state, stableErr, warning := classifyConfigLoadError(err)
		model := a.baseModel(state)
		model.Warnings = addWarning(model.Warnings, warning)
		a.setModel(model)
		return cloneModel(model), stableErr
	}
	if err := ctx.Err(); err != nil {
		return a.Model(), err
	}

	a.cache.set(snapshot)
	model, buildErr := a.buildModel(ctx)
	if buildErr != nil {
		if errors.Is(buildErr, context.Canceled) || errors.Is(buildErr, context.DeadlineExceeded) {
			if previousOK {
				a.cache.set(previous)
			} else {
				a.cache.clear()
			}
			return a.Model(), buildErr
		}
		// A validated FileStore snapshot should not fail desktopadmin's local
		// projection.  Fail closed if an unexpected adapter error does occur.
		if previousOK {
			a.cache.set(previous)
		} else {
			a.cache.clear()
		}
		fallback := a.baseModel(ConfigInvalid)
		fallback.Warnings = addWarning(fallback.Warnings, WarningOverviewUnavailable)
		a.setModel(fallback)
		return cloneModel(fallback), ErrConfigInvalid
	}
	if err := ctx.Err(); err != nil {
		if previousOK {
			a.cache.set(previous)
		} else {
			a.cache.clear()
		}
		return a.Model(), err
	}
	a.setModel(model)
	return cloneModel(model), nil
}

// Model returns the most recent immutable data snapshot.  It never performs
// filesystem, network, process, or credential I/O.
func (a *App) Model() ViewModel {
	if a == nil {
		return ViewModel{}
	}
	a.mu.RLock()
	model := cloneModel(a.model)
	a.mu.RUnlock()
	return model
}

// Close closes only the Preview projection.  It never stops or reconnects an
// external MCP/Tunnel process and never mutates config or credentials.
func (a *App) Close() error {
	if a == nil {
		return ErrClosed
	}
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	return a.admin.Close()
}

// Exit is the tray-facing alias for Close.
func (a *App) Exit() error { return a.Close() }

// Start is intentionally unavailable until a production lifecycle adapter is
// integrated.  It performs no validation or side effect and always returns a
// typed production_gate error.
func (a *App) Start(context.Context, string) error {
	return productionGate(ActionStart)
}

// Stop is intentionally unavailable until a production lifecycle adapter is
// integrated.  It performs no validation or side effect and always returns a
// typed production_gate error.
func (a *App) Stop(context.Context, string) error {
	return productionGate(ActionStop)
}

// Reconnect is intentionally unavailable until a production lifecycle
// adapter is integrated.  It performs no validation or side effect and always
// returns a typed production_gate error.
func (a *App) Reconnect(context.Context, string) error {
	return productionGate(ActionReconnect)
}

func productionGate(action Action) error {
	return &ProductionGateError{Action: action, Code: ErrProductionGate.Code}
}

type resolvedOptions struct {
	configPath string
	auditDir   string
}

func resolveOptions(options Options) (resolvedOptions, error) {
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" || !filepath.IsAbs(base) {
		if options.ConfigPath == "" || options.AuditDir == "" {
			return resolvedOptions{}, ErrInvalidOptions
		}
	}
	configPath := options.ConfigPath
	if configPath == "" {
		configPath = filepath.Join(base, "Local-Probe", defaultConfigFileName)
	}
	auditDir := options.AuditDir
	if auditDir == "" {
		auditDir = filepath.Join(base, "Local-Probe", defaultAuditDirName)
	}
	if !validAbsolutePath(configPath) || !validAbsolutePath(auditDir) {
		return resolvedOptions{}, ErrInvalidOptions
	}
	return resolvedOptions{configPath: filepath.Clean(configPath), auditDir: filepath.Clean(auditDir)}, nil
}

func validAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && !strings.ContainsRune(value, 0)
}

func (a *App) ensureOpen() error {
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return nil
}

// ensureAuditReader retries construction of the default reader after a
// refresh.  The audit sink may create its directory after the Preview starts;
// this retry keeps the GUI refreshable without creating anything itself.
func (a *App) ensureAuditReader() {
	if a.audit != nil || a.auditExplicit || a.auditDir == "" {
		return
	}
	if reader, err := newFileAuditReader(a.auditDir); err == nil {
		a.audit = reader
	}
}

func (a *App) setModel(model ViewModel) {
	a.mu.Lock()
	if !a.closed {
		a.model = cloneModel(model)
	}
	a.mu.Unlock()
}

func (a *App) baseModel(state ConfigState) ViewModel {
	caps := a.admin.Capabilities()
	if state != ConfigConfigured {
		caps.Overview = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
		caps.DeveloperRules = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
		caps.ConnectionStatus = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
	}
	if a.status == nil {
		caps.ConnectionStatus = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
	}
	caps.ManagementActions = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityProductionGate}
	model := ViewModel{
		SchemaVersion: SchemaVersion,
		ConfigState:   state,
		Overview:      Overview{ProductionReady: false},
		Connections:   ConnectionList{Entries: []Connection{}},
		Rules:         RuleSet{AllowedConnectionIDs: []string{}, Entries: []Rule{}},
		Logs:          LogView{Entries: []LogEntry{}},
		Diagnostics:   DiagnosticView{Entries: []desktopadmin.Diagnostic{}},
		About:         About{Name: defaultProductName, Version: defaultProductVersion, SchemaVersion: SchemaVersion, ProductionReady: false},
		Capabilities:  caps,
		Warnings:      []Warning{{Code: WarningManagementProductionGate}},
	}
	if a.status == nil {
		model.Warnings = addWarning(model.Warnings, WarningStatusUnavailable)
	}
	if a.audit == nil {
		caps.Diagnostics = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
		model.Capabilities = caps
	}
	return model
}

func (a *App) buildModel(ctx context.Context) (ViewModel, error) {
	if err := ctx.Err(); err != nil {
		return ViewModel{}, err
	}
	model := a.baseModel(ConfigConfigured)
	overview, err := a.admin.GetOverview()
	if err != nil && !errors.Is(err, desktopadmin.ErrCapabilityUnavailable) {
		return ViewModel{}, err
	}
	model.Overview = Overview{
		ConfigRevision: overview.ConfigRevision,
		RootCount:      overview.RootCount, ProfileCount: overview.ProfileCount,
		ConnectionCount:        overview.ConnectionCount,
		EnabledConnectionCount: overview.EnabledConnectionCount,
		StatusKnown:            overview.StatusKnown,
		ReadyConnections:       overview.ReadyConnections,
		DegradedConnections:    overview.DegradedConnections,
		ProductionReady:        false, Truncated: overview.Truncated,
	}
	model.Capabilities = overview.Capabilities
	model.Capabilities.ManagementActions = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityProductionGate}
	if a.status == nil {
		model.Capabilities.ConnectionStatus = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
	}
	if errors.Is(err, desktopadmin.ErrCapabilityUnavailable) {
		model.Warnings = addWarning(model.Warnings, WarningStatusUnavailable)
		model.Capabilities.ConnectionStatus = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
	}

	statuses, statusErr := a.admin.GetConnectionStatuses()
	model.Connections = copyConnections(statuses)
	if statusErr != nil {
		if !errors.Is(statusErr, desktopadmin.ErrCapabilityUnavailable) {
			return ViewModel{}, statusErr
		}
		model.Warnings = addWarning(model.Warnings, WarningStatusUnavailable)
		model.Capabilities.ConnectionStatus = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
	}
	if err := ctx.Err(); err != nil {
		return ViewModel{}, err
	}

	rules, rulesErr := a.admin.GetDeveloperRules()
	if rulesErr != nil {
		model.Warnings = addWarning(model.Warnings, WarningDeveloperRulesUnavailable)
	} else {
		model.Rules = copyRules(rules)
	}

	if a.audit != nil {
		auditSnapshot, auditErr := readAudit(a.audit, ctx, MaxItems)
		if auditErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ViewModel{}, ctxErr
			}
			if a.diagnostics != nil {
				a.diagnostics.clear()
			}
			model.Warnings = addWarning(model.Warnings, WarningAuditUnavailable)
			model.Warnings = addWarning(model.Warnings, WarningDiagnosticsUnavailable)
			model.Capabilities.Diagnostics = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
		} else {
			logs, logsErr := copyLogs(auditSnapshot.Logs, auditSnapshot.LogsTruncated)
			if logsErr != nil {
				model.Warnings = addWarning(model.Warnings, WarningAuditUnavailable)
			} else {
				model.Logs = logs
			}
			if a.diagnostics != nil {
				a.diagnostics.set(auditSnapshot.Diagnostics)
			}
			diagnosticsTruncated := auditSnapshot.DiagnosticsTruncated || len(auditSnapshot.Diagnostics) > MaxItems
			diagnostics, diagnosticsErr := a.admin.GetDiagnostics(ctx)
			if diagnosticsErr != nil {
				model.Warnings = addWarning(model.Warnings, WarningDiagnosticsUnavailable)
				if errors.Is(diagnosticsErr, desktopadmin.ErrInvalidDiagnostics) {
					model.Warnings = addWarning(model.Warnings, WarningDiagnosticsInvalid)
				}
				model.Capabilities.Diagnostics = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
			} else {
				model.Diagnostics = DiagnosticView{Available: true, Entries: append([]desktopadmin.Diagnostic(nil), diagnostics.Entries...), Truncated: diagnostics.Truncated || diagnosticsTruncated}
			}
		}
	} else {
		model.Capabilities.Diagnostics = desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason}
	}
	return model, nil
}

func classifyConfigLoadError(err error) (ConfigState, error, string) {
	if errors.Is(err, config.ErrConfigNotFound) {
		return ConfigUnconfigured, ErrConfigUnavailable, WarningConfigNotFound
	}
	if errors.Is(err, config.ErrInvalid) {
		return ConfigInvalid, ErrConfigInvalid, WarningConfigInvalid
	}
	return ConfigInvalid, ErrConfigUnavailable, WarningConfigUnavailable
}

func addWarning(warnings []Warning, code string) []Warning {
	if _, ok := validWarningCodes[code]; !ok {
		return warnings
	}
	for _, warning := range warnings {
		if warning.Code == code {
			return warnings
		}
	}
	if len(warnings) >= MaxItems {
		return warnings
	}
	return append(warnings, Warning{Code: code})
}

func copyConnections(statuses desktopadmin.ConnectionStatusList) ConnectionList {
	entries := statuses.Entries
	if len(entries) > MaxItems {
		entries = entries[:MaxItems]
	}
	result := ConnectionList{StatusKnown: statuses.StatusKnown, Truncated: statuses.Truncated, Entries: make([]Connection, 0, len(entries))}
	for _, entry := range entries {
		copyEntry := Connection{
			ConnectionID: entry.ConnectionID, Label: entry.Label, LabelOmitted: entry.LabelOmitted,
			ProfileID: entry.ProfileID, Enabled: entry.Enabled, Transport: entry.Transport,
			TunnelConfigured: entry.TunnelConfigured, StatusKnown: entry.StatusKnown,
			State: entry.State, Attempt: entry.Attempt, LastError: entry.LastError,
		}
		if entry.NextRetryAt != nil {
			when := entry.NextRetryAt.UTC()
			copyEntry.NextRetryAt = &when
		}
		result.Entries = append(result.Entries, copyEntry)
	}
	return result
}

func copyRules(rules desktopadmin.DeveloperRules) RuleSet {
	allowed := append([]string(nil), rules.AllowedConnectionIDs...)
	if len(allowed) > MaxItems {
		allowed = allowed[:MaxItems]
	}
	entries := rules.Rules
	if len(entries) > MaxItems {
		entries = entries[:MaxItems]
	}
	result := RuleSet{Enabled: rules.Enabled, AllowedConnectionIDs: allowed, Entries: make([]Rule, 0, len(entries)), Truncated: len(rules.Rules) > MaxItems || len(rules.AllowedConnectionIDs) > MaxItems}
	for _, entry := range entries {
		variants := append([]string(nil), entry.VariantIDs...)
		if len(variants) > MaxItems {
			variants = variants[:MaxItems]
			result.Truncated = true
		}
		slots := append([]commandprofile.SlotKind(nil), entry.SlotKinds...)
		if len(slots) > MaxItems {
			slots = slots[:MaxItems]
			result.Truncated = true
		}
		result.Entries = append(result.Entries, Rule{ID: entry.ID, Kind: entry.Kind, VariantIDs: variants, SlotKinds: slots})
	}
	return result
}

func copyLogs(entries []LogEntry, truncated bool) (LogView, error) {
	if len(entries) > MaxItems {
		truncated = true
		entries = entries[:MaxItems]
	}
	result := LogView{Available: true, Entries: make([]LogEntry, 0, len(entries)), Truncated: truncated}
	for _, entry := range entries {
		if err := validateLogEntry(entry); err != nil {
			return LogView{}, ErrAuditInvalid
		}
		entry.Timestamp = entry.Timestamp.UTC()
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

func readAudit(reader AuditReader, ctx context.Context, maxItems int) (snapshot AuditSnapshot, err error) {
	defer func() {
		if recover() != nil {
			snapshot = AuditSnapshot{}
			err = ErrAuditUnavailable
		}
	}()
	return reader.Read(ctx, maxItems)
}

func validateLogEntry(entry LogEntry) error {
	if entry.Timestamp.IsZero() || entry.DurationMS < 0 || entry.DurationMS > 24*60*60*1000 {
		return ErrAuditInvalid
	}
	if !oneOf(entry.Component, "MCP", "auth", "policy", "fs-search", "command", "network-tunnel", "service-config", "error") {
		return ErrAuditInvalid
	}
	if !oneOf(entry.Type, "mcp.call", "mcp.result", "mcp.list", "auth.accept", "auth.reject", "policy.decision", "fs.search", "command.admission", "command.start", "command.result", "command.reject", "command.exit", "network.connect", "tunnel.state", "config.change", "app.error") {
		return ErrAuditInvalid
	}
	if !oneOf(entry.Severity, "debug", "info", "warn", "error", "critical") || !oneOf(entry.Outcome, "started", "succeeded", "failed", "rejected", "degraded") {
		return ErrAuditInvalid
	}
	for _, value := range []string{entry.ErrorCode, entry.ConnectionID, entry.ProfileID, entry.Revision} {
		if value != "" && !safeAtom(value, 128) {
			return ErrAuditInvalid
		}
	}
	return nil
}

func safeAtom(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || strings.ContainsRune(value, 0) {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"token", "secret", "password", "passwd", "private", "bearer", "jwt", "cookie", "authorization", "api-key", "apikey"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:/+-", r)) {
			return false
		}
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// fileAuditReader adapts the concrete bounded JSONL reader without exposing
// its directory or file-selection details to the Preview model.  Keeping the
// adapter here lets the package compile independently of a future reader
// implementation while using the current internal/auditreader when present.
type fileAuditReader struct {
	reader *auditreader.Reader
}

func newFileAuditReader(directory string) (AuditReader, error) {
	reader, err := auditreader.New(directory, auditreader.DefaultLimits())
	if err != nil {
		return nil, err
	}
	return fileAuditReader{reader: reader}, nil
}

// AdaptAuditReader bridges the current concrete auditreader.Reader to the
// narrow Preview interface.  It is useful to callers that already own a
// reader with custom limits; the adapter still performs the Preview's second
// validation and collection bound.
func AdaptAuditReader(reader *auditreader.Reader) AuditReader {
	if reader == nil {
		return nil
	}
	return fileAuditReader{reader: reader}
}

func (r fileAuditReader) Read(ctx context.Context, maxItems int) (AuditSnapshot, error) {
	if r.reader == nil || maxItems < 1 || maxItems > MaxItems {
		return AuditSnapshot{}, ErrAuditUnavailable
	}
	// Apply the Preview request before the audit scan.  Trimming the report
	// after a default 128-record read would honor the output size but not the
	// caller's resource bound.
	report, err := r.reader.ReadWithMaxItems(ctx, maxItems)
	if err != nil {
		return AuditSnapshot{}, err
	}
	logs := make([]LogEntry, 0, len(report.Entries))
	diagnostics := make([]desktopadmin.DiagnosticRecord, 0, len(report.Entries))
	truncated := report.Summary.FileLimitReached || report.Summary.ByteLimitReached || report.Summary.LineLimitReached || report.Summary.RecordLimitReached
	for _, entry := range report.Entries {
		logEntry, err := NewLogEntry(entry.Timestamp, string(entry.Component), string(entry.Type), string(entry.Severity), string(entry.Outcome), entry.ErrorCode, entry.Connection, entry.Profile, entry.Revision, entry.DurationMS)
		if err != nil {
			return AuditSnapshot{}, ErrAuditInvalid
		}
		logs = append(logs, logEntry)
		// FileStore revisions are SHA-256 digests (`sha256:<64 hex>`), while
		// desktopadmin's local DiagnosticRecord intentionally accepts only the
		// identifier-shaped revision vocabulary.  Keep the full digest in the
		// in-process log projection, but omit it from the typed diagnostic
		// projection rather than making the whole audit refresh unavailable.
		diagnosticRevision := safeDiagnosticRevision(entry.Revision)
		diagnostic, err := desktopadmin.NewDiagnosticRecord(entry.Timestamp, desktopadmin.DiagnosticComponent(entry.Component), desktopadmin.DiagnosticSeverity(entry.Severity), desktopadmin.DiagnosticOutcome(entry.Outcome), entry.ErrorCode, entry.Connection, entry.Profile, diagnosticRevision, entry.DurationMS)
		if err != nil {
			return AuditSnapshot{}, ErrAuditInvalid
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	if len(logs) > maxItems {
		logs = logs[:maxItems]
		truncated = true
	}
	if len(diagnostics) > maxItems {
		diagnostics = diagnostics[:maxItems]
		truncated = true
	}
	return AuditSnapshot{Logs: logs, Diagnostics: diagnostics, LogsTruncated: truncated, DiagnosticsTruncated: truncated}, nil
}

func safeDiagnosticRevision(value string) string {
	if value == "" {
		return ""
	}
	if len(value) >= 2 && len(value) <= 21 && value[0] == 'r' {
		for _, r := range value[1:] {
			if r < '0' || r > '9' {
				return ""
			}
		}
		return value
	}
	// SHA-256 revisions are deliberately omitted.  The digest is not needed
	// for a user-facing diagnostic row and cannot be represented by the
	// stricter desktopadmin identifier type without inventing a new schema.
	if len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") {
		for _, r := range value[len("sha256:"):] {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return ""
			}
		}
	}
	return ""
}

type configCache struct {
	mu       sync.RWMutex
	snapshot config.Snapshot
	valid    bool
}

func (c *configCache) Snapshot() config.Snapshot {
	if c == nil {
		return config.Snapshot{}
	}
	c.mu.RLock()
	snapshot, valid := c.snapshot, c.valid
	c.mu.RUnlock()
	if !valid {
		return config.Snapshot{}
	}
	return snapshot
}

func (c *configCache) set(snapshot config.Snapshot) {
	c.mu.Lock()
	c.snapshot, c.valid = snapshot, true
	c.mu.Unlock()
}

func (c *configCache) clear() {
	c.mu.Lock()
	c.snapshot, c.valid = config.Snapshot{}, false
	c.mu.Unlock()
}

func (c *configCache) current() (config.Snapshot, bool) {
	c.mu.RLock()
	snapshot, valid := c.snapshot, c.valid
	c.mu.RUnlock()
	return snapshot, valid
}

type diagnosticCache struct {
	mu        sync.RWMutex
	records   []desktopadmin.DiagnosticRecord
	available bool
}

func (c *diagnosticCache) set(records []desktopadmin.DiagnosticRecord) {
	c.mu.Lock()
	c.records = append([]desktopadmin.DiagnosticRecord(nil), records...)
	c.available = true
	c.mu.Unlock()
}

func (c *diagnosticCache) clear() {
	c.mu.Lock()
	c.records = nil
	c.available = false
	c.mu.Unlock()
}

func (c *diagnosticCache) ReadDiagnostics(ctx context.Context, maxItems int) ([]desktopadmin.DiagnosticRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	available := c.available
	records := append([]desktopadmin.DiagnosticRecord(nil), c.records...)
	c.mu.RUnlock()
	if !available {
		return nil, desktopadmin.ErrCapabilityUnavailable
	}
	if maxItems < 0 {
		return nil, desktopadmin.ErrInvalidLimit
	}
	if maxItems > 0 && len(records) > maxItems {
		records = records[:maxItems]
	}
	return records, nil
}

func cloneModel(model ViewModel) ViewModel {
	model.Warnings = append([]Warning(nil), model.Warnings...)
	model.Connections.Entries = append([]Connection(nil), model.Connections.Entries...)
	for i := range model.Connections.Entries {
		if model.Connections.Entries[i].NextRetryAt != nil {
			when := model.Connections.Entries[i].NextRetryAt.UTC()
			model.Connections.Entries[i].NextRetryAt = &when
		}
	}
	model.Rules.AllowedConnectionIDs = append([]string(nil), model.Rules.AllowedConnectionIDs...)
	model.Rules.Entries = append([]Rule(nil), model.Rules.Entries...)
	for i := range model.Rules.Entries {
		model.Rules.Entries[i].VariantIDs = append([]string(nil), model.Rules.Entries[i].VariantIDs...)
		model.Rules.Entries[i].SlotKinds = append([]commandprofile.SlotKind(nil), model.Rules.Entries[i].SlotKinds...)
	}
	model.Logs.Entries = append([]LogEntry(nil), model.Logs.Entries...)
	model.Diagnostics.Entries = append([]desktopadmin.Diagnostic(nil), model.Diagnostics.Entries...)
	return model
}
