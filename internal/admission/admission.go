// Package admission implements the local, transport-neutral request gate.
//
// The gate owns only bounded admission and lifecycle state. It does not
// execute commands, open files, authenticate network requests, or create
// MCP responses. Callers must provide a binding obtained from trusted local
// policy code; model input must not be used to construct one.
package admission

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxIdentifierBytes   = 128
	maxLimit             = 1 << 16
	defaultGlobal        = 16
	defaultPerConnection = 4
	defaultAuditTimeout  = 250 * time.Millisecond
)

// ErrorCode is a stable, path-free and secret-free admission failure code.
type ErrorCode string

const (
	CodeInvalidConfig      ErrorCode = "invalid_config"
	CodeInvalidBinding     ErrorCode = "invalid_binding"
	CodeConnectionExists   ErrorCode = "connection_exists"
	CodeConnectionMissing  ErrorCode = "connection_missing"
	CodeConnectionDisabled ErrorCode = "connection_disabled"
	CodeConnectionRevoked  ErrorCode = "connection_revoked"
	CodeBindingMismatch    ErrorCode = "binding_mismatch"
	CodeAuditUnavailable   ErrorCode = "audit_unavailable"
	CodeGateClosed         ErrorCode = "admission_closed"
	CodeGlobalCapacity     ErrorCode = "global_capacity"
	CodeConnectionCapacity ErrorCode = "connection_capacity"
	CodeCancelled          ErrorCode = "cancelled"
	CodeLocalOnly          ErrorCode = "local_only"
)

// Error is an opaque, stable admission error. It intentionally does not carry
// connection IDs, profile IDs, revisions or underlying error text.
type Error struct {
	code ErrorCode
}

func (e *Error) Error() string {
	if e == nil {
		return string(CodeInvalidConfig)
	}
	return string(e.code)
}

// Code returns the stable error code. A nil error returns an empty code.
func (e *Error) Code() ErrorCode {
	if e == nil {
		return ""
	}
	return e.code
}

// Is lets callers use errors.Is with the package sentinels without exposing
// any dynamic diagnostic detail.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && e != nil && other != nil && e.code == other.code
}

var (
	ErrInvalidConfig      = &Error{code: CodeInvalidConfig}
	ErrInvalidBinding     = &Error{code: CodeInvalidBinding}
	ErrConnectionExists   = &Error{code: CodeConnectionExists}
	ErrConnectionMissing  = &Error{code: CodeConnectionMissing}
	ErrConnectionDisabled = &Error{code: CodeConnectionDisabled}
	ErrConnectionRevoked  = &Error{code: CodeConnectionRevoked}
	ErrBindingMismatch    = &Error{code: CodeBindingMismatch}
	ErrAuditUnavailable   = &Error{code: CodeAuditUnavailable}
	ErrGateClosed         = &Error{code: CodeGateClosed}
	ErrGlobalCapacity     = &Error{code: CodeGlobalCapacity}
	ErrConnectionCapacity = &Error{code: CodeConnectionCapacity}
	ErrCancelled          = &Error{code: CodeCancelled}
	ErrLocalOnly          = &Error{code: CodeLocalOnly}
)

// Limits bounds active permits. A zero Limits value means DefaultLimits when
// passed to New; individual zero or negative fields in a non-zero value are
// rejected rather than silently disabling a bound.
type Limits struct {
	MaxGlobal        int
	MaxPerConnection int
}

// DefaultLimits returns conservative local defaults. The returned value is a
// plain configuration value and has no live state.
func DefaultLimits() Limits {
	return Limits{MaxGlobal: defaultGlobal, MaxPerConnection: defaultPerConnection}
}

// NewLimits validates and constructs explicit admission limits.
func NewLimits(maxGlobal, maxPerConnection int) (Limits, error) {
	limits := Limits{MaxGlobal: maxGlobal, MaxPerConnection: maxPerConnection}
	if err := limits.Validate(); err != nil {
		return Limits{}, err
	}
	return limits, nil
}

