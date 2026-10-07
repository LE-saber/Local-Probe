// Package desktopadmin contains the local projection consumed by a future
// desktop GUI or tray client.
//
// This package is deliberately a client-side contract, not a management
// server. It does not listen on a socket, start a process, resolve a
// credential, or own a supervisor. In particular, the lifecycle action seam
// remains unavailable while the R6 supervisor is a non-production core.
package desktopadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/supervisor"
)

const (
	// ProductionReady is intentionally false. The package only projects local
	// state and keeps the future lifecycle adapter fail closed.
	ProductionReady = false

	SchemaVersion = "local-probe.desktop-admin.v1"

	// All wire-facing projections use these bounds. They are deliberately
	// smaller than the configuration and audit storage limits so a GUI cannot
	// accidentally render an unbounded local state dump.
	MaxWireBytes     = 64 << 10
	MaxItems         = 128
	MaxLabelBytes    = 128
	MaxActionRequest = 4096
	MaxAttempt       = 1 << 20
)

var (
	ErrInvalidConfig         = errors.New("invalid desktop admin configuration")
	ErrInvalidRequest        = errors.New("invalid desktop admin request")
	ErrInvalidDiagnostics    = errors.New("invalid desktop admin diagnostics")
	ErrInvalidLimit          = errors.New("invalid desktop admin limit")
	ErrCapabilityUnavailable = errors.New("desktop admin capability unavailable")
	ErrActionUnavailable     = errors.New("desktop admin action capability unavailable")
	ErrActionFailed          = errors.New("desktop admin action failed")
	ErrClosed                = errors.New("desktop admin client is closed")
	ErrWireLimit             = errors.New("desktop admin wire budget exceeded")
	ErrLocalOnly             = errors.New("desktop admin value is local-only")
)

// ConfigSource is the trusted, in-process, non-blocking read-only
// configuration dependency accepted by Client. config.Store implements it.
// A credential is represented by a reference in the returned config; this
// package never resolves or stores credential data. Implementations must not
// perform network or process I/O from Snapshot.
type ConfigSource interface {
	Snapshot() config.Snapshot
}

// StatusSource is the trusted, in-process, non-blocking read-only supervisor
// projection accepted by Client. supervisor.Supervisor implements it. Its
// snapshots are copied and filtered against the current configuration before
// they reach a wire model. Implementations must not perform network or
// process I/O from Snapshots.
type StatusSource interface {
	Snapshots() []supervisor.Snapshot
}

// ActionDispatcher is the typed seam a future production lifecycle adapter
// must implement. It accepts only fixed enum actions and a validated local
// connection identifier; it has no command, argv, environment, path, or
// credential parameter.
//
// Client intentionally does not invoke this seam yet. R6's supervisor and
// lifecycle adapter are non-production cores, so DispatchAction returns
// capability_unavailable even when a dispatcher is supplied. Keeping the
// interface here prevents a future GUI from inventing a string-command API.
type ActionDispatcher interface {
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Reconnect(context.Context, string) error
}

// DiagnosticsSource is an optional local source for already typed diagnostic
// records. It must honor maxItems and must not include messages, paths,
// commands, arguments, environment values, file content, or credentials.
// Client validates every returned record again before copying it.
type DiagnosticsSource interface {
	ReadDiagnostics(context.Context, int) ([]DiagnosticRecord, error)
}

// Dependencies are references to existing local components. Client does not
// retain a supervisor, tunnel, or credential object; it retains only these
// narrow interfaces and never performs network or process I/O.
type Dependencies struct {
	Config      ConfigSource
	Status      StatusSource
	Diagnostics DiagnosticsSource
	// Actions is intentionally not used until the production lifecycle gate
	// is closed. It is retained in the dependency shape only as a typed seam
	// for a later, separately reviewed adapter.
	Actions ActionDispatcher
}

// Client is a bounded, local-only projection for GUI/tray consumers.
type Client struct {
	config      ConfigSource
	status      StatusSource
	diagnostics DiagnosticsSource
	closed      bool
	mu          sync.RWMutex
}

// New validates the only mandatory dependency. Status, diagnostics, and
// action capabilities are optional and are reported as unavailable rather
// than being replaced with guessed state. The Actions dependency is not
// retained because the current lifecycle gate is fail closed.
func New(deps Dependencies) (*Client, error) {
	if deps.Config == nil {
		return nil, ErrInvalidConfig
	}
	return &Client{config: deps.Config, status: deps.Status, diagnostics: deps.Diagnostics}, nil
}

// Close exits only this client projection. It never calls Stop, Reconnect,
// supervisor.Close, or any other lifecycle operation. The supervisor is
// owned by its own local service and remains independent of a tray process.
func (c *Client) Close() error {
	if c == nil {
		return ErrClosed
	}
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return nil
}

// Exit is an explicit alias for Close for tray clients.
func (c *Client) Exit() error { return c.Close() }

// CapabilityReason is a stable, non-sensitive reason for an unavailable
// capability. It never contains an implementation error or endpoint.
type CapabilityReason string

const (
	CapabilityReady             CapabilityReason = ""
	CapabilityUnavailableReason CapabilityReason = "capability_unavailable"
	CapabilityClientClosed      CapabilityReason = "client_closed"
	CapabilityProductionGate    CapabilityReason = "production_gate"
)

// CapabilityStatus describes whether one local client feature can be used.
type CapabilityStatus struct {
	Available bool             `json:"available"`
	Reason    CapabilityReason `json:"reason,omitempty"`
}

func (s CapabilityStatus) validate() error {
	if s.Available {
		if s.Reason != CapabilityReady {
			return ErrInvalidConfig
		}
		return nil
	}
	if s.Reason != CapabilityUnavailableReason && s.Reason != CapabilityClientClosed && s.Reason != CapabilityProductionGate {
		return ErrInvalidConfig
	}
	return nil
}

func (s CapabilityStatus) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	type plain CapabilityStatus
	return marshalBounded(plain(s))
}

