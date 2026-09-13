package admission

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func lifecycleTestGate(t *testing.T, id string) (*Gate, *LifecycleCapability) {
	t.Helper()
	health := NewAuditHealthState(true)
	gate, err := New(Limits{MaxGlobal: 4, MaxPerConnection: 2}, health)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := NewLifecycleConnection(id, "profile-"+id, "revision-"+id, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(connection); err != nil {
		t.Fatal(err)
	}
	capability, err := gate.BeginLifecycle(id)
	if err != nil {
		t.Fatal(err)
	}
	return gate, capability
}

func TestLifecycleRequiresCapabilityAndMarkReady(t *testing.T) {
	gate, capability := lifecycleTestGate(t, "connection-a")
	legacy, err := NewBinding("connection-a", "profile-connection-a", "revision-connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.TryAcquire(legacy); !errors.Is(err, ErrLifecycleRequired) {
		t.Fatalf("legacy binding error = %v", err)
	}

	binding, err := NewLifecycleBinding(capability)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.TryAcquire(binding); !errors.Is(err, ErrLifecycleUnavailable) {
		t.Fatalf("pre-ready error = %v", err)
	}
	if err := gate.MarkReady(capability); err != nil {
		t.Fatal(err)
	}
	permit, err := gate.Acquire(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	permit.Release()
	snapshot, err := gate.Snapshot("connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.LifecycleRequired || !snapshot.LifecycleReady || snapshot.LifecycleEpoch == 0 {
		t.Fatalf("unexpected lifecycle snapshot: %#v", snapshot)
	}
}

func TestLifecycleCapabilityCannotBeCopiedOrSerialized(t *testing.T) {
	gate, capability := lifecycleTestGate(t, "connection-a")
	copyCapability := *capability
	if err := gate.MarkReady(&copyCapability); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("copied capability error = %v", err)
	}
	if err := gate.MarkReady(&LifecycleCapability{}); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("zero capability error = %v", err)
	}
	if _, err := json.Marshal(capability); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("capability JSON error = %v", err)
	}
	var decoded LifecycleCapability
	if err := json.Unmarshal([]byte(`{"epoch":1}`), &decoded); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("capability JSON decode error = %v", err)
	}
	if err := gate.MarkReady(capability); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleBeginInvalidatesPermitsAndOldCapability(t *testing.T) {
	gate, first := lifecycleTestGate(t, "connection-a")
	firstBinding, err := NewLifecycleBinding(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(first); err != nil {
		t.Fatal(err)
	}
	permit, err := gate.TryAcquire(firstBinding)
	if err != nil {
		t.Fatal(err)
	}
	second, err := gate.ReplaceLifecycle("connection-a")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-permit.Done():
	default:
		t.Fatal("replacing lifecycle did not cancel old permit")
	}
	permit.Release()
	if _, err := gate.TryAcquire(firstBinding); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("old binding error = %v", err)
	}
	if err := gate.MarkReady(first); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("old capability error = %v", err)
	}
	secondBinding, err := NewLifecycleBinding(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(second); err != nil {
		t.Fatal(err)
	}
	newPermit, err := gate.TryAcquire(secondBinding)
	if err != nil {
		t.Fatal(err)
	}
	newPermit.Release()
}

func TestLifecycleInvalidateDisableAndRevisionReplace(t *testing.T) {
	gate, capability := lifecycleTestGate(t, "connection-a")
	binding, err := NewLifecycleBinding(capability)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(capability); err != nil {
		t.Fatal(err)
	}
	permit, err := gate.TryAcquire(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.InvalidateLifecycle("connection-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-permit.Done():
	default:
		t.Fatal("invalidation did not cancel old permit")
	}
	permit.Release()
	if _, err := gate.TryAcquire(binding); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("invalidated binding error = %v", err)
	}

	next, err := gate.BeginLifecycle("connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(next); err != nil {
		t.Fatal(err)
	}
	nextBinding, err := NewLifecycleBinding(next)
	if err != nil {
		t.Fatal(err)
	}
	permit, err = gate.TryAcquire(nextBinding)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.DisableConnection("connection-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-permit.Done():
	default:
		t.Fatal("disable did not cancel old permit")
	}
	permit.Release()
	if _, err := gate.TryAcquire(nextBinding); !errors.Is(err, ErrConnectionDisabled) {
		t.Fatalf("disabled binding error = %v", err)
	}
	if err := gate.EnableConnection("connection-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.TryAcquire(nextBinding); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("post-disable stale binding error = %v", err)
	}

	replacement, err := NewLifecycleConnection("connection-a", "profile-connection-a", "revision-new", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.ReplaceConnection(replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.TryAcquire(nextBinding); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("revision stale binding error = %v", err)
	}
	newCapability, err := gate.BeginLifecycle("connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(newCapability); err != nil {
		t.Fatal(err)
	}
	newBinding, err := NewLifecycleBinding(newCapability)
	if err != nil {
		t.Fatal(err)
	}
	permit, err = gate.TryAcquire(newBinding)
	if err != nil {
		t.Fatal(err)
	}
	permit.Release()
}

func TestLifecycleConnectionsAreIsolated(t *testing.T) {
	health := NewAuditHealthState(true)
	gate, err := New(Limits{MaxGlobal: 4, MaxPerConnection: 1}, health)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"connection-a", "connection-b"} {
		connection, err := NewLifecycleConnection(id, "profile-"+id, "revision-"+id, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := gate.AddConnection(connection); err != nil {
			t.Fatal(err)
		}
		capability, err := gate.BeginLifecycle(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := gate.MarkReady(capability); err != nil {
			t.Fatal(err)
		}
	}
	capA, err := gate.BeginLifecycle("connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(capA); err != nil {
		t.Fatal(err)
	}
	capB, err := gate.BeginLifecycle("connection-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(capB); err != nil {
		t.Fatal(err)
	}
	bindingA, _ := NewLifecycleBinding(capA)
	bindingB, _ := NewLifecycleBinding(capB)
	permitA, err := gate.TryAcquire(bindingA)
	if err != nil {
		t.Fatal(err)
	}
	defer permitA.Release()
	permitB, err := gate.TryAcquire(bindingB)
	if err != nil {
		t.Fatal(err)
	}
	defer permitB.Release()
	if err := gate.InvalidateLifecycle("connection-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-permitA.Done():
	default:
		t.Fatal("A invalidation did not cancel A permit")
	}
	select {
	case <-permitB.Done():
		t.Fatal("A invalidation canceled B permit")
	default:
	}
}

func TestLifecycleConcurrentBeginReadyAcquire(t *testing.T) {
	gate, _ := lifecycleTestGate(t, "connection-a")
	var group sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < 80; i++ {
				capability, err := gate.BeginLifecycle("connection-a")
				if err != nil {
					continue
				}
				_ = gate.MarkReady(capability)
				binding, err := NewLifecycleBinding(capability)
				if err != nil {
					continue
				}
				permit, err := gate.TryAcquire(binding)
				if err == nil {
					permit.Release()
				}
			}
		}()
	}
	group.Wait()
}

