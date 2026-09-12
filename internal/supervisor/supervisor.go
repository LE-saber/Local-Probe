package supervisor

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

const (
	defaultInitialBackoff = time.Second
	defaultMaxBackoff     = time.Minute
	maxBackoffLimit       = time.Minute
	defaultCleanupTimeout = 5 * time.Second
	maxCleanupTimeout     = 5 * time.Minute
	defaultHealthTimeout  = 30 * time.Second
)

// RuntimeFactory is the only lifecycle entry point for a runtime. A
// production implementation may later wrap an audited official client, but
// this package never receives an executable path, argv, environment, URL or
// secret and never starts a real process itself. Implementations must honor
// context cancellation; a violation is reported as cleanup failure after the
// supervisor's finite shutdown wait, but Go cannot forcibly terminate a
// blocked implementation goroutine.
type RuntimeFactory interface {
	Start(context.Context, ConnectionSpec) (Child, error)
}

// Child is the opaque handle returned by RuntimeFactory. The supervisor only
// owns its lifetime; it cannot inspect a process command line or transport
// credentials.
type Child interface {
	Stop(context.Context) error
}

// RuntimeView is the read-only identity passed to health checkers. The
// unexported marker prevents a checker from manufacturing or taking ownership
// of a runtime, and deliberately does not expose Child.Stop.
type RuntimeView interface {
	runtimeView()
}

// ownedChild serializes cleanup at the supervisor boundary. A child can be
// reached by both a failure path and the worker defer; a failed Stop can be
// retried, while a successful Stop is never repeated.
type ownedChild struct {
	mu       sync.Mutex
	child    Child
	stopped  bool
	inFlight bool
	done     chan struct{}
	err      error
}

func (*ownedChild) runtimeView() {}

