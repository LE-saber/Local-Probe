// Package runtimeowner contains the smallest local ownership primitive for a
// future Windows runtime broker.
//
// The package deliberately does not create a process, accept an executable or
// command line, or connect a tunnel. A trusted launcher may hand one process
// handle to Job.OwnProcess; the Windows implementation first duplicates that
// handle, puts the duplicate in a package-created Job Object, and binds a
// private PID/creation-time identity to the resulting OwnedRuntime. The
// identity and runtime are intentionally not serializable. They are local
// capabilities, not request fields.
package runtimeowner

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrorCode is the stable, path-free category returned by this package.
type ErrorCode string

const (
	CodeInvalidInput         ErrorCode = "invalid_input"
	CodeUnsupported          ErrorCode = "unsupported_platform"
	CodeUnavailable          ErrorCode = "unavailable"
	CodeClosed               ErrorCode = "closed"
	CodeAlreadyOwned         ErrorCode = "already_owned"
	CodeNotOwned             ErrorCode = "not_owned"
	CodeIdentityChanged      ErrorCode = "identity_changed"
	CodeSerializationBlocked ErrorCode = "serialization_blocked"
	CodeCancelled            ErrorCode = "cancelled"
	CodeDeadlineExceeded     ErrorCode = "deadline_exceeded"
	CodeCleanupFailed        ErrorCode = "cleanup_failed"
)

// Error intentionally does not include a path, PID, handle, or OS error
// string. It is safe to expose at a local API boundary.
type Error struct {
	Code ErrorCode
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code)
}

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e != nil && t != nil && e.Code == t.Code
}

var (
	ErrInvalidInput         = &Error{Code: CodeInvalidInput}
	ErrUnsupported          = &Error{Code: CodeUnsupported}
	ErrUnavailable          = &Error{Code: CodeUnavailable}
	ErrClosed               = &Error{Code: CodeClosed}
	ErrAlreadyOwned         = &Error{Code: CodeAlreadyOwned}
	ErrNotOwned             = &Error{Code: CodeNotOwned}
	ErrIdentityChanged      = &Error{Code: CodeIdentityChanged}
	ErrSerializationBlocked = &Error{Code: CodeSerializationBlocked}
	ErrCancelled            = &Error{Code: CodeCancelled}
	ErrDeadlineExceeded     = &Error{Code: CodeDeadlineExceeded}
	ErrCleanupFailed        = &Error{Code: CodeCleanupFailed}
)

// ProductionReady is intentionally false until the process creator, Job,
// network policy, health check, direct/leaf membership proof, and supervisor
// are one audited lifecycle. A successful local snapshot below is evidence
// for that future integration, not a claim that this package is already a
// production launcher.
func ProductionReady() bool { return false }

// Identity is a local, non-serializable process identity. Its unexported
// fields make it impossible to manufacture a valid value with a JSON request.
// A zero Identity is always invalid.
type Identity struct {
	pid           uint32
	creationTicks uint64
	jobTag        [16]byte
	valid         bool
}

// Valid reports whether the identity was minted by an ownership operation.
func (id Identity) Valid() bool {
	return id.valid && id.pid != 0 && id.creationTicks != 0 && id.jobTag != [16]byte{}
}

// PID returns the operating-system process ID captured at ownership time.
// It is an observation only; callers must not use it to reopen or terminate a
// process. Termination is available only through OwnedRuntime.Terminate.
func (id Identity) PID() uint32 {
	if !id.Valid() {
		return 0
	}
	return id.pid
}

// CreationTimeTicks returns the native creation-time value. On Windows this
// is the FILETIME count of 100 ns intervals since 1601 UTC. It is returned as
// data for diagnostics, never as a lookup key.
func (id Identity) CreationTimeTicks() uint64 {
	if !id.Valid() {
		return 0
	}
	return id.creationTicks
}

func (id Identity) equal(other Identity) bool {
	return id.Valid() && other.Valid() && id.pid == other.pid &&
		id.creationTicks == other.creationTicks && id.jobTag == other.jobTag
}

// MarshalJSON deliberately rejects capability serialization. In particular,
// a PID plus creation time copied into a later request must not become an
// owned runtime identity.
func (Identity) MarshalJSON() ([]byte, error) { return nil, ErrSerializationBlocked }

func (*Identity) UnmarshalJSON([]byte) error { return ErrSerializationBlocked }

// Snapshot is a point-in-time local observation of the owned process. It has
// no public fields by design; use accessors for diagnostics and Revalidate on
// the OwnedRuntime before any lifecycle operation.
type Snapshot struct {
	identity       Identity
	ancestorMember bool
	observedAt     time.Time
}

// Identity returns the typed identity observed in this snapshot.
func (s Snapshot) Identity() Identity { return s.identity }

// Owned reports whether the process was in the package-owned Job or one of its
// descendant Jobs at the instant of the check. Windows IsProcessInJob exposes
// this ancestor-membership relation; it does not prove direct/leaf membership.
// The method does not prove process health or network-policy installation.
func (s Snapshot) Owned() bool { return s.ancestorMember && s.identity.Valid() }

