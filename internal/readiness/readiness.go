// Package readiness aggregates local runtime attestations into a bounded,
// one-use readiness decision.
//
// The package is deliberately transport-neutral. It does not perform an MCP
// request, contact a tunnel, inspect a process, or launch anything. A trusted
// local adapter owns an Issuer and Session, creates the typed attestations,
// and supplies the adapter's local observation time. Remote response times
// are not trusted as local freshness evidence. The scalar statuses below are
// adapter declarations, not OS proofs; a future production adapter must bind
// runtime-owner revalidation before using this result. This package alone is
// never an authorization boundary.
package readiness

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxIdentifierBytes = 128
	maxNonceBytes      = 32

	// MaxFreshnessWindow is the largest age accepted for any attestation.
	MaxFreshnessWindow = 5 * time.Minute
	// MaxNonceLimit bounds the in-memory replay set. Entries older than the
	// freshness window are retired before capacity is checked. The limit is
	// applied independently to each immutable session scope.
	MaxNonceLimit = 1 << 16
	// MaxScopeLimit bounds the number of live replay scopes retained by one
	// evaluator. This provides a global memory ceiling in addition to the
	// per-scope nonce limit.
	MaxScopeLimit = 1 << 14
	// MaxReplayEntries bounds the worst-case product of live scopes and nonce
	// entries per scope. Options whose product exceeds this operational memory
	// budget are rejected even when both individual values are valid.
	MaxReplayEntries = 1 << 20

	// DefaultFreshnessWindow is used when Options.Freshness is zero.
	DefaultFreshnessWindow = 30 * time.Second
	// DefaultNonceLimit is used when Options.NonceLimit is zero.
	DefaultNonceLimit = 4096
	// DefaultScopeLimit is used when Options.ScopeLimit is zero.
	DefaultScopeLimit = 256

	// ProductionReady is intentionally false. This package has no process,
	// MCP or tunnel authority and cannot by itself authorize an operation.
	ProductionReady = false
)

var (
	// ErrInvalidOptions means that an evaluator option is outside its bounded
	// range. The error contains no caller-controlled text.
	ErrInvalidOptions = errors.New("invalid readiness options")
	// ErrInvalidIssuer means that a trusted local issuer is absent or invalid.
	ErrInvalidIssuer = errors.New("invalid readiness issuer")
	// ErrInvalidSession means that a session was not created by an Issuer.
	ErrInvalidSession = errors.New("invalid readiness session")
	// ErrInvalidAttestation means that a typed local observation is malformed.
	ErrInvalidAttestation = errors.New("invalid readiness attestation")
	// ErrEntropyUnavailable means that a fresh local nonce could not be made.
	ErrEntropyUnavailable = errors.New("readiness entropy unavailable")
	// ErrLocalOnly is returned when a local readiness value is sent through a
	// JSON boundary. Readiness is an in-process contract, not a wire token.
	ErrLocalOnly = errors.New("readiness value is local-only")
)

// Clock supplies the instant used for all freshness checks. Implementations
// should be concurrency-safe when shared by evaluators.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time {
	if f == nil {
		return time.Time{}
	}
	return f()
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Options bounds one evaluator. Zero values select conservative defaults.
type Options struct {
	Clock      Clock
	Freshness  time.Duration
	NonceLimit int
	ScopeLimit int
}

// DefaultOptions returns the finite default freshness and replay bounds.
func DefaultOptions() Options {
	return Options{
		Clock:      systemClock{},
		Freshness:  DefaultFreshnessWindow,
		NonceLimit: DefaultNonceLimit,
		ScopeLimit: DefaultScopeLimit,
	}
}

// issuerState is the private capability held by trusted local adapter code.
// A pointer to this state is carried by Session and Context, so matching
// identifiers alone cannot make an attestation belong to another issuer.
type issuerState struct {
	mu       sync.Mutex
	entropy  io.Reader
	issuerID [maxNonceBytes]byte
}

