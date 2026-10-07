// Package networkguard defines the local, fail-closed contract for a
// process-scoped network deny implementation.
//
// This package deliberately contains no operating-system integration.  A
// platform backend is responsible for proving the coverage described by
// Coverage.  Until that proof is complete, Activate never returns a
// Capability.  The fake backend in this package is for contract tests only;
// it does not touch WFP, Windows Firewall, services, or any other system
// state.
package networkguard

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Code is the stable, non-sensitive error category returned by this package.
// Error text is exactly the code; it never contains an admission selector,
// path, command line, process id, or backend diagnostic.
type Code string

const (
	CodeInvalidRequest        Code = "invalid_request"
	CodeAdmissionExpired      Code = "network_deadline_exceeded"
	CodeAdmissionBlocked      Code = "network_enforcement_required"
	CodeNetworkPolicyConflict Code = "network_policy_conflict"
	CodeNetworkFilterInstall  Code = "network_filter_install_failed"
	CodeProcessLaunchFailed   Code = "network_process_launch_failed"
	CodeNetworkScopeUnknown   Code = "network_scope_unknown"
	CodeNetworkEnforcement    Code = "network_enforcement_required"
	CodeNetworkCleanupFailed  Code = "network_cleanup_failed"
	CodeOperationState        Code = "network_invalid_state"
	CodeOperationReplayed     Code = "network_replayed"
	CodeCancelled             Code = "cancelled"
	CodeDeadlineExceeded      Code = "deadline_exceeded"
	CodeUnavailable           Code = "unavailable"
)

// Fault is an error with a stable code and no sensitive detail.
type Fault struct{ code Code }

func (e *Fault) Error() string {
	if e == nil {
		return string(CodeUnavailable)
	}
	return string(e.code)
}

// Code returns the stable category of e.  Unknown errors map to unavailable.
func CodeOf(err error) Code {
	var fault *Fault
	if errors.As(err, &fault) && fault != nil && fault.code != "" {
		return fault.code
	}
	return CodeUnavailable
}

func (e *Fault) Is(target error) bool {
	other, ok := target.(*Fault)
	return ok && other != nil && e != nil && e.code == other.code
}

func fault(code Code) error { return &Fault{code: code} }

var (
	ErrInvalidRequest        = &Fault{code: CodeInvalidRequest}
	ErrAdmissionExpired      = &Fault{code: CodeAdmissionExpired}
	ErrAdmissionBlocked      = &Fault{code: CodeAdmissionBlocked}
	ErrNetworkPolicyConflict = &Fault{code: CodeNetworkPolicyConflict}
	ErrNetworkFilterInstall  = &Fault{code: CodeNetworkFilterInstall}
	ErrProcessLaunchFailed   = &Fault{code: CodeProcessLaunchFailed}
	ErrNetworkScopeUnknown   = &Fault{code: CodeNetworkScopeUnknown}
	ErrNetworkEnforcement    = &Fault{code: CodeNetworkEnforcement}
	ErrNetworkCleanupFailed  = &Fault{code: CodeNetworkCleanupFailed}
	ErrOperationState        = &Fault{code: CodeOperationState}
	ErrOperationReplayed     = &Fault{code: CodeOperationReplayed}
	ErrCancelled             = &Fault{code: CodeCancelled}
	ErrDeadlineExceeded      = &Fault{code: CodeDeadlineExceeded}
	ErrUnavailable           = &Fault{code: CodeUnavailable}
)

var errOpaque = errors.New("opaque value cannot be serialized")

// MaxAdmissionWindow bounds the lifetime of one local network-guard
// operation.  A caller cannot create an effectively unbounded lease.
const MaxAdmissionWindow = 30 * time.Second

const defaultCleanupTimeout = 5 * time.Second

// Admission is a trusted local selector.  It intentionally has no path,
// argv, environment, cwd, PID, wire capability, or arbitrary network rule.
// NonceDigest must be a fixed, non-zero SHA-256-sized digest produced by the
// trusted caller; the raw nonce never enters this package.
type Admission struct {
	ConnectionID    string
	ProfileID       string
	ProfileRevision string
	CommandID       string
	VariantID       string
	IdentityDigest  string
	NonceDigest     [32]byte
	Deadline        time.Time
}

