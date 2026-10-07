// Package supervisor contains the transport-neutral lifecycle contract for
// one local connection. It deliberately owns no process or tunnel launcher;
// production integrations must be supplied through the small interfaces in
// this package and are not part of this fake automation core.
package supervisor

import (
	"errors"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
)

const (
	// The lifecycle states are intentionally stable values for diagnostics and
	// management UI consumers. They do not assert that a real runtime exists.
	StateStopped       State = "stopped"
	StateStarting      State = "starting"
	StateLocalReady    State = "local_mcp_ready"
	StatePolling       State = "polling"
	StateReady         State = "ready"
	StateDegraded      State = "degraded"
	StateBackoff       State = "backoff"
	StateAuthFailed    State = "auth_failed"
	StateSleeping      State = "sleeping"
	StateResuming      State = "resuming"
	StateStopping      State = "stopping"
	StateCleanupFailed State = "cleanup_failed"
)

// State describes one connection's supervisor lifecycle.
// StateReady only means the injected local and remote checkers both returned
// HealthReady for the owned child and current spec revision. It is not proof
// of a production PID, HA, tunnel health, or external reachability.
type State string

// HealthMetadata contains only bounded, non-secret health-check labels and
// timing. It cannot hold an endpoint URL, local path, credential, or command.
type HealthMetadata struct {
	// CheckID is a logical checker label selected by trusted local code. It is
	// optional; an empty value means the default checker.
	CheckID string
	// Timeout bounds one injected health check. Zero uses the supervisor's
	// finite default health deadline.
	Timeout time.Duration
	// Interval optionally asks the supervisor to re-check a ready child. Zero
	// disables background health polling in this fake core.
	Interval time.Duration
}

// NewHealthMetadata validates and constructs bounded health metadata. The
// values are descriptive only; this function does not contact a service.
func NewHealthMetadata(checkID string, timeout, interval time.Duration) (HealthMetadata, error) {
	health := HealthMetadata{CheckID: checkID, Timeout: timeout, Interval: interval}
	if err := health.validate(); err != nil {
		return HealthMetadata{}, err
	}
	return health, nil
}

func (h HealthMetadata) validate() error {
	if h.CheckID != "" && !validSafeIdentifier(h.CheckID) {
		return ErrInvalidSpec
	}
	if h.Timeout < 0 || h.Timeout > maxHealthTimeout {
		return ErrInvalidSpec
	}
	if h.Interval < 0 || h.Interval > maxHealthInterval {
		return ErrInvalidSpec
	}
	return nil
}

const (
	maxHealthTimeout  = 5 * time.Minute
	maxHealthInterval = 24 * time.Hour
	minLocalPort      = 1024
)

var (
	ErrInvalidSpec       = errors.New("invalid connection spec")
	ErrConnectionExists  = errors.New("connection already exists")
	ErrConnectionMissing = errors.New("connection not found")
	ErrCircuitOpen       = errors.New("authentication circuit open")
	ErrSleeping          = errors.New("connection is sleeping")
	ErrRevisionConflict  = errors.New("connection revision changed")
	ErrSupervisorClosed  = errors.New("supervisor is closed")
	ErrNotRunning        = errors.New("connection is not running")
	ErrAuthFailed        = errors.New("authentication failed")
	ErrStopping          = errors.New("connection is stopping")
	ErrCleanupFailed     = errors.New("connection cleanup failed")
)

// ConnectionSpec is the complete trusted input to the lifecycle core. It
// intentionally has no executable path, argv, environment, URL, secret, or
// arbitrary filesystem path. Use NewConnectionSpec so callers cannot create
// a spec without the same validation applied by Add/Replace.
type ConnectionSpec struct {
	id          string
	revision    string
	transport   config.ConnectionTransport
	tunnelID    string
	tunnelAlias string
	localPort   uint16
	health      HealthMetadata
}