// Issuer is a local-only factory for sessions. It is intentionally opaque and
// cannot cross JSON/MCP boundaries. The caller must keep it in trusted local
// adapter code; obtaining an Issuer is not itself OS or network proof.
type Issuer struct {
	state *issuerState
}

// NewIssuer creates an issuer using crypto/rand for issuer identity and for
// generated session capability/nonce material.
func NewIssuer() (*Issuer, error) {
	return NewIssuerWithEntropy(rand.Reader)
}

// NewIssuerWithEntropy is the deterministic-test constructor. Production
// callers should use NewIssuer so nonce material comes from crypto/rand.
func NewIssuerWithEntropy(entropy io.Reader) (*Issuer, error) {
	if entropy == nil {
		return nil, ErrEntropyUnavailable
	}
	state := &issuerState{entropy: entropy}
	if err := state.readRandom(state.issuerID[:]); err != nil {
		return nil, err
	}
	return &Issuer{state: state}, nil
}

// NewSession binds a connection, revision and runtime generation to this
// issuer. A session is the unforgeable local capability passed to attestation
// constructors. Create a new session when the trusted connection revision or
// generation changes.
func (i *Issuer) NewSession(connectionID, revision string, generation uint64) (*Session, error) {
	if i == nil || i.state == nil {
		return nil, ErrInvalidIssuer
	}
	if !validIdentifier(connectionID, maxIdentifierBytes) || !validIdentifier(revision, maxIdentifierBytes) || generation == 0 {
		return nil, ErrInvalidSession
	}
	state := &sessionState{
		issuer:       i.state,
		connectionID: connectionID,
		revision:     revision,
		generation:   generation,
	}
	i.state.mu.Lock()
	err := i.state.readRandom(state.capability[:])
	i.state.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &Session{state: state}, nil
}

func (i Issuer) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*Issuer) UnmarshalJSON([]byte) error { return ErrLocalOnly }

type sessionState struct {
	issuer       *issuerState
	capability   [maxNonceBytes]byte
	connectionID string
	revision     string
	generation   uint64
	revoked      atomic.Bool
}

// Session is a local-only issuer capability for one immutable connection
// revision/generation. Its fields cannot be supplied by a wire caller.
type Session struct {
	state *sessionState
}

func (s *Session) valid() bool {
	return s != nil && s.state != nil && s.state.issuer != nil &&
		!s.state.revoked.Load() &&
		validIdentifier(s.state.connectionID, maxIdentifierBytes) &&
		validIdentifier(s.state.revision, maxIdentifierBytes) && s.state.generation != 0
}

// Revoke invalidates this session and every Context/attestation derived from
// it. Revocation is local and irreversible; callers create a new session for
// a new connection revision or runtime generation.
func (s *Session) Revoke() {
	if s != nil && s.state != nil {
		s.state.revoked.Store(true)
	}
}

func (s *Session) ConnectionID() string {
	if !s.valid() {
		return ""
	}
	return s.state.connectionID
}

func (s *Session) Revision() string {
	if !s.valid() {
		return ""
	}
	return s.state.revision
}

func (s *Session) Generation() uint64 {
	if !s.valid() {
		return 0
	}
	return s.state.generation
}

// NewContext creates a fresh challenge bound to this session. The nonce is
// generated by the issuer and is never accepted from caller/model input.
func (s *Session) NewContext() (Context, error) {
	if !s.valid() {
		return Context{}, ErrInvalidSession
	}
	var nonce Nonce
	s.state.issuer.mu.Lock()
	err := s.state.issuer.readRandom(nonce.value[:])
	s.state.issuer.mu.Unlock()
	if err != nil {
		return Context{}, err
	}
	nonce.valid = true
	return Context{
		issuer:       s.state.issuer,
		session:      s.state,
		issuerID:     s.state.issuer.issuerID,
		capability:   s.state.capability,
		connectionID: s.state.connectionID,
		revision:     s.state.revision,
		generation:   s.state.generation,
		nonce:        nonce,
	}, nil
}