// Capabilities lists features without claiming production readiness.
type Capabilities struct {
	ProductionReady   bool             `json:"production_ready"`
	Overview          CapabilityStatus `json:"overview"`
	ConnectionStatus  CapabilityStatus `json:"connection_status"`
	ManagementActions CapabilityStatus `json:"management_actions"`
	Diagnostics       CapabilityStatus `json:"diagnostics"`
	DeveloperRules    CapabilityStatus `json:"developer_rules"`
}

func (s Capabilities) validate() error {
	if s.ProductionReady {
		return ErrInvalidConfig
	}
	for _, value := range []CapabilityStatus{s.Overview, s.ConnectionStatus, s.ManagementActions, s.Diagnostics, s.DeveloperRules} {
		if err := value.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s Capabilities) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	type plain Capabilities
	return marshalBounded(plain(s))
}

// Capabilities returns the currently available local projection capabilities.
// ManagementActions is always unavailable until the production lifecycle
// adapter is separately proven; a supplied dispatcher cannot upgrade it.
func (c *Client) Capabilities() Capabilities {
	if c == nil {
		return unavailableCapabilities(CapabilityClientClosed)
	}
	c.mu.RLock()
	closed := c.closed
	configAvailable := c.config != nil
	statusAvailable := c.status != nil
	diagnosticsAvailable := c.diagnostics != nil
	c.mu.RUnlock()
	if closed {
		return unavailableCapabilities(CapabilityClientClosed)
	}
	return Capabilities{
		ProductionReady:   ProductionReady,
		Overview:          capability(configAvailable),
		ConnectionStatus:  capability(statusAvailable),
		ManagementActions: CapabilityStatus{Available: false, Reason: CapabilityProductionGate},
		Diagnostics:       capability(diagnosticsAvailable),
		DeveloperRules:    capability(configAvailable),
	}
}

func unavailableCapabilities(reason CapabilityReason) Capabilities {
	return Capabilities{
		ProductionReady:   ProductionReady,
		Overview:          CapabilityStatus{Reason: reason},
		ConnectionStatus:  CapabilityStatus{Reason: reason},
		ManagementActions: CapabilityStatus{Reason: reason},
		Diagnostics:       CapabilityStatus{Reason: reason},
		DeveloperRules:    CapabilityStatus{Reason: reason},
	}
}

func capability(available bool) CapabilityStatus {
	if available {
		return CapabilityStatus{Available: true}
	}
	return CapabilityStatus{Reason: CapabilityUnavailableReason}
}

// Overview is a path-free and credential-free summary. Counts are capped and
// Truncated reports that the source contained more configured items.
type Overview struct {
	SchemaVersion          string       `json:"schema_version"`
	ProductionReady        bool         `json:"production_ready"`
	Capabilities           Capabilities `json:"capabilities"`
	ConfigRevision         string       `json:"config_revision"`
	RootCount              int          `json:"root_count"`
	ProfileCount           int          `json:"profile_count"`
	ConnectionCount        int          `json:"connection_count"`
	EnabledConnectionCount int          `json:"enabled_connection_count"`
	StatusKnown            bool         `json:"status_known"`
	ReadyConnections       int          `json:"ready_connections,omitempty"`
	DegradedConnections    int          `json:"degraded_connections,omitempty"`
	Truncated              bool         `json:"truncated"`
}

func (s Overview) validate() error {
	if s.SchemaVersion != SchemaVersion || s.ProductionReady || !validRevision(s.ConfigRevision) || s.RootCount < 0 || s.ProfileCount < 0 || s.ConnectionCount < 0 || s.EnabledConnectionCount < 0 || s.ReadyConnections < 0 || s.DegradedConnections < 0 || s.RootCount > MaxItems || s.ProfileCount > MaxItems || s.ConnectionCount > MaxItems || s.EnabledConnectionCount > MaxItems || s.ReadyConnections > MaxItems || s.DegradedConnections > MaxItems {
		return ErrInvalidConfig
	}
	if err := s.Capabilities.validate(); err != nil {
		return err
	}
	if !s.StatusKnown && (s.ReadyConnections != 0 || s.DegradedConnections != 0) {
		return ErrInvalidConfig
	}
	return nil
}

func (s Overview) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	type plain Overview
	return marshalBounded(plain(s))
}

// GetOverview returns configuration counts and, when available, counts from
// the current supervisor projection. Missing status remains explicit through
// StatusKnown=false and an unavailable capability; no stopped/ready state is
// invented.
func (c *Client) GetOverview() (Overview, error) {
	if err := c.ensureOpen(); err != nil {
		return Overview{}, err
	}
	cfg, revision, err := c.readConfig()
	if err != nil {
		return Overview{}, err
	}
	connections := cfg.Connections()
	overview := Overview{
		SchemaVersion:   SchemaVersion,
		ProductionReady: ProductionReady,
		Capabilities:    c.Capabilities(),
		ConfigRevision:  revision,
		RootCount:       cappedCount(len(cfg.Roots())),
		ProfileCount:    cappedCount(len(cfg.Profiles())),
		ConnectionCount: cappedCount(len(connections)),
		Truncated:       len(cfg.Roots()) > MaxItems || len(cfg.Profiles()) > MaxItems || len(connections) > MaxItems,
	}
	for _, connection := range connections {
		if connection.Enabled() {
			overview.EnabledConnectionCount++
		}
	}
	if overview.EnabledConnectionCount > MaxItems {
		overview.EnabledConnectionCount = MaxItems
		overview.Truncated = true
	}
	if c.status == nil {
		overview.Capabilities.ConnectionStatus = CapabilityStatus{Reason: CapabilityUnavailableReason}
		return overview, nil
	}
	snapshots, err := c.readStatuses()
	if err != nil {
		overview.Capabilities.ConnectionStatus = CapabilityStatus{Reason: CapabilityUnavailableReason}
		return overview, ErrCapabilityUnavailable
	}
	known, ready, degraded := countStatuses(connections, snapshots, revision)
	overview.StatusKnown = known
	if known {
		overview.ReadyConnections = ready
		overview.DegradedConnections = degraded
	}
	return overview, nil
}

// Overview is a short alias convenient for local GUI adapters.
func (c *Client) Overview() (Overview, error) { return c.GetOverview() }

// LifecycleState is the sanitized state vocabulary exposed to desktop code.
type LifecycleState string

