package supervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
)

type testClock struct {
	now time.Time
}

func (c testClock) Now() time.Time { return c.now }

type testTimer struct {
	ch      chan time.Time
	stopped atomic.Bool
	fired   atomic.Bool
}

func (t *testTimer) C() <-chan time.Time { return t.ch }

func (t *testTimer) Stop() bool {
	return !t.stopped.Swap(true)
}

func (t *testTimer) fire(at time.Time) bool {
	if t.stopped.Load() || t.fired.Swap(true) {
		return false
	}
	t.ch <- at
	return true
}

type testTimers struct {
	mu            sync.Mutex
	clock         Clock
	created       []*testTimer
	delays        []time.Duration
	createdSignal chan struct{}
}

type nilTimers struct{}

func (nilTimers) NewTimer(time.Duration) Timer { return nil }

func (f *testTimers) NewTimer(delay time.Duration) Timer {
	timer := &testTimer{ch: make(chan time.Time, 1)}
	f.mu.Lock()
	f.created = append(f.created, timer)
	f.delays = append(f.delays, delay)
	createdSignal := f.createdSignal
	f.mu.Unlock()
	if createdSignal != nil {
		createdSignal <- struct{}{}
	}
	return timer
}

func (f *testTimers) fireNext() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, timer := range f.created {
		if timer.fire(f.clock.Now()) {
			return true
		}
	}
	return false
}

func (f *testTimers) delaysSnapshot() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.delays...)
}

func (f *testTimers) waitForCount(t *testing.T, count int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		f.mu.Lock()
		got := len(f.created)
		f.mu.Unlock()
		if got >= count {
			return
		}
		if f.createdSignal == nil {
			select {
			case <-deadline.C:
				t.Fatalf("timed out waiting for %d timers (got %d)", count, got)
			case <-time.After(time.Millisecond):
			}
			continue
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %d timers (got %d)", count, got)
		case <-f.createdSignal:
		}
	}
}

type testChild struct {
	stopCount atomic.Int32
	stopFn    func(context.Context) error
}

func (c *testChild) Stop(ctx context.Context) error {
	c.stopCount.Add(1)
	if c.stopFn != nil {
		return c.stopFn(ctx)
	}
	return nil
}

type factoryResult struct {
	child Child
	err   error
}

type testFactory struct {
	mu       sync.Mutex
	results  map[string][]factoryResult
	starts   map[string]int
	specs    []ConnectionSpec
	children []*testChild
}

func (f *testFactory) Start(_ context.Context, spec ConnectionSpec) (Child, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts[spec.ID()]++
	f.specs = append(f.specs, spec)
	results := f.results[spec.ID()]
	if len(results) == 0 {
		child := &testChild{}
		f.children = append(f.children, child)
		return child, nil
	}
	result := results[0]
	f.results[spec.ID()] = results[1:]
	if result.err != nil || result.child != nil {
		if child, ok := result.child.(*testChild); ok {
			f.children = append(f.children, child)
		}
		return result.child, result.err
	}
	child := &testChild{}
	f.children = append(f.children, child)
	return child, nil
}

func (f *testFactory) startCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[id]
}

func (f *testFactory) specsSnapshot() []ConnectionSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ConnectionSpec(nil), f.specs...)
}

func (f *testFactory) childStopCounts() []int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := make([]int32, 0, len(f.children))
	for _, child := range f.children {
		counts = append(counts, child.stopCount.Load())
	}
	return counts
}

type testHealth struct {
	local  func(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error)
	remote func(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error)
}

func (h testHealth) CheckLocal(ctx context.Context, spec ConnectionSpec, child RuntimeView) (HealthResult, error) {
	if h.local == nil {
		return HealthResult{Status: HealthReady}, nil
	}
	return h.local(ctx, spec, child)
}

func (h testHealth) CheckRemote(ctx context.Context, spec ConnectionSpec, child RuntimeView) (HealthResult, error) {
	if h.remote == nil {
		return HealthResult{Status: HealthReady}, nil
	}
	return h.remote(ctx, spec, child)
}

func testSpec(t *testing.T, id, revision string, transport config.ConnectionTransport, tunnelID string) ConnectionSpec {
	t.Helper()
	health, err := NewHealthMetadata("health", 0, 0)
	if err != nil {
		t.Fatalf("health metadata: %v", err)
	}
	port := uint32(2166136261)
	for i := 0; i < len(id); i++ {
		port = (port ^ uint32(id[i])) * 16777619
	}
	spec, err := NewConnectionSpec(id, revision, transport, tunnelID, "", uint16(10000+port%50000), health)
	if err != nil {
		t.Fatalf("connection spec: %v", err)
	}
	return spec
}

