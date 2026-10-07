package connectionmanager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/admission"
	"github.com/LE-saber/Local-Probe/internal/supervisor"
)

type fakeSupervisor struct {
	mu sync.Mutex

	addErr, replaceErr, startErr, stopErr, removeErr, closeErr             error
	addCalls, replaceCalls, startCalls, stopCalls, removeCalls, closeCalls int
	reconnectCalls, sleepCalls, wakeCalls                                  int
	addPanic                                                               bool
	startPanic                                                             bool
	onReplace                                                              func()
	onStop                                                                 func()
	onClose                                                                func()
	replaceBlock                                                           <-chan struct{}
	removeBlock                                                            <-chan struct{}
}

func (f *fakeSupervisor) Add(supervisor.ConnectionSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addCalls++
	if f.addPanic {
		panic("injected")
	}
	return f.addErr
}
func (f *fakeSupervisor) Replace(ctx context.Context, _ supervisor.ConnectionSpec) error {
	f.mu.Lock()
	f.replaceCalls++
	hook, block, err := f.onReplace, f.replaceBlock, f.replaceErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func (f *fakeSupervisor) Start(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	if f.startPanic {
		panic("injected")
	}
	return f.startErr
}
func (f *fakeSupervisor) Stop(context.Context, string) error {
	f.mu.Lock()
	f.stopCalls++
	hook, err := f.onStop, f.stopErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}
func (f *fakeSupervisor) Reconnect(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconnectCalls++
	return nil
}
func (f *fakeSupervisor) Sleep(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sleepCalls++
	return nil
}
func (f *fakeSupervisor) Wake(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakeCalls++
	return nil
}
func (f *fakeSupervisor) Remove(ctx context.Context, _ string) error {
	f.mu.Lock()
	f.removeCalls++
	block, err := f.removeBlock, f.removeErr
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func (f *fakeSupervisor) Close(context.Context) error {
	f.mu.Lock()
	f.closeCalls++
	hook, err := f.onClose, f.closeErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func newFixture(t *testing.T) (*admission.Gate, *fakeSupervisor, *Manager) {
	t.Helper()
	limits, err := admission.NewLimits(8, 4)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := admission.New(limits, admission.NewAuditHealthState(true))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSupervisor{}
	manager, err := New(gate, fake)
	if err != nil {
		t.Fatal(err)
	}
	return gate, fake, manager
}

func pair(t *testing.T, id, revision string, enabled bool, port uint16) (admission.Connection, supervisor.ConnectionSpec) {
	t.Helper()
	connection, err := admission.NewLifecycleConnection(id, "profile-"+id, revision, enabled)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := supervisor.NewLocalConnectionSpec(id, revision, port, supervisor.HealthMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	return connection, spec
}

func readyPermit(t *testing.T, gate *admission.Gate, id string) (admission.Binding, *admission.Permit) {
	t.Helper()
	capability, err := gate.BeginLifecycle(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(capability); err != nil {
		t.Fatal(err)
	}
	binding, err := gate.CurrentLifecycleBinding(id)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := gate.TryAcquire(binding)
	if err != nil {
		t.Fatal(err)
	}
	return binding, permit
}

func TestAddRequiresMatchingLifecyclePairAndRollsBack(t *testing.T) {
	gate, fake, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	legacy, _ := admission.NewConnection("legacy", "profile", "r1", true)
	if err := manager.Add(legacy, spec); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("legacy Add = %v", err)
	}

	fake.addErr = errors.New("injected")
	if err := manager.Add(connection, spec); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("Add = %v", err)
	}
	if _, err := gate.Snapshot("alpha"); !errors.Is(err, admission.ErrConnectionMissing) {
		t.Fatalf("gate snapshot = %v", err)
	}
	fake.addErr = nil
	if err := manager.Add(connection, spec); err != nil {
		t.Fatalf("retry Add: %v", err)
	}
	if err := manager.Add(connection, spec); !errors.Is(err, ErrConnectionExists) {
		t.Fatalf("duplicate Add = %v", err)
	}
}

func TestAddRecoversSupervisorPanicAndReleasesReservation(t *testing.T) {
	gate, fake, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	fake.addPanic = true
	fake.removeErr = supervisor.ErrConnectionMissing
	if err := manager.Add(connection, spec); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("panicking Add = %v", err)
	}
	if _, err := gate.Snapshot("alpha"); !errors.Is(err, admission.ErrConnectionMissing) {
		t.Fatalf("gate rollback = %v", err)
	}
	fake.addPanic = false
	fake.removeErr = nil
	if err := manager.Add(connection, spec); err != nil {
		t.Fatalf("retry Add: %v", err)
	}
}

func TestAddPanicCleanupTimeoutRetainsRetryableRemoval(t *testing.T) {
	gate, fake, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	fake.addPanic = true
	fake.removeBlock = make(chan struct{})
	started := time.Now()
	if err := manager.Add(connection, spec); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("panicking Add = %v", err)
	}
	if time.Since(started) > 2*panicCleanupTimeout {
		t.Fatal("panic cleanup was not bounded")
	}
	if _, err := gate.Snapshot("alpha"); !errors.Is(err, admission.ErrConnectionMissing) {
		t.Fatalf("gate rollback = %v", err)
	}
	fake.removeBlock = nil
	fake.removeErr = supervisor.ErrConnectionMissing
	if err := manager.Remove(context.Background(), "alpha"); err != nil {
		t.Fatalf("retry Remove: %v", err)
	}
}

func TestSupervisorPanicIsStableAndFailClosed(t *testing.T) {
	gate, fake, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(connection, spec); err != nil {
		t.Fatal(err)
	}
	_, permit := readyPermit(t, gate, "alpha")
	fake.startPanic = true
	if err := manager.Start(context.Background(), "alpha"); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("panicking Start = %v", err)
	}
	select {
	case <-permit.Context().Done():
	default:
		t.Fatal("panicking Start did not invalidate admission")
	}
	permit.Release()
}