// InOwnedJobTree is the explicit form of Owned. It names the ancestor-member
// semantics so callers do not mistake the result for direct Job membership.
func (s Snapshot) InOwnedJobTree() bool { return s.Owned() }

// PID returns the observed PID, or zero for an invalid snapshot.
func (s Snapshot) PID() uint32 { return s.identity.PID() }

// CreationTimeTicks returns the observed native process creation time.
func (s Snapshot) CreationTimeTicks() uint64 { return s.identity.CreationTimeTicks() }

// ObservedAt returns the local observation time. It is not an authorization
// timestamp and must not be used to extend the lifetime of a snapshot.
func (s Snapshot) ObservedAt() time.Time { return s.observedAt }

func (Snapshot) MarshalJSON() ([]byte, error) { return nil, ErrSerializationBlocked }

func (*Snapshot) UnmarshalJSON([]byte) error { return ErrSerializationBlocked }

// runtimeOps is intentionally private: an external package cannot implement
// a fake OwnedRuntime or turn model-supplied fields into a lifecycle token.
type runtimeOps interface {
	snapshot(context.Context, Identity) (Snapshot, error)
	terminate(context.Context, Identity) error
	waitExited(context.Context, Identity) error
	close() error
}

type runtimeAttachment struct {
	identity Identity
	ops      runtimeOps
}

// jobOps is the platform-specific owner of a package-created Job Object.
type jobOps interface {
	ownProcess(uintptr, [16]byte) (runtimeAttachment, error)
	close() error
}

type jobPhase uint8

const (
	jobOpen jobPhase = iota
	jobCleaning
	jobCleanupFailed
	jobClosed
)

// jobState is heap-shared by every copy of Job. Keeping lifecycle flags and
// the platform owner together prevents a copied Job value from carrying stale
// closed/claimed bits or a second view of the Job handle.
type jobState struct {
	mu    sync.Mutex
	ops   jobOps
	token [16]byte
	phase jobPhase
	claim bool
}

// Job owns one package-created process Job Object. It is a builder/lifetime
// anchor, not a serializable request object. Copies share the same state.
// Closing it is fail-safe on Windows because the object is configured with
// kill-on-close.
type Job struct{ state *jobState }

// OwnProcess transfers ownership of one already-created process handle into
// this Job. The handle is an input to a trusted local launcher boundary; this
// method does not accept an executable, argv, environment, or secret and never
// starts a process. The returned runtime owns a duplicated process handle.
//
// On Windows the caller's handle must permit DuplicateHandle to request
// PROCESS_QUERY_LIMITED_INFORMATION, SYNCHRONIZE, PROCESS_SET_QUOTA, and
// PROCESS_TERMINATE. The package duplicates before assigning the duplicate to
// its Job, so closing or replacing the caller's handle cannot change the
// package's ownership target.
func (j *Job) OwnProcess(processHandle uintptr) (*OwnedRuntime, error) {
	if j == nil || j.state == nil || processHandle == 0 {
		return nil, ErrInvalidInput
	}
	state := j.state
	state.mu.Lock()
	defer state.mu.Unlock()
	switch state.phase {
	case jobClosed:
		return nil, ErrClosed
	case jobCleanupFailed, jobCleaning:
		return nil, ErrCleanupFailed
	}
	if state.phase != jobOpen {
		return nil, ErrUnavailable
	}
	if state.claim {
		return nil, ErrAlreadyOwned
	}
	if state.ops == nil {
		return nil, ErrUnsupported
	}
	attachment, err := state.ops.ownProcess(processHandle, state.token)
	if err != nil {
		return nil, err
	}
	if attachment.ops == nil || !attachment.identity.Valid() || attachment.identity.jobTag != state.token {
		if attachment.ops != nil {
			_ = attachment.ops.close()
		}
		return nil, ErrUnavailable
	}
	state.claim = true
	return &OwnedRuntime{state: &ownedRuntimeState{
		identity: attachment.identity,
		ops:      attachment.ops,
		phase:    runtimeOpen,
	}}, nil
}

// Close releases the Job Object. It is idempotent. On Windows closing the
// object terminates any process still attached to it; if cleanup fails, the
// handle is retained in a cleanup_failed state and Close may be retried.
func (j *Job) Close() error {
	if j == nil || j.state == nil {
		return ErrInvalidInput
	}
	state := j.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase == jobClosed {
		return nil
	}
	if state.ops == nil {
		return ErrUnsupported
	}
	state.phase = jobCleaning
	if err := state.ops.close(); err != nil {
		state.phase = jobCleanupFailed
		return normalizeCleanupError(err)
	}
	state.phase = jobClosed
	return nil
}

func (Job) MarshalJSON() ([]byte, error) { return nil, ErrSerializationBlocked }

func (*Job) UnmarshalJSON([]byte) error { return ErrSerializationBlocked }

