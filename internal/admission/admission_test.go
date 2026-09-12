package admission

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testGate(t *testing.T, global, perConnection int) (*Gate, *AuditHealthState, Binding, Binding) {
	t.Helper()
	health := NewAuditHealthState(true)
	limits, err := NewLimits(global, perConnection)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := New(limits, health)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewConnection("connection-a", "profile-a", "revision-a", true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewConnection("connection-b", "profile-b", "revision-b", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(a); err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(b); err != nil {
		t.Fatal(err)
	}
	bindingA, err := NewBinding(a.ID(), a.ProfileID(), a.Revision())
	if err != nil {
		t.Fatal(err)
	}
	bindingB, err := NewBinding(b.ID(), b.ProfileID(), b.Revision())
	if err != nil {
		t.Fatal(err)
	}
	return gate, health, bindingA, bindingB
}

func TestLimitsAndTypedInputs(t *testing.T) {
	if got := DefaultLimits(); got.MaxGlobal != defaultGlobal || got.MaxPerConnection != defaultPerConnection {
		t.Fatalf("unexpected defaults: %#v", got)
	}
	if _, err := NewLimits(0, 1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("zero global limit error = %v", err)
	}
	if _, err := NewLimits(1, maxLimit+1); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("oversized connection limit error = %v", err)
	}
	if _, err := NewBinding("bad/path", "profile", "revision"); !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("path-like binding error = %v", err)
	}
	if _, err := NewConnection("connection", "profile", "revision", true); err != nil {
		t.Fatal(err)
	}

	binding, err := NewBinding("connection", "profile", "revision")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(binding); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("binding JSON error = %v", err)
	}
	var decoded Binding
	if err := json.Unmarshal([]byte(`{"connectionID":"connection"}`), &decoded); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("binding JSON decode error = %v", err)
	}
	var permit Permit
	if _, err := json.Marshal(&permit); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("permit JSON error = %v", err)
	}
	if err := json.Unmarshal([]byte(`{}`), &permit); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("permit JSON decode error = %v", err)
	}
}

func TestGlobalAndPerConnectionBoundsAreAtomic(t *testing.T) {
	gate, _, bindingA, bindingB := testGate(t, 2, 1)
	pA, err := gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatal(err)
	}
	defer pA.Release()
	if _, err := gate.TryAcquire(bindingA); !errors.Is(err, ErrConnectionCapacity) {
		t.Fatalf("same connection capacity error = %v", err)
	}
	pB, err := gate.TryAcquire(bindingB)
	if err != nil {
		t.Fatal(err)
	}
	if got := pB.Binding(); got.ConnectionID() != bindingB.ConnectionID() || got.ProfileID() != bindingB.ProfileID() || got.Revision() != bindingB.Revision() {
		t.Fatalf("permit binding was not preserved: %#v", got)
	}
	permitCopy := *pB
	if _, err := gate.TryAcquire(bindingB); !errors.Is(err, ErrGlobalCapacity) {
		t.Fatalf("global capacity error = %v", err)
	}
	pB.Release()
	permitCopy.Release()
	if snap, err := gate.Snapshot(bindingB.ConnectionID()); err != nil || snap.Active != 0 {
		t.Fatalf("double release left active permit: %#v, %v", snap, err)
	}
}

func TestAcquireCancellationDoesNotReserveCapacity(t *testing.T) {
	gate, _, bindingA, _ := testGate(t, 1, 1)
	p, err := gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := gate.Acquire(ctx, bindingA); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Acquire cancellation error = %v", err)
	}
	if snap, err := gate.Snapshot(bindingA.ConnectionID()); err != nil || snap.Active != 1 {
		t.Fatalf("waiting cancellation changed active count: %#v, %v", snap, err)
	}
	p.Release()
	second, err := gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatalf("capacity was not returned after release: %v", err)
	}
	second.Release()
}

