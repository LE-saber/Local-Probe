package audit

import (
	"errors"
	"strings"
	"time"
	"unicode"
)

const (
	// SchemaVersionV1 is retained as a historical identifier only. The sink
	// below emits the v2 schema because command audit fields are new wire
	// fields and are not v1-compatible.
	SchemaVersionV1     = "local-probe.audit.v1"
	SchemaVersionV2     = "local-probe.audit.v2"
	SchemaVersion       = SchemaVersionV2
	defaultFilePrefix   = "audit"
	defaultMaxFileBytes = 10 << 20
	defaultMaxFiles     = 14
	defaultQueueSize    = 256
	MaxFileBytes        = 64 << 20
	MaxFiles            = 128
	MaxQueueSize        = 4096
	maxPathBytes        = 4096
	maxIDBytes          = 128
	maxCounter          = int64(1) << 50
	maxDuration         = int64((24 * time.Hour) / time.Millisecond)
)

var (
	ErrInvalidConfig    = errors.New("invalid audit configuration")
	ErrInvalidEvent     = errors.New("invalid audit event")
	ErrSensitiveField   = errors.New("sensitive audit field rejected")
	ErrQueueFull        = errors.New("audit queue is full")
	ErrClosed           = errors.New("audit sink is closed")
	ErrEventTooLarge    = errors.New("audit event exceeds file budget")
	ErrStorage          = errors.New("audit storage unavailable")
	ErrRotation         = errors.New("audit rotation failed")
	ErrRetention        = errors.New("audit retention failed")
	ErrSecurityDelivery = errors.New("security events require synchronous delivery")
)

type Config struct {
	Directory    string
	FilePrefix   string
	MaxFileBytes int64
	MaxFiles     int
	QueueSize    int
	InstanceID   string
}

type Stats struct {
	Accepted   uint64
	Completed  uint64
	Dropped    uint64
	QueueDepth int
	Degraded   bool
}

// Recorder is the smallest dependency accepted by callers that need audit
// events. Sink implements it; tests and supervisors may provide another
// bounded recorder without exposing any event serialization details.
type Recorder interface {
	Emit(Event) error
	EmitSecurity(Event) error
}

type Component string

const (
	ComponentMCP           Component = "MCP"
	ComponentAuth          Component = "auth"
	ComponentPolicy        Component = "policy"
	ComponentFSSearch      Component = "fs-search"
	ComponentCommand       Component = "command"
	ComponentNetworkTunnel Component = "network-tunnel"
	ComponentServiceConfig Component = "service-config"
	ComponentError         Component = "error"
)

type EventType string

const (
	EventMCPCall          EventType = "mcp.call"
	EventMCPResult        EventType = "mcp.result"
	EventMCPList          EventType = "mcp.list"
	EventAuthAccept       EventType = "auth.accept"
	EventAuthReject       EventType = "auth.reject"
	EventPolicyDecision   EventType = "policy.decision"
	EventSearch           EventType = "fs.search"
	EventCommandAdmission EventType = "command.admission"
	EventCommandStart     EventType = "command.start"
	EventCommandResult    EventType = "command.result"
	EventCommandReject    EventType = "command.reject"
	// EventCommandExit is the original audit.v1 wire value. It remains
	// distinct from the stricter command.result event used by new producers.
	EventCommandExit    EventType = "command.exit"
	EventNetworkConnect EventType = "network.connect"
	EventTunnelState    EventType = "tunnel.state"
	EventConfigChange   EventType = "config.change"
	EventAppError       EventType = "app.error"
)

type EventClass string

const (
	ClassNormal   EventClass = "normal"
	ClassSecurity EventClass = "security"
)

type Severity string

const (
	SeverityDebug    Severity = "debug"
	SeverityInfo     Severity = "info"
	SeverityWarn     Severity = "warn"
	SeverityError    Severity = "error"
	SeverityCritical Severity = "critical"
)

type Outcome string

const (
	OutcomeStarted   Outcome = "started"
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
	OutcomeRejected  Outcome = "rejected"
	OutcomeDegraded  Outcome = "degraded"
)

// NetworkEnforcement is the only network fact that command audit records may
// carry. It intentionally describes enforcement state, never an address,
// policy rule, payload, or process detail.
type NetworkEnforcement string

const (
	NetworkEnforcementVerified    NetworkEnforcement = "verified"
	NetworkEnforcementUnavailable NetworkEnforcement = "unavailable"
	NetworkEnforcementRejected    NetworkEnforcement = "rejected"
	NetworkEnforcementNotChecked  NetworkEnforcement = "not_checked"
)

const (
	CommandActionAdmission = "command.admission"
	CommandActionStart     = "command.start"
	CommandActionResult    = "command.result"
	CommandActionReject    = "command.reject"
	CommandActionExit      = "command.exit"
)