// MarshalJSON prevents an admission from accidentally becoming a wire
// authorization object.  It is local state and must be constructed by
// trusted code only.
func (Admission) MarshalJSON() ([]byte, error) { return nil, errOpaque }

func (*Admission) UnmarshalJSON([]byte) error { return errOpaque }

func (a Admission) validate(now time.Time) error {
	if !validSelector(a.ConnectionID) || !validSelector(a.ProfileID) ||
		!validSelector(a.ProfileRevision) || !validSelector(a.CommandID) ||
		!validSelector(a.VariantID) || !validIdentityDigest(a.IdentityDigest) {
		return ErrInvalidRequest
	}
	var zero [32]byte
	if subtle.ConstantTimeCompare(a.NonceDigest[:], zero[:]) == 1 {
		return ErrInvalidRequest
	}
	if a.Deadline.IsZero() {
		return ErrInvalidRequest
	}
	if !a.Deadline.After(now) {
		return ErrAdmissionExpired
	}
	if a.Deadline.Sub(now) > MaxAdmissionWindow {
		return ErrInvalidRequest
	}
	return nil
}

func validSelector(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func validIdentityDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// CoverageState is the only information a backend may use to prove that the
// deny policy covers a category.  Unknown and incomplete are deliberately
// distinct from verified; both fail closed.
type CoverageState string

const (
	CoverageUnknown        CoverageState = "unknown"
	CoverageIncomplete     CoverageState = "incomplete"
	CoverageVerified       CoverageState = "verified"
	CoverageNoneTerminated CoverageState = "none_or_terminated"
)

// Coverage enumerates the minimum coverage proof for a process-scoped deny.
// The fields contain no addresses, ports, paths, PIDs, or diagnostics.
type Coverage struct {
	IPv4Outbound     CoverageState
	IPv6Outbound     CoverageState
	IPv4Inbound      CoverageState
	IPv6Inbound      CoverageState
	BindAndListen    CoverageState
	IPv4BindListen   CoverageState
	IPv6BindListen   CoverageState
	Loopback         CoverageState
	Children         CoverageState
	InheritedHandles CoverageState
	ExistingFlows    CoverageState
	DNSProxy         CoverageState
	CleanupReady     CoverageState
}

// Complete reports whether all required coverage is proven.  Existing flows
// may be proven absent or terminated, but every other category must be
// explicitly verified.
func (c Coverage) Complete() bool {
	return c.IPv4Outbound == CoverageVerified &&
		c.IPv6Outbound == CoverageVerified &&
		c.IPv4Inbound == CoverageVerified &&
		c.IPv6Inbound == CoverageVerified &&
		c.BindAndListen == CoverageVerified &&
		c.IPv4BindListen == CoverageVerified &&
		c.IPv6BindListen == CoverageVerified &&
		c.Loopback == CoverageVerified &&
		c.Children == CoverageVerified &&
		c.InheritedHandles == CoverageVerified &&
		(c.ExistingFlows == CoverageVerified || c.ExistingFlows == CoverageNoneTerminated) &&
		c.DNSProxy == CoverageVerified &&
		c.CleanupReady == CoverageVerified
}

// VerifiedCoverage returns a complete coverage value useful to a fake
// backend test.  It is not an OS attestation.
func VerifiedCoverage() Coverage {
	return Coverage{
		IPv4Outbound:     CoverageVerified,
		IPv6Outbound:     CoverageVerified,
		IPv4Inbound:      CoverageVerified,
		IPv6Inbound:      CoverageVerified,
		BindAndListen:    CoverageVerified,
		IPv4BindListen:   CoverageVerified,
		IPv6BindListen:   CoverageVerified,
		Loopback:         CoverageVerified,
		Children:         CoverageVerified,
		InheritedHandles: CoverageVerified,
		ExistingFlows:    CoverageNoneTerminated,
		DNSProxy:         CoverageVerified,
		CleanupReady:     CoverageVerified,
	}
}

type operationState uint8

const (
	statePrepared operationState = iota + 1
	stateLaunched
	stateActivated
	stateCleanupPending
	stateRevoked
)

type operation struct {
	guard           *Guard
	id              uint64
	admission       Admission
	mu              sync.Mutex
	state           atomic.Uint32
	run             *runToken        // immutable after construction
	cap             *capabilityToken // immutable after construction
	launched        atomic.Bool
	cleanupPending  bool
	cleanupInFlight chan error
}

func (op *operation) stateValue() operationState {
	if op == nil {
		return 0
	}
	return operationState(op.state.Load())
}

func (op *operation) setState(state operationState) { op.state.Store(uint32(state)) }

// Lease is an opaque handle returned after Prepare.  Its zero value and all
// JSON values are invalid.
type Lease struct{ op *operation }

func (Lease) MarshalJSON() ([]byte, error) { return nil, errOpaque }
func (*Lease) UnmarshalJSON([]byte) error  { return errOpaque }
func (l Lease) Valid() bool                { return l.op != nil && l.op.stateValue() != stateRevoked }

// RunHandle is an opaque handle for the backend-owned suspended process.  A
// caller cannot provide a numeric PID or another process identifier here.
type RunHandle struct{ op *operation }

func (RunHandle) MarshalJSON() ([]byte, error) { return nil, errOpaque }
func (*RunHandle) UnmarshalJSON([]byte) error  { return errOpaque }
func (r RunHandle) Valid() bool {
	return r.op != nil && r.op.run != nil && r.op.run.owner == r.op &&
		r.op.launched.Load() && r.op.stateValue() != stateRevoked
}

type runToken struct{ owner *operation }

// Capability is returned only after complete network coverage has been
// attested.  It is invalidated by Revoke and cannot cross a JSON/MCP boundary.
type Capability struct{ op *operation }

func (Capability) MarshalJSON() ([]byte, error) { return nil, errOpaque }
func (*Capability) UnmarshalJSON([]byte) error  { return errOpaque }
func (c Capability) Valid() bool {
	return c.op != nil && c.op.cap != nil && c.op.cap.owner == c.op && c.op.stateValue() == stateActivated
}

type capabilityToken struct{ owner *operation }

// CleanupReceipt is an opaque local result.  It is intentionally not a
// serializable success marker that a remote caller could replay.
type CleanupReceipt struct{ op *operation }

func (CleanupReceipt) MarshalJSON() ([]byte, error) { return nil, errOpaque }
func (*CleanupReceipt) UnmarshalJSON([]byte) error  { return errOpaque }
func (r CleanupReceipt) Valid() bool {
	return r.op != nil && r.op.stateValue() == stateRevoked
}

// Backend is the platform boundary.  Implementations must keep all process
// and operating-system handles private.  The contract package never receives
// paths, argv, environment, PIDs, wire capabilities, or network rule data.
// Prepare, LaunchSuspended and Activate must be reversible through Revoke if
// they return an error after partially changing state.
type Backend interface {
	Prepare(context.Context, Lease, Admission) error
	LaunchSuspended(context.Context, Lease) error
	Activate(context.Context, Lease, RunHandle) (Coverage, error)
	Revoke(context.Context, Lease, RunHandle) error
}

// Guard serializes the lifecycle and permanently rejects new admissions after
// a cleanup failure until the failed operation is successfully revoked.
type Guard struct {
	mu             sync.Mutex
	backend        Backend
	clock          func() time.Time
	cleanupTimeout time.Duration
	blocked        bool
	pending        int
	nextID         atomic.Uint64
}

// Option customizes a Guard for deterministic tests.  No option changes the
// fail-closed lifecycle or introduces system integration.
type Option func(*Guard)

// WithClock supplies a clock for deadline tests.
func WithClock(clock func() time.Time) Option {
	return func(g *Guard) {
		if clock != nil {
			g.clock = clock
		}
	}
}

// New constructs a guard.  A nil backend is rejected rather than silently
// selecting an unrestricted implementation.
func New(backend Backend, options ...Option) (*Guard, error) {
	if backend == nil {
		return nil, ErrInvalidRequest
	}
	g := &Guard{backend: backend, clock: time.Now, cleanupTimeout: defaultCleanupTimeout}
	for _, option := range options {
		if option != nil {
			option(g)
		}
	}
	if g.clock == nil {
		g.clock = time.Now
	}
	if g.cleanupTimeout <= 0 {
		g.cleanupTimeout = defaultCleanupTimeout
	}
	return g, nil
}

// Blocked reports whether a failed cleanup currently prevents new admissions.
func (g *Guard) Blocked() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.blocked
}

