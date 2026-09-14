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

const integrationWait = 2 * time.Second

// integrationBaseFactory is a process-free RuntimeFactory. It deliberately
// exposes only lifecycle events and stop counts to the test; it has no real
// process, command, credential, network, or filesystem behavior.
type integrationBaseFactory struct {
	mu         sync.Mutex
	children   map[string][]*integrationBaseChild
	authFailed map[string]bool
}

func newIntegrationBaseFactory() *integrationBaseFactory {
	return &integrationBaseFactory{
		children:   make(map[string][]*integrationBaseChild),
		authFailed: make(map[string]bool),
	}
}

func (f *integrationBaseFactory) Start(ctx context.Context, spec supervisor.ConnectionSpec) (supervisor.Child, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	child := &integrationBaseChild{}
	f.mu.Lock()
	f.children[spec.ID()] = append(f.children[spec.ID()], child)
	authFailed := f.authFailed[spec.ID()]
	f.mu.Unlock()
	if authFailed {
		return child, supervisor.ErrAuthFailed
	}
	return child, nil
}

func (f *integrationBaseFactory) childStopCount(id string) int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var count int32
	for _, child := range f.children[id] {
		count += child.stops.Load()
	}
	return count
}

type integrationBaseChild struct {
	stops atomic.Int32
}

func (c *integrationBaseChild) Stop(ctx context.Context) error {
	c.stops.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

type integrationHealth struct {
	local  func(context.Context, supervisor.ConnectionSpec) (supervisor.HealthResult, error)
	remote func(context.Context, supervisor.ConnectionSpec) (supervisor.HealthResult, error)
}

func (h integrationHealth) CheckLocal(ctx context.Context, spec supervisor.ConnectionSpec, _ supervisor.RuntimeView) (supervisor.HealthResult, error) {
	if h.local == nil {
		return supervisor.HealthResult{Status: supervisor.HealthReady}, nil
	}
	return h.local(ctx, spec)
}

func (h integrationHealth) CheckRemote(ctx context.Context, spec supervisor.ConnectionSpec, _ supervisor.RuntimeView) (supervisor.HealthResult, error) {
	if h.remote == nil {
		return supervisor.HealthResult{Status: supervisor.HealthReady}, nil
	}
	return h.remote(ctx, spec)
}

type integrationBlockingHealth struct {
	localEntered  chan struct{}
	localRelease  <-chan struct{}
	remoteEntered chan struct{}
	remoteRelease <-chan struct{}
}

func (h integrationBlockingHealth) CheckLocal(ctx context.Context, _ supervisor.ConnectionSpec, _ supervisor.RuntimeView) (supervisor.HealthResult, error) {
	notifyIntegration(h.localEntered)
	select {
	case <-h.localRelease:
		return supervisor.HealthResult{Status: supervisor.HealthReady}, nil
	case <-ctx.Done():
		return supervisor.HealthResult{}, ctx.Err()
	}
}

func (h integrationBlockingHealth) CheckRemote(ctx context.Context, _ supervisor.ConnectionSpec, _ supervisor.RuntimeView) (supervisor.HealthResult, error) {
	notifyIntegration(h.remoteEntered)
	select {
	case <-h.remoteRelease:
		return supervisor.HealthResult{Status: supervisor.HealthReady}, nil
	case <-ctx.Done():
		return supervisor.HealthResult{}, ctx.Err()
	}
}

func notifyIntegration(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func integrationHealthMetadata(t *testing.T) supervisor.HealthMetadata {
	t.Helper()
	metadata, err := supervisor.NewHealthMetadata("integration", time.Second, 0)
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func integrationSpec(t *testing.T, id, revision string, port uint16) supervisor.ConnectionSpec {
	t.Helper()
	spec, err := supervisor.NewConnectionSpec(id, revision, config.TransportLocal, "", "", port, integrationHealthMetadata(t))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func integrationConnection(t *testing.T, spec supervisor.ConnectionSpec) admission.Connection {
	t.Helper()
	connection, err := admission.NewLifecycleConnection(spec.ID(), "profile-"+spec.ID(), spec.Revision(), true)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func newIntegrationStack(t *testing.T, health supervisor.HealthChecker, base *integrationBaseFactory, specs ...supervisor.ConnectionSpec) (*admission.Gate, *supervisor.Supervisor) {
	t.Helper()
	audit := admission.NewAuditHealthState(true)
	gate, err := admission.New(admission.Limits{MaxGlobal: 8, MaxPerConnection: 2}, audit)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		if err := gate.AddConnection(integrationConnection(t, spec)); err != nil {
			t.Fatal(err)
		}
	}
	factory, err := New(gate, base)
	if err != nil {
		t.Fatal(err)
	}
	s, err := supervisor.New(factory, health, supervisor.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		if err := s.Add(spec); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), integrationWait)
		defer cancel()
		_ = s.Close(ctx)
		_ = gate.Close()
	})
	return gate, s
}

func waitIntegrationState(t *testing.T, s *supervisor.Supervisor, id string, want supervisor.State) supervisor.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationWait)
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
			t.Fatal(err)
		}
		if snapshot.State == want {
			return snapshot
		}
	}
}