func (s *Session) NewChildAttestation(context Context, observedAt time.Time, pid uint32, creationID uint64, jobOwned bool) (ChildAttestation, error) {
	if !s.valid() || !s.owns(context) || observedAt.IsZero() {
		return ChildAttestation{}, ErrInvalidAttestation
	}
	return ChildAttestation{
		context: context, observedAt: observedAt, pid: pid, creationID: creationID, jobOwned: jobOwned,
	}, nil
}

// NewLocalMCPAttestation wraps an observation made by a trusted local adapter.
// observedAt must be sampled locally by that adapter; a timestamp reported by
// an MCP peer is not sufficient freshness evidence.
func (s *Session) NewLocalMCPAttestation(context Context, observedAt time.Time, auth Authentication, ping, serverInfo CheckState) (LocalMCPAttestation, error) {
	if !s.valid() || !s.owns(context) || observedAt.IsZero() || !auth.valid() || !ping.valid() || !serverInfo.valid() {
		return LocalMCPAttestation{}, ErrInvalidAttestation
	}
	return LocalMCPAttestation{
		context: context, observedAt: observedAt, auth: auth, ping: ping, serverInfo: serverInfo,
	}, nil
}

// NewRemoteTunnelAttestation wraps a remote health observation after the
// trusted local adapter sampled it. Remote-provided timestamps, URLs, tokens,
// response bodies and error text are not stored or accepted here.
func (s *Session) NewRemoteTunnelAttestation(context Context, observedAt time.Time, auth Authentication, health TunnelHealth, ha TunnelHA) (RemoteTunnelAttestation, error) {
	if !s.valid() || !s.owns(context) || observedAt.IsZero() || !auth.valid() || !health.valid() || !ha.valid() {
		return RemoteTunnelAttestation{}, ErrInvalidAttestation
	}
	return RemoteTunnelAttestation{
		context: context, observedAt: observedAt, auth: auth, health: health, ha: ha,
	}, nil
}

func (s *Session) owns(context Context) bool {
	return context.valid() && context.issuer == s.state.issuer && context.session == s.state
}

func (s Session) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*Session) UnmarshalJSON([]byte) error { return ErrLocalOnly }

func (state *issuerState) readRandom(dst []byte) error {
	if state == nil || state.entropy == nil || len(dst) == 0 {
		return ErrEntropyUnavailable
	}
	if _, err := io.ReadFull(state.entropy, dst); err != nil {
		return ErrEntropyUnavailable
	}
	for _, value := range dst {
		if value != 0 {
			return nil
		}
	}
	return ErrEntropyUnavailable
}

// Nonce is an opaque challenge generated only by Issuer/Session. No public
// constructor accepts a caller-supplied nonce.
type Nonce struct {
	value [maxNonceBytes]byte
	valid bool
}

func (n Nonce) validValue() bool {
	if !n.valid {
		return false
	}
	for _, value := range n.value {
		if value != 0 {
			return true
		}
	}
	return false
}

func (n Nonce) Equal(other Nonce) bool {
	if !n.validValue() || !other.validValue() {
		return false
	}
	return n.value == other.value
}

func (Nonce) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*Nonce) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// Context identifies one current issuer/session, connection revision and
// runtime generation. Every attestation supplied to Evaluate must carry an
// identical context. The private issuer/session pointers prevent JSON or
// identifier-only reconstruction.
type Context struct {
	issuer       *issuerState
	session      *sessionState
	issuerID     [maxNonceBytes]byte
	capability   [maxNonceBytes]byte
	connectionID string
	revision     string
	generation   uint64
	nonce        Nonce
}