// Prepare creates a lease after validating the safe local selectors and
// deadline.  It is also available as Begin for the design document API.
func (g *Guard) Prepare(ctx context.Context, admission Admission) (Lease, error) {
	ctx = nonNilContext(ctx)
	if g == nil || g.backend == nil {
		return Lease{}, ErrUnavailable
	}
	if err := admission.validate(g.clock()); err != nil {
		return Lease{}, err
	}
	g.mu.Lock()
	blocked := g.blocked
	g.mu.Unlock()
	if blocked {
		return Lease{}, ErrAdmissionBlocked
	}
	if err := contextFailure(ctx); err != nil {
		return Lease{}, err
	}
	op := &operation{guard: g, id: g.nextID.Add(1), admission: admission}
	op.run = &runToken{owner: op}
	op.cap = &capabilityToken{owner: op}
	op.setState(statePrepared)
	lease := Lease{op: op}
	op.mu.Lock()
	defer op.mu.Unlock()
	stageCtx, cancel := stageContext(ctx, admission.Deadline)
	backendErr := g.backend.Prepare(stageCtx, lease, admission)
	stageCause := contextFailure(stageCtx)
	cancel()
	if backendErr != nil {
		if stageCause != nil {
			backendErr = stageCause
		}
		abortErr := g.abortLocked(op, CodeNetworkFilterInstall, backendErr)
		if op.stateValue() == stateCleanupPending {
			return lease, abortErr
		}
		return Lease{}, abortErr
	}
	cause := contextFailure(ctx)
	if cause == nil {
		cause = stageCause
	}
	if cause == nil && !admission.Deadline.After(g.clock()) {
		cause = ErrAdmissionExpired
	}
	if cause != nil {
		abortErr := g.abortLocked(op, CodeAdmissionExpired, cause)
		if op.stateValue() == stateCleanupPending {
			return lease, abortErr
		}
		return Lease{}, abortErr
	}
	// A cleanup failure may have blocked the guard while this backend Prepare
	// was in flight. Recheck at the admission linearization point; an operation
	// that loses this race must clean up instead of becoming a new lease.
	if g.Blocked() {
		abortErr := g.abortLocked(op, CodeNetworkCleanupFailed, ErrAdmissionBlocked)
		if op.stateValue() == stateCleanupPending {
			return lease, abortErr
		}
		return Lease{}, ErrAdmissionBlocked
	}
	return lease, nil
}