func TestKnownEntryLockRejectsRemovedAndReusedID(t *testing.T) {
	_, _, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(connection, spec); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	old := manager.entries["alpha"]
	replacement := &entry{op: make(chan struct{}, 1), connection: connection, spec: spec, enabled: true, phase: phaseStopped}
	manager.entries["alpha"] = replacement
	manager.mu.Unlock()
	if _, err := manager.lockKnownEntry(context.Background(), "alpha", old); !errors.Is(err, ErrConnectionMissing) {
		t.Fatalf("stale lock = %v", err)
	}
}

func TestStopInvalidatesBeforeCleanupAndIsRetryable(t *testing.T) {
	gate, fake, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(connection, spec); err != nil {
		t.Fatal(err)
	}
	binding, permit := readyPermit(t, gate, "alpha")
	fake.stopErr = errors.New("cleanup")
	fake.onStop = func() {
		if _, err := gate.TryAcquire(binding); err == nil {
			t.Errorf("binding remained usable: %v", err)
		}
		select {
		case <-permit.Context().Done():
		default:
			t.Error("permit not cancelled before Stop")
		}
	}
	if err := manager.Stop(context.Background(), "alpha"); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("Stop = %v", err)
	}
	fake.stopErr = nil
	if err := manager.Stop(context.Background(), "alpha"); err != nil {
		t.Fatalf("retry Stop: %v", err)
	}
	permit.Release()
}

func TestReplaceFailureKeepsNewRevisionFailClosedAndRetryable(t *testing.T) {
	gate, fake, manager := newFixture(t)
	oldConnection, oldSpec := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(oldConnection, oldSpec); err != nil {
		t.Fatal(err)
	}
	oldBinding, permit := readyPermit(t, gate, "alpha")
	newConnection, newSpec := pair(t, "alpha", "r2", true, 4102)
	fake.replaceErr = errors.New("injected")
	fake.onReplace = func() {
		snapshot, err := gate.Snapshot("alpha")
		if err != nil {
			t.Errorf("snapshot: %v", err)
			return
		}
		if snapshot.Revision != "r2" || snapshot.LifecycleReady {
			t.Errorf("unsafe snapshot: %+v", snapshot)
		}
		if _, err := gate.TryAcquire(oldBinding); err == nil {
			t.Error("old binding survived replacement")
		}
		select {
		case <-permit.Context().Done():
		default:
			t.Error("old permit not cancelled")
		}
	}
	if err := manager.Replace(context.Background(), newConnection, newSpec); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("Replace = %v", err)
	}
	if err := manager.Start(context.Background(), "alpha"); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("Start after partial Replace = %v", err)
	}
	fake.replaceErr = nil
	if err := manager.Replace(context.Background(), newConnection, newSpec); err != nil {
		t.Fatalf("retry Replace: %v", err)
	}
	if err := manager.Start(context.Background(), "alpha"); err != nil {
		t.Fatalf("Start new revision: %v", err)
	}
	if _, err := gate.CurrentLifecycleBinding("alpha"); !errors.Is(err, admission.ErrLifecycleUnavailable) {
		t.Fatalf("Start alone opened admission: %v", err)
	}
	permit.Release()
}