const (
	StateUnavailable   LifecycleState = "unavailable"
	StateStopped       LifecycleState = "stopped"
	StateStarting      LifecycleState = "starting"
	StateLocalReady    LifecycleState = "local_mcp_ready"
	StatePolling       LifecycleState = "polling"
	StateReady         LifecycleState = "ready"
	StateDegraded      LifecycleState = "degraded"
	StateBackoff       LifecycleState = "backoff"
	StateAuthFailed    LifecycleState = "auth_failed"
	StateSleeping      LifecycleState = "sleeping"
	StateResuming      LifecycleState = "resuming"
	StateStopping      LifecycleState = "stopping"
	StateCleanupFailed LifecycleState = "cleanup_failed"
)

// StatusError is the bounded error vocabulary exposed by the supervisor
// projection. No implementation error text is copied.
type StatusError string

const (
	ErrorNone           StatusError = ""
	ErrorRuntime        StatusError = "runtime"
	ErrorHealth         StatusError = "health"
	ErrorAuthFailed     StatusError = "auth_failed"
	ErrorRevisionChange StatusError = "revision_changed"
	ErrorStopped        StatusError = "stopped"
	ErrorSleeping       StatusError = "sleeping"
	ErrorCleanupFailed  StatusError = "cleanup_failed"
)

// ConnectionStatus is a path-free, credential-free status row. TunnelID and
// CredentialRef are intentionally not represented; TunnelConfigured is only
// a boolean derived from trusted config metadata.
type ConnectionStatus struct {
	ConnectionID     string                     `json:"connection_id"`
	Label            string                     `json:"label,omitempty"`
	LabelOmitted     bool                       `json:"label_omitted,omitempty"`
	ProfileID        string                     `json:"profile_id"`
	Enabled          bool                       `json:"enabled"`
	Transport        config.ConnectionTransport `json:"transport"`
	TunnelConfigured bool                       `json:"tunnel_configured"`
	StatusKnown      bool                       `json:"status_known"`
	State            LifecycleState             `json:"state"`
	Attempt          int                        `json:"attempt,omitempty"`
	NextRetryAt      *time.Time                 `json:"next_retry_at,omitempty"`
	LastError        StatusError                `json:"last_error,omitempty"`
}

func (s ConnectionStatus) validate() error {
	if !validIdentifier(s.ConnectionID) || !validIdentifier(s.ProfileID) || !validTransport(s.Transport) || s.Attempt < 0 || s.Attempt > MaxAttempt {
		return ErrInvalidConfig
	}
	if s.Label != "" && !isSafeLabel(s.Label) {
		return ErrInvalidConfig
	}
	if s.LabelOmitted && s.Label != "" {
		return ErrInvalidConfig
	}
	if !validLifecycleState(s.State) || !validStatusError(s.LastError) {
		return ErrInvalidConfig
	}
	if !s.StatusKnown {
		if s.State != StateUnavailable || s.Attempt != 0 || s.NextRetryAt != nil || s.LastError != ErrorNone {
			return ErrInvalidConfig
		}
	}
	if s.NextRetryAt != nil && s.NextRetryAt.IsZero() {
		return ErrInvalidConfig
	}
	return nil
}

func (s ConnectionStatus) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	type plain ConnectionStatus
	return marshalBounded(plain(s))
}

// ConnectionStatusList is the bounded status projection returned to a GUI.
type ConnectionStatusList struct {
	SchemaVersion   string             `json:"schema_version"`
	ProductionReady bool               `json:"production_ready"`
	StatusKnown     bool               `json:"status_known"`
	Entries         []ConnectionStatus `json:"entries"`
	Truncated       bool               `json:"truncated"`
}

func (s ConnectionStatusList) validate() error {
	if s.SchemaVersion != SchemaVersion || s.ProductionReady || len(s.Entries) > MaxItems {
		return ErrInvalidConfig
	}
	for _, entry := range s.Entries {
		if err := entry.validate(); err != nil {
			return err
		}
		if entry.StatusKnown != s.StatusKnown {
			return ErrInvalidConfig
		}
	}
	return nil
}

func (s ConnectionStatusList) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	type plain ConnectionStatusList
	return marshalBounded(plain(s))
}

// GetConnectionStatuses returns one row per configured connection, in config
// order. If status is unavailable, rows explicitly carry state=unavailable
// and the method returns ErrCapabilityUnavailable so callers cannot mistake
// them for supervisor state.
func (c *Client) GetConnectionStatuses() (ConnectionStatusList, error) {
	if err := c.ensureOpen(); err != nil {
		return ConnectionStatusList{}, err
	}
	cfg, revision, err := c.readConfig()
	if err != nil {
		return ConnectionStatusList{}, err
	}
	connections := cfg.Connections()
	truncated := len(connections) > MaxItems
	if len(connections) > MaxItems {
		connections = connections[:MaxItems]
	}
	entries := make([]ConnectionStatus, 0, len(connections))
	for _, connection := range connections {
		entries = append(entries, baseConnectionStatus(connection))
	}
	result := ConnectionStatusList{SchemaVersion: SchemaVersion, ProductionReady: ProductionReady, Entries: entries}
	if c.status == nil {
		result.Truncated = truncated
		return result, ErrCapabilityUnavailable
	}
	snapshots, err := c.readStatuses()
	if err != nil {
		result.Truncated = truncated
		return result, ErrCapabilityUnavailable
	}
	byID, duplicate := indexSnapshots(snapshots)
	statusKnown := true
	for i := range result.Entries {
		connection := connections[i]
		snapshot, ok := byID[connection.ID()]
		if !ok || duplicate[connection.ID()] {
			statusKnown = false
			continue
		}
		if !applySupervisorStatus(&result.Entries[i], connection, snapshot, revision) {
			statusKnown = false
		}
	}
	result.StatusKnown = statusKnown && len(result.Entries) != 0
	if len(result.Entries) == 0 {
		// An empty configured set is known empty when the status call succeeded.
		result.StatusKnown = true
	}
	if !result.StatusKnown {
		for i := range result.Entries {
			result.Entries[i] = baseConnectionStatus(connections[i])
		}
	}
	result.Truncated = truncated
	return result, nil
}