func testSpecAtPort(t *testing.T, id, revision string, port uint16) ConnectionSpec {
	t.Helper()
	health, err := NewHealthMetadata("health", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewLocalConnectionSpec(id, revision, port, health)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func testSupervisor(t *testing.T, factory RuntimeFactory, health HealthChecker, options Options) *Supervisor {
	t.Helper()
	s, err := New(factory, health, options)
	if err != nil {
		t.Fatalf("new supervisor: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s
}

func waitForState(t *testing.T, s *Supervisor, id string, want State) Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		ok, err := s.WaitForState(ctx, id, want)
		if err != nil {
			t.Fatalf("wait for %s=%s: %v", id, want, err)
		}
		if !ok {
			t.Fatalf("wait for %s=%s returned false", id, want)
		}
		snapshot, err := s.Snapshot(id)
		if err != nil {
			t.Fatalf("snapshot %s: %v", id, err)
		}
		// A state can advance between WaitForState's observation and this
		// snapshot (notably auth_failed -> stopping -> auth_failed while a
		// child is being cleaned). Re-observe until the requested state is
		// stable enough for the assertion below.
		if snapshot.State == want {
			return snapshot
		}
	}
}

func TestConnectionSpecRejectsExecutableURLsAndSecrets(t *testing.T) {
	health, err := NewHealthMetadata("health", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	valid := func() (ConnectionSpec, error) {
		return NewConnectionSpec("conn", "rev-1", config.TransportCloudflareNamed, "tunnel-1", "", 8788, health)
	}
	if _, err := valid(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	invalid := []struct {
		name      string
		id        string
		revision  string
		transport config.ConnectionTransport
		tunnelID  string
		port      uint16
	}{
		{name: "absolute executable path", id: `C:\\Windows\\cmd.exe`, revision: "rev", transport: config.TransportLocal, port: 8788},
		{name: "raw URL", id: "conn", revision: "rev", transport: config.TransportCloudflareNamed, tunnelID: "https://example.test", port: 8788},
		{name: "bearer token", id: "conn", revision: "rev", transport: config.TransportCloudflareNamed, tunnelID: "bearer-secret", port: 8788},
		{name: "hex secret", id: "conn", revision: "rev", transport: config.TransportCloudflareNamed, tunnelID: "0123456789abcdef0123456789abcdef", port: 8788},
		{name: "bad id", id: "../conn", revision: "rev", transport: config.TransportLocal, port: 8788},
		{name: "bad revision", id: "conn", revision: "rev/path", transport: config.TransportLocal, port: 8788},
		{name: "privileged port", id: "conn", revision: "rev", transport: config.TransportLocal, port: 443},
		{name: "local tunnel metadata", id: "conn", revision: "rev", transport: config.TransportLocal, tunnelID: "tunnel-1", port: 8788},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewConnectionSpec(test.id, test.revision, test.transport, test.tunnelID, "", test.port, health)
			if !errors.Is(err, ErrInvalidSpec) {
				t.Fatalf("error = %v, want ErrInvalidSpec", err)
			}
		})
	}
	if _, err := NewHealthMetadata("health --version", 0, 0); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("unsafe health check id error = %v", err)
	}
	if _, err := NewHealthMetadata("health", maxHealthTimeout+time.Nanosecond, 0); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("oversized health timeout error = %v", err)
	}
}

func TestSupervisorLifecycleReachesReadyThroughLocalAndRemoteChecks(t *testing.T) {
	factory := &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}
	localCalled := make(chan struct{}, 1)
	remoteCalled := make(chan struct{}, 1)
	allowLocal := make(chan struct{})
	allowRemote := make(chan struct{})
	var localReady atomic.Bool
	health := testHealth{
		local: func(ctx context.Context, _ ConnectionSpec, _ RuntimeView) (HealthResult, error) {
			localCalled <- struct{}{}
			select {
			case <-allowLocal:
				localReady.Store(true)
				return HealthResult{Status: HealthReady}, nil
			case <-ctx.Done():
				return HealthResult{}, ctx.Err()
			}
		},
		remote: func(ctx context.Context, _ ConnectionSpec, _ RuntimeView) (HealthResult, error) {
			if !localReady.Load() {
				return HealthResult{}, errors.New("remote check ran before local readiness")
			}
			remoteCalled <- struct{}{}
			select {
			case <-allowRemote:
				return HealthResult{Status: HealthReady}, nil
			case <-ctx.Done():
				return HealthResult{}, ctx.Err()
			}
		},
	}
	s := testSupervisor(t, factory, health, Options{})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	<-localCalled
	close(allowLocal)
	<-remoteCalled
	polling, err := s.Snapshot("conn-a")
	if err != nil {
		t.Fatal(err)
	}
	if polling.State != StatePolling {
		t.Fatalf("state while remote check is blocked = %s, want %s", polling.State, StatePolling)
	}
	close(allowRemote)
	ready := waitForState(t, s, "conn-a", StateReady)
	if ready.Attempt != 0 || ready.LastError != ErrorNone {
		t.Fatalf("ready snapshot = %+v", ready)
	}
	if got := factory.startCount("conn-a"); got != 1 {
		t.Fatalf("factory starts = %d, want 1", got)
	}
	if err := s.Stop(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	stopped := waitForState(t, s, "conn-a", StateStopped)
	if stopped.LastError != ErrorStopped {
		t.Fatalf("stopped snapshot = %+v", stopped)
	}
	if counts := factory.childStopCounts(); len(counts) != 1 || counts[0] != 1 {
		t.Fatalf("child stop counts = %v, want [1]", counts)
	}
}