// CommandBytes contains process output counts only. The output itself is
// deliberately absent from audit.v2. These counts are not transport or file
// read budget values; callers must leave Event.Budget zero for command output.
type CommandBytes struct {
	Stdout int64 `json:"stdout_bytes"`
	Stderr int64 `json:"stderr_bytes"`
}

// Budget covers transport and filesystem read accounting. A zero LimitBytes
// means that no limit was set; command process output counts use
// CommandBytes instead.
type Budget struct {
	WireInBytes      int64 `json:"wire_in_bytes"`
	WireOutBytes     int64 `json:"wire_out_bytes"`
	LogicalReadBytes int64 `json:"logical_read_bytes"`
	ReturnedBytes    int64 `json:"returned_bytes"`
	LimitBytes       int64 `json:"limit_bytes"`
}

type Event struct {
	EventID            string             `json:"-"`
	CorrelationID      string             `json:"-"`
	ParentID           string             `json:"-"`
	Class              EventClass         `json:"-"`
	Component          Component          `json:"-"`
	EventType          EventType          `json:"-"`
	Action             string             `json:"-"`
	Severity           Severity           `json:"-"`
	Outcome            Outcome            `json:"-"`
	ErrorCode          string             `json:"-"`
	DurationMS         int64              `json:"-"`
	ConnectionID       string             `json:"-"`
	ProfileID          string             `json:"-"`
	ProfileRevision    string             `json:"-"`
	Budget             Budget             `json:"-"`
	CommandID          string             `json:"-"`
	VariantID          string             `json:"-"`
	IdentityDigest     string             `json:"-"`
	ExitCode           *int64             `json:"-"`
	TimedOut           bool               `json:"-"`
	CommandBytes       CommandBytes       `json:"-"`
	NetworkEnforcement NetworkEnforcement `json:"-"`
	Timestamp          time.Time          `json:"-"`
}

type wireEvent struct {
	TS                 string             `json:"ts"`
	Event              string             `json:"event"`
	Schema             string             `json:"schema"`
	Instance           string             `json:"instance"`
	Correlation        string             `json:"correlation"`
	Parent             string             `json:"parent,omitempty"`
	Component          Component          `json:"component"`
	Type               EventType          `json:"type"`
	Action             string             `json:"action,omitempty"`
	Class              EventClass         `json:"class"`
	Severity           Severity           `json:"severity"`
	Outcome            Outcome            `json:"outcome"`
	ErrorCode          string             `json:"error_code,omitempty"`
	DurationMS         int64              `json:"duration_ms"`
	Connection         string             `json:"connection,omitempty"`
	Profile            string             `json:"profile,omitempty"`
	Revision           string             `json:"revision,omitempty"`
	Budget             Budget             `json:"budget"`
	CommandID          string             `json:"command_id,omitempty"`
	VariantID          string             `json:"variant_id,omitempty"`
	IdentityDigest     string             `json:"identity_digest,omitempty"`
	ExitCode           *int64             `json:"exit_code,omitempty"`
	TimedOut           bool               `json:"timed_out,omitempty"`
	Bytes              *CommandBytes      `json:"bytes,omitempty"`
	NetworkEnforcement NetworkEnforcement `json:"network_enforcement,omitempty"`
}

func toWireEvent(event Event, instanceID string) wireEvent {
	return wireEvent{
		TS: event.Timestamp.Format(time.RFC3339Nano), Event: event.EventID, Schema: SchemaVersion,
		Instance: instanceID, Correlation: event.CorrelationID, Parent: event.ParentID,
		Component: event.Component, Type: event.EventType, Action: event.Action, Class: event.Class,
		Severity: event.Severity, Outcome: event.Outcome, ErrorCode: event.ErrorCode,
		DurationMS: event.DurationMS, Connection: event.ConnectionID, Profile: event.ProfileID,
		Revision: event.ProfileRevision, Budget: event.Budget, CommandID: event.CommandID,
		VariantID: event.VariantID, IdentityDigest: event.IdentityDigest, ExitCode: event.ExitCode,
		TimedOut: event.TimedOut, Bytes: commandBytesPointer(event), NetworkEnforcement: event.NetworkEnforcement,
	}
}

func commandBytesPointer(event Event) *CommandBytes {
	if event.EventType != EventCommandResult {
		return nil
	}
	value := event.CommandBytes
	return &value
}