// ConnectionStatuses is a short alias for GetConnectionStatuses.
func (c *Client) ConnectionStatuses() (ConnectionStatusList, error) {
	return c.GetConnectionStatuses()
}

func baseConnectionStatus(connection config.Connection) ConnectionStatus {
	label, ok := safeLabel(connection.Label())
	return ConnectionStatus{
		ConnectionID:     connection.ID(),
		Label:            label,
		LabelOmitted:     connection.Label() != "" && !ok,
		ProfileID:        connection.ProfileID(),
		Enabled:          connection.Enabled(),
		Transport:        connection.Transport(),
		TunnelConfigured: connection.TunnelID() != "",
		StatusKnown:      false,
		State:            StateUnavailable,
	}
}

func applySupervisorStatus(dst *ConnectionStatus, connection config.Connection, source supervisor.Snapshot, revision string) bool {
	if !validSupervisorSnapshot(connection, source, revision) {
		return false
	}
	state, _ := lifecycleState(source.State)
	lastError, ok := statusError(source.LastError)
	if !ok {
		return false
	}
	dst.StatusKnown = true
	dst.State = state
	dst.Attempt = source.Attempt
	dst.LastError = lastError
	if !source.NextRetryAt.IsZero() {
		when := source.NextRetryAt.UTC()
		dst.NextRetryAt = &when
	}
	return true
}

func countStatuses(connections []config.Connection, snapshots []supervisor.Snapshot, revision string) (bool, int, int) {
	byID, duplicate := indexSnapshots(snapshots)
	if len(connections) != len(snapshots) {
		// The supervisor may contain a stale or extra entry. It is not safe to
		// call the aggregate counts known in that case.
		return false, 0, 0
	}
	ready, degraded := 0, 0
	for _, connection := range connections {
		snapshot, ok := byID[connection.ID()]
		if !ok || duplicate[connection.ID()] || !validSupervisorSnapshot(connection, snapshot, revision) {
			return false, 0, 0
		}
		state, _ := lifecycleState(snapshot.State)
		if state == StateReady {
			ready++
		}
		if state == StateDegraded || state == StateBackoff || state == StateAuthFailed || state == StateCleanupFailed {
			degraded++
		}
	}
	return true, cappedCount(ready), cappedCount(degraded)
}

func validSupervisorSnapshot(connection config.Connection, source supervisor.Snapshot, revision string) bool {
	if source.ID != connection.ID() || source.Revision != revision || source.Transport != connection.Transport() || source.LocalPort < 1024 || source.Attempt < 0 || source.Attempt > MaxAttempt {
		return false
	}
	if _, ok := lifecycleState(source.State); !ok {
		return false
	}
	if _, ok := statusError(source.LastError); !ok {
		return false
	}
	return validStateErrorCombination(connection.Enabled(), source.State, source.LastError, source.Attempt, source.NextRetryAt)
}

// validStateErrorCombination mirrors only the stable supervisor transitions
// that are safe to project. A malformed or malicious source fails closed as
// an unavailable row; callers must not infer a lifecycle state from it.
func validStateErrorCombination(enabled bool, state supervisor.State, lastError supervisor.ErrorCode, attempt int, nextRetryAt time.Time) bool {
	if state == supervisor.StateReady {
		return enabled && lastError == supervisor.ErrorNone && attempt == 0 && nextRetryAt.IsZero()
	}
	if state == supervisor.StateBackoff {
		return attempt > 0 && !nextRetryAt.IsZero() && (lastError == supervisor.ErrorRuntime || lastError == supervisor.ErrorHealth)
	}
	if !nextRetryAt.IsZero() {
		return false
	}
	switch state {
	case supervisor.StateAuthFailed:
		return lastError == supervisor.ErrorAuthFailed
	case supervisor.StateCleanupFailed:
		return lastError == supervisor.ErrorCleanupFailed
	case supervisor.StateSleeping:
		return lastError == supervisor.ErrorSleeping
	case supervisor.StateStopped:
		return lastError == supervisor.ErrorNone || lastError == supervisor.ErrorStopped || lastError == supervisor.ErrorRevisionChange
	case supervisor.StateDegraded:
		return lastError == supervisor.ErrorRuntime || lastError == supervisor.ErrorHealth
	case supervisor.StateStarting, supervisor.StateLocalReady, supervisor.StatePolling, supervisor.StateResuming, supervisor.StateStopping:
		return lastError == supervisor.ErrorNone
	default:
		return false
	}
}

func indexSnapshots(snapshots []supervisor.Snapshot) (map[string]supervisor.Snapshot, map[string]bool) {
	byID := make(map[string]supervisor.Snapshot, len(snapshots))
	duplicate := make(map[string]bool)
	for _, snapshot := range snapshots {
		if !validIdentifier(snapshot.ID) {
			continue
		}
		if _, exists := byID[snapshot.ID]; exists {
			duplicate[snapshot.ID] = true
			continue
		}
		byID[snapshot.ID] = snapshot
	}
	return byID, duplicate
}

// Action identifies one of the only lifecycle operations understood by the
// future GUI. It is not a command name and cannot carry arguments.
type Action string

const (
	ActionStart     Action = "start"
	ActionStop      Action = "stop"
	ActionReconnect Action = "reconnect"
)

// ActionRequest is a strict, path-free typed request.
type ActionRequest struct {
	Action       Action `json:"action"`
	ConnectionID string `json:"connection_id"`
}

func (r ActionRequest) Validate() error {
	if !validAction(r.Action) || !validIdentifier(r.ConnectionID) {
		return ErrInvalidRequest
	}
	return nil
}

func (r ActionRequest) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type plain ActionRequest
	return marshalBounded(plain(r))
}

// UnmarshalJSON rejects unknown and duplicate fields and validates the enum
// before exposing the request to a caller.
func (r *ActionRequest) UnmarshalJSON(data []byte) error {
	if r == nil || len(data) == 0 || len(data) > MaxActionRequest {
		return ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalidRequest
	}
	var candidate ActionRequest
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return ErrInvalidRequest
		}
		seen[key] = true
		switch key {
		case "action":
			if err := decoder.Decode(&candidate.Action); err != nil {
				return ErrInvalidRequest
			}
		case "connection_id":
			if err := decoder.Decode(&candidate.ConnectionID); err != nil {
				return ErrInvalidRequest
			}
		default:
			return ErrInvalidRequest
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return ErrInvalidRequest
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		// Decode reports io.EOF for the expected end. Avoid accepting a second
		// JSON value without importing a second parser path.
		return ErrInvalidRequest
	}
	if err := candidate.Validate(); err != nil {
		return ErrInvalidRequest
	}
	*r = candidate
	return nil
}

