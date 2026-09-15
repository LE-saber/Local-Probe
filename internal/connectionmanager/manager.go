// Package connectionmanager coordinates trusted local admission and supervisor
// mutations. It performs no process, MCP, tunnel, credential, or network I/O.
package connectionmanager

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/LE-saber/Local-Probe/internal/admission"
	"github.com/LE-saber/Local-Probe/internal/supervisor"
)

const ProductionReady = false

const panicCleanupTimeout = 100 * time.Millisecond

var (
	ErrInvalidConfig      = errors.New("invalid connection manager configuration")
	ErrClosed             = errors.New("connection manager closed")
	ErrConnectionExists   = errors.New("connection already managed")
	ErrConnectionMissing  = errors.New("connection is not managed")
	ErrConnectionDisabled = errors.New("connection is disabled")
	ErrAdmission          = errors.New("admission coordination failed")
	ErrSupervisor         = errors.New("supervisor coordination failed")
)

// Supervisor is the local lifecycle surface used by Manager. The concrete
// supervisor.Supervisor implements it; the interface keeps failure ordering
// testable without creating a real child or tunnel.
type Supervisor interface {
	// Add must have no side effect when it returns an error. A panic has
	// uncertain side effects and is handled by bounded best-effort removal.
	// Context-bearing methods must honor cancellation; this is a trusted local
	// integration contract, not an untrusted plugin interface.
	Add(supervisor.ConnectionSpec) error
	Replace(context.Context, supervisor.ConnectionSpec) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Reconnect(context.Context, string) error
	Sleep(context.Context, string) error
	Wake(context.Context, string) error
	Remove(context.Context, string) error
	Close(context.Context) error
}

type phase uint8

const (
	phaseAdding phase = iota
	phaseStopped
	phaseRunning
	phaseDegraded
	phaseRemoving
)

type entry struct {
	op chan struct{}

	connection admission.Connection
	spec       supervisor.ConnectionSpec
	enabled    bool
	phase      phase
	gateGone   bool
}

// Manager owns coordination order, not the Gate or Supervisor themselves.
// Callers must route management mutations through this value after Add.
type Manager struct {
	gate       *admission.Gate
	supervisor Supervisor

	mu      sync.Mutex
	entries map[string]*entry
	closed  bool
	closeOp chan struct{}
}

func New(gate *admission.Gate, lifecycle Supervisor) (*Manager, error) {
	if gate == nil || lifecycle == nil {
		return nil, ErrInvalidConfig
	}
	return &Manager{gate: gate, supervisor: lifecycle, entries: make(map[string]*entry), closeOp: make(chan struct{}, 1)}, nil
}

// Add registers a lifecycle-required admission record and matching stopped
// supervisor record. The reservation is visible before external calls, while
// the per-entry lock prevents another operation observing partial setup.
func (m *Manager) Add(connection admission.Connection, spec supervisor.ConnectionSpec) (result error) {
	if err := validatePair(connection, spec); err != nil {
		return err
	}
	e := &entry{op: make(chan struct{}, 1), connection: connection, spec: spec, enabled: connection.Enabled(), phase: phaseAdding}
	e.op <- struct{}{}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		<-e.op
		return ErrClosed
	}
	if _, exists := m.entries[connection.ID()]; exists {
		m.mu.Unlock()
		<-e.op
		return ErrConnectionExists
	}
	m.entries[connection.ID()] = e
	m.mu.Unlock()
	inserted := true
	gateAdded := false
	supervisorUnknown := false
	defer func() {
		if recover() != nil {
			result = ErrAdmission
		}
		if result != nil && inserted {
			if supervisorUnknown {
				// A panicking interface may have mutated before panicking. Try to
				// remove that uncertain record; retain a removal-pending entry if
				// cleanup itself cannot be confirmed.
				cleanupCtx, cancel := context.WithTimeout(context.Background(), panicCleanupTimeout)
				cleanupErr := callSupervisorRemove(m.supervisor, cleanupCtx, connection.ID())
				cancel()
				if cleanupErr != nil && !errors.Is(cleanupErr, supervisor.ErrConnectionMissing) {
					e.phase = phaseRemoving
				} else {
					supervisorUnknown = false
				}
			}
			if gateAdded {
				_ = m.gate.RemoveConnection(connection.ID())
				e.gateGone = true
			}
			if !supervisorUnknown {
				m.drop(connection.ID(), e)
			}
		}
		<-e.op
	}()

	if err := m.gate.AddConnection(connection); err != nil {
		return ErrAdmission
	}
	gateAdded = true
	if err, panicked := callSupervisorAdd(m.supervisor, spec); err != nil {
		supervisorUnknown = panicked
		return ErrSupervisor
	}
	e.phase = phaseStopped
	inserted = false
	return nil
}