func (c *ownedChild) Stop(ctx context.Context) error {
	ctx = nonNilContext(ctx)
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	done := c.done
	if !c.inFlight {
		c.inFlight = true
		done = make(chan struct{})
		c.done = done
		child := c.child
		go func() {
			err := child.Stop(ctx)
			c.mu.Lock()
			c.err = err
			if err == nil {
				c.stopped = true
			}
			c.inFlight = false
			close(done)
			c.mu.Unlock()
		}()
	}
	c.mu.Unlock()
	select {
	case <-done:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// HealthStatus is the bounded result vocabulary for injected checks.
type HealthStatus string

const (
	HealthReady      HealthStatus = "ready"
	HealthNotReady   HealthStatus = "not_ready"
	HealthAuthFailed HealthStatus = "auth_failed"
)

// HealthResult intentionally contains no message or endpoint details.
type HealthResult struct {
	Status HealthStatus
}

// HealthChecker separates the local-MCP readiness boundary from remote
// polling/readiness. Implementations must use only the supplied safe spec and
// read-only runtime view, and must honor context cancellation. Errors are
// classified by sentinel and never returned verbatim by Supervisor.
type HealthChecker interface {
	CheckLocal(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error)
	CheckRemote(context.Context, ConnectionSpec, RuntimeView) (HealthResult, error)
}

// Clock and TimerFactory are injectable so retry behavior is deterministic in
// tests and does not require sleeping for real time.
type Clock interface {
	Now() time.Time
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type TimerFactory interface {
	NewTimer(time.Duration) Timer
}

// JitterFunc receives a deterministic exponential delay and returns the delay
// to use. The result is clamped to [0, 60s]. A nil function means no jitter.
type JitterFunc func(attempt int, delay time.Duration) time.Duration

// Options controls only lifecycle timing. The maximum is always capped at 60s
// even when a caller supplies a larger value.
type Options struct {
	Clock          Clock
	Timers         TimerFactory
	Jitter         JitterFunc
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// CleanupTimeout bounds one child Stop attempt. Zero uses the finite
	// default; values above the package limit are rejected.
	CleanupTimeout time.Duration
}

type normalizedOptions struct {
	clock          Clock
	timers         TimerFactory
	jitter         JitterFunc
	initialBackoff time.Duration
	maxBackoff     time.Duration
	cleanupTimeout time.Duration
}

func normalizeOptions(options Options) (normalizedOptions, error) {
	if options.Clock == nil {
		options.Clock = wallClock{}
	}
	if options.Timers == nil {
		options.Timers = realTimerFactory{}
	}
	if options.Jitter == nil {
		options.Jitter = func(_ int, delay time.Duration) time.Duration { return delay }
	}
	if options.InitialBackoff < 0 || options.MaxBackoff < 0 || options.CleanupTimeout < 0 || options.CleanupTimeout > maxCleanupTimeout {
		return normalizedOptions{}, ErrInvalidSpec
	}
	if options.InitialBackoff == 0 {
		options.InitialBackoff = defaultInitialBackoff
	}
	if options.MaxBackoff == 0 {
		options.MaxBackoff = defaultMaxBackoff
	}
	if options.MaxBackoff > maxBackoffLimit {
		options.MaxBackoff = maxBackoffLimit
	}
	if options.InitialBackoff > options.MaxBackoff {
		options.InitialBackoff = options.MaxBackoff
	}
	if options.CleanupTimeout == 0 {
		options.CleanupTimeout = defaultCleanupTimeout
	}
	return normalizedOptions{
		clock:          options.Clock,
		timers:         options.Timers,
		jitter:         options.Jitter,
		initialBackoff: options.InitialBackoff,
		maxBackoff:     options.MaxBackoff,
		cleanupTimeout: options.CleanupTimeout,
	}, nil
}

// Supervisor owns independent lifecycle state for every configured
// connection. A failure in one entry only changes that entry and never
// cancels another connection's worker.
type Supervisor struct {
	mu        sync.RWMutex
	factory   RuntimeFactory
	health    HealthChecker
	options   normalizedOptions
	entries   map[string]*entry
	closed    bool
	closeDone chan struct{}
	closeErr  error
	// detachedCleanupFailed records a child that could not be stopped after
	// its entry/generation ceased to be current. It prevents Close from
	// reporting success when ownership can no longer be represented by an
	// entry snapshot.
	detachedCleanupFailed bool
}

// operationMutex is a zero-value context-aware mutex. Shutdown must not wait
// forever behind a caller that holds an entry operation lock while an injected
// implementation ignores cancellation.
type operationMutex struct {
	once sync.Once
	ch   chan struct{}
}

func (m *operationMutex) channel() chan struct{} {
	m.once.Do(func() {
		m.ch = make(chan struct{}, 1)
		m.ch <- struct{}{}
	})
	return m.ch
}

func (m *operationMutex) Lock() { <-m.channel() }

func (m *operationMutex) LockContext(ctx context.Context) bool {
	select {
	case <-m.channel():
		return true
	case <-ctx.Done():
		return false
	}
}

func (m *operationMutex) Unlock() { m.channel() <- struct{}{} }

type entry struct {
	opMu operationMutex

	spec        ConnectionSpec
	state       State
	attempt     int
	nextRetryAt time.Time
	lastError   ErrorCode

	running       bool
	generation    uint64
	cancel        context.CancelFunc
	done          chan struct{}
	child         *ownedChild
	notify        chan struct{}
	stopRequested bool
	stopState     State
	stopError     ErrorCode
	terminalState State
	terminalError ErrorCode
	authFailed    bool
	cleanupFailed bool
	cleanupError  ErrorCode
}

// New creates a transport-neutral supervisor. It validates the injected
// contracts but performs no I/O, process launch, or tunnel operation.
func New(factory RuntimeFactory, health HealthChecker, options Options) (*Supervisor, error) {
	if factory == nil || health == nil {
		return nil, ErrInvalidSpec
	}
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	return &Supervisor{factory: factory, health: health, options: normalized, entries: make(map[string]*entry)}, nil
}

// Add registers one stopped connection. Registration alone never starts a
// child. The spec is copied by value and contains no mutable backing data.
func (s *Supervisor) Add(spec ConnectionSpec) error {
	if s == nil || spec.Validate() != nil {
		return ErrInvalidSpec
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSupervisorClosed
	}
	if _, exists := s.entries[spec.ID()]; exists {
		return ErrConnectionExists
	}
	if !s.portAvailableLocked(spec.LocalPort(), nil) {
		return ErrInvalidSpec
	}
	s.entries[spec.ID()] = &entry{spec: spec.Clone(), state: StateStopped, lastError: ErrorNone, notify: make(chan struct{})}
	return nil
}

// Register is an explicit alias for Add for management callers.
func (s *Supervisor) Register(spec ConnectionSpec) error { return s.Add(spec) }

// Replace atomically swaps a stopped or running connection specification. A
// running old worker is cancelled and waited for before the new spec is
// installed. Replacement does not auto-start the new revision.
func (s *Supervisor) Replace(ctx context.Context, spec ConnectionSpec) error {
	if s == nil || spec.Validate() != nil {
		return ErrInvalidSpec
	}
	ctx = nonNilContext(ctx)
	e, err := s.lookup(spec.ID())
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return ErrSupervisorClosed
	}
	if !s.isRegisteredLocked(e) {
		s.mu.RUnlock()
		return ErrConnectionMissing
	}
	portAvailable := s.portAvailableLocked(spec.LocalPort(), e)
	s.mu.RUnlock()
	if !portAvailable {
		return ErrInvalidSpec
	}
	if err := s.stopEntry(ctx, e, StateStopped, ErrorRevisionChange, true); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSupervisorClosed
	}
	e.spec = spec.Clone()
	e.attempt = 0
	e.nextRetryAt = time.Time{}
	e.lastError = ErrorRevisionChange
	e.authFailed = false
	e.terminalState = State("")
	e.terminalError = ErrorNone
	e.cleanupFailed = false
	e.cleanupError = ErrorNone
	s.signalLocked(e)
	return nil
}

// Update is a descriptive alias for Replace.
func (s *Supervisor) Update(ctx context.Context, spec ConnectionSpec) error {
	return s.Replace(ctx, spec)
}

// Start schedules an independent worker for one stopped connection. Runtime
// and health failures are reported through Snapshot; no implementation error
// text is returned to callers.
func (s *Supervisor) Start(ctx context.Context, id string) error {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	e, err := s.lookup(id)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSupervisorClosed
	}
	if !s.isRegisteredLocked(e) {
		return ErrConnectionMissing
	}
	if e.state == StateCleanupFailed {
		return ErrCleanupFailed
	}
	if e.state == StateStopping {
		return ErrStopping
	}
	if e.child != nil {
		return ErrCleanupFailed
	}
	if e.state == StateAuthFailed {
		return ErrCircuitOpen
	}
	if e.state == StateSleeping {
		return ErrSleeping
	}
	if e.running {
		return nil
	}
	e.attempt = 0
	e.nextRetryAt = time.Time{}
	e.lastError = ErrorNone
	s.startLocked(e, StateStarting)
	return nil
}