// ActionResult is the only wire result for a lifecycle request. Rejections
// contain a stable category and never an implementation error string.
type ActionResult struct {
	SchemaVersion   string          `json:"schema_version"`
	ProductionReady bool            `json:"production_ready"`
	Action          Action          `json:"action,omitempty"`
	ConnectionID    string          `json:"connection_id,omitempty"`
	Accepted        bool            `json:"accepted"`
	ErrorCode       ActionErrorCode `json:"error_code,omitempty"`
}

type ActionErrorCode string

const (
	ActionErrorInvalidRequest        ActionErrorCode = "invalid_request"
	ActionErrorCapabilityUnavailable ActionErrorCode = "capability_unavailable"
	ActionErrorClientClosed          ActionErrorCode = "client_closed"
	ActionErrorConnectionMissing     ActionErrorCode = "connection_missing"
	ActionErrorDisabled              ActionErrorCode = "connection_disabled"
	ActionErrorCancelled             ActionErrorCode = "cancelled"
	ActionErrorDeadlineExceeded      ActionErrorCode = "deadline_exceeded"
	ActionErrorFailed                ActionErrorCode = "action_failed"
)

func (r ActionResult) validate() error {
	if r.SchemaVersion != SchemaVersion || r.ProductionReady || (r.Action != "" && !validAction(r.Action)) || (r.ConnectionID != "" && !validIdentifier(r.ConnectionID)) || !validActionError(r.ErrorCode) {
		return ErrInvalidConfig
	}
	if r.Accepted {
		if !validAction(r.Action) || !validIdentifier(r.ConnectionID) || r.ErrorCode != "" {
			return ErrInvalidConfig
		}
	} else if r.ErrorCode == "" {
		return ErrInvalidConfig
	}
	return nil
}

func (r ActionResult) MarshalJSON() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	type plain ActionResult
	return marshalBounded(plain(r))
}

// DispatchAction validates the request and configured connection, then stays
// fail closed because the current lifecycle implementation is non-production.
// It deliberately never calls Dependencies.Actions or connectionmanager.
func (c *Client) DispatchAction(ctx context.Context, request ActionRequest) (ActionResult, error) {
	result := ActionResult{SchemaVersion: SchemaVersion, ProductionReady: ProductionReady}
	if err := request.Validate(); err != nil {
		return resultWithRequest(result, request, ActionErrorInvalidRequest), ErrInvalidRequest
	}
	result = resultWithRequest(result, request, ActionErrorCapabilityUnavailable)
	if err := c.ensureOpen(); err != nil {
		result.ErrorCode = ActionErrorClientClosed
		return result, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.ErrorCode = ActionErrorDeadlineExceeded
		} else {
			result.ErrorCode = ActionErrorCancelled
		}
		return result, err
	}
	if _, _, err := c.readConnection(request.ConnectionID); err != nil {
		if errors.Is(err, supervisor.ErrConnectionMissing) {
			result.ErrorCode = ActionErrorConnectionMissing
			return result, err
		}
		result.ErrorCode = ActionErrorCapabilityUnavailable
		return result, ErrCapabilityUnavailable
	}
	return result, ErrActionUnavailable
}

func resultWithRequest(result ActionResult, request ActionRequest, code ActionErrorCode) ActionResult {
	if validAction(request.Action) {
		result.Action = request.Action
	}
	if validIdentifier(request.ConnectionID) {
		result.ConnectionID = request.ConnectionID
	}
	result.ErrorCode = code
	return result
}

// DeveloperRule is a safe preview: it includes only immutable identifiers and
// enum kinds. Executable paths, argv literals, slot values, and environment
// fields are intentionally absent.
type DeveloperRule struct {
	ID         string                    `json:"id"`
	Kind       commandprofile.Kind       `json:"kind"`
	VariantIDs []string                  `json:"variant_ids"`
	SlotKinds  []commandprofile.SlotKind `json:"slot_kinds"`
}

func (r DeveloperRule) validate() error {
	if !validIdentifier(r.ID) || (r.Kind != commandprofile.KindVersionProbe && r.Kind != commandprofile.KindFixedCommand) || len(r.VariantIDs) > MaxItems || len(r.SlotKinds) > MaxItems {
		return ErrInvalidConfig
	}
	for _, value := range r.VariantIDs {
		if !validIdentifier(value) {
			return ErrInvalidConfig
		}
	}
	for _, value := range r.SlotKinds {
		if value != commandprofile.SlotEnum && value != commandprofile.SlotBoundedInteger && value != commandprofile.SlotRootRelativePath {
			return ErrInvalidConfig
		}
	}
	return nil
}

func (r DeveloperRule) MarshalJSON() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	type plain DeveloperRule
	return marshalBounded(plain(r))
}

// DeveloperRules is a read-only preview of existing trusted configuration.
// It cannot enable developer mode or alter server policy.
type DeveloperRules struct {
	SchemaVersion        string          `json:"schema_version"`
	ProductionReady      bool            `json:"production_ready"`
	Enabled              bool            `json:"enabled"`
	AllowedConnectionIDs []string        `json:"allowed_connection_ids"`
	Rules                []DeveloperRule `json:"rules"`
}