func (c Context) valid() bool {
	return c.issuer != nil && c.session != nil && c.session.issuer == c.issuer &&
		!c.session.revoked.Load() &&
		c.issuerID == c.issuer.issuerID &&
		c.capability == c.session.capability &&
		validIdentifier(c.connectionID, maxIdentifierBytes) &&
		validIdentifier(c.revision, maxIdentifierBytes) && c.generation != 0 &&
		c.connectionID == c.session.connectionID && c.revision == c.session.revision &&
		c.generation == c.session.generation && c.nonce.validValue()
}

func (c Context) ConnectionID() string { return c.connectionID }
func (c Context) Revision() string     { return c.revision }
func (c Context) Generation() uint64   { return c.generation }
func (c Context) Nonce() Nonce         { return c.nonce }

// SameIdentity reports whether two valid contexts refer to exactly the same
// issuer/session, connection, revision, generation and nonce.
func (c Context) SameIdentity(other Context) bool {
	return c.valid() && other.valid() && c.issuer == other.issuer && c.session == other.session &&
		c.connectionID == other.connectionID && c.revision == other.revision &&
		c.generation == other.generation && c.nonce.Equal(other.nonce)
}

func (Context) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*Context) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// ChildAttestation records a trusted adapter's observation that the owned
// child has a non-zero identity and is held by the expected ownership scope.
// PID and CreationID are safe metadata; no path or command line is retained.
type ChildAttestation struct {
	context    Context
	observedAt time.Time
	pid        uint32
	creationID uint64
	jobOwned   bool
}

func (a ChildAttestation) valid() bool { return a.context.valid() && !a.observedAt.IsZero() }

// Zero PID/CreationID or false jobOwned are retained as typed negative
// evidence so the evaluator can reject them instead of treating process
// existence alone as ownership proof.
func (a ChildAttestation) Context() Context      { return a.context }
func (a ChildAttestation) ObservedAt() time.Time { return a.observedAt }
func (a ChildAttestation) PID() uint32           { return a.pid }
func (a ChildAttestation) CreationID() uint64    { return a.creationID }
func (a ChildAttestation) JobOwned() bool        { return a.jobOwned }

func (ChildAttestation) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*ChildAttestation) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// Authentication is the bounded classification returned by a trusted local
// authentication adapter. It intentionally has no provider message, token or
// endpoint field.
type Authentication string

const (
	AuthUnknown       Authentication = "unknown"
	AuthAuthenticated Authentication = "authenticated"
	AuthMissing       Authentication = "missing"
	AuthRejected      Authentication = "rejected"
	AuthExpired       Authentication = "expired"
)

func (a Authentication) valid() bool {
	switch a {
	case AuthUnknown, AuthAuthenticated, AuthMissing, AuthRejected, AuthExpired:
		return true
	default:
		return false
	}
}

// CheckState is the bounded result for one authenticated local MCP check.
type CheckState string

const (
	CheckUnknown  CheckState = "unknown"
	CheckReady    CheckState = "ready"
	CheckNotReady CheckState = "not_ready"
)

func (s CheckState) valid() bool {
	switch s {
	case CheckUnknown, CheckReady, CheckNotReady:
		return true
	default:
		return false
	}
}

// LocalMCPAttestation records authenticated ping and server_info observations
// made by a trusted local adapter. Both checks are required for readiness.
type LocalMCPAttestation struct {
	context    Context
	observedAt time.Time
	auth       Authentication
	ping       CheckState
	serverInfo CheckState
}

func (a LocalMCPAttestation) valid() bool {
	return a.context.valid() && !a.observedAt.IsZero() && a.auth.valid() && a.ping.valid() && a.serverInfo.valid()
}

func (a LocalMCPAttestation) Context() Context               { return a.context }
func (a LocalMCPAttestation) ObservedAt() time.Time          { return a.observedAt }
func (a LocalMCPAttestation) Authentication() Authentication { return a.auth }
func (a LocalMCPAttestation) Ping() CheckState               { return a.ping }
func (a LocalMCPAttestation) ServerInfo() CheckState         { return a.serverInfo }