// Start schedules the registered supervisor. Admission remains unavailable
// until lifecycleadapter marks the exact child generation ready.
func (m *Manager) Start(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if !e.enabled {
		return ErrConnectionDisabled
	}
	if e.phase == phaseDegraded {
		return ErrSupervisor
	}
	if err := callSupervisor(func() error { return m.supervisor.Start(ctx, id) }); err != nil {
		_ = m.gate.InvalidateLifecycle(id)
		e.phase = phaseDegraded
		return ErrSupervisor
	}
	e.phase = phaseRunning
	return nil
}

// Stop invalidates admission before waiting for child cleanup. A cleanup
// failure is retryable and cannot reopen admission.
func (m *Manager) Stop(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if err := m.gate.InvalidateLifecycle(id); err != nil {
		return ErrAdmission
	}
	if err := callSupervisor(func() error { return m.supervisor.Stop(ctx, id) }); err != nil {
		e.phase = phaseDegraded
		return ErrSupervisor
	}
	e.phase = phaseStopped
	return nil
}

// Reconnect is the explicit authentication-circuit recovery path. It closes
// the current admission epoch before the supervisor performs its stop/start
// cycle; the replacement child must independently reach Ready.
func (m *Manager) Reconnect(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if !e.enabled {
		return ErrConnectionDisabled
	}
	if err := m.gate.InvalidateLifecycle(id); err != nil {
		return ErrAdmission
	}
	if err := callSupervisor(func() error { return m.supervisor.Reconnect(ctx, id) }); err != nil {
		e.phase = phaseDegraded
		return ErrSupervisor
	}
	e.phase = phaseRunning
	return nil
}

// Sleep invalidates admission before stopping the child in the supervisor's
// sleeping state.
func (m *Manager) Sleep(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if err := m.gate.InvalidateLifecycle(id); err != nil {
		return ErrAdmission
	}
	if err := callSupervisor(func() error { return m.supervisor.Sleep(ctx, id) }); err != nil {
		e.phase = phaseDegraded
		return ErrSupervisor
	}
	e.phase = phaseStopped
	return nil
}

// Wake schedules a fresh child from supervisor sleeping state. Lifecycle
// admission remains unavailable until that child reaches Ready.
func (m *Manager) Wake(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if !e.enabled {
		return ErrConnectionDisabled
	}
	if err := m.gate.InvalidateLifecycle(id); err != nil {
		return ErrAdmission
	}
	if err := callSupervisor(func() error { return m.supervisor.Wake(ctx, id) }); err != nil {
		_ = m.gate.InvalidateLifecycle(id)
		e.phase = phaseDegraded
		return ErrSupervisor
	}
	e.phase = phaseRunning
	return nil
}

// Replace first swaps the admission revision, canceling old permits and
// bindings. Only then is the supervisor revision replaced. If that second
// step fails, the new admission record has no ready lifecycle and stays
// fail-closed; calling Replace again is the recovery path.
func (m *Manager) Replace(ctx context.Context, connection admission.Connection, spec supervisor.ConnectionSpec) error {
	if err := validatePair(connection, spec); err != nil {
		return err
	}
	e, err := m.lockEntry(ctx, connection.ID())
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if err := m.gate.ReplaceConnection(connection); err != nil {
		return ErrAdmission
	}
	// Record the fail-closed desired revision before the fallible second step.
	e.connection = connection
	e.spec = spec
	e.enabled = connection.Enabled()
	e.phase = phaseDegraded
	if err := callSupervisor(func() error { return m.supervisor.Replace(ctx, spec) }); err != nil {
		return ErrSupervisor
	}
	e.phase = phaseStopped
	return nil
}

// Disable closes admission before stopping the child. Repeated calls remain
// invalidation boundaries and cleanup failures remain retryable via Stop.
func (m *Manager) Disable(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if err := m.gate.DisableConnection(id); err != nil {
		return ErrAdmission
	}
	e.enabled = false
	if err := callSupervisor(func() error { return m.supervisor.Stop(ctx, id) }); err != nil {
		e.phase = phaseDegraded
		return ErrSupervisor
	}
	e.phase = phaseStopped
	return nil
}

// Revoke permanently closes this registered admission record until a trusted
// Replace installs a fresh record. The supervisor child is stopped after the
// revocation boundary.
func (m *Manager) Revoke(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if err := m.gate.RevokeConnection(id); err != nil {
		return ErrAdmission
	}
	e.enabled = false
	if err := callSupervisor(func() error { return m.supervisor.Stop(ctx, id) }); err != nil {
		e.phase = phaseDegraded
		return ErrSupervisor
	}
	e.phase = phaseStopped
	return nil
}