// Begin is the contract name used by the network design.
func (g *Guard) Begin(ctx context.Context, admission Admission) (Lease, error) {
	return g.Prepare(ctx, admission)
}

// LaunchSuspended asks the backend to create its process in a suspended,
// backend-owned state.  No PID or process handle crosses this API.
func (g *Guard) LaunchSuspended(ctx context.Context, lease Lease) (RunHandle, error) {
	ctx = nonNilContext(ctx)
	if g == nil {
		return RunHandle{}, ErrUnavailable
	}
	op, err := g.operationLocked(lease)
	if err != nil {
		return RunHandle{}, err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.stateValue() == stateRevoked {
		return RunHandle{}, ErrOperationReplayed
	}
	if op.stateValue() != statePrepared {
		return RunHandle{}, stateError(op.stateValue(), statePrepared)
	}
	if err := g.expiredOrCancelledLocked(op, ctx); err != nil {
		abortErr := g.abortLocked(op, CodeAdmissionExpired, err)
		if op.stateValue() == stateCleanupPending && op.launched.Load() {
			return RunHandle{op: op}, abortErr
		}
		return RunHandle{}, abortErr
	}
	stageCtx, cancel := stageContext(ctx, op.admission.Deadline)
	backendErr := g.backend.LaunchSuspended(stageCtx, lease)
	stageCause := contextFailure(stageCtx)
	cancel()
	if backendErr != nil {
		if stageCause != nil {
			backendErr = stageCause
		}
		abortErr := g.abortLocked(op, CodeProcessLaunchFailed, backendErr)
		if op.stateValue() == stateCleanupPending && op.launched.Load() {
			return RunHandle{op: op}, abortErr
		}
		return RunHandle{}, abortErr
	}
	op.launched.Store(true)
	cause := contextFailure(ctx)
	if cause == nil {
		cause = stageCause
	}
	if cause == nil && !op.admission.Deadline.After(g.clock()) {
		cause = ErrAdmissionExpired
	}
	if cause != nil {
		abortErr := g.abortLocked(op, CodeAdmissionExpired, cause)
		if op.stateValue() == stateCleanupPending && op.launched.Load() {
			return RunHandle{op: op}, abortErr
		}
		return RunHandle{}, abortErr
	}
	op.setState(stateLaunched)
	return RunHandle{op: op}, nil
}

// Attach is a compatibility alias for the contract's suspended-process
// attachment stage.  The backend owns the suspended process; callers cannot
// inject an external handle.
func (g *Guard) Attach(ctx context.Context, lease Lease) (RunHandle, error) {
	return g.LaunchSuspended(ctx, lease)
}

// Activate accepts a proof from the backend.  Any unknown, incomplete, or
// conflicting coverage causes cleanup and returns without a Capability.
func (g *Guard) Activate(ctx context.Context, lease Lease, run RunHandle) (Capability, error) {
	ctx = nonNilContext(ctx)
	if g == nil {
		return Capability{}, ErrUnavailable
	}
	op, err := g.operationLocked(lease)
	if err != nil {
		return Capability{}, err
	}
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.stateValue() == stateRevoked {
		return Capability{}, ErrOperationReplayed
	}
	if op.stateValue() != stateLaunched {
		return Capability{}, stateError(op.stateValue(), stateLaunched)
	}
	if run.op != op || op.run == nil || run.op.run != op.run {
		return Capability{}, ErrInvalidRequest
	}
	if err := g.expiredOrCancelledLocked(op, ctx); err != nil {
		return Capability{}, g.abortLocked(op, CodeAdmissionExpired, err)
	}
	stageCtx, cancel := stageContext(ctx, op.admission.Deadline)
	coverage, backendErr := g.backend.Activate(stageCtx, lease, run)
	stageCause := contextFailure(stageCtx)
	cancel()
	if backendErr != nil {
		if stageCause != nil {
			backendErr = stageCause
		}
		return Capability{}, g.abortLocked(op, CodeNetworkScopeUnknown, backendErr)
	}
	cause := contextFailure(ctx)
	if cause == nil {
		cause = stageCause
	}
	if cause == nil && !op.admission.Deadline.After(g.clock()) {
		cause = ErrAdmissionExpired
	}
	if cause != nil {
		return Capability{}, g.abortLocked(op, CodeAdmissionExpired, cause)
	}
	if !coverage.Complete() {
		return Capability{}, g.abortLocked(op, CodeNetworkScopeUnknown, nil)
	}
	op.setState(stateActivated)
	return Capability{op: op}, nil
}

// Revoke terminates the operation and cleans backend resources.  It is safe
// to call repeatedly after success.  If cleanup fails, the guard remains
// blocked and later calls may retry this same operation.
func (g *Guard) Revoke(ctx context.Context, lease Lease, run RunHandle) (CleanupReceipt, error) {
	if g == nil {
		return CleanupReceipt{}, ErrUnavailable
	}
	if lease.op == nil || lease.op.guard != g {
		return CleanupReceipt{}, ErrInvalidRequest
	}
	op := lease.op
	op.mu.Lock()
	defer op.mu.Unlock()
	if op.stateValue() == stateRevoked {
		return CleanupReceipt{op: op}, nil
	}
	if op.launched.Load() && run.op != op {
		return CleanupReceipt{}, ErrInvalidRequest
	}
	if !op.launched.Load() && run.op != nil {
		return CleanupReceipt{}, ErrInvalidRequest
	}
	// Cleanup is fail-safe and must not be skipped merely because the caller
	// cancelled its request.  A backend must still enforce its own bounded
	// cleanup timeout.
	if err := g.cleanupBackendLocked(op, run); err != nil {
		op.setState(stateCleanupPending)
		g.markCleanupPending(op)
		return CleanupReceipt{}, ErrNetworkCleanupFailed
	}
	op.setState(stateRevoked)
	g.clearCleanupPending(op)
	return CleanupReceipt{op: op}, nil
}

func (g *Guard) operationLocked(lease Lease) (*operation, error) {
	if lease.op == nil || lease.op.guard != g {
		return nil, ErrInvalidRequest
	}
	if lease.op.stateValue() == stateRevoked {
		return nil, ErrOperationReplayed
	}
	return lease.op, nil
}

func (g *Guard) expiredOrCancelledLocked(op *operation, ctx context.Context) error {
	if err := contextFailure(ctx); err != nil {
		return err
	}
	if !op.admission.Deadline.After(g.clock()) {
		return ErrAdmissionExpired
	}
	return nil
}

// abortLocked makes every failed stage attempt cleanup.  If cleanup itself
// fails, the cleanup error wins and the guard is blocked; otherwise the
// original stable stage error is returned.  The caller must hold op.mu.
func (g *Guard) abortLocked(op *operation, code Code, cause error) error {
	if op.stateValue() != stateRevoked {
		run := RunHandle{}
		if op.launched.Load() {
			run = RunHandle{op: op}
		}
		if cleanupErr := g.cleanupBackendLocked(op, run); cleanupErr != nil {
			op.setState(stateCleanupPending)
			g.markCleanupPending(op)
			return ErrNetworkCleanupFailed
		}
		op.setState(stateRevoked)
		g.clearCleanupPending(op)
	}
	if errors.Is(cause, context.Canceled) || CodeOf(cause) == CodeCancelled {
		return ErrCancelled
	}
	if errors.Is(cause, context.DeadlineExceeded) || CodeOf(cause) == CodeDeadlineExceeded {
		return ErrDeadlineExceeded
	}
	return fault(code)
}

func (g *Guard) markCleanupPending(op *operation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if op == nil || op.cleanupPending {
		return
	}
	op.cleanupPending = true
	g.pending++
	g.blocked = true
}

func (g *Guard) clearCleanupPending(op *operation) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if op == nil || !op.cleanupPending {
		return
	}
	op.cleanupPending = false
	if g.pending > 0 {
		g.pending--
	}
	if g.pending == 0 {
		g.blocked = false
	}
}