func (LocalMCPAttestation) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*LocalMCPAttestation) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// TunnelHealth is the bounded health state supplied by a trusted remote
// tunnel adapter.
type TunnelHealth string

const (
	TunnelHealthUnknown  TunnelHealth = "unknown"
	TunnelHealthReady    TunnelHealth = "ready"
	TunnelHealthNotReady TunnelHealth = "not_ready"
)

func (s TunnelHealth) valid() bool {
	switch s {
	case TunnelHealthUnknown, TunnelHealthReady, TunnelHealthNotReady:
		return true
	default:
		return false
	}
}

// TunnelHA is the bounded high-availability state supplied by a trusted
// remote tunnel adapter. A ready result requires an explicitly ready HA view.
type TunnelHA string

const (
	TunnelHAUnknown  TunnelHA = "unknown"
	TunnelHAReady    TunnelHA = "ready"
	TunnelHANotReady TunnelHA = "not_ready"
)

func (s TunnelHA) valid() bool {
	switch s {
	case TunnelHAUnknown, TunnelHAReady, TunnelHANotReady:
		return true
	default:
		return false
	}
}

// RemoteTunnelAttestation records the authenticated tunnel health and HA
// observation. It contains no URL, tunnel token, raw response or error text.
type RemoteTunnelAttestation struct {
	context    Context
	observedAt time.Time
	auth       Authentication
	health     TunnelHealth
	ha         TunnelHA
}

func (a RemoteTunnelAttestation) valid() bool {
	return a.context.valid() && !a.observedAt.IsZero() && a.auth.valid() && a.health.valid() && a.ha.valid()
}

func (a RemoteTunnelAttestation) Context() Context               { return a.context }
func (a RemoteTunnelAttestation) ObservedAt() time.Time          { return a.observedAt }
func (a RemoteTunnelAttestation) Authentication() Authentication { return a.auth }
func (a RemoteTunnelAttestation) Health() TunnelHealth           { return a.health }
func (a RemoteTunnelAttestation) HA() TunnelHA                   { return a.ha }

func (RemoteTunnelAttestation) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*RemoteTunnelAttestation) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// AuthFailureClass classifies an authentication-related negative decision.
// Empty means that authentication was not the reason for the decision.
type AuthFailureClass string

const (
	AuthFailureNone     AuthFailureClass = ""
	AuthFailureUnknown  AuthFailureClass = "unknown"
	AuthFailureMissing  AuthFailureClass = "missing"
	AuthFailureRejected AuthFailureClass = "rejected"
	AuthFailureExpired  AuthFailureClass = "expired"
)

// Reason is a stable, non-sensitive readiness result. It never includes
// connection identifiers, endpoints, paths or adapter error text.
type Reason string

const (
	ReasonReady                   Reason = "ready"
	ReasonEvaluatorUnavailable    Reason = "evaluator_unavailable"
	ReasonInvalidContext          Reason = "invalid_context"
	ReasonClockUnavailable        Reason = "clock_unavailable"
	ReasonClockRollback           Reason = "clock_rollback"
	ReasonChildMissing            Reason = "child_missing"
	ReasonConnectionMismatch      Reason = "connection_mismatch"
	ReasonRevisionMismatch        Reason = "revision_mismatch"
	ReasonGenerationMismatch      Reason = "generation_mismatch"
	ReasonIssuerMismatch          Reason = "issuer_mismatch"
	ReasonNonceMismatch           Reason = "nonce_mismatch"
	ReasonChildStale              Reason = "child_stale"
	ReasonChildNotOwned           Reason = "child_not_owned"
	ReasonLocalMCPMissing         Reason = "local_mcp_missing"
	ReasonLocalMCPStale           Reason = "local_mcp_stale"
	ReasonLocalAuthFailed         Reason = "local_auth_failed"
	ReasonLocalPingNotReady       Reason = "local_ping_not_ready"
	ReasonLocalServerInfoNotReady Reason = "local_server_info_not_ready"
	ReasonRemoteTunnelMissing     Reason = "remote_tunnel_missing"
	ReasonRemoteTunnelStale       Reason = "remote_tunnel_stale"
	ReasonRemoteAuthFailed        Reason = "remote_auth_failed"
	ReasonRemoteHealthNotReady    Reason = "remote_health_not_ready"
	ReasonRemoteHANotReady        Reason = "remote_ha_not_ready"
	ReasonDuplicateNonce          Reason = "duplicate_nonce"
	ReasonNonceCapacity           Reason = "nonce_capacity"
)