// Validate checks hard bounds for both dimensions.
func (l Limits) Validate() error {
	if l.MaxGlobal < 1 || l.MaxGlobal > maxLimit || l.MaxPerConnection < 1 || l.MaxPerConnection > maxLimit {
		return ErrInvalidConfig
	}
	return nil
}

// AuditHealth is supplied by the trusted local audit owner. Healthy must be
// false while the sink is degraded, closed, or otherwise unable to accept the
// audit evidence required by the caller. A nil health provider fails closed.
type AuditHealth interface {
	Healthy() bool
}

// AuditHealthFunc adapts a local health check without exposing transport or
// sink implementation details to this package.
type AuditHealthFunc func() bool

func (f AuditHealthFunc) Healthy() bool {
	return f != nil && f()
}

// AuditHealthState is a small concurrency-safe health source for adapters
// that learn about sink failures asynchronously. Its zero value is unhealthy.
type AuditHealthState struct {
	healthy atomic.Bool
}

// NewAuditHealthState creates a mutable health source with an explicit state.
func NewAuditHealthState(healthy bool) *AuditHealthState {
	state := &AuditHealthState{}
	state.healthy.Store(healthy)
	return state
}

func (s *AuditHealthState) Healthy() bool {
	return s != nil && s.healthy.Load()
}

// SetHealthy changes the local admission health view. The caller should set
// false immediately when audit delivery fails and set true only after the
// sink has recovered.
func (s *AuditHealthState) SetHealthy(healthy bool) {
	if s != nil {
		s.healthy.Store(healthy)
	}
}

// Connection is a trusted local connection/profile/revision tuple. It has no
// credential, endpoint, path or command fields. Use NewConnection rather than
// constructing a zero value.
type Connection struct {
	id        string
	profileID string
	revision  string
	enabled   bool
}

// NewConnection constructs a validated connection record. Disabled records
// are valid and can be enabled by the local management layer later.
func NewConnection(id, profileID, revision string, enabled bool) (Connection, error) {
	connection := Connection{id: id, profileID: profileID, revision: revision, enabled: enabled}
	if err := connection.Validate(); err != nil {
		return Connection{}, err
	}
	return connection, nil
}

func (c Connection) Validate() error {
	if !validIdentifier(c.id) || !validIdentifier(c.profileID) || !validIdentifier(c.revision) {
		return ErrInvalidConfig
	}
	return nil
}

func (c Connection) ID() string        { return c.id }
func (c Connection) ProfileID() string { return c.profileID }
func (c Connection) Revision() string  { return c.revision }
func (c Connection) Enabled() bool     { return c.enabled }

// Binding is the local typed identity required for admission. The fields are
// private and JSON serialization is explicitly refused so a model cannot
// manufacture or alter a binding through a wire request.
type Binding struct {
	connectionID string
	profileID    string
	revision     string
}