func TestDisableRevokeAndReplaceCancelExistingPermits(t *testing.T) {
	gate, _, bindingA, _ := testGate(t, 2, 1)
	p, err := gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.DisableConnection(bindingA.ConnectionID()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Fatal("disable did not cancel existing permit")
	}
	if _, err := gate.TryAcquire(bindingA); !errors.Is(err, ErrConnectionDisabled) {
		t.Fatalf("disabled connection error = %v", err)
	}
	if err := gate.EnableConnection(bindingA.ConnectionID()); err != nil {
		t.Fatal(err)
	}
	p.Release()

	p, err = gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.RevokeConnection(bindingA.ConnectionID()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Fatal("revoke did not cancel existing permit")
	}
	if _, err := gate.TryAcquire(bindingA); !errors.Is(err, ErrConnectionRevoked) {
		t.Fatalf("revoked connection error = %v", err)
	}
	if err := gate.EnableConnection(bindingA.ConnectionID()); !errors.Is(err, ErrConnectionRevoked) {
		t.Fatalf("revoked enable error = %v", err)
	}
	p.Release()

	replacement, err := NewConnection(bindingA.ConnectionID(), bindingA.ProfileID(), "revision-new", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.ReplaceConnection(replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.TryAcquire(bindingA); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("stale revision error = %v", err)
	}
	newBinding, err := NewBinding(replacement.ID(), replacement.ProfileID(), replacement.Revision())
	if err != nil {
		t.Fatal(err)
	}
	newPermit, err := gate.TryAcquire(newBinding)
	if err != nil {
		t.Fatal(err)
	}
	newPermit.Release()
}

func TestAuditHealthFailsClosedForNewAdmissions(t *testing.T) {
	gate, health, bindingA, _ := testGate(t, 2, 2)
	health.SetHealthy(false)
	if _, err := gate.TryAcquire(bindingA); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("degraded audit error = %v", err)
	}
	health.SetHealthy(true)
	p, err := gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatal(err)
	}
	gate.SetAuditAvailable(false)
	if _, err := gate.TryAcquire(bindingA); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("audit override error = %v", err)
	}
	// A sink failure blocks new work but does not silently release work that a
	// caller already owns; the caller observes its own operation context.
	if p.Released() {
		t.Fatal("audit degradation released an active permit")
	}
	select {
	case <-p.Done():
		t.Fatal("audit degradation canceled an existing permit")
	default:
	}
	p.Release()
	gate.ClearAuditAvailabilityOverride()
	second, err := gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatalf("admission did not recover after audit health: %v", err)
	}
	second.Release()
}