func validateEvent(event Event) error {
	for _, value := range []string{event.EventID, event.CorrelationID} {
		if err := validateID(value, false); err != nil {
			return err
		}
	}
	if err := validateID(event.ParentID, true); err != nil {
		return err
	}
	if event.Action != "" {
		if err := validateAtom(event.Action, 128); err != nil {
			return err
		}
	}
	if !oneOf(event.Component, ComponentMCP, ComponentAuth, ComponentPolicy, ComponentFSSearch, ComponentCommand, ComponentNetworkTunnel, ComponentServiceConfig, ComponentError) ||
		!oneOf(event.EventType, EventMCPCall, EventMCPResult, EventMCPList, EventAuthAccept, EventAuthReject, EventPolicyDecision, EventSearch, EventCommandAdmission, EventCommandStart, EventCommandResult, EventCommandReject, EventCommandExit, EventNetworkConnect, EventTunnelState, EventConfigChange, EventAppError) ||
		!oneOf(event.Class, ClassNormal, ClassSecurity) || !oneOf(event.Severity, SeverityDebug, SeverityInfo, SeverityWarn, SeverityError, SeverityCritical) ||
		!oneOf(event.Outcome, OutcomeStarted, OutcomeSucceeded, OutcomeFailed, OutcomeRejected, OutcomeDegraded) {
		return ErrInvalidEvent
	}
	if event.ErrorCode != "" {
		if containsSensitive(event.ErrorCode) {
			return ErrSensitiveField
		}
		if !validErrorCode(event.ErrorCode) {
			return ErrInvalidEvent
		}
	}
	for _, value := range []string{event.ConnectionID, event.ProfileID, event.ProfileRevision} {
		if err := validateID(value, true); err != nil {
			return err
		}
	}
	if event.DurationMS < 0 || event.DurationMS > maxDuration || event.Timestamp.IsZero() {
		return ErrInvalidEvent
	}
	if err := validateBudget(event.Budget); err != nil {
		return err
	}
	return validateCommandEvent(event)
}

func isCommandEvent(value EventType) bool {
	switch value {
	case EventCommandAdmission, EventCommandStart, EventCommandResult, EventCommandReject:
		return true
	default:
		return false
	}
}

func validateCommandEvent(event Event) error {
	if event.EventType == EventCommandExit {
		return validateLegacyCommandExit(event)
	}
	if !isCommandEvent(event.EventType) {
		if event.CommandID != "" || event.VariantID != "" || event.IdentityDigest != "" || event.ExitCode != nil || event.TimedOut || event.CommandBytes != (CommandBytes{}) || event.NetworkEnforcement != "" {
			return ErrInvalidEvent
		}
		return nil
	}
	if event.Component != ComponentCommand {
		return ErrInvalidEvent
	}
	if event.Action != commandActionForEvent(event.EventType) {
		return ErrInvalidEvent
	}
	reject := event.EventType == EventCommandReject
	for _, field := range []string{event.ConnectionID, event.ProfileID, event.ProfileRevision} {
		if err := validateRuleID(field, true); err != nil {
			return err
		}
	}
	if err := validateRuleID(event.CommandID, reject); err != nil {
		return err
	}
	if err := validateRuleID(event.VariantID, reject); err != nil {
		return err
	}
	if err := validateNetworkEnforcement(event.NetworkEnforcement); err != nil {
		return err
	}
	if (event.EventType == EventCommandAdmission || event.EventType == EventCommandStart) && event.NetworkEnforcement != NetworkEnforcementVerified {
		return ErrInvalidEvent
	}
	if event.IdentityDigest != "" {
		if err := validateDigest(event.IdentityDigest); err != nil {
			return err
		}
	}
	if event.EventType != EventCommandReject && event.IdentityDigest == "" {
		return ErrInvalidEvent
	}
	if event.EventType != EventCommandResult {
		if event.ExitCode != nil || event.TimedOut || event.CommandBytes != (CommandBytes{}) {
			return ErrInvalidEvent
		}
	} else {
		if event.CommandBytes.Stdout < 0 || event.CommandBytes.Stdout > maxCounter || event.CommandBytes.Stderr < 0 || event.CommandBytes.Stderr > maxCounter {
			return ErrInvalidEvent
		}
		if event.ExitCode == nil && !event.TimedOut {
			return ErrInvalidEvent
		}
		if event.ExitCode != nil && (*event.ExitCode < 0 || uint64(*event.ExitCode) > uint64(^uint32(0))) {
			return ErrInvalidEvent
		}
	}
	if event.EventType == EventCommandReject {
		if event.Class != ClassSecurity || event.Outcome != OutcomeRejected || !isWarningOrHigher(event.Severity) {
			return ErrInvalidEvent
		}
		if event.ErrorCode == "" {
			return ErrInvalidEvent
		}
	} else if event.Class != ClassNormal || event.Severity != SeverityInfo {
		return ErrInvalidEvent
	}
	if event.EventType == EventCommandAdmission && (event.Outcome != OutcomeSucceeded || event.NetworkEnforcement != NetworkEnforcementVerified) {
		return ErrInvalidEvent
	}
	if event.EventType == EventCommandStart && (event.Outcome != OutcomeStarted || event.NetworkEnforcement != NetworkEnforcementVerified) {
		return ErrInvalidEvent
	}
	if event.EventType == EventCommandResult {
		if err := validateCommandResultState(event); err != nil {
			return err
		}
	}
	return nil
}