func TestInvalidateCapabilityOnlyClosesCurrentEpoch(t *testing.T) {
	gate, first := lifecycleTestGate(t, "connection-a")
	if err := gate.MarkReady(first); err != nil {
		t.Fatal(err)
	}
	firstBinding, err := NewLifecycleBinding(first)
	if err != nil {
		t.Fatal(err)
	}
	firstPermit, err := gate.TryAcquire(firstBinding)
	if err != nil {
		t.Fatal(err)
	}
	defer firstPermit.Release()

	second, err := gate.ReplaceLifecycle("connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(second); err != nil {
		t.Fatal(err)
	}
	secondBinding, err := NewLifecycleBinding(second)
	if err != nil {
		t.Fatal(err)
	}
	secondPermit, err := gate.TryAcquire(secondBinding)
	if err != nil {
		t.Fatal(err)
	}
	defer secondPermit.Release()

	if err := gate.InvalidateCapability(first); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("stale capability error = %v", err)
	}
	select {
	case <-secondPermit.Done():
		t.Fatal("stale capability invalidated the current permit")
	default:
	}
	secondPermit.Release()
	secondPermit = nil
	current, err := gate.TryAcquire(secondBinding)
	if err != nil {
		t.Fatalf("current capability was changed by stale cleanup: %v", err)
	}
	current.Release()

	active, err := gate.TryAcquire(secondBinding)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Release()
	copyCapability := *second
	if err := gate.InvalidateCapability(&copyCapability); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("copied capability invalidation error = %v", err)
	}
	otherGate, otherCapability := lifecycleTestGate(t, "connection-b")
	if err := otherGate.MarkReady(otherCapability); err != nil {
		t.Fatal(err)
	}
	if err := gate.InvalidateCapability(otherCapability); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("cross-gate capability invalidation error = %v", err)
	}
	select {
	case <-active.Done():
		t.Fatal("invalid capability invalidated the current permit")
	default:
	}
	if err := gate.InvalidateCapability(second); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.TryAcquire(secondBinding); !errors.Is(err, ErrLifecycleMismatch) {
		t.Fatalf("invalidated current binding error = %v", err)
	}
	select {
	case <-active.Done():
	default:
		t.Fatal("current capability invalidation did not cancel permit")
	}
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gate.InvalidateCapability(second); !errors.Is(err, ErrGateClosed) {
		t.Fatalf("closed gate capability invalidation error = %v", err)
	}
}

func TestInvalidateCapabilityConcurrentWithNewLifecycle(t *testing.T) {
	gate, first := lifecycleTestGate(t, "connection-a")
	if err := gate.MarkReady(first); err != nil {
		t.Fatal(err)
	}
	second, err := gate.BeginLifecycle("connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(second); err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < 100; i++ {
				_ = gate.InvalidateCapability(first)
			}
		}()
	}
	for i := 0; i < 100; i++ {
		current, err := gate.BeginLifecycle("connection-a")
		if err != nil {
			t.Fatal(err)
		}
		if err := gate.MarkReady(current); err != nil {
			t.Fatal(err)
		}
		binding, err := NewLifecycleBinding(current)
		if err != nil {
			t.Fatal(err)
		}
		permit, err := gate.TryAcquire(binding)
		if err == nil {
			permit.Release()
		}
	}
	group.Wait()
	final, err := gate.BeginLifecycle("connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(final); err != nil {
		t.Fatal(err)
	}
	finalBinding, err := NewLifecycleBinding(final)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := gate.TryAcquire(finalBinding)
	if err != nil {
		t.Fatalf("stale cleanup corrupted final lifecycle: %v", err)
	}
	permit.Release()
}