func TestOperationsSerializePerConnectionButNotAcrossConnections(t *testing.T) {
	_, fake, manager := newFixture(t)
	a1, s1 := pair(t, "alpha", "r1", true, 4101)
	b1, bs := pair(t, "beta", "r1", true, 4102)
	if err := manager.Add(a1, s1); err != nil {
		t.Fatal(err)
	}
	if err := manager.Add(b1, bs); err != nil {
		t.Fatal(err)
	}
	a2, s2 := pair(t, "alpha", "r2", true, 4103)
	block := make(chan struct{})
	entered := make(chan struct{})
	fake.replaceBlock = block
	fake.onReplace = func() { close(entered) }
	replaceDone := make(chan error, 1)
	go func() { replaceDone <- manager.Replace(context.Background(), a2, s2) }()
	<-entered

	stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Stop(stopCtx, "alpha"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent alpha Stop = %v", err)
	}
	if err := manager.Start(context.Background(), "beta"); err != nil {
		t.Fatalf("beta Start blocked by alpha: %v", err)
	}
	close(block)
	if err := <-replaceDone; err != nil {
		t.Fatalf("Replace: %v", err)
	}
}

func TestDisableEnableDoesNotOpenAdmission(t *testing.T) {
	gate, _, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(connection, spec); err != nil {
		t.Fatal(err)
	}
	binding, permit := readyPermit(t, gate, "alpha")
	if err := manager.Disable(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.TryAcquire(binding); err == nil {
		t.Fatal("disabled binding remained usable")
	}
	if err := manager.Start(context.Background(), "alpha"); !errors.Is(err, ErrConnectionDisabled) {
		t.Fatalf("disabled Start = %v", err)
	}
	if err := manager.Enable(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.CurrentLifecycleBinding("alpha"); !errors.Is(err, admission.ErrLifecycleUnavailable) {
		t.Fatalf("Enable opened admission: %v", err)
	}
	permit.Release()
}

func TestRevokeRequiresTrustedReplaceBeforeRecovery(t *testing.T) {
	gate, _, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(connection, spec); err != nil {
		t.Fatal(err)
	}
	_, permit := readyPermit(t, gate, "alpha")
	if err := manager.Revoke(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(context.Background(), "alpha"); !errors.Is(err, ErrAdmission) {
		t.Fatalf("Enable revoked connection = %v", err)
	}
	replacement, replacementSpec := pair(t, "alpha", "r2", true, 4102)
	if err := manager.Replace(context.Background(), replacement, replacementSpec); err != nil {
		t.Fatalf("Replace revoked connection: %v", err)
	}
	if _, err := gate.CurrentLifecycleBinding("alpha"); !errors.Is(err, admission.ErrLifecycleUnavailable) {
		t.Fatalf("Replace opened admission: %v", err)
	}
	permit.Release()
}

func TestReconnectSleepWakeAllRequireFreshLifecycle(t *testing.T) {
	gate, fake, manager := newFixture(t)
	connection, spec := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(connection, spec); err != nil {
		t.Fatal(err)
	}
	binding, permit := readyPermit(t, gate, "alpha")
	if err := manager.Reconnect(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.TryAcquire(binding); err == nil {
		t.Fatal("Reconnect retained old binding")
	}
	if _, err := gate.CurrentLifecycleBinding("alpha"); !errors.Is(err, admission.ErrLifecycleUnavailable) {
		t.Fatalf("Reconnect opened admission: %v", err)
	}
	capability, err := gate.BeginLifecycle("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkReady(capability); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sleep(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.CurrentLifecycleBinding("alpha"); !errors.Is(err, admission.ErrLifecycleUnavailable) {
		t.Fatalf("Sleep left admission ready: %v", err)
	}
	if err := manager.Wake(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.CurrentLifecycleBinding("alpha"); !errors.Is(err, admission.ErrLifecycleUnavailable) {
		t.Fatalf("Wake opened admission without Ready: %v", err)
	}
	fake.mu.Lock()
	calls := [3]int{fake.reconnectCalls, fake.sleepCalls, fake.wakeCalls}
	fake.mu.Unlock()
	if calls != [3]int{1, 1, 1} {
		t.Fatalf("recovery calls = %v", calls)
	}
	permit.Release()
}

func TestRemoveAndCloseKeepAdmissionFailClosedWhileCleanupRetries(t *testing.T) {
	gate, fake, manager := newFixture(t)
	a, as := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(a, as); err != nil {
		t.Fatal(err)
	}
	_, permit := readyPermit(t, gate, "alpha")
	fake.removeErr = errors.New("cleanup")
	if err := manager.Remove(context.Background(), "alpha"); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("Remove = %v", err)
	}
	if _, err := gate.Snapshot("alpha"); !errors.Is(err, admission.ErrConnectionMissing) {
		t.Fatalf("gate retained removed connection: %v", err)
	}
	fake.removeErr = nil
	if err := manager.Remove(context.Background(), "alpha"); err != nil {
		t.Fatalf("retry Remove: %v", err)
	}
	permit.Release()

	gate2, fake2, manager2 := newFixture(t)
	b, bs := pair(t, "beta", "r1", true, 4102)
	if err := manager2.Add(b, bs); err != nil {
		t.Fatal(err)
	}
	_, permit2 := readyPermit(t, gate2, "beta")
	fake2.closeErr = errors.New("cleanup")
	fake2.onClose = func() {
		if _, err := gate2.CurrentLifecycleBinding("beta"); !errors.Is(err, admission.ErrGateClosed) {
			t.Errorf("gate not closed before supervisor: %v", err)
		}
		select {
		case <-permit2.Context().Done():
		default:
			t.Error("Close did not cancel permit")
		}
	}
	if err := manager2.Close(context.Background()); !errors.Is(err, ErrSupervisor) {
		t.Fatalf("Close = %v", err)
	}
	permit2.Release()
}

func TestCloseFailsAdmissionImmediatelyThenWaitsForActiveOperation(t *testing.T) {
	gate, fake, manager := newFixture(t)
	legacy, err := admission.NewConnection("observer", "profile-observer", "r1", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(legacy); err != nil {
		t.Fatal(err)
	}
	observer, err := admission.NewBinding("observer", "profile-observer", "r1")
	if err != nil {
		t.Fatal(err)
	}
	oldConnection, oldSpec := pair(t, "alpha", "r1", true, 4101)
	if err := manager.Add(oldConnection, oldSpec); err != nil {
		t.Fatal(err)
	}
	newConnection, newSpec := pair(t, "alpha", "r2", true, 4102)
	block := make(chan struct{})
	entered := make(chan struct{})
	fake.replaceBlock = block
	fake.onReplace = func() { close(entered) }
	replaceDone := make(chan error, 1)
	go func() { replaceDone <- manager.Replace(context.Background(), newConnection, newSpec) }()
	<-entered

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close(context.Background()) }()
	deadline := time.Now().Add(time.Second)
	for {
		permit, err := gate.TryAcquire(observer)
		if err != nil {
			break
		}
		permit.Release()
		select {
		case err := <-closeDone:
			t.Fatalf("Close returned before waiting: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Close did not close admission promptly")
		}
		time.Sleep(time.Millisecond)
	}
	fake.mu.Lock()
	closeCalls := fake.closeCalls
	fake.mu.Unlock()
	if closeCalls != 0 {
		t.Fatal("supervisor Close raced active Replace")
	}
	close(block)
	if err := <-replaceDone; err != nil {
		t.Fatalf("active Replace: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
}