// Enable changes only the trusted local configuration flag. Lifecycle mode
// still rejects admission until a subsequent Start reaches Ready.
func (m *Manager) Enable(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	if e.phase == phaseRemoving || e.gateGone {
		return ErrConnectionMissing
	}
	if err := m.gate.EnableConnection(id); err != nil {
		return ErrAdmission
	}
	e.enabled = true
	return nil
}

// Remove deletes admission first. If supervisor cleanup fails, the retained
// manager entry remembers that Gate removal already succeeded and a retry
// continues only the remaining cleanup.
func (m *Manager) Remove(ctx context.Context, id string) error {
	e, err := m.lockEntry(ctx, id)
	if err != nil {
		return err
	}
	defer func() { <-e.op }()
	e.phase = phaseRemoving
	if !e.gateGone {
		if err := m.gate.RemoveConnection(id); err != nil {
			e.phase = phaseDegraded
			return ErrAdmission
		}
		e.gateGone = true
	}
	if err := callSupervisorRemove(m.supervisor, ctx, id); err != nil && !errors.Is(err, supervisor.ErrConnectionMissing) {
		return ErrSupervisor
	}
	m.drop(id, e)
	return nil
}

// Close makes all admission unavailable before asking the supervisor to
// clean up. A concrete supervisor cleanup failure is terminal: repeated Close
// returns that retained result and does not retry child cleanup.
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return ErrClosed
	}
	ctx = nonNilContext(ctx)
	select {
	case m.closeOp <- struct{}{}:
		defer func() { <-m.closeOp }()
	case <-ctx.Done():
		return ctx.Err()
	}
	m.mu.Lock()
	m.closed = true
	entries := make([]*entry, 0, len(m.entries))
	for _, e := range m.entries {
		entries = append(entries, e)
	}
	m.mu.Unlock()
	if err := m.gate.Close(); err != nil {
		return ErrAdmission
	}
	// Wait for already-authorized management operations before closing the
	// supervisor. Gate.Close above makes their admission side fail closed;
	// taking every entry lock prevents their supervisor calls racing Close.
	locked := make([]*entry, 0, len(entries))
	defer func() {
		for i := len(locked) - 1; i >= 0; i-- {
			<-locked[i].op
		}
	}()
	for _, e := range entries {
		select {
		case e.op <- struct{}{}:
			locked = append(locked, e)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := callSupervisor(func() error { return m.supervisor.Close(ctx) }); err != nil {
		return ErrSupervisor
	}
	return nil
}

func (m *Manager) lockEntry(ctx context.Context, id string) (*entry, error) {
	if m == nil {
		return nil, ErrClosed
	}
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	e := m.entries[id]
	m.mu.Unlock()
	if e == nil {
		return nil, ErrConnectionMissing
	}
	return m.lockKnownEntry(ctx, id, e)
}

func (m *Manager) lockKnownEntry(ctx context.Context, id string, e *entry) (*entry, error) {
	select {
	case e.op <- struct{}{}:
		m.mu.Lock()
		closed := m.closed
		stale := closed || m.entries[id] != e
		m.mu.Unlock()
		if stale {
			<-e.op
			if closed {
				return nil, ErrClosed
			}
			return nil, ErrConnectionMissing
		}
		return e, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *Manager) drop(id string, e *entry) {
	m.mu.Lock()
	if m.entries[id] == e {
		delete(m.entries, id)
	}
	m.mu.Unlock()
}

func validatePair(connection admission.Connection, spec supervisor.ConnectionSpec) error {
	if connection.Validate() != nil || !connection.RequiresLifecycle() || spec.Validate() != nil ||
		connection.ID() != spec.ID() || connection.Revision() != spec.Revision() {
		return ErrInvalidConfig
	}
	return nil
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func callSupervisorAdd(lifecycle Supervisor, spec supervisor.ConnectionSpec) (err error, panicked bool) {
	defer func() {
		if recover() != nil {
			err = ErrSupervisor
			panicked = true
		}
	}()
	return lifecycle.Add(spec), false
}

func callSupervisorRemove(lifecycle Supervisor, ctx context.Context, id string) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrSupervisor
		}
	}()
	return lifecycle.Remove(ctx, id)
}

func callSupervisor(call func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrSupervisor
		}
	}()
	return call()
}

var _ Supervisor = (*supervisor.Supervisor)(nil)