// Decision is an opaque local result. Use Ready, Reason and AuthFailure to
// inspect it; do not serialize it or use it as a remote authorization token.
type Decision struct {
	ready       bool
	reason      Reason
	authFailure AuthFailureClass
}

func readyDecision() Decision { return Decision{ready: true, reason: ReasonReady} }

func rejectedDecision(reason Reason) Decision { return Decision{reason: reason} }

func (d Decision) Ready() bool                   { return d.ready && d.reason == ReasonReady }
func (d Decision) Reason() Reason                { return d.reason }
func (d Decision) AuthFailure() AuthFailureClass { return d.authFailure }

func (Decision) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*Decision) UnmarshalJSON([]byte) error { return ErrLocalOnly }

// Evaluator performs freshness and one-use nonce checks. It is safe for
// concurrent use. A trusted adapter must keep one evaluator for the full
// readiness lifecycle; constructing an evaluator per request would discard
// replay history. The replay set is local memory and intentionally cannot be
// restored from configuration or a wire request. Entries are retired after
// the freshness window to keep the memory bound finite.
type Evaluator struct {
	clock      Clock
	freshness  time.Duration
	nonceLimit int
	scopeLimit int

	state *replayState
}

type replayState struct {
	mu      sync.Mutex
	seen    map[nonceScope]map[nonceKey]time.Time
	lastNow time.Time
}

// nonceKey intentionally binds replay identity to all current scope fields;
// equal raw nonce bytes from another connection/revision/generation are not a
// replay of this scope.
type nonceKey struct {
	issuerID   [maxNonceBytes]byte
	capability [maxNonceBytes]byte
	nonce      [maxNonceBytes]byte
}

type nonceScope struct {
	issuerID     [maxNonceBytes]byte
	capability   [maxNonceBytes]byte
	connectionID string
	revision     string
	generation   uint64
}