// NewBinding constructs a binding for a trusted local caller.
func NewBinding(connectionID, profileID, revision string) (Binding, error) {
	binding := Binding{connectionID: connectionID, profileID: profileID, revision: revision}
	if err := binding.Validate(); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

func (b Binding) Validate() error {
	if !validIdentifier(b.connectionID) || !validIdentifier(b.profileID) || !validIdentifier(b.revision) {
		return ErrInvalidBinding
	}
	return nil
}

func (b Binding) ConnectionID() string { return b.connectionID }
func (b Binding) ProfileID() string    { return b.profileID }
func (b Binding) Revision() string     { return b.revision }

func (Binding) MarshalJSON() ([]byte, error) {
	return nil, ErrLocalOnly
}

func (*Binding) UnmarshalJSON([]byte) error {
	return ErrLocalOnly
}

// Snapshot is a non-sensitive point-in-time view of one gate connection.
type Snapshot struct {
	ID        string
	ProfileID string
	Revision  string
	Enabled   bool
	Revoked   bool
	Active    int
	Limit     int
}

// Gate bounds all active local requests and tracks connection lifecycle. It
// is safe for concurrent callers. It does not promise FIFO fairness.
type Gate struct {
	mu     sync.Mutex
	limits Limits
	audit  AuditHealth
	ready  atomic.Bool
	// auditOverride is controlled by trusted local management code. It lets an
	// adapter fail closed immediately when a sink error is observed, without
	// requiring the adapter to mutate its own health implementation.
	auditBlocked bool
	auditEpoch   uint64
	closed       bool

	connections map[string]*connectionState
	global      int
	nextID      uint64
	notify      chan struct{}
	drained     chan struct{}
	auditCheck  *auditChecker
}

// auditChecker invokes an external health provider on one bounded worker.
// Calling the provider from an admission goroutine would make context
// cancellation impossible when a provider blocks; starting one goroutine per
// request would make an outage an unbounded goroutine leak. The single worker
// and one-item queue keep both failure modes bounded. A blocked provider can
// still occupy this worker until its own implementation returns, but callers
// waiting in Acquire remain cancellable.
type auditChecker struct {
	jobs chan auditCheckRequest
	stop chan struct{}
	once sync.Once
}

type auditCheckRequest struct {
	provider AuditHealth
	result   chan bool
}

type auditSnapshot struct {
	provider AuditHealth
	checker  *auditChecker
	blocked  bool
	epoch    uint64
}

func newAuditChecker() *auditChecker {
	checker := &auditChecker{
		// An unbuffered handoff prevents a reentrant provider from leaving a
		// recursively generated check queued behind the check it is currently
		// executing. The finite check deadline below makes such reentrancy fail
		// closed instead of deadlocking admission forever.
		jobs: make(chan auditCheckRequest),
		stop: make(chan struct{}),
	}
	go checker.run()
	return checker
}

func (c *auditChecker) run() {
	for {
		select {
		case <-c.stop:
			return
		case request := <-c.jobs:
			healthy := safeHealthy(request.provider)
			// result is buffered, so an Acquire whose context was canceled
			// cannot strand this bounded worker after the provider returns.
			select {
			case request.result <- healthy:
			default:
			}
		}
	}
}

func (c *auditChecker) check(ctx context.Context, provider AuditHealth) (bool, error) {
	if c == nil || provider == nil {
		return false, nil
	}
	result := make(chan bool, 1)
	request := auditCheckRequest{provider: provider, result: result}
	select {
	case <-ctx.Done():
		return false, ErrCancelled
	case <-c.stop:
		return false, nil
	case c.jobs <- request:
	}
	select {
	case <-ctx.Done():
		return false, ErrCancelled
	case <-c.stop:
		return false, nil
	case healthy := <-result:
		return healthy, nil
	}
}

func (c *auditChecker) close() {
	if c != nil {
		c.once.Do(func() { close(c.stop) })
	}
}

func safeHealthy(provider AuditHealth) (healthy bool) {
	if provider == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			healthy = false
		}
	}()
	return provider.Healthy()
}

// Permit is an opaque local capability returned by a successful admission.
// It carries the immutable binding and a cancellation context, but no
// serializable fields that a transport caller can forge.
type Permit struct {
	gate      *Gate
	owner     *connectionState
	id        uint64
	binding   Binding
	ctx       context.Context
	cancel    context.CancelFunc
	lifecycle *permitLifecycle
}

// permitLifecycle is deliberately shared by copies of a Permit value. The
// public API returns *Permit, but sharing the guard also keeps an accidental
// value copy from releasing capacity twice.
type permitLifecycle struct {
	once     sync.Once
	released atomic.Bool
}

type connectionState struct {
	spec    Connection
	enabled bool
	revoked bool
	active  int
	permits map[uint64]*Permit
}

// New creates a gate. A nil audit health source is accepted for construction
// but causes every new admission to return ErrAuditUnavailable (fail closed).
func New(limits Limits, auditHealth AuditHealth) (*Gate, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	gate := &Gate{
		limits:      limits,
		audit:       auditHealth,
		connections: make(map[string]*connectionState),
		notify:      make(chan struct{}),
		drained:     make(chan struct{}),
	}
	if auditHealth != nil {
		gate.auditCheck = newAuditChecker()
	}
	close(gate.drained)
	gate.ready.Store(true)
	return gate, nil
}

