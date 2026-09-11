package audit

import (
	"errors"
	"strings"
	"time"
	"unicode"
)

const (
	SchemaVersion       = "local-probe.audit.v1"
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
	EventMCPCall        EventType = "mcp.call"
	EventMCPResult      EventType = "mcp.result"
	EventMCPList        EventType = "mcp.list"
	EventAuthAccept     EventType = "auth.accept"
	EventAuthReject     EventType = "auth.reject"
	EventPolicyDecision EventType = "policy.decision"
	EventSearch         EventType = "fs.search"
	EventCommandStart   EventType = "command.start"
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

type Budget struct {
	WireInBytes      int64 `json:"wire_in_bytes"`
	WireOutBytes     int64 `json:"wire_out_bytes"`
	LogicalReadBytes int64 `json:"logical_read_bytes"`
	ReturnedBytes    int64 `json:"returned_bytes"`
	LimitBytes       int64 `json:"limit_bytes"`
}

type Event struct {
	EventID         string     `json:"-"`
	CorrelationID   string     `json:"-"`
	ParentID        string     `json:"-"`
	Class           EventClass `json:"-"`
	Component       Component  `json:"-"`
	EventType       EventType  `json:"-"`
	Action          string     `json:"-"`
	Severity        Severity   `json:"-"`
	Outcome         Outcome    `json:"-"`
	ErrorCode       string     `json:"-"`
	DurationMS      int64      `json:"-"`
	ConnectionID    string     `json:"-"`
	ProfileID       string     `json:"-"`
	ProfileRevision string     `json:"-"`
	Budget          Budget     `json:"-"`
	Timestamp       time.Time  `json:"-"`
}

type wireEvent struct {
	TS          string     `json:"ts"`
	Event       string     `json:"event"`
	Schema      string     `json:"schema"`
	Instance    string     `json:"instance"`
	Correlation string     `json:"correlation"`
	Parent      string     `json:"parent,omitempty"`
	Component   Component  `json:"component"`
	Type        EventType  `json:"type"`
	Action      string     `json:"action,omitempty"`
	Class       EventClass `json:"class"`
	Severity    Severity   `json:"severity"`
	Outcome     Outcome    `json:"outcome"`
	ErrorCode   string     `json:"error_code,omitempty"`
	DurationMS  int64      `json:"duration_ms"`
	Connection  string     `json:"connection,omitempty"`
	Profile     string     `json:"profile,omitempty"`
	Revision    string     `json:"revision,omitempty"`
	Budget      Budget     `json:"budget"`
}

func toWireEvent(event Event, instanceID string) wireEvent {
	return wireEvent{
		TS: event.Timestamp.Format(time.RFC3339Nano), Event: event.EventID, Schema: SchemaVersion,
		Instance: instanceID, Correlation: event.CorrelationID, Parent: event.ParentID,
		Component: event.Component, Type: event.EventType, Action: event.Action, Class: event.Class,
		Severity: event.Severity, Outcome: event.Outcome, ErrorCode: event.ErrorCode,
		DurationMS: event.DurationMS, Connection: event.ConnectionID, Profile: event.ProfileID,
		Revision: event.ProfileRevision, Budget: event.Budget,
	}
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
		!oneOf(event.EventType, EventMCPCall, EventMCPResult, EventMCPList, EventAuthAccept, EventAuthReject, EventPolicyDecision, EventSearch, EventCommandStart, EventCommandExit, EventNetworkConnect, EventTunnelState, EventConfigChange, EventAppError) ||
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
	return validateBudget(event.Budget)
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