// cleanupBackendLocked gives every cleanup attempt its own fixed upper bound,
// independent of the caller's context.  A backend that ignores context is
// treated as failed; its result may complete later, but the guard stays
// blocked until a subsequent revoke observes successful cleanup.
func (g *Guard) cleanupBackendLocked(op *operation, run RunHandle) error {
	if op.cleanupInFlight == nil {
		done := make(chan error, 1)
		op.cleanupInFlight = done
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), g.cleanupTimeout)
			defer cancel()
			done <- g.backend.Revoke(ctx, Lease{op: op}, run)
		}()
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.cleanupTimeout)
	defer cancel()
	select {
	case err := <-op.cleanupInFlight:
		op.cleanupInFlight = nil
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func stateError(got, want operationState) error {
	if got == stateRevoked || got == stateCleanupPending {
		return ErrOperationReplayed
	}
	_ = want
	return ErrOperationState
}

func contextFailure(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	switch ctx.Err() {
	case context.Canceled:
		return ErrCancelled
	case context.DeadlineExceeded:
		return ErrDeadlineExceeded
	default:
		return nil
	}
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func stageContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(nonNilContext(ctx), deadline)
}

var _ json.Marshaler = Admission{}
var _ json.Marshaler = Lease{}
var _ json.Marshaler = RunHandle{}
var _ json.Marshaler = Capability{}
var _ json.Marshaler = CleanupReceipt{}