// New constructs an evaluator with bounded options. A nil clock selects the
// process clock; deterministic tests should inject a Clock explicitly.
func New(options Options) (*Evaluator, error) {
	if options == (Options{}) {
		options = DefaultOptions()
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	if options.Freshness == 0 {
		options.Freshness = DefaultFreshnessWindow
	}
	if options.NonceLimit == 0 {
		options.NonceLimit = DefaultNonceLimit
	}
	if options.ScopeLimit == 0 {
		options.ScopeLimit = DefaultScopeLimit
	}
	if options.Freshness <= 0 || options.Freshness > MaxFreshnessWindow ||
		options.NonceLimit <= 0 || options.NonceLimit > MaxNonceLimit ||
		options.ScopeLimit <= 0 || options.ScopeLimit > MaxScopeLimit ||
		options.ScopeLimit > MaxReplayEntries/options.NonceLimit {
		return nil, ErrInvalidOptions
	}
	return &Evaluator{
		clock: options.Clock, freshness: options.Freshness,
		nonceLimit: options.NonceLimit, scopeLimit: options.ScopeLimit,
		state: &replayState{seen: make(map[nonceScope]map[nonceKey]time.Time)},
	}, nil
}

// Evaluate returns ready only when every required attestation is bound to the
// supplied current context, fresh at the same sampled instant, and positive:
// an owned child, authenticated local MCP ping+server_info, and authenticated
// remote tunnel health+HA. Every structurally valid matching attempt consumes
// its nonce, including stale and negative auth/health results, so a challenge
// cannot be replayed before its bounded replay entry expires.
func (e *Evaluator) Evaluate(current Context, child ChildAttestation, local LocalMCPAttestation, remote RemoteTunnelAttestation) Decision {
	if e == nil {
		return rejectedDecision(ReasonEvaluatorUnavailable)
	}
	if !current.valid() {
		return rejectedDecision(ReasonInvalidContext)
	}
	now, ok := e.now()
	if !ok {
		return rejectedDecision(ReasonClockUnavailable)
	}

	if !child.valid() {
		return rejectedDecision(ReasonChildMissing)
	}
	if reason := compareContext(current, child.context); reason != ReasonReady {
		return rejectedDecision(reason)
	}
	if !local.valid() {
		return rejectedDecision(ReasonLocalMCPMissing)
	}
	if reason := compareContext(current, local.context); reason != ReasonReady {
		return rejectedDecision(reason)
	}
	if !remote.valid() {
		return rejectedDecision(ReasonRemoteTunnelMissing)
	}
	if reason := compareContext(current, remote.context); reason != ReasonReady {
		return rejectedDecision(reason)
	}

	// A matching nonce is single-use even when a later freshness or health
	// check fails. The trusted adapter must issue a new challenge for every
	// retry; otherwise a stale observation could be replayed after recovery.
	scope := nonceScope{
		issuerID:     current.issuerID,
		capability:   current.capability,
		connectionID: current.connectionID,
		revision:     current.revision,
		generation:   current.generation,
	}
	outcome := e.consumeNonce(scope, nonceKey{
		issuerID: current.issuerID, capability: current.capability, nonce: current.nonce.value,
	}, now)
	switch outcome {
	case nonceClockRollback:
		return rejectedDecision(ReasonClockRollback)
	case nonceCapacity:
		return rejectedDecision(ReasonNonceCapacity)
	case nonceDuplicate:
		return rejectedDecision(ReasonDuplicateNonce)
	}

	if !fresh(now, child.observedAt, e.freshness) {
		return rejectedDecision(ReasonChildStale)
	}
	if !fresh(now, local.observedAt, e.freshness) {
		return rejectedDecision(ReasonLocalMCPStale)
	}
	if !fresh(now, remote.observedAt, e.freshness) {
		return rejectedDecision(ReasonRemoteTunnelStale)
	}
	if child.pid == 0 || child.creationID == 0 || !child.jobOwned {
		return rejectedDecision(ReasonChildNotOwned)
	}

	if local.auth != AuthAuthenticated {
		return Decision{reason: ReasonLocalAuthFailed, authFailure: classifyAuth(local.auth)}
	}
	if local.ping != CheckReady {
		return rejectedDecision(ReasonLocalPingNotReady)
	}
	if local.serverInfo != CheckReady {
		return rejectedDecision(ReasonLocalServerInfoNotReady)
	}
	if remote.auth != AuthAuthenticated {
		return Decision{reason: ReasonRemoteAuthFailed, authFailure: classifyAuth(remote.auth)}
	}
	if remote.health != TunnelHealthReady {
		return rejectedDecision(ReasonRemoteHealthNotReady)
	}
	if remote.ha != TunnelHAReady {
		return rejectedDecision(ReasonRemoteHANotReady)
	}
	// This is the readiness operation's revocation linearization point. A
	// concurrent Revoke that wins before this load prevents a Ready result;
	// a Revoke after it is ordered after this overlapping evaluation.
	if current.session.revoked.Load() {
		return rejectedDecision(ReasonInvalidContext)
	}
	return readyDecision()
}

func (e *Evaluator) now() (now time.Time, ok bool) {
	if e == nil || e.clock == nil {
		return time.Time{}, false
	}
	defer func() {
		if recover() != nil {
			now, ok = time.Time{}, false
		}
	}()
	now = e.clock.Now()
	return now, !now.IsZero()
}

type nonceOutcome uint8

const (
	nonceAccepted nonceOutcome = iota
	nonceDuplicate
	nonceCapacity
	nonceClockRollback
)

func (e *Evaluator) consumeNonce(scope nonceScope, key nonceKey, now time.Time) nonceOutcome {
	if e == nil || e.state == nil {
		return nonceCapacity
	}
	e.state.mu.Lock()
	defer e.state.mu.Unlock()
	if !e.state.lastNow.IsZero() && now.Before(e.state.lastNow) {
		return nonceClockRollback
	}
	if now.After(e.state.lastNow) {
		e.state.lastNow = now
	}
	for seenScope, entries := range e.state.seen {
		for seenKey, seenAt := range entries {
			// fresh accepts age == freshness, so retain the replay marker at
			// that exact boundary as well. Deletion must use the strict inverse.
			if now.Sub(seenAt) > e.freshness {
				delete(entries, seenKey)
			}
		}
		if len(entries) == 0 {
			delete(e.state.seen, seenScope)
		}
	}
	entries := e.state.seen[scope]
	if _, exists := entries[key]; exists {
		return nonceDuplicate
	}
	if entries == nil && len(e.state.seen) >= e.scopeLimit {
		return nonceCapacity
	}
	// NonceLimit is per immutable issuer/session/connection scope. One noisy
	// connection cannot consume another connection's replay capacity.
	if len(entries) >= e.nonceLimit {
		return nonceCapacity
	}
	if entries == nil {
		entries = make(map[nonceKey]time.Time)
		e.state.seen[scope] = entries
	}
	entries[key] = now
	return nonceAccepted
}

func compareContext(want, got Context) Reason {
	if !got.valid() {
		return ReasonInvalidContext
	}
	if want.connectionID != got.connectionID {
		return ReasonConnectionMismatch
	}
	if want.revision != got.revision {
		return ReasonRevisionMismatch
	}
	if want.generation != got.generation {
		return ReasonGenerationMismatch
	}
	if want.issuer != got.issuer || want.session != got.session || want.issuerID != got.issuerID || want.capability != got.capability {
		return ReasonIssuerMismatch
	}
	if want.nonce.value != got.nonce.value {
		return ReasonNonceMismatch
	}
	return ReasonReady
}

func fresh(now, observed time.Time, window time.Duration) bool {
	if now.IsZero() || observed.IsZero() {
		return false
	}
	age := now.Sub(observed)
	return age >= 0 && age <= window
}

func classifyAuth(auth Authentication) AuthFailureClass {
	switch auth {
	case AuthMissing:
		return AuthFailureMissing
	case AuthRejected:
		return AuthFailureRejected
	case AuthExpired:
		return AuthFailureExpired
	default:
		return AuthFailureUnknown
	}
}

func validIdentifier(value string, limit int) bool {
	if len(value) == 0 || len(value) > limit {
		return false
	}
	if !asciiAlphaNumeric(value[0]) {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !asciiAlphaNumeric(value[i]) && value[i] != '-' && value[i] != '_' {
			return false
		}
	}
	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func (Evaluator) MarshalJSON() ([]byte, error) { return nil, ErrLocalOnly }

func (*Evaluator) UnmarshalJSON([]byte) error { return ErrLocalOnly }

var (
	_ json.Marshaler = Issuer{}
	_ json.Marshaler = Session{}
	_ json.Marshaler = Nonce{}
	_ json.Marshaler = Context{}
	_ json.Marshaler = ChildAttestation{}
	_ json.Marshaler = LocalMCPAttestation{}
	_ json.Marshaler = RemoteTunnelAttestation{}
	_ json.Marshaler = Decision{}
	_ json.Marshaler = Evaluator{}
)