func TestAuditHealthProviderIsCalledOutsideGateLock(t *testing.T) {
	var gate *Gate
	health := AuditHealthFunc(func() bool {
		// Snapshot takes g.mu. This would deadlock if Healthy ran while the
		// admission gate still held its mutex.
		_, _ = gate.Snapshot("connection-a")
		return true
	})
	limits, err := NewLimits(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	gate, err = New(limits, health)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := NewConnection("connection-a", "profile-a", "revision-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding(connection.ID(), connection.ProfileID(), connection.Revision())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		permit, err := gate.Acquire(context.Background(), binding)
		if permit != nil {
			permit.Release()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reentrant audit health check deadlocked")
	}
}

func TestBlockedAuditHealthHonorsAcquireCancellation(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	health := AuditHealthFunc(func() bool {
		select {
		case <-started:
		default:
			close(started)
		}
		<-finish
		return true
	})
	limits, err := NewLimits(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := New(limits, health)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := NewConnection("connection-a", "profile-a", "revision-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding(connection.ID(), connection.ProfileID(), connection.Revision())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		permit, err := gate.Acquire(ctx, binding)
		if permit != nil {
			permit.Release()
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("audit health provider did not start")
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrCancelled) {
			t.Fatalf("blocked provider cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Acquire did not honor cancellation while audit provider blocked")
	}
	close(finish)
	_ = gate.Close()
}

func TestAuditHealthPanicAndNilProviderFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		health AuditHealth
	}{
		{name: "nil", health: nil},
		{name: "panic", health: AuditHealthFunc(func() bool { panic("do not expose") })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits, err := NewLimits(1, 1)
			if err != nil {
				t.Fatal(err)
			}
			gate, err := New(limits, test.health)
			if err != nil {
				t.Fatal(err)
			}
			connection, err := NewConnection("connection-a", "profile-a", "revision-a", true)
			if err != nil {
				t.Fatal(err)
			}
			if err := gate.AddConnection(connection); err != nil {
				t.Fatal(err)
			}
			binding, err := NewBinding(connection.ID(), connection.ProfileID(), connection.Revision())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := gate.TryAcquire(binding); !errors.Is(err, ErrAuditUnavailable) {
				t.Fatalf("audit failure error = %v", err)
			}
			// A true override is only a clear operation; it cannot turn a
			// nil or panicking provider into a healthy audit sink.
			gate.SetAuditAvailable(true)
			if _, err := gate.TryAcquire(binding); !errors.Is(err, ErrAuditUnavailable) {
				t.Fatalf("true override bypassed audit provider: %v", err)
			}
			_ = gate.Close()
		})
	}
}

func TestAuditAvailabilityRecoveryStillRequiresProvider(t *testing.T) {
	health := NewAuditHealthState(false)
	limits, err := NewLimits(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := New(limits, health)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := NewConnection("connection-a", "profile-a", "revision-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding(connection.ID(), connection.ProfileID(), connection.Revision())
	if err != nil {
		t.Fatal(err)
	}
	gate.SetAuditAvailable(false)
	gate.SetAuditAvailable(true)
	if _, err := gate.TryAcquire(binding); !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("provider failure bypassed by true recovery override: %v", err)
	}
	health.SetHealthy(true)
	permit, err := gate.TryAcquire(binding)
	if err != nil {
		t.Fatal(err)
	}
	permit.Release()
	gate.SetAuditAvailable(false)
	gate.SetAuditAvailable(true)
	permit, err = gate.TryAcquire(binding)
	if err != nil {
		t.Fatalf("healthy provider did not recover after clearing block: %v", err)
	}
	permit.Release()
}

func TestCloseCancelsAndWaitsForExplicitRelease(t *testing.T) {
	gate, _, bindingA, _ := testGate(t, 1, 1)
	p, err := gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Fatal("close did not cancel permit")
	}
	if _, err := gate.TryAcquire(bindingA); !errors.Is(err, ErrGateClosed) {
		t.Fatalf("closed gate error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := gate.Wait(ctx); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait before release error = %v", err)
	}
	p.Release()
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after release error = %v", err)
	}
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAcquireReleaseAndLifecycle(t *testing.T) {
	gate, _, bindingA, bindingB := testGate(t, 8, 4)
	bindings := []Binding{bindingA, bindingB}
	var wg sync.WaitGroup
	var active atomic.Int64
	var maxActive atomic.Int64
	for i := 0; i < 32; i++ {
		binding := bindings[i%len(bindings)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				permit, err := gate.Acquire(ctx, binding)
				cancel()
				if err != nil {
					if !errors.Is(err, ErrCancelled) && !errors.Is(err, ErrConnectionDisabled) && !errors.Is(err, ErrAuditUnavailable) {
						t.Errorf("Acquire error = %v", err)
					}
					continue
				}
				current := active.Add(1)
				for {
					old := maxActive.Load()
					if current <= old || maxActive.CompareAndSwap(old, current) {
						break
					}
				}
				time.Sleep(time.Microsecond)
				active.Add(-1)
				permit.Release()
			}
		}()
	}
	// Lifecycle changes race with workers but are intentionally bounded and
	// local. Replacing with the same trusted record cancels current permits and
	// restores admission without altering the binding contract.
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			_ = gate.DisableConnection(bindingA.ConnectionID())
		} else {
			_ = gate.EnableConnection(bindingA.ConnectionID())
		}
		if i%5 == 0 {
			gate.SetAuditAvailable(false)
			gate.SetAuditAvailable(true)
		}
	}
	_ = gate.EnableConnection(bindingA.ConnectionID())
	wg.Wait()
	if active.Load() != 0 {
		t.Fatalf("test workers still active: %d", active.Load())
	}
	if maxActive.Load() > 8 {
		t.Fatalf("global bound exceeded: %d", maxActive.Load())
	}
	if err := gate.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTryAcquireFailsClosedWhenAuditProviderBlocks(t *testing.T) {
	release := make(chan struct{})
	health := AuditHealthFunc(func() bool {
		<-release
		return true
	})
	gate, err := New(Limits{MaxGlobal: 1, MaxPerConnection: 1}, health)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := NewConnection("conn", "profile", "rev", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding("conn", "profile", "rev")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := gate.TryAcquire(binding); !errors.Is(err, ErrAuditUnavailable) {
		close(release)
		t.Fatalf("TryAcquire = %v, want ErrAuditUnavailable", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		close(release)
		t.Fatalf("TryAcquire was not bounded: %v", elapsed)
	}
	close(release)
	_ = gate.Close()
}

func TestReentrantAuditAdmissionFailsClosedWithoutDeadlock(t *testing.T) {
	var gate *Gate
	var binding Binding
	innerResult := make(chan error, 1)
	health := AuditHealthFunc(func() bool {
		_, err := gate.TryAcquire(binding)
		innerResult <- err
		return true
	})
	var err error
	gate, err = New(Limits{MaxGlobal: 1, MaxPerConnection: 1}, health)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := NewConnection("conn", "profile", "rev", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(connection); err != nil {
		t.Fatal(err)
	}
	binding, err = NewBinding("conn", "profile", "rev")
	if err != nil {
		t.Fatal(err)
	}
	permit, outerErr := gate.TryAcquire(binding)
	if outerErr != nil && !errors.Is(outerErr, ErrAuditUnavailable) {
		t.Fatalf("outer TryAcquire = %v", outerErr)
	}
	if permit != nil {
		defer permit.Release()
	}
	select {
	case err := <-innerResult:
		if !errors.Is(err, ErrAuditUnavailable) {
			t.Fatalf("reentrant TryAcquire = %v, want ErrAuditUnavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reentrant audit check deadlocked")
	}
}
