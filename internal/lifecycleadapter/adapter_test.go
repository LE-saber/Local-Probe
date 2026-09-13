package lifecycleadapter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/admission"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/supervisor"
)

type testRuntimeFactory struct {
	mu      sync.Mutex
	results []testStartResult
	panic   bool
	startFn func(context.Context) (supervisor.Child, error)
}

type testStartResult struct {
	child supervisor.Child
	err   error
}

func (f *testRuntimeFactory) Start(ctx context.Context, _ supervisor.ConnectionSpec) (supervisor.Child, error) {
	return f.start(ctx)
}

func (f *testRuntimeFactory) start(ctx context.Context) (supervisor.Child, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startFn != nil {
		return f.startFn(ctx)
	}
	if f.panic {
		panic("secret runtime panic")
	}
	if len(f.results) == 0 {
		return nil, errors.New("secret start error")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result.child, result.err
}

type testChild struct {
	stops    atomic.Int32
	failures atomic.Int32
	panic    atomic.Bool
}

type childFunc func(context.Context) error

func (f childFunc) Stop(ctx context.Context) error { return f(ctx) }

func (c *testChild) Stop(context.Context) error {
	c.stops.Add(1)
	if c.panic.Load() {
		c.panic.Store(false)
		panic("secret stop panic")
	}
	if c.failures.Add(-1) >= 0 {
		return errors.New("secret stop error")
	}
	return nil
}

func testSetup(t *testing.T, id, revision string, results ...testStartResult) (*admission.Gate, *Factory, supervisor.ConnectionSpec) {
	t.Helper()
	health := admission.NewAuditHealthState(true)
	gate, err := admission.New(admission.DefaultLimits(), health)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := admission.NewLifecycleConnection(id, "profile-a", revision, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.AddConnection(connection); err != nil {
		t.Fatal(err)
	}
	factory, err := New(gate, &testRuntimeFactory{results: results})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := supervisor.NewHealthMetadata("health", time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := supervisor.NewConnectionSpec(id, revision, config.TransportLocal, "", "", 8788, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return gate, factory, spec
}

func TestFactoryChildActivatesAndInvalidatesExactLifecycle(t *testing.T) {
	baseChild := &testChild{}
	gate, factory, spec := testSetup(t, "connection-a", "revision-a", testStartResult{child: baseChild})
	wrapped, err := factory.Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ready := wrapped.(supervisor.ReadyChild)
	if err := ready.MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := gate.Snapshot(spec.ID())
	if !snapshot.LifecycleReady {
		t.Fatal("ready hook did not activate lifecycle")
	}
	if err := wrapped.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = gate.Snapshot(spec.ID())
	if snapshot.LifecycleReady || baseChild.stops.Load() != 1 {
		t.Fatalf("stop snapshot=%#v calls=%d", snapshot, baseChild.stops.Load())
	}
	if err := wrapped.Stop(context.Background()); err != nil || baseChild.stops.Load() != 1 {
		t.Fatalf("idempotent stop err=%v calls=%d", err, baseChild.stops.Load())
	}
}

func TestStartPartialFailureReturnsCleanupChildAndInvalidates(t *testing.T) {
	baseChild := &testChild{}
	gate, factory, spec := testSetup(t, "connection-a", "revision-a", testStartResult{child: baseChild, err: errors.New("secret")})
	wrapped, err := factory.Start(context.Background(), spec)
	if !errors.Is(err, ErrRuntimeStart) || wrapped == nil {
		t.Fatalf("Start = %#v, %v", wrapped, err)
	}
	snapshot, _ := gate.Snapshot(spec.ID())
	if snapshot.LifecycleReady {
		t.Fatal("partial start left lifecycle ready")
	}
	if err := wrapped.Stop(context.Background()); err != nil || baseChild.stops.Load() != 1 {
		t.Fatalf("cleanup err=%v calls=%d", err, baseChild.stops.Load())
	}
}

func TestStartPreservesAuthFailureAndPostStartCancellation(t *testing.T) {
	for _, withChild := range []bool{false, true} {
		var baseChild supervisor.Child
		if withChild {
			baseChild = &testChild{}
		}
		_, factory, spec := testSetup(t, "connection-a", "revision-a", testStartResult{child: baseChild, err: supervisor.ErrAuthFailed})
		wrapped, err := factory.Start(context.Background(), spec)
		if !errors.Is(err, supervisor.ErrAuthFailed) || (wrapped != nil) != withChild {
			t.Fatalf("auth Start child=%T err=%v", wrapped, err)
		}
		if wrapped != nil {
			_ = wrapped.Stop(context.Background())
		}
	}

	baseChild := &testChild{}
	_, factory, spec := testSetup(t, "connection-b", "revision-b")
	ctx, cancel := context.WithCancel(context.Background())
	factory.base.(*testRuntimeFactory).startFn = func(context.Context) (supervisor.Child, error) {
		cancel()
		return baseChild, nil
	}
	wrapped, err := factory.Start(ctx, spec)
	if !errors.Is(err, ErrCancelled) || wrapped == nil {
		t.Fatalf("cancelled Start child=%T err=%v", wrapped, err)
	}
	if err := wrapped.Stop(context.Background()); err != nil {
		t.Fatalf("cancelled child cleanup: %v", err)
	}
}

func TestStopTreatsClosedGateAsUnavailableAndClassifiesCancellation(t *testing.T) {
	baseChild := &testChild{}
	gate, factory, spec := testSetup(t, "connection-a", "revision-a", testStartResult{child: baseChild})
	wrapped, _ := factory.Start(context.Background(), spec)
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
	if err := wrapped.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after gate close: %v", err)
	}

	gate, factory, spec = testSetup(t, "connection-b", "revision-b", testStartResult{child: childFunc(func(ctx context.Context) error { return ctx.Err() })})
	wrapped, _ = factory.Start(context.Background(), spec)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wrapped.Stop(cancelled); !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancelled Stop = %v", err)
	}
}

func TestStaleChildStopCannotInvalidateNewLifecycle(t *testing.T) {
	oldBase, newBase := &testChild{}, &testChild{}
	gate, factory, spec := testSetup(t, "connection-a", "revision-a", testStartResult{child: oldBase}, testStartResult{child: newBase})
	oldChild, _ := factory.Start(context.Background(), spec)
	newChild, _ := factory.Start(context.Background(), spec)
	if err := newChild.(supervisor.ReadyChild).MarkReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := oldChild.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := gate.Snapshot(spec.ID())
	if !snapshot.LifecycleReady || oldBase.stops.Load() != 1 {
		t.Fatalf("stale stop harmed current lifecycle: %#v", snapshot)
	}
	_ = newChild.Stop(context.Background())
}

func TestStopFailureAndPanicRemainRetryable(t *testing.T) {
	for _, panicFirst := range []bool{false, true} {
		baseChild := &testChild{}
		if panicFirst {
			baseChild.panic.Store(true)
		} else {
			baseChild.failures.Store(1)
		}
		_, factory, spec := testSetup(t, "connection-a", "revision-a", testStartResult{child: baseChild})
		wrapped, _ := factory.Start(context.Background(), spec)
		if err := wrapped.Stop(context.Background()); !errors.Is(err, ErrRuntimeStop) {
			t.Fatalf("first Stop = %v", err)
		}
		if err := wrapped.Stop(context.Background()); err != nil || baseChild.stops.Load() != 2 {
			t.Fatalf("retry Stop = %v calls=%d", err, baseChild.stops.Load())
		}
	}
}

func TestMarkReadyAndStopRaceEndsUnavailable(t *testing.T) {
	for i := 0; i < 100; i++ {
		baseChild := &testChild{}
		gate, factory, spec := testSetup(t, "connection-a", "revision-a", testStartResult{child: baseChild})
		wrapped, _ := factory.Start(context.Background(), spec)
		var group sync.WaitGroup
		group.Add(2)
		go func() { defer group.Done(); _ = wrapped.(supervisor.ReadyChild).MarkReady(context.Background()) }()
		go func() { defer group.Done(); _ = wrapped.Stop(context.Background()) }()
		group.Wait()
		snapshot, _ := gate.Snapshot(spec.ID())
		if snapshot.LifecycleReady {
			t.Fatal("ready/stop race left lifecycle ready")
		}
	}
}
