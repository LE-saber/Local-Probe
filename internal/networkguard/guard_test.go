package networkguard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testAdmission(now time.Time) Admission {
	var digest [32]byte
	digest[0] = 7
	return Admission{
		ConnectionID:    "connection-a",
		ProfileID:       "version-probe",
		ProfileRevision: "revision-1",
		CommandID:       "tool-version",
		VariantID:       "short",
		IdentityDigest:  strings.Repeat("a", 64),
		NonceDigest:     digest,
		Deadline:        now.Add(10 * time.Second),
	}
}

func newTestGuard(t *testing.T, backend *FakeBackend) (*Guard, Admission) {
	t.Helper()
	now := time.Now()
	guard, err := New(backend, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return guard, testAdmission(now)
}

func TestCompleteLifecycleAndIdempotentRevoke(t *testing.T) {
	backend := NewFakeBackend()
	guard, admission := newTestGuard(t, backend)

	lease, err := guard.Begin(context.Background(), admission)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if !lease.Valid() {
		t.Fatal("prepared lease is not valid")
	}
	if err := backend.SetCoverage(lease, VerifiedCoverage()); err != nil {
		t.Fatal(err)
	}
	run, err := guard.LaunchSuspended(context.Background(), lease)
	if err != nil {
		t.Fatalf("LaunchSuspended: %v", err)
	}
	if !run.Valid() {
		t.Fatal("suspended run handle is not valid")
	}
	capability, err := guard.Activate(context.Background(), lease, run)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !capability.Valid() {
		t.Fatal("verified capability is not valid")
	}
	receipt, err := guard.Revoke(context.Background(), lease, run)
	if err != nil || !receipt.Valid() {
		t.Fatalf("Revoke: receipt=%v err=%v", receipt.Valid(), err)
	}
	if capability.Valid() || lease.Valid() || run.Valid() {
		t.Fatal("revocation did not invalidate opaque handles")
	}
	receipt, err = guard.Revoke(context.Background(), lease, run)
	if err != nil || !receipt.Valid() {
		t.Fatalf("idempotent Revoke: receipt=%v err=%v", receipt.Valid(), err)
	}
	if got := backend.Calls(FakeRevoke); got != 1 {
		t.Fatalf("idempotent revoke called backend %d times", got)
	}
}

func TestInvalidOrderAndCrossOperationHandlesFailClosed(t *testing.T) {
	backend := NewFakeBackend()
	guard, admission := newTestGuard(t, backend)
	if _, err := guard.Activate(context.Background(), Lease{}, RunHandle{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("zero handles: got %v", err)
	}
	leaseA, err := guard.Prepare(context.Background(), admission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Activate(context.Background(), leaseA, RunHandle{}); !errors.Is(err, ErrOperationState) {
		t.Fatalf("activate before launch: got %v", err)
	}
	runA, err := guard.LaunchSuspended(context.Background(), leaseA)
	if err != nil {
		t.Fatal(err)
	}
	leaseB, err := guard.Prepare(context.Background(), admission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Activate(context.Background(), leaseB, runA); !errors.Is(err, ErrOperationState) {
		t.Fatalf("cross-operation run: got %v", err)
	}
	if _, err := guard.Revoke(context.Background(), leaseA, RunHandle{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing run after launch: got %v", err)
	}
	if _, err := guard.Revoke(context.Background(), leaseB, RunHandle{}); err != nil {
		t.Fatalf("cleanup prepared operation: %v", err)
	}
	if _, err := guard.Revoke(context.Background(), leaseA, runA); err != nil {
		t.Fatalf("cleanup launched operation: %v", err)
	}
}

func TestUnknownCoverageCannotActivateAndIsCleaned(t *testing.T) {
	backend := NewFakeBackend()
	guard, admission := newTestGuard(t, backend)
	lease, err := guard.Prepare(context.Background(), admission)
	if err != nil {
		t.Fatal(err)
	}
	run, err := guard.LaunchSuspended(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Activate(context.Background(), lease, run); !errors.Is(err, ErrNetworkScopeUnknown) {
		t.Fatalf("unknown coverage: got %v", err)
	}
	if backend.Active(lease) || guard.Blocked() {
		t.Fatalf("unknown coverage cleanup state: active=%v blocked=%v", backend.Active(lease), guard.Blocked())
	}
	if got := backend.Calls(FakeRevoke); got != 1 {
		t.Fatalf("unknown coverage cleanup calls=%d", got)
	}
}

func TestIncompleteCoverageEachRequiredFieldFailsClosed(t *testing.T) {
	fields := []struct {
		name string
		set  func(*Coverage)
	}{
		{"ipv4 outbound", func(c *Coverage) { c.IPv4Outbound = CoverageIncomplete }},
		{"ipv6 outbound", func(c *Coverage) { c.IPv6Outbound = CoverageUnknown }},
		{"ipv4 inbound", func(c *Coverage) { c.IPv4Inbound = CoverageIncomplete }},
		{"ipv6 inbound", func(c *Coverage) { c.IPv6Inbound = CoverageUnknown }},
		{"bind and listen", func(c *Coverage) { c.BindAndListen = CoverageIncomplete }},
		{"ipv4 bind and listen", func(c *Coverage) { c.IPv4BindListen = CoverageIncomplete }},
		{"ipv6 bind and listen", func(c *Coverage) { c.IPv6BindListen = CoverageIncomplete }},
		{"loopback", func(c *Coverage) { c.Loopback = CoverageUnknown }},
		{"children", func(c *Coverage) { c.Children = CoverageIncomplete }},
		{"inherited handles", func(c *Coverage) { c.InheritedHandles = CoverageUnknown }},
		{"existing flows", func(c *Coverage) { c.ExistingFlows = CoverageIncomplete }},
		{"dns and proxy", func(c *Coverage) { c.DNSProxy = CoverageUnknown }},
		{"cleanup readiness", func(c *Coverage) { c.CleanupReady = CoverageIncomplete }},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			backend := NewFakeBackend()
			coverage := VerifiedCoverage()
			field.set(&coverage)
			guard, admission := newTestGuard(t, backend)
			lease, err := guard.Prepare(context.Background(), admission)
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.SetCoverage(lease, coverage); err != nil {
				t.Fatal(err)
			}
			run, err := guard.LaunchSuspended(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := guard.Activate(context.Background(), lease, run); !errors.Is(err, ErrNetworkScopeUnknown) {
				t.Fatalf("coverage was accepted: %v", err)
			}
			if backend.Active(lease) {
				t.Fatal("incomplete coverage left backend active")
			}
		})
	}
}

func TestStageFailuresAndCleanupFailureBlockNewAdmissions(t *testing.T) {
	stages := []struct {
		stage FakeStage
		code  Code
	}{
		{FakePrepare, CodeNetworkFilterInstall},
		{FakeLaunchSuspended, CodeProcessLaunchFailed},
		{FakeActivate, CodeNetworkScopeUnknown},
	}
	for _, test := range stages {
		t.Run(string(test.stage), func(t *testing.T) {
			backend := NewFakeBackend()
			backend.FailNext(test.stage)
			guard, admission := newTestGuard(t, backend)
			lease, err := guard.Prepare(context.Background(), admission)
			if test.stage == FakePrepare {
				if err == nil || CodeOf(err) != test.code {
					t.Fatalf("prepare failure: err=%v code=%s", err, CodeOf(err))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.SetCoverage(lease, VerifiedCoverage()); err != nil {
				t.Fatal(err)
			}
			run, err := guard.LaunchSuspended(context.Background(), lease)
			if test.stage == FakeLaunchSuspended {
				if err == nil || CodeOf(err) != test.code {
					t.Fatalf("launch failure: err=%v code=%s", err, CodeOf(err))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := guard.Activate(context.Background(), lease, run); err == nil || CodeOf(err) != test.code {
				t.Fatalf("activate failure: err=%v code=%s", err, CodeOf(err))
			}
		})
	}

	backend := NewFakeBackend()
	backend.FailNext(FakeRevoke)
	guard, admission := newTestGuard(t, backend)
	lease, err := guard.Prepare(context.Background(), admission)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.SetCoverage(lease, VerifiedCoverage()); err != nil {
		t.Fatal(err)
	}
	run, err := guard.LaunchSuspended(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := guard.Activate(context.Background(), lease, run)
	if err != nil || !capability.Valid() {
		t.Fatalf("activation before cleanup failure: capability=%v err=%v", capability.Valid(), err)
	}
	if _, err := guard.Revoke(context.Background(), lease, run); err == nil || CodeOf(err) != CodeNetworkCleanupFailed {
		t.Fatalf("cleanup failure was not surfaced: %v", err)
	}
	if !guard.Blocked() {
		t.Fatal("cleanup failure did not block new admissions")
	}
	if _, err := guard.Prepare(context.Background(), admission); !errors.Is(err, ErrAdmissionBlocked) {
		t.Fatalf("blocked admission: %v", err)
	}
	if _, err := guard.Revoke(context.Background(), lease, run); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if guard.Blocked() {
		t.Fatal("successful retry left guard blocked")
	}
}

func TestExpiredAdmissionAndDeadlineDoNotActivate(t *testing.T) {
	now := time.Now()
	backend := NewFakeBackend()
	guard, err := New(backend, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	admission := testAdmission(now)
	admission.Deadline = now
	if _, err := guard.Prepare(context.Background(), admission); !errors.Is(err, ErrAdmissionExpired) {
		t.Fatalf("expired admission: %v", err)
	}
	admission = testAdmission(now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := guard.Prepare(ctx, admission); !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancelled admission: %v", err)
	}
}

func TestOpaqueValuesRejectJSONAndDoNotLeakSelectors(t *testing.T) {
	backend := NewFakeBackend()
	guard, admission := newTestGuard(t, backend)
	lease, err := guard.Prepare(context.Background(), admission)
	if err != nil {
		t.Fatal(err)
	}
	run, err := guard.LaunchSuspended(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	values := []any{admission, lease, run, Capability{}, CleanupReceipt{}}
	for _, value := range values {
		if _, err := json.Marshal(value); err == nil {
			t.Fatalf("%T unexpectedly marshaled", value)
		}
	}
	var decoded Lease
	if err := json.Unmarshal([]byte(`{}`), &decoded); err == nil {
		t.Fatal("opaque lease unexpectedly unmarshaled")
	}
	bad := admission
	bad.ProfileID = "secret/path"
	if _, err := guard.Prepare(context.Background(), bad); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid selector leaked or was accepted: %v", err)
	}
}

func TestSelectorsDoNotAcceptPathLikeOrOversizedValues(t *testing.T) {
	backend := NewFakeBackend()
	guard, admission := newTestGuard(t, backend)
	for _, selector := range []string{"..", "a/b", `a\\b`, "a:b", "a\x00b", strings.Repeat("x", 129)} {
		bad := admission
		bad.VariantID = selector
		if _, err := guard.Prepare(context.Background(), bad); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("selector %q accepted: %v", selector, err)
		}
	}
}

func TestAdmissionRequiresCommandIdentityAndBoundedDeadline(t *testing.T) {
	backend := NewFakeBackend()
	guard, admission := newTestGuard(t, backend)
	cases := []struct {
		name string
		edit func(*Admission)
	}{
		{"missing command", func(a *Admission) { a.CommandID = "" }},
		{"missing identity", func(a *Admission) { a.IdentityDigest = "" }},
		{"uppercase identity", func(a *Admission) { a.IdentityDigest = strings.Repeat("A", 64) }},
		{"non hexadecimal identity", func(a *Admission) { a.IdentityDigest = strings.Repeat("g", 64) }},
		{"short identity", func(a *Admission) { a.IdentityDigest = strings.Repeat("a", 63) }},
		{"long window", func(a *Admission) { a.Deadline = time.Now().Add(MaxAdmissionWindow + time.Second) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			bad := admission
			test.edit(&bad)
			if _, err := guard.Prepare(context.Background(), bad); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("admission accepted: %v", err)
			}
		})
	}
}

func TestSameAdmissionOperationsKeepBackendStateSeparate(t *testing.T) {
	backend := NewFakeBackend()
	guard, admission := newTestGuard(t, backend)
	leaseA, err := guard.Prepare(context.Background(), admission)
	if err != nil {
		t.Fatal(err)
	}
	leaseB, err := guard.Prepare(context.Background(), admission)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.SetCoverage(leaseA, VerifiedCoverage()); err != nil {
		t.Fatal(err)
	}
	if err := backend.SetCoverage(leaseB, Coverage{}); err != nil {
		t.Fatal(err)
	}
	runA, err := guard.LaunchSuspended(context.Background(), leaseA)
	if err != nil {
		t.Fatal(err)
	}
	runB, err := guard.LaunchSuspended(context.Background(), leaseB)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := guard.Activate(context.Background(), leaseA, runA)
	if err != nil || !capability.Valid() {
		t.Fatalf("operation A activation: capability=%v err=%v", capability.Valid(), err)
	}
	if _, err := guard.Activate(context.Background(), leaseB, runB); !errors.Is(err, ErrNetworkScopeUnknown) {
		t.Fatalf("operation B reused operation A coverage: %v", err)
	}
	if !backend.Active(leaseA) || backend.Active(leaseB) {
		t.Fatalf("per-lease active state mixed: A=%v B=%v", backend.Active(leaseA), backend.Active(leaseB))
	}
	if _, err := guard.Revoke(context.Background(), leaseA, runA); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAdmissionsAndCleanupAreRaceSafe(t *testing.T) {
	backend := NewFakeBackend()
	guard, admission := newTestGuard(t, backend)
	const workers = 32
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := guard.Prepare(context.Background(), admission)
			if err != nil {
				return
			}
			if err := backend.SetCoverage(lease, VerifiedCoverage()); err != nil {
				return
			}
			run, err := guard.LaunchSuspended(context.Background(), lease)
			if err != nil {
				return
			}
			capability, err := guard.Activate(context.Background(), lease, run)
			if err == nil && capability.Valid() {
				successes.Add(1)
			}
			_, _ = guard.Revoke(context.Background(), lease, run)
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != workers {
		t.Fatalf("concurrent lifecycle successes=%d want=%d", got, workers)
	}
	if guard.Blocked() {
		t.Fatalf("concurrent cleanup state blocked=%v", guard.Blocked())
	}
}

func TestCoverageExistingFlowsAcceptsNoneOrTerminatedOnly(t *testing.T) {
	coverage := VerifiedCoverage()
	coverage.ExistingFlows = CoverageUnknown
	if coverage.Complete() {
		t.Fatal("unknown existing flow coverage accepted")
	}
	coverage.ExistingFlows = CoverageNoneTerminated
	if !coverage.Complete() {
		t.Fatal("none_or_terminated existing flow coverage rejected")
	}
}

type prepareBarrierBackend struct {
	*FakeBackend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *prepareBarrierBackend) Prepare(ctx context.Context, lease Lease, admission Admission) error {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return b.FakeBackend.Prepare(ctx, lease, admission)
}

func TestPrepareRechecksCleanupBlockAfterBackendReturns(t *testing.T) {
	fake := NewFakeBackend()
	backend := &prepareBarrierBackend{FakeBackend: fake, entered: make(chan struct{}), release: make(chan struct{})}
	now := time.Now()
	guard, err := New(backend, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		lease Lease
		err   error
	}
	done := make(chan result, 1)
	go func() {
		lease, prepareErr := guard.Prepare(context.Background(), testAdmission(now))
		done <- result{lease: lease, err: prepareErr}
	}()
	<-backend.entered
	guard.mu.Lock()
	guard.blocked = true
	guard.pending = 1
	guard.mu.Unlock()
	close(backend.release)
	got := <-done
	if !errors.Is(got.err, ErrAdmissionBlocked) || got.lease.Valid() {
		t.Fatalf("prepare crossed cleanup block: lease=%v err=%v", got.lease.Valid(), got.err)
	}
}

type blockingRevokeBackend struct {
	*FakeBackend
	release chan struct{}
}

func (b *blockingRevokeBackend) Revoke(context.Context, Lease, RunHandle) error {
	<-b.release
	return nil
}

func TestCleanupHasHardTimeoutAndCanBeRetried(t *testing.T) {
	backend := &blockingRevokeBackend{FakeBackend: NewFakeBackend(), release: make(chan struct{})}
	now := time.Now()
	guard, err := New(backend, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	guard.cleanupTimeout = 20 * time.Millisecond
	lease, err := guard.Prepare(context.Background(), testAdmission(now))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := guard.Revoke(context.Background(), lease, RunHandle{}); !errors.Is(err, ErrNetworkCleanupFailed) {
		t.Fatalf("unbounded cleanup error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cleanup timeout took %v", elapsed)
	}
	if !guard.Blocked() {
		t.Fatal("cleanup timeout did not block admissions")
	}
	close(backend.release)
	if _, err := guard.Revoke(context.Background(), lease, RunHandle{}); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if guard.Blocked() {
		t.Fatal("successful cleanup retry did not unblock admissions")
	}
}