func (r DeveloperRules) validate() error {
	if r.SchemaVersion != SchemaVersion || r.ProductionReady || len(r.AllowedConnectionIDs) > MaxItems || len(r.Rules) > MaxItems {
		return ErrInvalidConfig
	}
	for _, value := range r.AllowedConnectionIDs {
		if !validIdentifier(value) {
			return ErrInvalidConfig
		}
	}
	for _, value := range r.Rules {
		if err := value.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (r DeveloperRules) MarshalJSON() ([]byte, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	type plain DeveloperRules
	return marshalBounded(plain(r))
}

// GetDeveloperRules revalidates trusted config and creates a path-free
// preview. It never invokes a policy mutation API.
func (c *Client) GetDeveloperRules() (DeveloperRules, error) {
	if err := c.ensureOpen(); err != nil {
		return DeveloperRules{}, err
	}
	cfg, _, err := c.readConfig()
	if err != nil {
		return DeveloperRules{}, err
	}
	mode := cfg.DeveloperMode()
	profiles := cfg.CommandProfiles()
	if err := mode.Validate(); err != nil || commandprofile.ValidateProfiles(profiles) != nil {
		return DeveloperRules{}, ErrInvalidConfig
	}
	connections := cfg.Connections()
	knownConnections := make(map[string]struct{}, len(connections))
	for _, connection := range connections {
		knownConnections[connection.ID()] = struct{}{}
	}
	allowed := mode.AllowedConnections()
	for _, connectionID := range allowed {
		if !validIdentifier(connectionID) {
			return DeveloperRules{}, ErrInvalidConfig
		}
		if _, ok := knownConnections[connectionID]; !ok {
			return DeveloperRules{}, ErrInvalidConfig
		}
	}
	if len(profiles) > MaxItems || len(allowed) > MaxItems {
		return DeveloperRules{}, ErrInvalidConfig
	}
	result := DeveloperRules{SchemaVersion: SchemaVersion, ProductionReady: ProductionReady, Enabled: mode.Enabled(), AllowedConnectionIDs: append([]string(nil), allowed...)}
	result.Rules = make([]DeveloperRule, 0, len(profiles))
	for _, profile := range profiles {
		rule := DeveloperRule{ID: profile.ID(), Kind: profile.Kind()}
		for _, variant := range profile.Variants() {
			rule.VariantIDs = append(rule.VariantIDs, variant.ID())
		}
		for _, slot := range profile.Slots() {
			rule.SlotKinds = append(rule.SlotKinds, slot.Kind())
		}
		result.Rules = append(result.Rules, rule)
	}
	return result, nil
}

// DeveloperRulePreview is a short alias for GetDeveloperRules.
func (c *Client) DeveloperRulePreview() (DeveloperRules, error) { return c.GetDeveloperRules() }

// DiagnosticComponent is a fixed audit component vocabulary.
type DiagnosticComponent string

const (
	DiagnosticMCP           DiagnosticComponent = "MCP"
	DiagnosticAuth          DiagnosticComponent = "auth"
	DiagnosticPolicy        DiagnosticComponent = "policy"
	DiagnosticFSSearch      DiagnosticComponent = "fs-search"
	DiagnosticCommand       DiagnosticComponent = "command"
	DiagnosticNetworkTunnel DiagnosticComponent = "network-tunnel"
	DiagnosticServiceConfig DiagnosticComponent = "service-config"
	DiagnosticError         DiagnosticComponent = "error"
)

type DiagnosticSeverity string

const (
	DiagnosticDebug    DiagnosticSeverity = "debug"
	DiagnosticInfo     DiagnosticSeverity = "info"
	DiagnosticWarn     DiagnosticSeverity = "warn"
	DiagnosticErrorLvl DiagnosticSeverity = "error"
	DiagnosticCritical DiagnosticSeverity = "critical"
)

type DiagnosticOutcome string

const (
	DiagnosticStarted   DiagnosticOutcome = "started"
	DiagnosticSucceeded DiagnosticOutcome = "succeeded"
	DiagnosticFailed    DiagnosticOutcome = "failed"
	DiagnosticRejected  DiagnosticOutcome = "rejected"
	DiagnosticDegraded  DiagnosticOutcome = "degraded"
)

// DiagnosticRecord is local-only input for CopyDiagnostics. Its fields are
// private so accidentally marshaling a source record cannot expose raw data.
type DiagnosticRecord struct {
	timestamp    time.Time
	component    DiagnosticComponent
	severity     DiagnosticSeverity
	outcome      DiagnosticOutcome
	errorCode    string
	connectionID string
	profileID    string
	revision     string
	durationMS   int64
}

// MarshalJSON makes accidental direct serialization of local input fail
// closed. Only Diagnostic, produced by CopyDiagnostics, is wire-facing.
func (DiagnosticRecord) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

// UnmarshalJSON keeps callers from manufacturing a local record through a
// wire payload. NewDiagnosticRecord is the only construction path.
func (*DiagnosticRecord) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// NewDiagnosticRecord constructs a bounded, message-free diagnostic record.
func NewDiagnosticRecord(timestamp time.Time, component DiagnosticComponent, severity DiagnosticSeverity, outcome DiagnosticOutcome, errorCode, connectionID, profileID, revision string, durationMS int64) (DiagnosticRecord, error) {
	record := DiagnosticRecord{timestamp: timestamp.UTC(), component: component, severity: severity, outcome: outcome, errorCode: errorCode, connectionID: connectionID, profileID: profileID, revision: revision, durationMS: durationMS}
	if err := record.validate(); err != nil {
		return DiagnosticRecord{}, ErrInvalidDiagnostics
	}
	return record, nil
}

func (r DiagnosticRecord) validate() error {
	if r.timestamp.IsZero() || !validDiagnosticComponent(r.component) || !validDiagnosticSeverity(r.severity) || !validDiagnosticOutcome(r.outcome) || !validDiagnosticCode(r.errorCode) || !optionalIdentifier(r.connectionID) || !optionalIdentifier(r.profileID) || !optionalIdentifier(r.revision) || r.durationMS < 0 || r.durationMS > 24*60*60*1000 {
		return ErrInvalidDiagnostics
	}
	return nil
}

// Diagnostic is the sanitized wire copy of a DiagnosticRecord. It has no
// message, path, command, output, endpoint, token, or key field.
type Diagnostic struct {
	Timestamp    time.Time           `json:"timestamp"`
	Component    DiagnosticComponent `json:"component"`
	Severity     DiagnosticSeverity  `json:"severity"`
	Outcome      DiagnosticOutcome   `json:"outcome"`
	ErrorCode    string              `json:"error_code,omitempty"`
	ConnectionID string              `json:"connection_id,omitempty"`
	ProfileID    string              `json:"profile_id,omitempty"`
	Revision     string              `json:"revision,omitempty"`
	DurationMS   int64               `json:"duration_ms"`
}

func (d Diagnostic) validate() error {
	return (DiagnosticRecord{timestamp: d.Timestamp, component: d.Component, severity: d.Severity, outcome: d.Outcome, errorCode: d.ErrorCode, connectionID: d.ConnectionID, profileID: d.ProfileID, revision: d.Revision, durationMS: d.DurationMS}).validate()
}

func (d Diagnostic) MarshalJSON() ([]byte, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	type plain Diagnostic
	return marshalBounded(plain(d))
}

// Diagnostics is a bounded support/diagnostic copy.
type Diagnostics struct {
	SchemaVersion   string       `json:"schema_version"`
	ProductionReady bool         `json:"production_ready"`
	Entries         []Diagnostic `json:"entries"`
	Truncated       bool         `json:"truncated"`
}

func (d Diagnostics) validate() error {
	if d.SchemaVersion != SchemaVersion || d.ProductionReady || len(d.Entries) > MaxItems {
		return ErrInvalidDiagnostics
	}
	for _, entry := range d.Entries {
		if err := entry.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (d Diagnostics) MarshalJSON() ([]byte, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	type plain Diagnostics
	return marshalBounded(plain(d))
}

// CopyDiagnostics copies at most maxItems records into the support model.
// maxItems=0 selects MaxItems; a larger explicit limit is rejected rather
// than silently weakening the wire contract.
func CopyDiagnostics(records []DiagnosticRecord, maxItems int) (Diagnostics, error) {
	if maxItems == 0 {
		maxItems = MaxItems
	}
	if maxItems < 0 || maxItems > MaxItems {
		return Diagnostics{}, ErrInvalidLimit
	}
	truncated := len(records) > maxItems
	if len(records) > maxItems {
		records = records[:maxItems]
	}
	result := Diagnostics{SchemaVersion: SchemaVersion, ProductionReady: ProductionReady, Truncated: truncated, Entries: make([]Diagnostic, 0, len(records))}
	for _, record := range records {
		if err := record.validate(); err != nil {
			return Diagnostics{}, ErrInvalidDiagnostics
		}
		result.Entries = append(result.Entries, Diagnostic{Timestamp: record.timestamp, Component: record.component, Severity: record.severity, Outcome: record.outcome, ErrorCode: record.errorCode, ConnectionID: record.connectionID, ProfileID: record.profileID, Revision: record.revision, DurationMS: record.durationMS})
	}
	if _, err := result.MarshalJSON(); err != nil {
		return Diagnostics{}, err
	}
	return result, nil
}

// GetDiagnostics reads optional typed local diagnostics and copies them into
// a bounded support model. Source failures return capability_unavailable; raw
// source errors are never returned to the caller.
func (c *Client) GetDiagnostics(ctx context.Context) (Diagnostics, error) {
	if err := c.ensureOpen(); err != nil {
		return Diagnostics{}, err
	}
	c.mu.RLock()
	source := c.diagnostics
	c.mu.RUnlock()
	if source == nil {
		return Diagnostics{}, ErrCapabilityUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Diagnostics{}, err
	}
	records, err := safeReadDiagnostics(source, ctx)
	if err != nil {
		return Diagnostics{}, ErrCapabilityUnavailable
	}
	return CopyDiagnostics(records, MaxItems)
}

// Diagnostics is a short alias for GetDiagnostics.
func (c *Client) Diagnostics(ctx context.Context) (Diagnostics, error) { return c.GetDiagnostics(ctx) }

func (c *Client) ensureOpen() error {
	if c == nil {
		return ErrClosed
	}
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return ErrClosed
	}
	return nil
}

func (c *Client) readConfig() (cfg config.Config, revision string, err error) {
	c.mu.RLock()
	source := c.config
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return config.Config{}, "", ErrClosed
	}
	if source == nil {
		return config.Config{}, "", ErrCapabilityUnavailable
	}
	defer func() {
		if recover() != nil {
			cfg = config.Config{}
			revision = ""
			err = ErrCapabilityUnavailable
		}
	}()
	snapshot := source.Snapshot()
	cfg = snapshot.Config()
	revision = snapshot.Revision()
	if cfg.SchemaVersion() != config.SchemaVersionV1 || !validRevision(revision) {
		return config.Config{}, "", ErrInvalidConfig
	}
	for _, connection := range cfg.Connections() {
		if !validIdentifier(connection.ID()) || !validIdentifier(connection.ProfileID()) || !validTransport(connection.Transport()) {
			return config.Config{}, "", ErrInvalidConfig
		}
	}
	return cfg, revision, nil
}

func (c *Client) readConnection(id string) (config.Connection, string, error) {
	cfg, revision, err := c.readConfig()
	if err != nil {
		return config.Connection{}, "", err
	}
	connection, ok := cfg.Connection(id)
	if !ok {
		return config.Connection{}, revision, supervisor.ErrConnectionMissing
	}
	return connection, revision, nil
}

func (c *Client) readStatuses() (snapshots []supervisor.Snapshot, err error) {
	c.mu.RLock()
	source := c.status
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	if source == nil {
		return nil, ErrCapabilityUnavailable
	}
	defer func() {
		if recover() != nil {
			snapshots = nil
			err = ErrCapabilityUnavailable
		}
	}()
	snapshots = source.Snapshots()
	if len(snapshots) > MaxItems {
		return nil, ErrCapabilityUnavailable
	}
	return snapshots, nil
}

func safeReadDiagnostics(source DiagnosticsSource, ctx context.Context) (records []DiagnosticRecord, err error) {
	defer func() {
		if recover() != nil {
			records = nil
			err = ErrCapabilityUnavailable
		}
	}()
	return source.ReadDiagnostics(ctx, MaxItems)
}

func marshalBounded(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxWireBytes {
		return nil, ErrWireLimit
	}
	return data, nil
}

func cappedCount(value int) int {
	if value < 0 {
		return 0
	}
	if value > MaxItems {
		return MaxItems
	}
	return value
}

func validRevision(value string) bool {
	if len(value) >= 2 && value[0] == 'r' && len(value) <= 21 {
		for i := 1; i < len(value); i++ {
			if value[i] < '0' || value[i] > '9' {
				return false
			}
		}
		return true
	}
	const shaPrefix = "sha256:"
	if len(value) != len(shaPrefix)+64 || !strings.HasPrefix(value, shaPrefix) {
		return false
	}
	for i := len(shaPrefix); i < len(value); i++ {
		valueByte := value[i]
		if !((valueByte >= '0' && valueByte <= '9') || (valueByte >= 'a' && valueByte <= 'f') || (valueByte >= 'A' && valueByte <= 'F')) {
			return false
		}
	}
	return true
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func optionalIdentifier(value string) bool { return value == "" || validIdentifier(value) }

func validTransport(value config.ConnectionTransport) bool {
	return value == config.TransportLocal || value == config.TransportOpenAIRuntime || value == config.TransportCloudflareNamed
}

func validAction(value Action) bool {
	return value == ActionStart || value == ActionStop || value == ActionReconnect
}

func validActionError(value ActionErrorCode) bool {
	switch value {
	case ActionErrorInvalidRequest, ActionErrorCapabilityUnavailable, ActionErrorClientClosed, ActionErrorConnectionMissing, ActionErrorDisabled, ActionErrorCancelled, ActionErrorDeadlineExceeded, ActionErrorFailed:
		return true
	default:
		return false
	}
}

func validLifecycleState(value LifecycleState) bool {
	switch value {
	case StateUnavailable, StateStopped, StateStarting, StateLocalReady, StatePolling, StateReady, StateDegraded, StateBackoff, StateAuthFailed, StateSleeping, StateResuming, StateStopping, StateCleanupFailed:
		return true
	default:
		return false
	}
}

func lifecycleState(value supervisor.State) (LifecycleState, bool) {
	switch value {
	case supervisor.StateStopped:
		return StateStopped, true
	case supervisor.StateStarting:
		return StateStarting, true
	case supervisor.StateLocalReady:
		return StateLocalReady, true
	case supervisor.StatePolling:
		return StatePolling, true
	case supervisor.StateReady:
		return StateReady, true
	case supervisor.StateDegraded:
		return StateDegraded, true
	case supervisor.StateBackoff:
		return StateBackoff, true
	case supervisor.StateAuthFailed:
		return StateAuthFailed, true
	case supervisor.StateSleeping:
		return StateSleeping, true
	case supervisor.StateResuming:
		return StateResuming, true
	case supervisor.StateStopping:
		return StateStopping, true
	case supervisor.StateCleanupFailed:
		return StateCleanupFailed, true
	default:
		return StateUnavailable, false
	}
}

func validStatusError(value StatusError) bool {
	switch value {
	case ErrorNone, ErrorRuntime, ErrorHealth, ErrorAuthFailed, ErrorRevisionChange, ErrorStopped, ErrorSleeping, ErrorCleanupFailed:
		return true
	default:
		return false
	}
}

func statusError(value supervisor.ErrorCode) (StatusError, bool) {
	switch value {
	case supervisor.ErrorNone:
		return ErrorNone, true
	case supervisor.ErrorRuntime:
		return ErrorRuntime, true
	case supervisor.ErrorHealth:
		return ErrorHealth, true
	case supervisor.ErrorAuthFailed:
		return ErrorAuthFailed, true
	case supervisor.ErrorRevisionChange:
		return ErrorRevisionChange, true
	case supervisor.ErrorStopped:
		return ErrorStopped, true
	case supervisor.ErrorSleeping:
		return ErrorSleeping, true
	case supervisor.ErrorCleanupFailed:
		return ErrorCleanupFailed, true
	default:
		return ErrorNone, false
	}
}

func validDiagnosticComponent(value DiagnosticComponent) bool {
	switch value {
	case DiagnosticMCP, DiagnosticAuth, DiagnosticPolicy, DiagnosticFSSearch, DiagnosticCommand, DiagnosticNetworkTunnel, DiagnosticServiceConfig, DiagnosticError:
		return true
	default:
		return false
	}
}

func validDiagnosticSeverity(value DiagnosticSeverity) bool {
	switch value {
	case DiagnosticDebug, DiagnosticInfo, DiagnosticWarn, DiagnosticErrorLvl, DiagnosticCritical:
		return true
	default:
		return false
	}
}

func validDiagnosticOutcome(value DiagnosticOutcome) bool {
	switch value {
	case DiagnosticStarted, DiagnosticSucceeded, DiagnosticFailed, DiagnosticRejected, DiagnosticDegraded:
		return true
	default:
		return false
	}
}

func validDiagnosticCode(value string) bool {
	if value == "" {
		return true
	}
	switch value {
	case "invalid_request", "denied", "not_found", "unsupported_type", "unsupported_encoding", "stale_version", "budget_exhausted", "deadline_exceeded", "cancelled", "unavailable", "auth_failed", "connection_refused", "port_conflict", "config_invalid", "queue_full", "child_exit", "identity_changed", "output_limit", "invalid_input", "rejected_executable", "invalid_output", "hash_mismatch", "hash_limit", "unsupported_platform", "runtime", "health", "revision_changed", "stopped", "sleeping", "cleanup_failed":
		return true
	default:
		return false
	}
}

func safeLabel(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	if !isSafeLabel(value) {
		return "", false
	}
	return value, true
}

func isSafeLabel(value string) bool {
	if len(value) > MaxLabelBytes || !utf8.ValidString(value) || strings.ContainsAny(value, `/\\:`) || strings.HasPrefix(value, "~") {
		return false
	}
	// A long separator-free label is indistinguishable from an opaque token;
	// omit it instead of attempting to classify its contents.
	if len(value) >= 24 && !strings.ContainsAny(value, " \t") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || strings.ContainsRune("-_.()[]", r)) {
			return false
		}
	}
	lower := strings.ToLower(value)
	words := strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for _, forbidden := range []string{"token", "secret", "password", "passwd", "api", "key", "cookie", "bearer", "authorization", "private", "credential", "jwt", "sk"} {
		for _, word := range words {
			if word == forbidden {
				return false
			}
		}
	}
	return true
}