// Stop cancels the worker, including a pending backoff timer, and waits for
// child cleanup unless the caller's wait context expires. It is idempotent.
func (s *Supervisor) Stop(ctx context.Context, id string) error {
	ctx = nonNilContext(ctx)
	e, err := s.lookup(id)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	return s.stopEntry(ctx, e, StateStopped, ErrorStopped, false)
}

// Reconnect performs a manual stop/start cycle. It is the explicit way to
// reset an authentication circuit breaker after credentials are rotated by a
// caller outside this package.
func (s *Supervisor) Reconnect(ctx context.Context, id string) error {
	ctx = nonNilContext(ctx)
	e, err := s.lookup(id)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	if err := s.stopEntry(ctx, e, StateStopped, ErrorStopped, true); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSupervisorClosed
	}
	if !s.isRegisteredLocked(e) {
		return ErrConnectionMissing
	}
	if e.child != nil {
		return ErrCleanupFailed
	}
	e.attempt = 0
	e.nextRetryAt = time.Time{}
	e.lastError = ErrorNone
	s.startLocked(e, StateResuming)
	return nil
}

// Sleep stops a worker and leaves the connection in sleeping until Wake. It
// does not remove or rewrite its configuration.
func (s *Supervisor) Sleep(ctx context.Context, id string) error {
	ctx = nonNilContext(ctx)
	e, err := s.lookup(id)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	s.mu.RLock()
	current := e.state
	authFailed := e.authFailed
	s.mu.RUnlock()
	if current == StateAuthFailed || authFailed {
		return ErrCircuitOpen
	}
	return s.stopEntry(ctx, e, StateSleeping, ErrorSleeping, false)
}