func TestSupervisorAuthCircuitAndConnectionIsolation(t *testing.T) {
	factory := &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}
	var aRemoteCalls atomic.Int32
	health := testHealth{
		remote: func(_ context.Context, spec ConnectionSpec, _ RuntimeView) (HealthResult, error) {
			if spec.ID() == "conn-a" && aRemoteCalls.Add(1) == 1 {
				return HealthResult{Status: HealthAuthFailed}, nil
			}
			return HealthResult{Status: HealthReady}, nil
		},
	}
	s := testSupervisor(t, factory, health, Options{})
	for _, id := range []string{"conn-a", "conn-b"} {
		if err := s.Add(testSpec(t, id, "rev-1", config.TransportLocal, "")); err != nil {
			t.Fatal(err)
		}
		if err := s.Start(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	authFailed := waitForState(t, s, "conn-a", StateAuthFailed)
	ready := waitForState(t, s, "conn-b", StateReady)
	if authFailed.LastError != ErrorAuthFailed {
		t.Fatalf("auth failure snapshot = %+v", authFailed)
	}
	if ready.LastError != ErrorNone {
		t.Fatalf("healthy connection snapshot = %+v", ready)
	}
	if err := s.Start(context.Background(), "conn-a"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("start auth-failed error = %v", err)
	}
	if err := s.Stop(context.Background(), "conn-a"); err != nil {
		t.Fatalf("stop auth-failed connection = %v", err)
	}
	if got, err := s.Snapshot("conn-a"); err != nil || got.State != StateAuthFailed {
		t.Fatalf("stop bypassed auth circuit: snapshot=%+v err=%v", got, err)
	}
	if err := s.Start(context.Background(), "conn-a"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("start after stop auth-failed error = %v", err)
	}
	if err := s.Stop(context.Background(), "conn-b"); err != nil {
		t.Fatal(err)
	}
	if got := waitForState(t, s, "conn-b", StateStopped); got.LastError != ErrorStopped {
		t.Fatalf("stopped B snapshot = %+v", got)
	}
	if got, err := s.Snapshot("conn-a"); err != nil || got.State != StateAuthFailed {
		t.Fatalf("A changed after B stop: snapshot=%+v err=%v", got, err)
	}
	if err := s.Reconnect(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	if got := waitForState(t, s, "conn-a", StateReady); got.LastError != ErrorNone {
		t.Fatalf("reconnected A snapshot = %+v", got)
	}
}

func TestSupervisorBackoffUsesInjectedClockJitterAndCapsAtSixtySeconds(t *testing.T) {
	clock := testClock{now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}
	timers := &testTimers{clock: clock, createdSignal: make(chan struct{}, 8)}
	jitterCalls := make(chan struct {
		attempt int
		delay   time.Duration
	}, 8)
	factory := &testFactory{
		results: map[string][]factoryResult{
			"conn-a": {
				{err: errors.New("transient")},
				{err: errors.New("transient")},
				{err: errors.New("transient")},
			},
		},
		starts: map[string]int{},
	}
	s := testSupervisor(t, factory, testHealth{}, Options{
		Clock:          clock,
		Timers:         timers,
		InitialBackoff: time.Second,
		MaxBackoff:     2 * time.Minute,
		Jitter: func(attempt int, delay time.Duration) time.Duration {
			jitterCalls <- struct {
				attempt int
				delay   time.Duration
			}{attempt, delay}
			return delay
		},
	})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	firstBackoff := waitForState(t, s, "conn-a", StateBackoff)
	timers.waitForCount(t, 1)
	if want := clock.now.Add(time.Second); !firstBackoff.NextRetryAt.Equal(want) {
		t.Fatalf("first retry deadline = %s, want %s", firstBackoff.NextRetryAt, want)
	}
	if got := timers.delaysSnapshot(); len(got) != 1 || got[0] != time.Second {
		t.Fatalf("first retry delays = %v", got)
	}
	first := <-jitterCalls
	if first.attempt != 1 || first.delay != time.Second {
		t.Fatalf("first jitter call = %+v", first)
	}
	if !timers.fireNext() {
		t.Fatal("first retry timer did not fire")
	}
	timers.waitForCount(t, 2)
	if got := timers.delaysSnapshot(); len(got) != 2 || got[1] != 2*time.Second {
		t.Fatalf("second retry delays = %v", got)
	}
	second := <-jitterCalls
	if second.attempt != 2 || second.delay != 2*time.Second {
		t.Fatalf("second jitter call = %+v", second)
	}
	if !timers.fireNext() {
		t.Fatal("second retry timer did not fire")
	}
	timers.waitForCount(t, 3)
	if got := timers.delaysSnapshot(); len(got) != 3 || got[2] != 4*time.Second {
		t.Fatalf("third retry delays = %v", got)
	}
	third := <-jitterCalls
	if third.attempt != 3 || third.delay != 4*time.Second {
		t.Fatalf("third jitter call = %+v", third)
	}
	if got := s.backoff(7); got != time.Minute {
		t.Fatalf("backoff(7) = %s, want 1m", got)
	}
}

func TestSupervisorStopCancelsPendingRetry(t *testing.T) {
	clock := testClock{now: time.Unix(0, 0)}
	timers := &testTimers{clock: clock, createdSignal: make(chan struct{}, 8)}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{err: errors.New("transient")}}},
		starts:  map[string]int{},
	}
	s := testSupervisor(t, factory, testHealth{}, Options{
		Clock:          clock,
		Timers:         timers,
		InitialBackoff: time.Minute,
		MaxBackoff:     time.Minute,
	})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateBackoff)
	timers.waitForCount(t, 1)
	if err := s.Stop(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateStopped)
	if timers.created[0].stopped.Load() != true {
		t.Fatal("retry timer was not stopped")
	}
	if got := factory.startCount("conn-a"); got != 1 {
		t.Fatalf("factory starts after cancelled retry = %d, want 1", got)
	}
	if timers.fireNext() {
		t.Fatal("cancelled retry timer fired")
	}
}