func isWarningOrHigher(value Severity) bool {
	return oneOf(value, SeverityWarn, SeverityError, SeverityCritical)
}

func validateCommandResultState(event Event) error {
	if event.TimedOut {
		if event.ExitCode != nil || event.Outcome != OutcomeFailed || event.ErrorCode != "deadline_exceeded" {
			return ErrInvalidEvent
		}
		return nil
	}
	if event.ExitCode == nil {
		return ErrInvalidEvent
	}
	if *event.ExitCode == 0 {
		if event.NetworkEnforcement == NetworkEnforcementVerified {
			if event.Outcome != OutcomeSucceeded || event.ErrorCode != "" {
				return ErrInvalidEvent
			}
			return nil
		}
		if event.Outcome != OutcomeDegraded || event.ErrorCode != "unavailable" {
			return ErrInvalidEvent
		}
		return nil
	}
	if event.Outcome != OutcomeFailed || event.ErrorCode == "" || event.ErrorCode == "deadline_exceeded" {
		return ErrInvalidEvent
	}
	return nil
}

func validateLegacyCommandExit(event Event) error {
	if event.Component != ComponentCommand || (event.Action != "" && event.Action != CommandActionExit) {
		return ErrInvalidEvent
	}
	if event.Class != ClassNormal {
		return ErrInvalidEvent
	}
	if event.ExitCode != nil || event.TimedOut || event.CommandBytes != (CommandBytes{}) {
		return ErrInvalidEvent
	}
	// Validate optional new metadata if a legacy producer starts supplying it,
	// but do not require fields that were absent from the original wire event.
	for _, field := range []string{event.CommandID, event.VariantID} {
		if field != "" {
			if err := validateRuleID(field, false); err != nil {
				return err
			}
		}
	}
	if event.IdentityDigest != "" {
		if err := validateDigest(event.IdentityDigest); err != nil {
			return err
		}
	}
	if event.NetworkEnforcement != "" {
		if err := validateNetworkEnforcement(event.NetworkEnforcement); err != nil {
			return err
		}
	}
	return nil
}

func commandActionForEvent(value EventType) string {
	switch value {
	case EventCommandAdmission:
		return CommandActionAdmission
	case EventCommandStart:
		return CommandActionStart
	case EventCommandResult:
		return CommandActionResult
	case EventCommandReject:
		return CommandActionReject
	case EventCommandExit:
		return CommandActionExit
	default:
		return ""
	}
}

func validateRuleID(value string, optional bool) error {
	if value == "" {
		if optional {
			return nil
		}
		return ErrInvalidEvent
	}
	if containsSensitive(value) {
		return ErrSensitiveField
	}
	if len(value) > maxIDBytes {
		return ErrInvalidEvent
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return ErrInvalidEvent
		}
	}
	return nil
}

func validateDigest(value string) error {
	if len(value) != 64 {
		return ErrInvalidEvent
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
			return ErrInvalidEvent
		}
	}
	return nil
}

func validateNetworkEnforcement(value NetworkEnforcement) error {
	if !oneOf(value, NetworkEnforcementVerified, NetworkEnforcementUnavailable, NetworkEnforcementRejected, NetworkEnforcementNotChecked) {
		return ErrInvalidEvent
	}
	return nil
}

func validateBudget(value Budget) error {
	for _, counter := range []int64{value.WireInBytes, value.WireOutBytes, value.LogicalReadBytes, value.ReturnedBytes, value.LimitBytes} {
		if counter < 0 || counter > maxCounter {
			return ErrInvalidEvent
		}
	}
	return nil
}

func validateID(value string, optional bool) error {
	if value == "" {
		if optional {
			return nil
		}
		return ErrInvalidEvent
	}
	return validateAtom(value, maxIDBytes)
}

func validateAtom(value string, max int) error {
	if value == "" || len(value) > max {
		return ErrInvalidEvent
	}
	if containsSensitive(value) {
		return ErrSensitiveField
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:/+-", r)) {
			return ErrInvalidEvent
		}
	}
	return nil
}

func oneOf[T comparable](value T, allowed ...T) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validErrorCode(value string) bool {
	if validateAtom(value, 64) != nil {
		return false
	}
	return oneOf(value, "invalid_request", "denied", "not_found", "unsupported_type", "unsupported_encoding", "stale_version", "budget_exhausted", "deadline_exceeded", "cancelled", "unavailable", "auth_failed", "connection_refused", "port_conflict", "config_invalid", "queue_full", "child_exit", "identity_changed", "output_limit")
}