// AddConnection registers a new connection. It does not start anything.
func (g *Gate) AddConnection(connection Connection) error {
	if g == nil {
		return ErrGateClosed
	}
	if !g.initialized() {
		return ErrInvalidConfig
	}
	if err := connection.Validate(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrGateClosed
	}
	if _, exists := g.connections[connection.id]; exists {
		return ErrConnectionExists
	}
	g.connections[connection.id] = &connectionState{
		spec: connection, enabled: connection.enabled, permits: make(map[uint64]*Permit),
	}
	g.signalLocked()
	return nil
}

// ReplaceConnection atomically swaps a registered connection's profile,
// revision and enabled state. Existing permits are canceled and remain
// counted until their owners call Release.
func (g *Gate) ReplaceConnection(connection Connection) error {
	if g == nil {
		return ErrGateClosed
	}
	if !g.initialized() {
		return ErrInvalidConfig
	}
	if err := connection.Validate(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrGateClosed
	}
	state, ok := g.connections[connection.id]
	if !ok {
		return ErrConnectionMissing
	}
	cancelPermitsLocked(state)
	state.spec = connection
	state.enabled = connection.enabled
	state.revoked = false
	g.signalLocked()
	return nil
}

// RemoveConnection revokes and removes a connection from future lookup.
// Existing permits retain their private owner state and must still be
// released by their callers; they are canceled before this method returns.
func (g *Gate) RemoveConnection(id string) error {
	if g == nil {
		return ErrGateClosed
	}
	if !g.initialized() {
		return ErrInvalidConfig
	}
	if !validIdentifier(id) {
		return ErrConnectionMissing
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrGateClosed
	}
	state, ok := g.connections[id]
	if !ok {
		return ErrConnectionMissing
	}
	cancelPermitsLocked(state)
	state.enabled = false
	state.revoked = true
	delete(g.connections, id)
	g.signalLocked()
	return nil
}

// DisableConnection rejects new admissions and cancels all existing permit
// contexts. It is idempotent for an already disabled connection.
func (g *Gate) DisableConnection(id string) error {
	return g.setConnectionEnabled(id, false)
}

// EnableConnection allows admissions again unless the connection was
// explicitly revoked. Replacing a revoked connection with a new revision is
// the only way to clear revocation.
func (g *Gate) EnableConnection(id string) error {
	return g.setConnectionEnabled(id, true)
}

func (g *Gate) setConnectionEnabled(id string, enabled bool) error {
	if g == nil {
		return ErrGateClosed
	}
	if !g.initialized() {
		return ErrInvalidConfig
	}
	if !validIdentifier(id) {
		return ErrConnectionMissing
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrGateClosed
	}
	state, ok := g.connections[id]
	if !ok {
		return ErrConnectionMissing
	}
	if enabled && state.revoked {
		return ErrConnectionRevoked
	}
	if state.enabled == enabled {
		return nil
	}
	state.enabled = enabled
	if !enabled {
		cancelPermitsLocked(state)
	}
	g.signalLocked()
	return nil
}

// RevokeConnection permanently rejects new admissions for the registered
// record and cancels all current permit contexts. ReplaceConnection with a
// fresh trusted record clears the revoked state.
func (g *Gate) RevokeConnection(id string) error {
	if g == nil {
		return ErrGateClosed
	}
	if !g.initialized() {
		return ErrInvalidConfig
	}
	if !validIdentifier(id) {
		return ErrConnectionMissing
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return ErrGateClosed
	}
	state, ok := g.connections[id]
	if !ok {
		return ErrConnectionMissing
	}
	state.enabled = false
	state.revoked = true
	cancelPermitsLocked(state)
	g.signalLocked()
	return nil
}

