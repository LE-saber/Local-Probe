package wfp

import (
	"context"
	"sync"

	"github.com/LE-saber/Local-Probe/internal/networkguard"
)

// ErrDisabled is the stable fail-closed result for every operation that would
// require OS network enforcement. It aliases the contract's stable
// network_enforcement_required error; this backend never returns a capability.
var ErrDisabled = networkguard.ErrNetworkEnforcement

type leaseState struct {
	plan Plan
}

// DisabledBackend is the default backend on every platform. It only keeps a
// fixed plan in memory per opaque lease. It never starts a process, changes a
// firewall, opens WFP, starts a service, or calls any operating-system API.
type DisabledBackend struct {
	mu     sync.Mutex
	leases map[networkguard.Lease]leaseState
}

// NewDisabledBackend constructs the default fail-closed backend.
func NewDisabledBackend() *DisabledBackend {
	return &DisabledBackend{leases: make(map[networkguard.Lease]leaseState)}
}

// Prepare builds and stores only the fixed plan for lease. It does not install
// filters or otherwise change system state.
func (b *DisabledBackend) Prepare(ctx context.Context, lease networkguard.Lease, _ networkguard.Admission) error {
	if b == nil {
		return networkguard.ErrUnavailable
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if !lease.Valid() {
		return networkguard.ErrInvalidRequest
	}
	b.mu.Lock()
	if b.leases == nil {
		b.leases = make(map[networkguard.Lease]leaseState)
	}
	b.leases[lease] = leaseState{plan: BuildPlan()}
	b.mu.Unlock()
	return nil
}

// LaunchSuspended is permanently disabled. No process or process handle is
// created, even though the contract names this lifecycle stage.
func (b *DisabledBackend) LaunchSuspended(_ context.Context, lease networkguard.Lease) error {
	if b == nil {
		return networkguard.ErrUnavailable
	}
	if !lease.Valid() {
		return networkguard.ErrInvalidRequest
	}
	return ErrDisabled
}

// Activate always returns zero coverage and a stable fail-closed error. A
// disabled backend can never prove network coverage or produce a capability.
func (b *DisabledBackend) Activate(_ context.Context, lease networkguard.Lease, _ networkguard.RunHandle) (networkguard.Coverage, error) {
	if b == nil {
		return networkguard.Coverage{}, networkguard.ErrUnavailable
	}
	if !lease.Valid() {
		return networkguard.Coverage{}, networkguard.ErrInvalidRequest
	}
	return networkguard.Coverage{}, ErrDisabled
}

// Revoke is idempotent and removes only this lease's in-memory plan. Cleanup
// is local and bounded, so it remains safe to complete even if the caller's
// context has been cancelled.
func (b *DisabledBackend) Revoke(_ context.Context, lease networkguard.Lease, _ networkguard.RunHandle) error {
	if b == nil {
		return networkguard.ErrUnavailable
	}
	if lease == (networkguard.Lease{}) {
		return networkguard.ErrInvalidRequest
	}
	b.mu.Lock()
	delete(b.leases, lease)
	b.mu.Unlock()
	return nil
}

// PlanSummary returns a diagnostic-only fixed summary for one currently
// prepared lease. It is not coverage, attestation, or a capability, and
// cannot be used with Activate. The returned value contains no mutable
// backend state.
func (b *DisabledBackend) PlanSummary(lease networkguard.Lease) (PlanSummary, bool) {
	if b == nil {
		return PlanSummary{}, false
	}
	b.mu.Lock()
	state, ok := b.leases[lease]
	b.mu.Unlock()
	if !ok {
		return PlanSummary{}, false
	}
	return state.plan.Summary(), true
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	switch ctx.Err() {
	case context.Canceled:
		return networkguard.ErrCancelled
	case context.DeadlineExceeded:
		return networkguard.ErrDeadlineExceeded
	default:
		return nil
	}
}

var _ networkguard.Backend = (*DisabledBackend)(nil)