// Wake resumes a sleeping connection with a fresh worker. It does not bypass
// authentication failure; that requires Reconnect.
func (s *Supervisor) Wake(ctx context.Context, id string) error {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	e, err := s.lookup(id)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSupervisorClosed
	}
	if !s.isRegisteredLocked(e) {
		return ErrConnectionMissing
	}
	if e.state != StateSleeping {
		return ErrNotRunning
	}
	if e.child != nil {
		return ErrCleanupFailed
	}
	e.attempt = 0
	e.nextRetryAt = time.Time{}
	e.lastError = ErrorNone
	s.startLocked(e, StateResuming)
	return nil
}

// Remove cancels and forgets one connection. It only removes supervisor
// state; it never touches user files, credentials, or shared configuration.
func (s *Supervisor) Remove(ctx context.Context, id string) error {
	ctx = nonNilContext(ctx)
	e, err := s.lookup(id)
	if err != nil {
		return err
	}
	e.opMu.Lock()
	defer e.opMu.Unlock()
	if err := s.stopEntry(ctx, e, StateStopped, ErrorStopped, true); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isRegisteredLocked(e) {
		return ErrConnectionMissing
	}
	s.signalLocked(e)
	delete(s.entries, id)
	return nil
}

// Close stops all owned workers independently. It is safe to call more than
// once; no real process or tunnel is ever created by this package.
func (s *Supervisor) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	ctx = nonNilContext(ctx)
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
			s.mu.RLock()
			err := s.closeErr
			s.mu.RUnlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.closed = true
	s.closeDone = make(chan struct{})
	done := s.closeDone
	entries := make([]*entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	// The caller's context bounds how long this invocation waits, but it must
	// not cancel the ownership drain itself. The drain applies a separate
	// finite per-entry deadline; a non-cooperating injected implementation is
	// reported as ErrCleanupFailed instead of blocking Close forever.
	go s.finishClose(entries, done)
	select {
	case <-done:
		s.mu.RLock()
		err := s.closeErr
		s.mu.RUnlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finishClose drains the exact entry set captured when Close marked the
// supervisor closed. New entries cannot be added after that point. A Remove
// racing with this drain is serialized by the entry mutex; if it wins, the
// missing entry has already completed its own cleanup and is safe to skip.
func (s *Supervisor) finishClose(entries []*entry, done chan struct{}) {
	var closeErr error
	for _, e := range entries {
		stopCtx, cancel := context.WithTimeout(context.Background(), s.options.cleanupTimeout)
		if !e.opMu.LockContext(stopCtx) {
			cancel()
			closeErr = ErrCleanupFailed
			continue
		}
		err := s.stopEntry(stopCtx, e, StateStopped, ErrorStopped, false)
		cancel()
		e.opMu.Unlock()
		if err != nil && !errors.Is(err, ErrConnectionMissing) {
			closeErr = ErrCleanupFailed
		}
	}
	s.mu.Lock()
	if s.detachedCleanupFailed {
		closeErr = ErrCleanupFailed
	}
	s.closeErr = closeErr
	close(done)
	s.mu.Unlock()
}

// Snapshot returns a safe, path-free, secret-free view of one connection.
func (s *Supervisor) Snapshot(id string) (Snapshot, error) {
	e, err := s.lookup(id)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return snapshotLocked(e), nil
}

// Snapshots returns deterministic ID order so management and tests do not
// depend on map iteration order.
func (s *Supervisor) Snapshots() []Snapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	ids := make([]string, 0, len(s.entries))
	for id := range s.entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Snapshot, 0, len(ids))
	for _, id := range ids {
		out = append(out, snapshotLocked(s.entries[id]))
	}
	s.mu.RUnlock()
	return out
}