// SetAuditAvailable sets a trusted local override. False blocks every new
// admission immediately. True only clears a prior local block; it never
// overrides a nil or unhealthy provider, so recovery remains fail closed until
// the provider itself reports healthy. Existing permits are not canceled by
// audit degradation; only new admissions are blocked.
func (g *Gate) SetAuditAvailable(available bool) {
	if g == nil || !g.initialized() {
		return
	}
	g.mu.Lock()
	g.auditBlocked = !available
	g.auditEpoch++
	g.signalLocked()
	g.mu.Unlock()
}

// ClearAuditAvailabilityOverride returns health decisions to the provider.
func (g *Gate) ClearAuditAvailabilityOverride() {
	if g == nil || !g.initialized() {
		return
	}
	g.mu.Lock()
	g.auditBlocked = false
	g.auditEpoch++
	g.signalLocked()
	g.mu.Unlock()
}

// Acquire waits for both the global and connection limits, honoring ctx. It
// reserves both dimensions atomically, so a waiter never holds one permit
// while waiting indefinitely for the other. Context cancellation after a
// permit is returned cancels Permit.Context; callers must still Release.
func (g *Gate) Acquire(ctx context.Context, binding Binding) (*Permit, error) {
	if ctx == nil {
		return nil, ErrInvalidBinding
	}
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	for {
		if ctx.Err() != nil {
			return nil, ErrCancelled
		}
		if g == nil {
			return nil, ErrGateClosed
		}
		if !g.initialized() {
			return nil, ErrInvalidConfig
		}
		g.mu.Lock()
		err := g.checkAdmissionLocked(binding)
		if err != nil {
			g.mu.Unlock()
			return nil, err
		}
		state := g.connections[binding.connectionID]
		if g.global >= g.limits.MaxGlobal {
			wait := g.notify
			g.mu.Unlock()
			if err := waitForSignal(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}
		if state.active >= g.limits.MaxPerConnection {
			wait := g.notify
			g.mu.Unlock()
			if err := waitForSignal(ctx, wait); err != nil {
				return nil, err
			}
			continue
		}
		audit := g.auditSnapshotLocked()
		g.mu.Unlock()

		healthy, err := g.checkAudit(ctx, audit)
		if err != nil {
			return nil, err
		}
		if !healthy {
			return nil, ErrAuditUnavailable
		}

		// The health provider ran without g.mu. Recheck all mutable state and
		// the audit epoch before reserving capacity, so a concurrent revoke,
		// replacement, close or health override cannot be bypassed.
		g.mu.Lock()
		err = g.checkAdmissionLocked(binding)
		if err == nil {
			state = g.connections[binding.connectionID]
			if g.global >= g.limits.MaxGlobal || state.active >= g.limits.MaxPerConnection {
				err = nil
				g.mu.Unlock()
				continue
			}
			if g.auditEpoch != audit.epoch {
				g.mu.Unlock()
				continue
			}
			if ctx.Err() != nil {
				g.mu.Unlock()
				return nil, ErrCancelled
			}
			permit := g.grantLocked(ctx, binding, state)
			g.mu.Unlock()
			return permit, nil
		}
		g.mu.Unlock()
		return nil, err
	}
}

// TryAcquire performs the same checks as Acquire but never waits for
// capacity. It is useful for bounded workers that already own a scheduler
// slot.
func (g *Gate) TryAcquire(binding Binding) (*Permit, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if g == nil {
		return nil, ErrGateClosed
	}
	if !g.initialized() {
		return nil, ErrInvalidConfig
	}
	g.mu.Lock()
	if err := g.checkAdmissionLocked(binding); err != nil {
		g.mu.Unlock()
		return nil, err
	}
	state := g.connections[binding.connectionID]
	if g.global >= g.limits.MaxGlobal {
		g.mu.Unlock()
		return nil, ErrGlobalCapacity
	}
	if state.active >= g.limits.MaxPerConnection {
		g.mu.Unlock()
		return nil, ErrConnectionCapacity
	}
	audit := g.auditSnapshotLocked()
	g.mu.Unlock()
	healthy, err := g.checkAudit(context.Background(), audit)
	if err != nil {
		return nil, err
	}
	if !healthy {
		return nil, ErrAuditUnavailable
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.checkAdmissionLocked(binding); err != nil {
		return nil, err
	}
	state = g.connections[binding.connectionID]
	if g.global >= g.limits.MaxGlobal {
		return nil, ErrGlobalCapacity
	}
	if state.active >= g.limits.MaxPerConnection {
		return nil, ErrConnectionCapacity
	}
	if g.auditEpoch != audit.epoch {
		return nil, ErrAuditUnavailable
	}
	return g.grantLocked(context.Background(), binding, state), nil
}

func (g *Gate) checkAdmissionLocked(binding Binding) error {
	if g.closed {
		return ErrGateClosed
	}
	state, ok := g.connections[binding.connectionID]
	if !ok {
		return ErrConnectionMissing
	}
	if state.revoked {
		return ErrConnectionRevoked
	}
	if !state.enabled {
		return ErrConnectionDisabled
	}
	if state.spec.profileID != binding.profileID || state.spec.revision != binding.revision {
		return ErrBindingMismatch
	}
	return nil
}

func (g *Gate) auditSnapshotLocked() auditSnapshot {
	return auditSnapshot{provider: g.audit, checker: g.auditCheck, blocked: g.auditBlocked, epoch: g.auditEpoch}
}

func (g *Gate) checkAudit(ctx context.Context, snapshot auditSnapshot) (bool, error) {
	if snapshot.blocked || snapshot.provider == nil {
		return false, nil
	}
	if snapshot.checker == nil {
		return false, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, defaultAuditTimeout)
	healthy, err := snapshot.checker.check(checkCtx, snapshot.provider)
	cancel()
	if err == ErrCancelled && ctx.Err() == nil {
		// The internal health deadline expired while the caller remained live.
		// Treat an unresponsive or reentrant provider as unavailable.
		return false, nil
	}
	return healthy, err
}

func (g *Gate) grantLocked(parent context.Context, binding Binding, state *connectionState) *Permit {
	g.nextID++
	if g.nextID == 0 {
		g.nextID++
	}
	permitContext, cancel := context.WithCancel(parent)
	permit := &Permit{
		gate: g, owner: state, id: g.nextID, binding: binding,
		ctx: permitContext, cancel: cancel, lifecycle: &permitLifecycle{},
	}
	if g.global == 0 {
		g.drained = make(chan struct{})
	}
	g.global++
	state.active++
	state.permits[permit.id] = permit
	return permit
}

func waitForSignal(ctx context.Context, signal <-chan struct{}) error {
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return ErrCancelled
	}
}

func (g *Gate) signalLocked() {
	close(g.notify)
	g.notify = make(chan struct{})
}

func cancelPermitsLocked(state *connectionState) {
	for _, permit := range state.permits {
		permit.cancel()
	}
}

// Release returns both capacity dimensions. It is safe and idempotent to call
// from multiple cleanup paths. Releasing after RemoveConnection or Close is
// still required and still decrements the original owner state correctly.
func (p *Permit) Release() {
	if p == nil {
		return
	}
	if p.lifecycle == nil {
		return
	}
	p.lifecycle.once.Do(func() {
		g := p.gate
		if g == nil || p.owner == nil || p.cancel == nil {
			return
		}
		p.cancel()
		g.mu.Lock()
		if _, exists := p.owner.permits[p.id]; exists {
			delete(p.owner.permits, p.id)
			if p.owner.active > 0 {
				p.owner.active--
			}
			if g.global > 0 {
				g.global--
			}
			if g.global == 0 {
				close(g.drained)
			}
			g.signalLocked()
		}
		g.mu.Unlock()
		// Publish Released only after the owner map and both counters have
		// been updated. A caller observing true can therefore rely on the
		// capacity having actually returned to the gate.
		p.lifecycle.released.Store(true)
	})
}

// Cancel cancels the operation context while leaving the permit counted until
// Release. This separation lets an operation stop first and clean up second.
func (p *Permit) Cancel() {
	if p != nil && p.cancel != nil {
		p.cancel()
	}
}

func (p *Permit) Binding() Binding {
	if p == nil {
		return Binding{}
	}
	return p.binding
}

// Context returns a child context canceled by request cancellation, explicit
// Cancel, connection disable/revoke/replace, gate Close, or Release.
func (p *Permit) Context() context.Context {
	if p == nil || p.ctx == nil {
		return canceledContext
	}
	return p.ctx
}

func (p *Permit) Done() <-chan struct{} {
	if p == nil || p.ctx == nil {
		return canceledContext.Done()
	}
	return p.ctx.Done()
}

func (p *Permit) Released() bool {
	return p != nil && p.lifecycle != nil && p.lifecycle.released.Load()
}

func (p *Permit) MarshalJSON() ([]byte, error) {
	return nil, ErrLocalOnly
}

func (*Permit) UnmarshalJSON([]byte) error {
	return ErrLocalOnly
}

// Wait blocks until all currently held permits have been released. It is
// useful during local shutdown after Close has canceled active operations.
func (g *Gate) Wait(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidBinding
	}
	if g == nil {
		return nil
	}
	for {
		g.mu.Lock()
		if g.global == 0 {
			g.mu.Unlock()
			return nil
		}
		wait := g.drained
		g.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return ErrCancelled
		}
	}
}