// NewConnectionSpec creates a validated, immutable-by-convention lifecycle
// specification. Tunnel IDs and aliases are descriptive opaque identifiers;
// credentials and endpoint resolution remain outside this package.
func NewConnectionSpec(id, revision string, transport config.ConnectionTransport, tunnelID, tunnelAlias string, localPort uint16, health HealthMetadata) (ConnectionSpec, error) {
	spec := ConnectionSpec{
		id:          id,
		revision:    revision,
		transport:   transport,
		tunnelID:    tunnelID,
		tunnelAlias: tunnelAlias,
		localPort:   localPort,
		health:      health,
	}
	if err := spec.Validate(); err != nil {
		return ConnectionSpec{}, err
	}
	return spec, nil
}

// NewLocalConnectionSpec is a convenience constructor for the common local
// transport. It does not enable or start anything.
func NewLocalConnectionSpec(id, revision string, localPort uint16, health HealthMetadata) (ConnectionSpec, error) {
	return NewConnectionSpec(id, revision, config.TransportLocal, "", "", localPort, health)
}

func (s ConnectionSpec) ID() string                            { return s.id }
func (s ConnectionSpec) Revision() string                      { return s.revision }
func (s ConnectionSpec) Transport() config.ConnectionTransport { return s.transport }
func (s ConnectionSpec) TunnelID() string                      { return s.tunnelID }
func (s ConnectionSpec) TunnelAlias() string                   { return s.tunnelAlias }
func (s ConnectionSpec) LocalPort() uint16                     { return s.localPort }
func (s ConnectionSpec) Health() HealthMetadata                { return s.health }
func (s ConnectionSpec) Clone() ConnectionSpec                 { return s }

// Validate rechecks all trusted boundaries. It intentionally returns only a
// stable sentinel so malformed values (including secret-shaped input) cannot
// be echoed to logs or management clients.
func (s ConnectionSpec) Validate() error {
	if !validSafeIdentifier(s.id) || !validSafeIdentifier(s.revision) {
		return ErrInvalidSpec
	}
	if s.localPort < minLocalPort {
		return ErrInvalidSpec
	}
	if err := s.health.validate(); err != nil {
		return err
	}
	switch s.transport {
	case config.TransportLocal:
		if s.tunnelID != "" || s.tunnelAlias != "" {
			return ErrInvalidSpec
		}
	case config.TransportOpenAIRuntime, config.TransportCloudflareNamed:
		if !config.ValidTransportIdentifier(s.tunnelID) {
			return ErrInvalidSpec
		}
		if s.tunnelAlias != "" && !config.ValidTransportIdentifier(s.tunnelAlias) {
			return ErrInvalidSpec
		}
	default:
		return ErrInvalidSpec
	}
	return nil
}

// Snapshot is a path-free, secret-free view of one connection. Tunnel IDs
// and aliases are non-secret identifiers; no credentials or implementation
// errors are included.
type Snapshot struct {
	ID          string
	Revision    string
	Transport   config.ConnectionTransport
	TunnelID    string
	TunnelAlias string
	LocalPort   uint16
	Health      HealthMetadata
	State       State
	Attempt     int
	NextRetryAt time.Time
	LastError   ErrorCode
}

// ErrorCode is a stable, non-sensitive diagnostic category. It deliberately
// carries no wrapped error message.
type ErrorCode string

const (
	ErrorNone           ErrorCode = ""
	ErrorRuntime        ErrorCode = "runtime"
	ErrorHealth         ErrorCode = "health"
	ErrorAuthFailed     ErrorCode = "auth_failed"
	ErrorRevisionChange ErrorCode = "revision_changed"
	ErrorStopped        ErrorCode = "stopped"
	ErrorSleeping       ErrorCode = "sleeping"
	ErrorCleanupFailed  ErrorCode = "cleanup_failed"
)

func validSafeIdentifier(value string) bool {
	if value == "" || len(value) > 128 || !isASCIIAlphaNumeric(value[0]) {
		return false
	}
	for i := 0; i < len(value); i++ {
		if !isASCIIAlphaNumeric(value[i]) && value[i] != '-' && value[i] != '_' {
			return false
		}
	}
	return true
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