// WaitForState waits without exposing an implementation error or message. A
// caller can observe transitions deterministically with an injected timer.
func (s *Supervisor) WaitForState(ctx context.Context, id string, want State) (bool, error) {
	ctx = nonNilContext(ctx)
	e, err := s.lookup(id)
	if err != nil {
		return false, err
	}
	for {
		s.mu.RLock()
		current, registered := s.entries[id]
		if !registered || current != e {
			s.mu.RUnlock()
			return false, ErrConnectionMissing
		}
		if e.state == want {
			s.mu.RUnlock()
			return true, nil
		}
		notify := e.notify
		s.mu.RUnlock()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-notify:
		}
	}
}

func (s *Supervisor) lookup(id string) (*entry, error) {
	if s == nil {
		return nil, ErrSupervisorClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[id]
	if !ok {
		return nil, ErrConnectionMissing
	}
	return e, nil
}

// isRegisteredLocked prevents an operation that looked up an entry before a
// concurrent Remove from starting or mutating an orphaned worker. Callers
// must hold s.mu while invoking it.
func (s *Supervisor) isRegisteredLocked(e *entry) bool {
	if e == nil {
		return false
	}
	current, ok := s.entries[e.spec.ID()]
	return ok && current == e
}

func (s *Supervisor) startLocked(e *entry, initial State) {
	e.generation++
	generation := e.generation
	workerCtx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	e.running = true
	e.stopRequested = false
	e.stopState = StateStopped
	e.stopError = ErrorNone
	e.terminalState = State("")
	e.terminalError = ErrorNone
	e.authFailed = false
	e.cleanupFailed = false
	e.cleanupError = ErrorNone
	e.state = initial
	e.nextRetryAt = time.Time{}
	s.signalLocked(e)
	go s.run(workerCtx, e, generation)
}

func (s *Supervisor) stopEntry(ctx context.Context, e *entry, finalState State, errorCode ErrorCode, resetAuth bool) error {
	ctx = nonNilContext(ctx)
	s.mu.Lock()
	if !s.isRegisteredLocked(e) {
		s.mu.Unlock()
		return ErrConnectionMissing
	}
	if !e.running {
		child := e.child
		generation := e.generation
		if child == nil {
			if e.authFailed && !resetAuth {
				e.state = StateAuthFailed
				e.lastError = ErrorAuthFailed
			} else {
				e.state = finalState
				e.lastError = errorCode
				if resetAuth {
					e.authFailed = false
				}
			}
			e.attempt = 0
			e.nextRetryAt = time.Time{}
			s.signalLocked(e)
			s.mu.Unlock()
			return ctx.Err()
		}
		e.state = StateStopping
		e.lastError = ErrorNone
		s.signalLocked(e)
		s.mu.Unlock()
		if err := s.cleanupChild(e, generation, child); err != nil {
			return ErrCleanupFailed
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.isRegisteredLocked(e) {
			return ErrConnectionMissing
		}
		if e.authFailed && !resetAuth {
			e.state = StateAuthFailed
			e.lastError = ErrorAuthFailed
		} else {
			e.state = finalState
			e.lastError = errorCode
			if resetAuth {
				e.authFailed = false
			}
		}
		e.attempt = 0
		e.nextRetryAt = time.Time{}
		s.signalLocked(e)
		return ctx.Err()
	}
	e.stopRequested = true
	if e.authFailed && !resetAuth {
		e.stopState = StateAuthFailed
		e.stopError = ErrorAuthFailed
	} else {
		e.stopState = finalState
		e.stopError = errorCode
	}
	cancel := e.cancel
	done := e.done
	e.state = StateStopping
	e.nextRetryAt = time.Time{}
	e.lastError = ErrorNone
	s.signalLocked(e)
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
		s.mu.RLock()
		cleanupFailed := e.cleanupFailed
		s.mu.RUnlock()
		if cleanupFailed {
			return ErrCleanupFailed
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type checkOutcome uint8

const (
	checkReady checkOutcome = iota
	checkRetry
	checkAuth
)

func (s *Supervisor) run(ctx context.Context, e *entry, generation uint64) {
	spec := e.spec.Clone()
	var child *ownedChild
	cleanupAttempted := false
	defer func() {
		if child != nil && !cleanupAttempted {
			cleanupAttempted = true
			if err := s.cleanupChild(e, generation, child); err != nil {
				s.recordDetachedCleanupFailure(e, generation, child)
			}
		}
		s.workerDone(e, generation)
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		if !s.setState(e, generation, StateStarting, ErrorNone) {
			return
		}
		started, startErr := s.factory.Start(ctx, spec)
		if started != nil {
			child = &ownedChild{child: started}
			cleanupAttempted = false
			if !s.setChild(e, generation, child) {
				cleanupAttempted = true
				if err := s.cleanupChild(e, generation, child); err != nil {
					s.recordDetachedCleanupFailure(e, generation, child)
				}
				child = nil
				return
			}
		}
		if startErr != nil {
			if errors.Is(startErr, ErrAuthFailed) {
				s.markAuthFailure(e, generation)
			} else if child == nil {
				if !s.enterBackoff(ctx, e, generation, ErrorRuntime) {
					return
				}
				continue
			}
			if child != nil {
				cleanupAttempted = true
				if err := s.cleanupChild(e, generation, child); err != nil {
					return
				}
				child = nil
			}
			if errors.Is(startErr, ErrAuthFailed) {
				return
			}
			if !s.enterBackoff(ctx, e, generation, ErrorRuntime) {
				return
			}
			continue
		}
		if child == nil {
			if !s.enterBackoff(ctx, e, generation, ErrorRuntime) {
				return
			}
			continue
		}

		for {
			if ctx.Err() != nil {
				return
			}
			if outcome := s.checkLocal(ctx, spec, child); outcome != checkReady {
				if outcome == checkAuth {
					s.markAuthFailure(e, generation)
					cleanupAttempted = true
					if err := s.cleanupChild(e, generation, child); err != nil {
						return
					}
					child = nil
					return
				}
				break
			}
			if !s.setState(e, generation, StateLocalReady, ErrorNone) {
				return
			}
			if !s.setState(e, generation, StatePolling, ErrorNone) {
				return
			}
			if outcome := s.checkRemote(ctx, spec, child); outcome != checkReady {
				if outcome == checkAuth {
					s.markAuthFailure(e, generation)
					cleanupAttempted = true
					if err := s.cleanupChild(e, generation, child); err != nil {
						return
					}
					child = nil
					return
				}
				break
			}
			// Ready means both injected checkers verified this owned child and
			// current revision; it is not production PID/HA/tunnel evidence.
			if !s.setState(e, generation, StateReady, ErrorNone) {
				return
			}
			if spec.health.Interval <= 0 {
				<-ctx.Done()
				return
			}
			if !s.waitTimer(ctx, e, generation, spec.health.Interval, false) {
				return
			}
		}

		cleanupAttempted = true
		if err := s.cleanupChild(e, generation, child); err != nil {
			return
		}
		child = nil
		if !s.enterBackoff(ctx, e, generation, ErrorHealth) {
			return
		}
	}
}

func (s *Supervisor) checkLocal(ctx context.Context, spec ConnectionSpec, child *ownedChild) checkOutcome {
	checkCtx, cancel := healthContext(ctx, spec.health.Timeout)
	result, err := s.health.CheckLocal(checkCtx, spec, child)
	contextErr := checkCtx.Err()
	cancel()
	if contextErr != nil {
		return checkRetry
	}
	return classifyHealth(result, err)
}

func (s *Supervisor) checkRemote(ctx context.Context, spec ConnectionSpec, child *ownedChild) checkOutcome {
	checkCtx, cancel := healthContext(ctx, spec.health.Timeout)
	result, err := s.health.CheckRemote(checkCtx, spec, child)
	contextErr := checkCtx.Err()
	cancel()
	if contextErr != nil {
		return checkRetry
	}
	return classifyHealth(result, err)
}

func classifyHealth(result HealthResult, err error) checkOutcome {
	if errors.Is(err, ErrAuthFailed) || result.Status == HealthAuthFailed {
		return checkAuth
	}
	if err != nil || result.Status != HealthReady {
		return checkRetry
	}
	return checkReady
}

func (s *Supervisor) markAuthFailure(e *entry, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.generation != generation || !e.running || e.stopRequested {
		return
	}
	e.authFailed = true
	e.terminalState = StateAuthFailed
	e.terminalError = ErrorAuthFailed
	e.state = StateAuthFailed
	e.lastError = ErrorAuthFailed
	s.signalLocked(e)
}

func (s *Supervisor) enterBackoff(ctx context.Context, e *entry, generation uint64, code ErrorCode) bool {
	if !s.setState(e, generation, StateDegraded, code) {
		return false
	}
	s.mu.Lock()
	if e.generation != generation || !e.running || e.stopRequested {
		s.mu.Unlock()
		return false
	}
	e.attempt++
	attempt := e.attempt
	s.mu.Unlock()
	delay := s.backoff(attempt)
	if !s.setState(e, generation, StateBackoff, code) {
		return false
	}
	return s.waitTimer(ctx, e, generation, delay, true)
}

func (s *Supervisor) cleanupChild(e *entry, generation uint64, child *ownedChild) error {
	if child == nil {
		return nil
	}
	s.mu.Lock()
	if e.generation == generation && s.isRegisteredLocked(e) && e.child == child {
		e.state = StateStopping
		e.lastError = ErrorNone
		s.signalLocked(e)
	}
	s.mu.Unlock()

	cleanupCtx, cancel := context.WithTimeout(context.Background(), s.options.cleanupTimeout)
	err := child.Stop(cleanupCtx)
	timedOut := errors.Is(cleanupCtx.Err(), context.DeadlineExceeded)
	cancel()
	if err == nil && timedOut {
		err = context.DeadlineExceeded
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if e.generation == generation && s.isRegisteredLocked(e) && e.child == child {
		if err != nil {
			e.cleanupFailed = true
			e.cleanupError = ErrorCleanupFailed
			e.state = StateCleanupFailed
			e.lastError = ErrorCleanupFailed
		} else {
			e.child = nil
			e.cleanupFailed = false
			e.cleanupError = ErrorNone
		}
		s.signalLocked(e)
	}
	return err
}

func (s *Supervisor) waitTimer(ctx context.Context, e *entry, generation uint64, delay time.Duration, retry bool) bool {
	if delay < 0 {
		return false
	}
	if retry {
		s.mu.Lock()
		if e.generation == generation && e.running && !e.stopRequested {
			e.nextRetryAt = s.options.clock.Now().Add(delay)
			s.signalLocked(e)
		}
		s.mu.Unlock()
	}
	timer := s.options.timers.NewTimer(delay)
	if timer == nil {
		s.setTerminal(e, generation, StateDegraded, ErrorRuntime)
		return false
	}
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C():
		if retry {
			s.mu.Lock()
			if e.generation == generation {
				e.nextRetryAt = time.Time{}
				s.signalLocked(e)
			}
			s.mu.Unlock()
		}
		return true
	}
}

func (s *Supervisor) setTerminal(e *entry, generation uint64, state State, code ErrorCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.generation != generation || !e.running || e.stopRequested {
		return
	}
	e.terminalState = state
	e.terminalError = code
	e.state = state
	e.lastError = code
	s.signalLocked(e)
}

func (s *Supervisor) recordDetachedCleanupFailure(e *entry, generation uint64, child *ownedChild) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.generation == generation && s.isRegisteredLocked(e) && e.child == child {
		return // cleanupChild already recorded the entry-scoped failure
	}
	s.detachedCleanupFailed = true
	if s.closed {
		s.closeErr = ErrCleanupFailed
	}
}

func (s *Supervisor) portAvailableLocked(port uint16, except *entry) bool {
	for _, candidate := range s.entries {
		if candidate != except && candidate.spec.LocalPort() == port {
			return false
		}
	}
	return true
}

func (s *Supervisor) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := s.options.initialBackoff
	for i := 1; i < attempt && delay < s.options.maxBackoff; i++ {
		if delay > s.options.maxBackoff/2 {
			delay = s.options.maxBackoff
			break
		}
		delay *= 2
	}
	delay = s.options.jitter(attempt, delay)
	if delay < 0 {
		return 0
	}
	if delay > maxBackoffLimit {
		return maxBackoffLimit
	}
	if delay > s.options.maxBackoff {
		return s.options.maxBackoff
	}
	return delay
}

func (s *Supervisor) setState(e *entry, generation uint64, state State, code ErrorCode) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.generation != generation || !e.running || e.stopRequested {
		return false
	}
	e.state = state
	e.lastError = code
	if state == StateReady {
		e.attempt = 0
		e.nextRetryAt = time.Time{}
	}
	s.signalLocked(e)
	return true
}

func (s *Supervisor) setChild(e *entry, generation uint64, child *ownedChild) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.generation != generation || !e.running || !s.isRegisteredLocked(e) {
		return false
	}
	e.child = child
	return true
}