func waitIntegrationPermitDone(t *testing.T, permit *admission.Permit) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), integrationWait)
	defer cancel()
	select {
	case <-permit.Done():
	case <-ctx.Done():
		t.Fatal("permit was not canceled")
	}
}

func requireIntegrationBindingError(t *testing.T, gate *admission.Gate, id string, want error) {
	t.Helper()
	if _, err := gate.CurrentLifecycleBinding(id); !errors.Is(err, want) {
		t.Fatalf("CurrentLifecycleBinding(%q) = %v, want %v", id, err, want)
	}
}

func TestSupervisorLifecycleAdapterBlocksBindingUntilBothHealthChecksPass(t *testing.T) {
	localEntered := make(chan struct{}, 1)
	localRelease := make(chan struct{})
	remoteEntered := make(chan struct{}, 1)
	remoteRelease := make(chan struct{})
	health := integrationBlockingHealth{
		localEntered: localEntered, localRelease: localRelease,
		remoteEntered: remoteEntered, remoteRelease: remoteRelease,
	}
	spec := integrationSpec(t, "integration-health", "revision-a", 18788)
	gate, s := newIntegrationStack(t, health, newIntegrationBaseFactory(), spec)
	if err := s.Start(context.Background(), spec.ID()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), integrationWait)
	defer cancel()
	select {
	case <-localEntered:
	case <-ctx.Done():
		t.Fatal("local health check did not start")
	}
	requireIntegrationBindingError(t, gate, spec.ID(), admission.ErrLifecycleUnavailable)

	close(localRelease)
	select {
	case <-remoteEntered:
	case <-ctx.Done():
		t.Fatal("remote health check did not start")
	}
	requireIntegrationBindingError(t, gate, spec.ID(), admission.ErrLifecycleUnavailable)

	close(remoteRelease)
	waitIntegrationState(t, s, spec.ID(), supervisor.StateReady)
	binding, err := gate.CurrentLifecycleBinding(spec.ID())
	if err != nil {
		t.Fatal(err)
	}
	permit, err := gate.Acquire(context.Background(), binding)
	if err != nil {
		t.Fatalf("ready binding acquire: %v", err)
	}
	permit.Release()
}