type runtimePhase uint8

const (
	runtimeOpen runtimePhase = iota
	runtimeCleanupFailed
	runtimeClosed
)

// ownedRuntimeState is heap-shared by every copy of OwnedRuntime. All
// lifecycle flags and the private handle owner therefore have one source of
// truth even if a caller copies the small public wrapper.
type ownedRuntimeState struct {
	mu       sync.Mutex
	identity Identity
	ops      runtimeOps
	phase    runtimePhase
	exited   bool
}

// OwnedRuntime is the only object that can revalidate or terminate the
// process. Its private identity is bound to the process handle and the Job
// created by this package. Copies share the same state.
type OwnedRuntime struct{ state *ownedRuntimeState }

// Identity returns a copy of the local typed identity. The copy cannot be
// used to create another OwnedRuntime and serializing it is rejected.
func (r *OwnedRuntime) Identity() Identity {
	if r == nil || r.state == nil {
		return Identity{}
	}
	state := r.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.identity
}

func (r *OwnedRuntime) snapshotLocked(ctx context.Context) (Snapshot, error) {
	if r == nil || r.state == nil {
		return Snapshot{}, ErrInvalidInput
	}
	state := r.state
	switch state.phase {
	case runtimeClosed:
		return Snapshot{}, ErrClosed
	case runtimeCleanupFailed:
		return Snapshot{}, ErrCleanupFailed
	}
	if state.ops == nil || !state.identity.Valid() {
		return Snapshot{}, ErrInvalidInput
	}
	snapshot, err := state.ops.snapshot(ctx, state.identity)
	if err != nil {
		return snapshot, err
	}
	if !snapshot.identity.Valid() || !snapshot.identity.equal(state.identity) {
		return snapshot, ErrIdentityChanged
	}
	if !snapshot.ancestorMember {
		return snapshot, ErrNotOwned
	}
	return snapshot, nil
}

// Snapshot rechecks PID, creation time, and Job-tree membership through the
// platform handle. A snapshot is never a reusable capability.
func (r *OwnedRuntime) Snapshot(ctx context.Context) (Snapshot, error) {
	if r == nil || r.state == nil {
		return Snapshot{}, ErrInvalidInput
	}
	state := r.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return r.snapshotLocked(ctx)
}

// Revalidate performs the same ownership check without returning observations.
func (r *OwnedRuntime) Revalidate(ctx context.Context) error {
	_, err := r.Snapshot(ctx)
	return err
}

// Terminate revalidates immediately before asking the package-owned Job to
// terminate. It never opens a process by PID and never calls TerminateProcess.
// Success means termination was dispatched; it does not mean the process has
// exited. Call WaitExited before reporting a confirmed stopped state.
func (r *OwnedRuntime) Terminate(ctx context.Context) error {
	if r == nil || r.state == nil {
		return ErrInvalidInput
	}
	state := r.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, err := r.snapshotLocked(ctx); err != nil {
		return err
	}
	return state.ops.terminate(ctx, state.identity)
}

// WaitExited waits until the owned process object signals exit. A successful
// call records the local confirmed-exit observation in the shared state.
func (r *OwnedRuntime) WaitExited(ctx context.Context) error {
	if r == nil || r.state == nil {
		return ErrInvalidInput
	}
	state := r.state
	state.mu.Lock()
	defer state.mu.Unlock()
	switch state.phase {
	case runtimeClosed:
		return ErrClosed
	case runtimeCleanupFailed:
		return ErrCleanupFailed
	}
	if state.ops == nil || !state.identity.Valid() {
		return ErrInvalidInput
	}
	if err := state.ops.waitExited(ctx, state.identity); err != nil {
		return err
	}
	state.exited = true
	return nil
}

// Exited reports only that this wrapper's WaitExited call completed
// successfully. It does not re-query the process and is not itself a
// lifecycle authorization.
func (r *OwnedRuntime) Exited() bool {
	if r == nil || r.state == nil {
		return false
	}
	state := r.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.exited
}

// Close releases the duplicated process handle and its owning Job. It is
// idempotent. If cleanup fails, the state remains retryable and Close returns
// the stable ErrCleanupFailed category.
func (r *OwnedRuntime) Close() error {
	if r == nil || r.state == nil {
		return ErrInvalidInput
	}
	state := r.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.phase == runtimeClosed {
		return nil
	}
	if state.ops == nil {
		return ErrInvalidInput
	}
	if err := state.ops.close(); err != nil {
		state.phase = runtimeCleanupFailed
		return normalizeCleanupError(err)
	}
	state.phase = runtimeClosed
	return nil
}

func (OwnedRuntime) MarshalJSON() ([]byte, error) { return nil, ErrSerializationBlocked }

func (*OwnedRuntime) UnmarshalJSON([]byte) error { return ErrSerializationBlocked }

func normalizeCleanupError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrCleanupFailed) {
		return ErrCleanupFailed
	}
	return ErrCleanupFailed
}