// Close prevents new admissions and cancels all current permit contexts. It
// does not wait for user cleanup; call Wait after releasing or stopping work.
func (g *Gate) Close() error {
	if g == nil {
		return nil
	}
	if !g.initialized() {
		return ErrInvalidConfig
	}
	g.mu.Lock()
	var checker *auditChecker
	if !g.closed {
		g.closed = true
		g.auditEpoch++
		checker = g.auditCheck
		for _, state := range g.connections {
			cancelPermitsLocked(state)
		}
		g.signalLocked()
	}
	g.mu.Unlock()
	if checker != nil {
		checker.close()
	}
	return nil
}

// Snapshot returns a point-in-time connection status without exposing active
// permit identities or implementation errors.
func (g *Gate) Snapshot(id string) (Snapshot, error) {
	if g == nil {
		return Snapshot{}, ErrGateClosed
	}
	if !g.initialized() {
		return Snapshot{}, ErrInvalidConfig
	}
	if !validIdentifier(id) {
		return Snapshot{}, ErrConnectionMissing
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	state, ok := g.connections[id]
	if !ok {
		return Snapshot{}, ErrConnectionMissing
	}
	return Snapshot{
		ID: state.spec.id, ProfileID: state.spec.profileID, Revision: state.spec.revision,
		Enabled: state.enabled, Revoked: state.revoked, Active: state.active,
		Limit: g.limits.MaxPerConnection,
	}, nil
}

// Snapshots returns a stable-ID ordered view of all registered connections.
// The values contain no credentials, paths or endpoint information.
func (g *Gate) Snapshots() []Snapshot {
	if g == nil || !g.initialized() {
		return nil
	}
	g.mu.Lock()
	result := make([]Snapshot, 0, len(g.connections))
	for _, state := range g.connections {
		result = append(result, Snapshot{
			ID: state.spec.id, ProfileID: state.spec.profileID, Revision: state.spec.revision,
			Enabled: state.enabled, Revoked: state.revoked, Active: state.active,
			Limit: g.limits.MaxPerConnection,
		})
	}
	g.mu.Unlock()
	// Stable ordering is useful for diagnostics and requires no fairness claim
	// about admission itself.
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j].ID < result[j-1].ID; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

func (g *Gate) initialized() bool {
	return g != nil && g.ready.Load()
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > maxIdentifierBytes {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

var canceledContext = func() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}()

// Keep the explicit JSON methods from being lost during future refactors.
var _ json.Marshaler = Binding{}
var _ json.Unmarshaler = (*Binding)(nil)
var _ json.Marshaler = (*Permit)(nil)
var _ json.Unmarshaler = (*Permit)(nil)
var _ error = (*Error)(nil)