func (s *Supervisor) workerDone(e *entry, generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.generation != generation || !e.running {
		return
	}
	e.running = false
	e.cancel = nil
	if e.cleanupFailed || e.child != nil {
		e.cleanupFailed = true
		e.cleanupError = ErrorCleanupFailed
		e.state = StateCleanupFailed
		e.lastError = ErrorCleanupFailed
	} else if e.stopRequested {
		e.state = e.stopState
		e.lastError = e.stopError
	} else if e.terminalState != State("") {
		e.state = e.terminalState
		e.lastError = e.terminalError
	} else {
		e.state = StateStopped
		e.lastError = ErrorNone
	}
	e.stopRequested = false
	e.terminalState = State("")
	e.terminalError = ErrorNone
	e.nextRetryAt = time.Time{}
	s.signalLocked(e)
	if e.done != nil {
		close(e.done)
	}
}

func (s *Supervisor) signalLocked(e *entry) {
	if e.notify == nil {
		e.notify = make(chan struct{})
		return
	}
	close(e.notify)
	e.notify = make(chan struct{})
}

func snapshotLocked(e *entry) Snapshot {
	return Snapshot{
		ID:          e.spec.ID(),
		Revision:    e.spec.Revision(),
		Transport:   e.spec.Transport(),
		TunnelID:    e.spec.TunnelID(),
		TunnelAlias: e.spec.TunnelAlias(),
		LocalPort:   e.spec.LocalPort(),
		Health:      e.spec.Health(),
		State:       e.state,
		Attempt:     e.attempt,
		NextRetryAt: e.nextRetryAt,
		LastError:   e.lastError,
	}
}

func healthContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}
	return context.WithTimeout(parent, timeout)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type realTimerFactory struct{}

func (realTimerFactory) NewTimer(delay time.Duration) Timer {
	return realTimer{timer: time.NewTimer(delay)}
}

type realTimer struct{ timer *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.timer.C }

func (t realTimer) Stop() bool { return t.timer.Stop() }