func TestSupervisorStopCancelsPermitAndInvalidatesOldBinding(t *testing.T) {
	spec := integrationSpec(t, "integration-stop", "revision-a", 18789)
	gate, s := newIntegrationStack(t, integrationHealth{}, newIntegrationBaseFactory(), spec)
	if err := s.Start(context.Background(), spec.ID()); err != nil {
		t.Fatal(err)
	}
	waitIntegrationState(t, s, spec.ID(), supervisor.StateReady)
	binding, err := gate.CurrentLifecycleBinding(spec.ID())
	if err != nil {
		t.Fatal(err)
	}
	permit, err := gate.Acquire(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Release()

	if err := s.Stop(context.Background(), spec.ID()); err != nil {
		t.Fatal(err)
	}
	waitIntegrationPermitDone(t, permit)
	if _, err := gate.TryAcquire(binding); !errors.Is(err, admission.ErrLifecycleMismatch) {
		t.Fatalf("old binding acquire after Stop = %v, want lifecycle mismatch", err)
	}
	requireIntegrationBindingError(t, gate, spec.ID(), admission.ErrLifecycleUnavailable)
	if got := waitIntegrationState(t, s, spec.ID(), supervisor.StateStopped); got.State != supervisor.StateStopped {
		t.Fatalf("post-stop snapshot = %#v", got)
	}
}

func TestSupervisorAndGateReplacementFailClosedAcrossCoordinationGap(t *testing.T) {
	oldSpec := integrationSpec(t, "integration-replace", "revision-a", 18790)
	newSpec := integrationSpec(t, "integration-replace", "revision-b", 18790)
	base := newIntegrationBaseFactory()
	gate, s := newIntegrationStack(t, integrationHealth{}, base, oldSpec)
	if err := s.Start(context.Background(), oldSpec.ID()); err != nil {
		t.Fatal(err)
	}
	waitIntegrationState(t, s, oldSpec.ID(), supervisor.StateReady)
	oldBinding, err := gate.CurrentLifecycleBinding(oldSpec.ID())
	if err != nil {
		t.Fatal(err)
	}
	oldPermit, err := gate.Acquire(context.Background(), oldBinding)
	if err != nil {
		t.Fatal(err)
	}
	defer oldPermit.Release()

	// The manager closes the admission epoch first. Supervisor.Replace is a
	// separate operation; the gap must reject old callers rather than expose a
	// partially replaced runtime.
	if err := gate.ReplaceConnection(integrationConnection(t, newSpec)); err != nil {
		t.Fatal(err)
	}
	waitIntegrationPermitDone(t, oldPermit)
	if _, err := gate.TryAcquire(oldBinding); !errors.Is(err, admission.ErrBindingMismatch) {
		t.Fatalf("old binding during replacement gap = %v, want binding mismatch", err)
	}
	requireIntegrationBindingError(t, gate, oldSpec.ID(), admission.ErrLifecycleUnavailable)

	if err := s.Replace(context.Background(), newSpec); err != nil {
		t.Fatal(err)
	}
	requireIntegrationBindingError(t, gate, newSpec.ID(), admission.ErrLifecycleUnavailable)
	if err := s.Start(context.Background(), newSpec.ID()); err != nil {
		t.Fatal(err)
	}
	waitIntegrationState(t, s, newSpec.ID(), supervisor.StateReady)
	newBinding, err := gate.CurrentLifecycleBinding(newSpec.ID())
	if err != nil {
		t.Fatal(err)
	}
	newPermit, err := gate.Acquire(context.Background(), newBinding)
	if err != nil {
		t.Fatalf("new binding acquire: %v", err)
	}
	newPermit.Release()
}

func TestBaseAuthFailureEndsAuthFailedWithoutBinding(t *testing.T) {
	spec := integrationSpec(t, "integration-auth", "revision-a", 18791)
	base := newIntegrationBaseFactory()
	base.authFailed[spec.ID()] = true
	gate, s := newIntegrationStack(t, integrationHealth{}, base, spec)
	if err := s.Start(context.Background(), spec.ID()); err != nil {
		t.Fatal(err)
	}
	waitIntegrationState(t, s, spec.ID(), supervisor.StateAuthFailed)
	requireIntegrationBindingError(t, gate, spec.ID(), admission.ErrLifecycleUnavailable)
	if got := base.childStopCount(spec.ID()); got != 1 {
		t.Fatalf("auth-failed child stop count=%d, want 1", got)
	}
}

func TestSupervisorConnectionFailureDoesNotAffectOtherReadyBinding(t *testing.T) {
	specA := integrationSpec(t, "integration-a", "revision-a", 18792)
	specB := integrationSpec(t, "integration-b", "revision-b", 18793)
	health := integrationHealth{
		remote: func(_ context.Context, spec supervisor.ConnectionSpec) (supervisor.HealthResult, error) {
			if spec.ID() == specA.ID() {
				return supervisor.HealthResult{Status: supervisor.HealthAuthFailed}, nil
			}
			return supervisor.HealthResult{Status: supervisor.HealthReady}, nil
		},
	}
	gate, s := newIntegrationStack(t, health, newIntegrationBaseFactory(), specA, specB)
	if err := s.Start(context.Background(), specA.ID()); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background(), specB.ID()); err != nil {
		t.Fatal(err)
	}
	waitIntegrationState(t, s, specA.ID(), supervisor.StateAuthFailed)
	waitIntegrationState(t, s, specB.ID(), supervisor.StateReady)
	requireIntegrationBindingError(t, gate, specA.ID(), admission.ErrLifecycleUnavailable)

	bindingB, err := gate.CurrentLifecycleBinding(specB.ID())
	if err != nil {
		t.Fatal(err)
	}
	permitB, err := gate.Acquire(context.Background(), bindingB)
	if err != nil {
		t.Fatalf("B binding acquire after A failure: %v", err)
	}
	defer permitB.Release()
	if _, err := gate.CurrentLifecycleBinding(specA.ID()); !errors.Is(err, admission.ErrLifecycleUnavailable) {
		t.Fatalf("A binding after failure = %v", err)
	}
}
