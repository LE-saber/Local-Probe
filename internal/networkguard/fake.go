package networkguard

import (
	"context"
	"errors"
	"sync"
)

// FakeStage names one lifecycle stage for deterministic contract tests.
type FakeStage string

const (
	FakePrepare         FakeStage = "prepare"
	FakeLaunchSuspended FakeStage = "launch_suspended"
	FakeActivate        FakeStage = "activate"
	FakeRevoke          FakeStage = "revoke"
)

var errFakeStage = errors.New("fake backend stage failure")

type fakeOperation struct {
	prepared bool
	launched bool
	active   bool
	revoked  bool
	coverage Coverage
}

// FakeBackend is a state-only backend for tests.  It never opens a process,
// changes a firewall, starts a service, or touches any operating-system
// state. FailNext injects an opaque failure at any lifecycle stage. State is
// kept per opaque Lease, so equal selectors cannot share operation state.
type FakeBackend struct {
	mu       sync.Mutex
	failures map[FakeStage]int
	calls    map[FakeStage]int
	ops      map[*operation]*fakeOperation
}

// NewFakeBackend constructs a backend with unknown coverage. Tests must
// explicitly provide VerifiedCoverage for each lease before Activate succeeds.
func NewFakeBackend() *FakeBackend {
	return &FakeBackend{
		failures: make(map[FakeStage]int),
		calls:    make(map[FakeStage]int),
		ops:      make(map[*operation]*fakeOperation),
	}
}

// FailNext injects one failure at stage. It is safe to call concurrently with
// lifecycle operations.
func (f *FakeBackend) FailNext(stage FakeStage) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.failures[stage]++
	f.mu.Unlock()
}

// FailCount injects count failures at stage. Non-positive counts clear pending
// failures for that stage.
func (f *FakeBackend) FailCount(stage FakeStage, count int) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if count <= 0 {
		delete(f.failures, stage)
	} else {
		f.failures[stage] = count
	}
	f.mu.Unlock()
}

func (f *FakeBackend) stage(stage FakeStage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[stage]++
	if f.failures[stage] > 0 {
		f.failures[stage]--
		return errFakeStage
	}
	return nil
}

func (f *FakeBackend) lookup(lease Lease) (*fakeOperation, error) {
	if lease.op == nil {
		return nil, ErrInvalidRequest
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	op := f.ops[lease.op]
	if op == nil {
		return nil, ErrInvalidRequest
	}
	return op, nil
}

func (f *FakeBackend) Prepare(ctx context.Context, lease Lease, _ Admission) error {
	if err := contextFailure(ctx); err != nil {
		return err
	}
	if lease.op == nil {
		return ErrInvalidRequest
	}
	f.mu.Lock()
	op := &fakeOperation{}
	f.ops[lease.op] = op
	f.mu.Unlock()
	if err := f.stage(FakePrepare); err != nil {
		return err
	}
	f.mu.Lock()
	op.prepared = true
	f.mu.Unlock()
	return nil
}

func (f *FakeBackend) LaunchSuspended(ctx context.Context, lease Lease) error {
	if err := contextFailure(ctx); err != nil {
		return err
	}
	op, err := f.lookup(lease)
	if err != nil {
		return err
	}
	f.mu.Lock()
	prepared, revoked := op.prepared, op.revoked
	f.mu.Unlock()
	if !prepared || revoked {
		return ErrInvalidRequest
	}
	if err := f.stage(FakeLaunchSuspended); err != nil {
		return err
	}
	f.mu.Lock()
	op.launched = true
	f.mu.Unlock()
	return nil
}

func (f *FakeBackend) Activate(ctx context.Context, lease Lease, _ RunHandle) (Coverage, error) {
	if err := contextFailure(ctx); err != nil {
		return Coverage{}, err
	}
	op, err := f.lookup(lease)
	if err != nil {
		return Coverage{}, err
	}
	f.mu.Lock()
	launched, revoked := op.launched, op.revoked
	f.mu.Unlock()
	if !launched || revoked {
		return Coverage{}, ErrInvalidRequest
	}
	if err := f.stage(FakeActivate); err != nil {
		return Coverage{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	op.active = true
	return op.coverage, nil
}

func (f *FakeBackend) Revoke(ctx context.Context, lease Lease, _ RunHandle) error {
	if err := contextFailure(ctx); err != nil {
		return err
	}
	op, err := f.lookup(lease)
	if err != nil {
		return err
	}
	if err := f.stage(FakeRevoke); err != nil {
		return err
	}
	f.mu.Lock()
	op.active = false
	op.revoked = true
	f.mu.Unlock()
	return nil
}

// SetCoverage assigns coverage to one prepared lease. It is impossible to
// set a package-global proof shared by unrelated operations.
func (f *FakeBackend) SetCoverage(lease Lease, coverage Coverage) error {
	if f == nil {
		return ErrUnavailable
	}
	op, err := f.lookup(lease)
	if err != nil {
		return err
	}
	f.mu.Lock()
	op.coverage = coverage
	f.mu.Unlock()
	return nil
}

// Active reports whether this specific lease still owns fake backend state.
func (f *FakeBackend) Active(lease Lease) bool {
	if f == nil {
		return false
	}
	op, err := f.lookup(lease)
	if err != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return op.active
}

// Prepared reports whether this specific lease was prepared successfully.
func (f *FakeBackend) Prepared(lease Lease) bool {
	if f == nil {
		return false
	}
	op, err := f.lookup(lease)
	if err != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return op.prepared && !op.revoked
}

// Calls returns the number of calls observed at stage across all leases.
func (f *FakeBackend) Calls(stage FakeStage) int {
	if f == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[stage]
}

var _ Backend = (*FakeBackend)(nil)