func TestSupervisorStopsEachChildAtMostOnceAcrossRetry(t *testing.T) {
	clock := testClock{now: time.Unix(0, 0)}
	timers := &testTimers{clock: clock, createdSignal: make(chan struct{}, 8)}
	factory := &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}
	var remoteCalls atomic.Int32
	health := testHealth{
		remote: func(_ context.Context, _ ConnectionSpec, _ RuntimeView) (HealthResult, error) {
			if remoteCalls.Add(1) == 1 {
				return HealthResult{Status: HealthNotReady}, nil
			}
			return HealthResult{Status: HealthReady}, nil
		},
	}
	s := testSupervisor(t, factory, health, Options{
		Clock:          clock,
		Timers:         timers,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
	})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateBackoff)
	timers.waitForCount(t, 1)
	if !timers.fireNext() {
		t.Fatal("retry timer did not fire")
	}
	waitForState(t, s, "conn-a", StateReady)
	if err := s.Stop(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateStopped)
	if counts := factory.childStopCounts(); len(counts) != 2 || counts[0] != 1 || counts[1] != 1 {
		t.Fatalf("child stop counts = %v, want [1 1]", counts)
	}
}

func TestSupervisorCleanupFailureBlocksRetryUntilReconnectCleanupSucceeds(t *testing.T) {
	clock := testClock{now: time.Unix(0, 0)}
	timers := &testTimers{clock: clock, createdSignal: make(chan struct{}, 8)}
	var stopCalls atomic.Int32
	child := &testChild{stopFn: func(context.Context) error {
		if stopCalls.Add(1) == 1 {
			return errors.New("cleanup failed")
		}
		return nil
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	var remoteCalls atomic.Int32
	health := testHealth{remote: func(_ context.Context, _ ConnectionSpec, _ RuntimeView) (HealthResult, error) {
		if remoteCalls.Add(1) == 1 {
			return HealthResult{Status: HealthNotReady}, nil
		}
		return HealthResult{Status: HealthReady}, nil
	}}
	s := testSupervisor(t, factory, health, Options{
		Clock:          clock,
		Timers:         timers,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
	})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	failed := waitForState(t, s, "conn-a", StateCleanupFailed)
	if failed.LastError != ErrorCleanupFailed || failed.State == StateStopped {
		t.Fatalf("cleanup failure snapshot = %+v", failed)
	}
	if got := factory.startCount("conn-a"); got != 1 {
		t.Fatalf("factory starts after cleanup failure = %d, want 1", got)
	}
	if timersCreated := timers.delaysSnapshot(); len(timersCreated) != 0 {
		t.Fatalf("retry timers after cleanup failure = %v, want none", timersCreated)
	}
	if err := s.Start(context.Background(), "conn-a"); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("start after cleanup failure = %v", err)
	}
	if err := s.Reconnect(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	if got := factory.startCount("conn-a"); got != 2 {
		t.Fatalf("factory starts after reconnect = %d, want 2", got)
	}
	if got := child.stopCount.Load(); got != 2 {
		t.Fatalf("cleanup attempts = %d, want 2", got)
	}
}

func TestSupervisorStopReportsCleanupFailureAndDoesNotReportStopped(t *testing.T) {
	child := &testChild{stopFn: func(context.Context) error {
		return errors.New("cleanup failed")
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	s := testSupervisor(t, factory, testHealth{}, Options{})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	if err := s.Stop(context.Background(), "conn-a"); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("stop cleanup error = %v", err)
	}
	failed := waitForState(t, s, "conn-a", StateCleanupFailed)
	if failed.State == StateStopped || failed.LastError != ErrorCleanupFailed {
		t.Fatalf("stop cleanup snapshot = %+v", failed)
	}
	if err := s.Start(context.Background(), "conn-a"); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("start after stop cleanup failure = %v", err)
	}
}

func TestSupervisorCleanupTimeoutIsTerminalUntilCleanupSucceeds(t *testing.T) {
	child := &testChild{stopFn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	health := testHealth{remote: func(_ context.Context, _ ConnectionSpec, _ RuntimeView) (HealthResult, error) {
		return HealthResult{Status: HealthNotReady}, nil
	}}
	s := testSupervisor(t, factory, health, Options{CleanupTimeout: time.Millisecond})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	failed := waitForState(t, s, "conn-a", StateCleanupFailed)
	if failed.State == StateStopped || failed.LastError != ErrorCleanupFailed {
		t.Fatalf("timeout cleanup snapshot = %+v", failed)
	}
	if err := s.Start(context.Background(), "conn-a"); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("start after timeout cleanup failure = %v", err)
	}
}

func TestSupervisorCloseAndRemoveRaceDoesNotLeakChildren(t *testing.T) {
	factory := &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}
	s := testSupervisor(t, factory, testHealth{}, Options{})
	for _, id := range []string{"conn-a", "conn-b"} {
		if err := s.Add(testSpec(t, id, "rev-1", config.TransportLocal, "")); err != nil {
			t.Fatal(err)
		}
		if err := s.Start(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		waitForState(t, s, id, StateReady)
	}
	start := make(chan struct{})
	closeDone := make(chan error, 1)
	removeDone := make(chan error, 1)
	go func() {
		<-start
		closeDone <- s.Close(context.Background())
	}()
	go func() {
		<-start
		removeDone <- s.Remove(context.Background(), "conn-a")
	}()
	close(start)
	if err := <-closeDone; err != nil {
		t.Fatalf("close during remove = %v", err)
	}
	if err := <-removeDone; err != nil && !errors.Is(err, ErrConnectionMissing) {
		t.Fatalf("remove during close = %v", err)
	}
	if _, err := s.Snapshot("conn-a"); !errors.Is(err, ErrConnectionMissing) {
		t.Fatalf("removed A snapshot error = %v", err)
	}
	if got, err := s.Snapshot("conn-b"); err != nil || got.State != StateStopped {
		t.Fatalf("closed B snapshot = %+v err=%v", got, err)
	}
	counts := factory.childStopCounts()
	if len(counts) != 2 || counts[0] != 1 || counts[1] != 1 {
		t.Fatalf("close/remove child stop counts = %v, want [1 1]", counts)
	}
}

func TestSupervisorConcurrentCloseWaitsForFirstClose(t *testing.T) {
	stopEntered := make(chan struct{}, 1)
	releaseStop := make(chan struct{})
	child := &testChild{stopFn: func(ctx context.Context) error {
		stopEntered <- struct{}{}
		select {
		case <-releaseStop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	s := testSupervisor(t, factory, testHealth{}, Options{CleanupTimeout: time.Second})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Close(context.Background()) }()
	<-stopEntered
	if got, err := s.Snapshot("conn-a"); err != nil || got.State != StateStopping {
		t.Fatalf("state during close cleanup = %+v err=%v, want stopping", got, err)
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- s.Close(context.Background()) }()
	select {
	case err := <-secondDone:
		t.Fatalf("second close returned before first cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseStop)
	if err := <-firstDone; err != nil {
		t.Fatalf("first close = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second close = %v", err)
	}
}

func TestSupervisorCloseCallerTimeoutDoesNotFinalizeEarly(t *testing.T) {
	stopEntered := make(chan struct{}, 1)
	releaseStop := make(chan struct{})
	child := &testChild{stopFn: func(ctx context.Context) error {
		stopEntered <- struct{}{}
		select {
		case <-releaseStop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	s := testSupervisor(t, factory, testHealth{}, Options{CleanupTimeout: time.Second})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out close = %v, want context deadline", err)
	}
	<-stopEntered
	secondDone := make(chan error, 1)
	go func() { secondDone <- s.Close(context.Background()) }()
	select {
	case err := <-secondDone:
		t.Fatalf("second close returned before child cleanup: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseStop)
	if err := <-secondDone; err != nil {
		t.Fatalf("second close = %v", err)
	}
	if got, err := s.Snapshot("conn-a"); err != nil || got.State != StateStopped {
		t.Fatalf("snapshot after completed close = %+v err=%v", got, err)
	}
}

func TestSupervisorCloseContinuesAfterCleanupFailure(t *testing.T) {
	badChild := &testChild{stopFn: func(context.Context) error {
		return errors.New("cleanup failed")
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{
			"conn-a": {{child: badChild}},
			"conn-b": {},
		},
		starts: map[string]int{},
	}
	s := testSupervisor(t, factory, testHealth{}, Options{})
	for _, id := range []string{"conn-a", "conn-b"} {
		if err := s.Add(testSpec(t, id, "rev-1", config.TransportLocal, "")); err != nil {
			t.Fatal(err)
		}
		if err := s.Start(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		waitForState(t, s, id, StateReady)
	}
	if err := s.Close(context.Background()); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("close cleanup error = %v", err)
	}
	if got, err := s.Snapshot("conn-a"); err != nil || got.State != StateCleanupFailed {
		t.Fatalf("A after close cleanup failure = %+v err=%v", got, err)
	}
	if got, err := s.Snapshot("conn-b"); err != nil || got.State != StateStopped {
		t.Fatalf("B after close cleanup failure = %+v err=%v", got, err)
	}
}

func TestSupervisorWaitForStateReturnsMissingAfterRemove(t *testing.T) {
	factory := &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}
	s := testSupervisor(t, factory, testHealth{}, Options{})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := s.WaitForState(ctx, "conn-a", StateReady)
		result <- err
	}()
	if err := s.Remove(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrConnectionMissing) {
		t.Fatalf("wait after remove error = %v", err)
	}
}

func TestSupervisorHealthTimeoutDefaultsToFiniteDeadline(t *testing.T) {
	ctx, cancel := healthContext(context.Background(), 0)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("default health context has no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > defaultHealthTimeout {
		t.Fatalf("default health deadline remaining = %s, want <= %s", remaining, defaultHealthTimeout)
	}
}

func TestSupervisorReplaceInvalidatesOldRevision(t *testing.T) {
	factory := &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}
	v1Started := make(chan struct{}, 1)
	health := testHealth{
		local: func(ctx context.Context, spec ConnectionSpec, _ RuntimeView) (HealthResult, error) {
			if spec.Revision() == "rev-1" {
				v1Started <- struct{}{}
				<-ctx.Done()
				return HealthResult{}, ctx.Err()
			}
			return HealthResult{Status: HealthReady}, nil
		},
	}
	s := testSupervisor(t, factory, health, Options{})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	<-v1Started
	if err := s.Replace(context.Background(), testSpec(t, "conn-a", "rev-2", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	replaced, err := s.Snapshot("conn-a")
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Revision != "rev-2" || replaced.State != StateStopped {
		t.Fatalf("replacement snapshot = %+v", replaced)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	ready := waitForState(t, s, "conn-a", StateReady)
	if ready.Revision != "rev-2" || ready.LastError != ErrorNone {
		t.Fatalf("new revision snapshot = %+v", ready)
	}
	for _, spec := range factory.specsSnapshot() {
		if spec.Revision() != "rev-1" && spec.Revision() != "rev-2" {
			t.Fatalf("unexpected factory revision %q", spec.Revision())
		}
	}
}

func TestSupervisorSleepWakeAndReconnectLifecycle(t *testing.T) {
	factory := &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}
	s := testSupervisor(t, factory, testHealth{}, Options{})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	if err := s.Sleep(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	if got := waitForState(t, s, "conn-a", StateSleeping); got.LastError != ErrorSleeping {
		t.Fatalf("sleep snapshot = %+v", got)
	}
	if err := s.Start(context.Background(), "conn-a"); !errors.Is(err, ErrSleeping) {
		t.Fatalf("start while sleeping error = %v", err)
	}
	if err := s.Wake(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	if got := factory.startCount("conn-a"); got != 2 {
		t.Fatalf("factory starts after wake = %d, want 2", got)
	}
	if err := s.Reconnect(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	if got := factory.startCount("conn-a"); got != 3 {
		t.Fatalf("factory starts after reconnect = %d, want 3", got)
	}
}

func TestLifecycleStateUsesStableLocalMCPReadyValue(t *testing.T) {
	if StateLocalReady != State("local_mcp_ready") {
		t.Fatalf("StateLocalReady = %q", StateLocalReady)
	}
}

func TestSupervisorRejectsDuplicateLocalPorts(t *testing.T) {
	factory := &testFactory{results: make(map[string][]factoryResult), starts: make(map[string]int)}
	s, err := New(factory, testHealth{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(testSpecAtPort(t, "conn-a", "rev-1", 18788)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(testSpecAtPort(t, "conn-b", "rev-1", 18788)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("duplicate port add = %v, want ErrInvalidSpec", err)
	}
	if err := s.Add(testSpecAtPort(t, "conn-b", "rev-1", 18789)); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(context.Background(), testSpecAtPort(t, "conn-b", "rev-2", 18788)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("duplicate port replace = %v, want ErrInvalidSpec", err)
	}
	got, err := s.Snapshot("conn-b")
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != "rev-1" || got.LocalPort != 18789 {
		t.Fatalf("rejected replace mutated spec: %+v", got)
	}
}

func TestNilTimerRemainsDegraded(t *testing.T) {
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{err: errors.New("start")}}},
		starts:  make(map[string]int),
	}
	s, err := New(factory, testHealth{}, Options{Timers: nilTimers{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	got := waitForState(t, s, "conn-a", StateDegraded)
	if got.LastError != ErrorRuntime {
		t.Fatalf("nil timer snapshot = %+v", got)
	}
}

func TestCloseIsBoundedWhenHealthCheckerIgnoresContext(t *testing.T) {
	factory := &testFactory{results: make(map[string][]factoryResult), starts: make(map[string]int)}
	entered := make(chan struct{})
	release := make(chan struct{})
	health := testHealth{local: func(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error) {
		close(entered)
		<-release
		return HealthResult{Status: HealthNotReady}, nil
	}}
	s, err := New(factory, health, Options{CleanupTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("health checker was not called")
	}
	started := time.Now()
	if err := s.Close(context.Background()); !errors.Is(err, ErrCleanupFailed) {
		close(release)
		t.Fatalf("Close = %v, want ErrCleanupFailed", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		close(release)
		t.Fatalf("Close was not bounded: %v", elapsed)
	}
	close(release)
}

func TestExpiredHealthContextCannotPublishReady(t *testing.T) {
	factory := &testFactory{results: make(map[string][]factoryResult), starts: make(map[string]int)}
	health := testHealth{local: func(ctx context.Context, _ ConnectionSpec, _ RuntimeView) (HealthResult, error) {
		<-ctx.Done()
		return HealthResult{Status: HealthReady}, nil
	}}
	s, err := New(factory, health, Options{InitialBackoff: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := NewHealthMetadata("health", time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewLocalConnectionSpec("conn-a", "rev-1", 18788, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(spec); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	got := waitForState(t, s, "conn-a", StateBackoff)
	if got.LastError != ErrorHealth {
		t.Fatalf("expired health snapshot = %+v", got)
	}
}

func TestCloseDoesNotBlockBehindEntryOperationMutex(t *testing.T) {
	factory := &testFactory{results: make(map[string][]factoryResult), starts: make(map[string]int)}
	entered := make(chan struct{})
	release := make(chan struct{})
	health := testHealth{local: func(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error) {
		close(entered)
		<-release
		return HealthResult{Status: HealthNotReady}, nil
	}}
	s, err := New(factory, health, Options{CleanupTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	<-entered
	stopDone := make(chan error, 1)
	go func() { stopDone <- s.Stop(context.Background(), "conn-a") }()
	waitForState(t, s, "conn-a", StateStopping)
	started := time.Now()
	if err := s.Close(context.Background()); !errors.Is(err, ErrCleanupFailed) {
		close(release)
		t.Fatalf("Close = %v, want ErrCleanupFailed", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		close(release)
		t.Fatalf("Close waited behind operation mutex: %v", elapsed)
	}
	close(release)
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after health release")
	}
}

// panicFactory/ panicHealth/ panicChild intentionally model a broken injected
// implementation. The supervisor boundary must turn each panic into its
// stable diagnostic category instead of allowing it to escape a worker.
type panicFactory struct{}

func (panicFactory) Start(context.Context, ConnectionSpec) (Child, error) {
	panic("factory panic must not escape")
}

type panicHealth struct{}

func (panicHealth) CheckLocal(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error) {
	panic("health panic must not escape")
}

func (panicHealth) CheckRemote(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error) {
	panic("health panic must not escape")
}

type panicChild struct{}

func (panicChild) Stop(context.Context) error {
	panic("child panic must not escape")
}

type panicChildFactory struct {
	child Child
}

func (f panicChildFactory) Start(context.Context, ConnectionSpec) (Child, error) {
	return f.child, nil
}

type panicClock struct{}

func (panicClock) Now() time.Time { panic("clock panic must not escape") }

type panicTimerFactory struct {
	timer Timer
}

func (f panicTimerFactory) NewTimer(time.Duration) Timer { return f.timer }

type panicNewTimerFactory struct{}

func (panicNewTimerFactory) NewTimer(time.Duration) Timer {
	panic("timer factory panic must not escape")
}

type panicTimer struct {
	channel   <-chan time.Time
	panicC    bool
	panicStop bool
}

func (t panicTimer) C() <-chan time.Time {
	if t.panicC {
		panic("timer C panic must not escape")
	}
	return t.channel
}

func (t panicTimer) Stop() bool {
	if t.panicStop {
		panic("timer Stop panic must not escape")
	}
	return true
}

func TestSupervisorPublicOperationsHonorContextWhileEntryLockIsHeld(t *testing.T) {
	operations := []struct {
		name string
		call func(*Supervisor, context.Context, ConnectionSpec) error
	}{
		{name: "start", call: func(s *Supervisor, ctx context.Context, _ ConnectionSpec) error {
			return s.Start(ctx, "conn-a")
		}},
		{name: "stop", call: func(s *Supervisor, ctx context.Context, _ ConnectionSpec) error {
			return s.Stop(ctx, "conn-a")
		}},
		{name: "reconnect", call: func(s *Supervisor, ctx context.Context, _ ConnectionSpec) error {
			return s.Reconnect(ctx, "conn-a")
		}},
		{name: "sleep", call: func(s *Supervisor, ctx context.Context, _ ConnectionSpec) error {
			return s.Sleep(ctx, "conn-a")
		}},
		{name: "wake", call: func(s *Supervisor, ctx context.Context, _ ConnectionSpec) error {
			return s.Wake(ctx, "conn-a")
		}},
		{name: "remove", call: func(s *Supervisor, ctx context.Context, _ ConnectionSpec) error {
			return s.Remove(ctx, "conn-a")
		}},
		{name: "replace", call: func(s *Supervisor, ctx context.Context, spec ConnectionSpec) error {
			return s.Replace(ctx, spec)
		}},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			s := testSupervisor(t, &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}, testHealth{}, Options{})
			spec := testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")
			if err := s.Add(spec); err != nil {
				t.Fatal(err)
			}
			e, err := s.lookup("conn-a")
			if err != nil {
				t.Fatal(err)
			}
			e.opMu.Lock()
			defer e.opMu.Unlock()

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- operation.call(s, ctx, spec) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("operation error = %v, want context deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation did not honor context while entry lock was held")
			}
		})
	}
}

func TestSupervisorRecoversRuntimeFactoryPanicAsRuntimeError(t *testing.T) {
	s := testSupervisor(t, panicFactory{}, testHealth{}, Options{InitialBackoff: time.Hour})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	got := waitForState(t, s, "conn-a", StateBackoff)
	if got.LastError != ErrorRuntime || got.State == StateReady || got.State == StateStopped {
		t.Fatalf("factory panic snapshot = %+v", got)
	}
}

func TestSupervisorRecoversHealthCheckerPanicAsHealthError(t *testing.T) {
	s := testSupervisor(t, &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}, panicHealth{}, Options{InitialBackoff: time.Hour})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	got := waitForState(t, s, "conn-a", StateBackoff)
	if got.LastError != ErrorHealth || got.State == StateReady || got.State == StateStopped {
		t.Fatalf("health panic snapshot = %+v", got)
	}
}

func TestSupervisorRecoversChildStopPanicAsCleanupFailure(t *testing.T) {
	s := testSupervisor(t, panicChildFactory{child: panicChild{}}, testHealth{
		local: func(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error) {
			return HealthResult{Status: HealthNotReady}, nil
		},
	}, Options{})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	got := waitForState(t, s, "conn-a", StateCleanupFailed)
	if got.LastError != ErrorCleanupFailed || got.State == StateReady || got.State == StateStopped {
		t.Fatalf("child stop panic snapshot = %+v", got)
	}
}

func TestSupervisorStartPrefersAuthCircuitDuringCleanupWindow(t *testing.T) {
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStop) }) }
	child := &testChild{stopFn: func(ctx context.Context) error {
		close(stopEntered)
		select {
		case <-releaseStop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	health := testHealth{remote: func(_ context.Context, _ ConnectionSpec, _ RuntimeView) (HealthResult, error) {
		return HealthResult{Status: HealthAuthFailed}, nil
	}}
	s := testSupervisor(t, factory, health, Options{CleanupTimeout: time.Second})
	defer release()
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopEntered:
	case <-time.After(time.Second):
		t.Fatal("auth cleanup did not enter child stop")
	}
	if got, err := s.Snapshot("conn-a"); err != nil || got.State != StateStopping {
		t.Fatalf("auth cleanup window snapshot = %+v err=%v, want stopping", got, err)
	}
	if err := s.Start(context.Background(), "conn-a"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("start during auth cleanup = %v, want ErrCircuitOpen", err)
	}
	if got := factory.startCount("conn-a"); got != 1 {
		t.Fatalf("factory starts during auth cleanup = %d, want 1", got)
	}
	release()
	if got := waitForState(t, s, "conn-a", StateAuthFailed); got.LastError != ErrorAuthFailed {
		t.Fatalf("auth cleanup completion snapshot = %+v", got)
	}
}

func TestSupervisorStartDoesNotMaskAuthCleanupFailure(t *testing.T) {
	child := &testChild{stopFn: func(context.Context) error {
		return errors.New("cleanup failure")
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	health := testHealth{remote: func(_ context.Context, _ ConnectionSpec, _ RuntimeView) (HealthResult, error) {
		return HealthResult{Status: HealthAuthFailed}, nil
	}}
	s := testSupervisor(t, factory, health, Options{})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	if got := waitForState(t, s, "conn-a", StateCleanupFailed); got.LastError != ErrorCleanupFailed {
		t.Fatalf("auth cleanup failure snapshot = %+v", got)
	}
	if err := s.Start(context.Background(), "conn-a"); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("start after auth cleanup failure = %v, want ErrCleanupFailed", err)
	}
	if got := factory.startCount("conn-a"); got != 1 {
		t.Fatalf("factory starts after auth cleanup failure = %d, want 1", got)
	}
}

func TestSupervisorReplaceReservesPortBeforeStoppingOldChild(t *testing.T) {
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStop) }) }
	child := &testChild{stopFn: func(ctx context.Context) error {
		close(stopEntered)
		select {
		case <-releaseStop:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	s := testSupervisor(t, factory, testHealth{}, Options{CleanupTimeout: time.Second})
	defer release()
	if err := s.Add(testSpecAtPort(t, "conn-a", "rev-1", 21001)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(testSpecAtPort(t, "conn-b", "rev-1", 21002)); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)

	updatedA := testSpecAtPort(t, "conn-a", "rev-2", 21003)
	firstDone := make(chan error, 1)
	go func() { firstDone <- s.Replace(context.Background(), updatedA) }()
	select {
	case <-stopEntered:
	case <-time.After(time.Second):
		t.Fatal("first replace did not enter old child cleanup")
	}
	if err := s.Replace(context.Background(), testSpecAtPort(t, "conn-b", "rev-2", 21003)); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("second replace reserved-port error = %v, want ErrInvalidSpec", err)
	}
	release()
	if err := <-firstDone; err != nil {
		t.Fatalf("first replace = %v", err)
	}
	if got, err := s.Snapshot("conn-a"); err != nil || got.LocalPort != 21003 || got.Revision != "rev-2" {
		t.Fatalf("first replace snapshot = %+v err=%v", got, err)
	}
	if got, err := s.Snapshot("conn-b"); err != nil || got.LocalPort != 21002 || got.Revision != "rev-1" {
		t.Fatalf("rejected second replace snapshot = %+v err=%v", got, err)
	}
}

func TestRuntimeViewDoesNotExposeOwnedChildMethods(t *testing.T) {
	var hasStop atomic.Bool
	health := testHealth{
		local: func(_ context.Context, _ ConnectionSpec, view RuntimeView) (HealthResult, error) {
			if _, ok := view.(interface{ Stop(context.Context) error }); ok {
				hasStop.Store(true)
			}
			return HealthResult{Status: HealthReady}, nil
		},
		remote: func(_ context.Context, _ ConnectionSpec, view RuntimeView) (HealthResult, error) {
			if _, ok := view.(interface{ Stop(context.Context) error }); ok {
				hasStop.Store(true)
			}
			return HealthResult{Status: HealthReady}, nil
		},
	}
	s := testSupervisor(t, &testFactory{results: map[string][]factoryResult{}, starts: map[string]int{}}, health, Options{})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	if hasStop.Load() {
		t.Fatal("health checker received a RuntimeView exposing Child.Stop")
	}
}

func TestSupervisorCleanupRetryHonorsCallerDeadline(t *testing.T) {
	var stopCalls atomic.Int32
	child := &testChild{stopFn: func(ctx context.Context) error {
		switch stopCalls.Add(1) {
		case 1:
			return errors.New("first cleanup failure")
		case 2:
			<-ctx.Done()
			return ctx.Err()
		default:
			return nil
		}
	}}
	factory := &testFactory{
		results: map[string][]factoryResult{"conn-a": {{child: child}}},
		starts:  map[string]int{},
	}
	s := testSupervisor(t, factory, testHealth{}, Options{CleanupTimeout: time.Second})
	if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), "conn-a"); err != nil {
		t.Fatal(err)
	}
	waitForState(t, s, "conn-a", StateReady)
	if err := s.Stop(context.Background(), "conn-a"); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("first stop = %v, want ErrCleanupFailed", err)
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := s.Stop(ctx, "conn-a")
	cancel()
	if !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("cleanup retry = %v, want ErrCleanupFailed", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cleanup retry ignored caller deadline: %s", elapsed)
	}
	if got := stopCalls.Load(); got != 2 {
		t.Fatalf("cleanup calls after deadline = %d, want 2", got)
	}
}

func TestSupervisorInjectedTimingPanicsBecomeRuntimeErrors(t *testing.T) {
	closed := make(chan time.Time)
	close(closed)
	cases := []struct {
		name    string
		options Options
	}{
		{name: "clock", options: Options{Clock: panicClock{}}},
		{name: "jitter", options: Options{Jitter: func(int, time.Duration) time.Duration {
			panic("jitter panic must not escape")
		}}},
		{name: "timer factory", options: Options{Timers: panicNewTimerFactory{}}},
		{name: "timer C", options: Options{Timers: panicTimerFactory{timer: panicTimer{panicC: true}}}},
		{name: "timer Stop", options: Options{Timers: panicTimerFactory{timer: panicTimer{channel: closed, panicStop: true}}}},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.options.InitialBackoff = time.Second
			test.options.MaxBackoff = time.Second
			s := testSupervisor(t, &testFactory{
				results: map[string][]factoryResult{"conn-a": {{err: errors.New("transient")}}},
				starts:  map[string]int{},
			}, testHealth{}, test.options)
			if err := s.Add(testSpec(t, "conn-a", "rev-1", config.TransportLocal, "")); err != nil {
				t.Fatal(err)
			}
			if err := s.Start(context.Background(), "conn-a"); err != nil {
				t.Fatal(err)
			}
			got := waitForState(t, s, "conn-a", StateDegraded)
			if got.LastError != ErrorRuntime || got.State == StateReady || got.State == StateStopped {
				t.Fatalf("timing panic snapshot = %+v", got)
			}
		})
	}
}
