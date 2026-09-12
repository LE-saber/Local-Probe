//go:build windows

package runtimeowner

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsKernel is the narrow syscall seam used by the ownership core. The
// production implementation below is the only implementation used by
// NewJob; Windows unit tests can supply a fake without creating or killing a
// real process.
type windowsKernel interface {
	createJob() (windows.Handle, error)
	setJobLimits(windows.Handle) error
	closeHandle(windows.Handle) error
	assignProcess(windows.Handle, windows.Handle) error
	duplicateProcess(windows.Handle) (windows.Handle, error)
	processID(windows.Handle) (uint32, error)
	processCreationTicks(windows.Handle) (uint64, error)
	isProcessInJob(windows.Handle, windows.Handle) (bool, error)
	waitProcess(windows.Handle, uint32) (uint32, error)
	terminateJob(windows.Handle, uint32) error
}

type realWindowsKernel struct{}

func (realWindowsKernel) createJob() (windows.Handle, error) {
	return windows.CreateJobObject(nil, nil)
}

func (realWindowsKernel) setJobLimits(job windows.Handle) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// No breakaway flag is enabled. The active-process limit applies to this
	// Job tree; Windows nested-job membership is still an ancestor relation,
	// not a proof of direct/leaf membership.
	info.BasicLimitInformation.LimitFlags =
		windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
			windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	info.BasicLimitInformation.ActiveProcessLimit = 1
	_, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	return err
}

func (realWindowsKernel) closeHandle(handle windows.Handle) error {
	if handle == windows.InvalidHandle {
		return nil
	}
	return windows.CloseHandle(handle)
}

func (realWindowsKernel) assignProcess(job, process windows.Handle) error {
	return windows.AssignProcessToJobObject(job, process)
}

func (realWindowsKernel) duplicateProcess(process windows.Handle) (windows.Handle, error) {
	current := windows.CurrentProcess()
	var duplicate windows.Handle
	const access = windows.PROCESS_QUERY_LIMITED_INFORMATION |
		windows.SYNCHRONIZE |
		windows.PROCESS_SET_QUOTA |
		windows.PROCESS_TERMINATE
	if err := windows.DuplicateHandle(current, process, current, &duplicate, access, false, 0); err != nil {
		return windows.InvalidHandle, err
	}
	return duplicate, nil
}

func (realWindowsKernel) processID(process windows.Handle) (uint32, error) {
	return windows.GetProcessId(process)
}

func (realWindowsKernel) processCreationTicks(process windows.Handle) (uint64, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(process, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}

var isProcessInJobProc = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func (realWindowsKernel) isProcessInJob(process, job windows.Handle) (bool, error) {
	var result int32
	ret, _, err := isProcessInJobProc.Call(
		uintptr(process),
		uintptr(job),
		uintptr(unsafe.Pointer(&result)),
	)
	if ret == 0 {
		return false, err
	}
	return result != 0, nil
}

func (realWindowsKernel) waitProcess(process windows.Handle, milliseconds uint32) (uint32, error) {
	return windows.WaitForSingleObject(process, milliseconds)
}

func (realWindowsKernel) terminateJob(job windows.Handle, exitCode uint32) error {
	return windows.TerminateJobObject(job, exitCode)
}

// winJobOps owns the actual Job Object handle. Its mutex is held across the
// membership check and the destructive Job call, so Job.Close cannot race a
// revalidation and replace the handle underneath it.
type winJobOps struct {
	mu             sync.Mutex
	api            windowsKernel
	handle         windows.Handle
	closed         bool
	cleanupFailed  bool
	needsTerminate bool
	pendingHandles []windows.Handle
}

func NewJob() (*Job, error) {
	token, err := newJobTag()
	if err != nil {
		return nil, ErrUnavailable
	}
	return newJobWithKernel(realWindowsKernel{}, token)
}

func newJobWithKernel(api windowsKernel, token [16]byte) (*Job, error) {
	handle, err := api.createJob()
	if err != nil || handle == windows.InvalidHandle || handle == 0 {
		return nil, ErrUnavailable
	}
	job := &winJobOps{api: api, handle: handle}
	if err := api.setJobLimits(handle); err != nil {
		if closeErr := job.close(); closeErr != nil {
			return &Job{state: &jobState{
				ops: job, token: token, phase: jobCleanupFailed,
			}}, ErrCleanupFailed
		}
		return nil, ErrUnavailable
	}
	return &Job{state: &jobState{ops: job, token: token}}, nil
}

func newJobTag() ([16]byte, error) {
	var tag [16]byte
	for {
		if _, err := rand.Read(tag[:]); err != nil {
			return [16]byte{}, err
		}
		if tag != [16]byte{} {
			return tag, nil
		}
	}
}

func (j *winJobOps) ownProcess(processHandle uintptr, token [16]byte) (runtimeAttachment, error) {
	if processHandle == 0 || isRejectedWindowsProcessHandle(processHandle) {
		return runtimeAttachment{}, ErrInvalidInput
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.handle == windows.InvalidHandle || j.handle == 0 {
		return runtimeAttachment{}, ErrClosed
	}
	if j.cleanupFailed {
		return runtimeAttachment{}, ErrCleanupFailed
	}
	process := windows.Handle(processHandle)
	// Duplicate first. All subsequent operations use this package-owned
	// duplicate; the caller's handle may be closed immediately after return and
	// can never be swapped underneath the assignment/query sequence.
	duplicate, err := j.api.duplicateProcess(process)
	if err != nil || duplicate == windows.InvalidHandle || duplicate == 0 {
		return runtimeAttachment{}, ErrUnavailable
	}
	// Assignment is performed using only the duplicate. If assignment fails,
	// no process has entered this Job and the duplicate is the only cleanup
	// obligation.
	if err := j.api.assignProcess(j.handle, duplicate); err != nil {
		if closeErr := j.closeHandleLocked(duplicate); closeErr != nil {
			j.cleanupFailed = true
			return runtimeAttachment{}, ErrCleanupFailed
		}
		return runtimeAttachment{}, mapAssignmentError(err)
	}
	pid, err := j.api.processID(duplicate)
	if err != nil || pid == 0 {
		return runtimeAttachment{}, j.failClaimLocked(duplicate, ErrUnavailable)
	}
	creationTicks, err := j.api.processCreationTicks(duplicate)
	if err != nil || creationTicks == 0 {
		return runtimeAttachment{}, j.failClaimLocked(duplicate, ErrUnavailable)
	}
	inJob, err := j.api.isProcessInJob(duplicate, j.handle)
	if err != nil {
		return runtimeAttachment{}, j.failClaimLocked(duplicate, ErrUnavailable)
	}
	if !inJob {
		return runtimeAttachment{}, j.failClaimLocked(duplicate, ErrNotOwned)
	}
	identity := Identity{
		pid:           pid,
		creationTicks: creationTicks,
		jobTag:        token,
		valid:         true,
	}
	return runtimeAttachment{
		identity: identity,
		ops: &winRuntimeOps{
			api:     j.api,
			process: duplicate,
			job:     j,
			jobTag:  token,
		},
	}, nil
}

// failClaimLocked is used only after assignment succeeded. It attempts the
// full cleanup sequence without clearing any handle before its individual
// close succeeds. Failed handles stay in the owner for Close retry.
func (j *winJobOps) failClaimLocked(duplicate windows.Handle, result error) error {
	cleanupFailed := false
	j.needsTerminate = true
	if j.handle != windows.InvalidHandle && j.handle != 0 {
		// Termination is best effort; closing a KILL_ON_JOB_CLOSE Job below is
		// the fail-safe fallback only after the termination call has succeeded.
		// If it fails, retain both handles and let Close retry; no handle is
		// cleared while cleanup remains unproven.
		if err := j.api.terminateJob(j.handle, 1); err != nil {
			cleanupFailed = true
		}
	}
	if cleanupFailed {
		if duplicate != windows.InvalidHandle && duplicate != 0 {
			j.pendingHandles = append(j.pendingHandles, duplicate)
		}
		j.cleanupFailed = true
		return ErrCleanupFailed
	}
	j.needsTerminate = false
	if duplicate != windows.InvalidHandle && duplicate != 0 {
		if err := j.closeHandleLocked(duplicate); err != nil {
			cleanupFailed = true
		}
	}
	if j.handle != windows.InvalidHandle && j.handle != 0 {
		if err := j.api.closeHandle(j.handle); err != nil {
			cleanupFailed = true
		} else {
			j.handle = windows.InvalidHandle
		}
	}
	if cleanupFailed || len(j.pendingHandles) != 0 {
		j.cleanupFailed = true
		return ErrCleanupFailed
	}
	j.closed = true
	return result
}

func (j *winJobOps) closeHandleLocked(handle windows.Handle) error {
	if handle == windows.InvalidHandle || handle == 0 {
		return nil
	}
	if err := j.api.closeHandle(handle); err != nil {
		j.pendingHandles = append(j.pendingHandles, handle)
		return ErrCleanupFailed
	}
	return nil
}

func (j *winJobOps) close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	cleanupFailed := false
	if j.needsTerminate && j.handle != windows.InvalidHandle && j.handle != 0 {
		if err := j.api.terminateJob(j.handle, 1); err != nil {
			j.cleanupFailed = true
			return ErrCleanupFailed
		}
		j.needsTerminate = false
	}
	remaining := j.pendingHandles[:0]
	for _, handle := range j.pendingHandles {
		if err := j.api.closeHandle(handle); err != nil {
			remaining = append(remaining, handle)
			cleanupFailed = true
		}
	}
	j.pendingHandles = remaining
	if j.handle != windows.InvalidHandle && j.handle != 0 {
		if err := j.api.closeHandle(j.handle); err != nil {
			cleanupFailed = true
		} else {
			j.handle = windows.InvalidHandle
		}
	}
	if cleanupFailed || len(j.pendingHandles) != 0 {
		j.cleanupFailed = true
		return ErrCleanupFailed
	}
	j.cleanupFailed = false
	j.closed = true
	return nil
}

func (j *winJobOps) withHandle(fn func(windows.Handle) error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.handle == windows.InvalidHandle || j.handle == 0 {
		return ErrClosed
	}
	if j.cleanupFailed {
		return ErrCleanupFailed
	}
	return fn(j.handle)
}

type winRuntimeOps struct {
	mu            sync.Mutex
	api           windowsKernel
	process       windows.Handle
	job           *winJobOps
	jobTag        [16]byte
	closed        bool
	cleanupFailed bool
}

func (r *winRuntimeOps) snapshot(ctx context.Context, expected Identity) (Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, contextError(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.process == windows.InvalidHandle || r.process == 0 {
		return Snapshot{}, ErrClosed
	}
	if r.cleanupFailed {
		return Snapshot{}, ErrCleanupFailed
	}
	if !expected.Valid() || expected.jobTag != r.jobTag {
		return Snapshot{}, ErrIdentityChanged
	}
	var result Snapshot
	err := r.job.withHandle(func(job windows.Handle) error {
		var err error
		result, err = r.observeLocked(ctx, job)
		return err
	})
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *winRuntimeOps) observeLocked(ctx context.Context, job windows.Handle) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, contextError(err)
	}
	pid, err := r.api.processID(r.process)
	if err != nil || pid == 0 {
		return Snapshot{}, ErrUnavailable
	}
	creationTicks, err := r.api.processCreationTicks(r.process)
	if err != nil || creationTicks == 0 {
		return Snapshot{}, ErrUnavailable
	}
	ancestorMember, err := r.api.isProcessInJob(r.process, job)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	identity := Identity{
		pid:           pid,
		creationTicks: creationTicks,
		jobTag:        r.jobTag,
		valid:         true,
	}
	return Snapshot{
		identity:       identity,
		ancestorMember: ancestorMember,
		observedAt:     time.Now().UTC(),
	}, nil
}

func (r *winRuntimeOps) terminate(ctx context.Context, expected Identity) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.process == windows.InvalidHandle || r.process == 0 {
		return ErrClosed
	}
	if r.cleanupFailed {
		return ErrCleanupFailed
	}
	if !expected.Valid() || expected.jobTag != r.jobTag {
		return ErrIdentityChanged
	}
	return r.job.withHandle(func(job windows.Handle) error {
		snapshot, err := r.observeLocked(ctx, job)
		if err != nil {
			return err
		}
		if !snapshot.ancestorMember {
			return ErrNotOwned
		}
		if !snapshot.identity.equal(expected) {
			return ErrIdentityChanged
		}
		if err := ctx.Err(); err != nil {
			return contextError(err)
		}
		if err := r.api.terminateJob(job, 1); err != nil {
			return ErrUnavailable
		}
		return nil
	})
}

const waitSliceMilliseconds = 50

func (r *winRuntimeOps) waitExited(ctx context.Context, expected Identity) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return contextError(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.process == windows.InvalidHandle || r.process == 0 {
		return ErrClosed
	}
	if r.cleanupFailed {
		return ErrCleanupFailed
	}
	if !expected.Valid() || expected.jobTag != r.jobTag {
		return ErrIdentityChanged
	}
	// Query identity from the retained process object before interpreting its
	// signaled state. An exited process can cease to be reported as a current
	// Job member, but its process handle remains the same identity and signals
	// completion. In that case membership is no longer required to confirm the
	// already-completed exit.
	pid, err := r.api.processID(r.process)
	if err != nil || pid == 0 {
		return ErrUnavailable
	}
	creationTicks, err := r.api.processCreationTicks(r.process)
	if err != nil || creationTicks == 0 {
		return ErrUnavailable
	}
	observedIdentity := Identity{
		pid: pid, creationTicks: creationTicks, jobTag: r.jobTag, valid: true,
	}
	if !observedIdentity.equal(expected) {
		return ErrIdentityChanged
	}
	result, err := r.api.waitProcess(r.process, 0)
	if err != nil {
		return ErrUnavailable
	}
	if result == windows.WAIT_OBJECT_0 {
		return nil
	}
	if result != waitTimeout {
		return ErrUnavailable
	}
	return r.job.withHandle(func(job windows.Handle) error {
		snapshot, err := r.observeLocked(ctx, job)
		if err != nil {
			return err
		}
		if !snapshot.ancestorMember {
			// The process may have exited between the initial zero-time wait and
			// this membership observation. Close that window before classifying
			// the handle as no longer owned.
			result, waitErr := r.api.waitProcess(r.process, 0)
			if waitErr != nil {
				return ErrUnavailable
			}
			if result == windows.WAIT_OBJECT_0 {
				return nil
			}
			if result != waitTimeout {
				return ErrUnavailable
			}
			return ErrNotOwned
		}
		if !snapshot.identity.equal(expected) {
			return ErrIdentityChanged
		}
		for {
			if err := ctx.Err(); err != nil {
				return contextError(err)
			}
			result, err := r.api.waitProcess(r.process, waitSliceMilliseconds)
			if err != nil {
				return ErrUnavailable
			}
			switch result {
			case windows.WAIT_OBJECT_0:
				return nil
			case waitTimeout:
				continue
			default:
				return ErrUnavailable
			}
		}
	})
}

func (r *winRuntimeOps) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	cleanupFailed := false
	if r.process != windows.InvalidHandle && r.process != 0 {
		if err := r.api.closeHandle(r.process); err != nil {
			cleanupFailed = true
		} else {
			r.process = windows.InvalidHandle
		}
	}
	if err := r.job.close(); err != nil {
		cleanupFailed = true
	}
	if cleanupFailed {
		r.cleanupFailed = true
		return ErrCleanupFailed
	}
	r.cleanupFailed = false
	r.closed = true
	return nil
}

const waitTimeout = 258 // WAIT_TIMEOUT, kept local because x/sys exposes ERROR_TIMEOUT.

func contextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrDeadlineExceeded
	}
	if errors.Is(err, context.Canceled) {
		return ErrCancelled
	}
	return ErrUnavailable
}

func mapAssignmentError(err error) error {
	if err == nil {
		return ErrUnavailable
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_INVALID_PARAMETER) ||
		errors.Is(err, windows.ERROR_PROCESS_IN_JOB) {
		return ErrNotOwned
	}
	return ErrUnavailable
}

func isRejectedWindowsProcessHandle(raw uintptr) bool {
	// -1 is both the current-process pseudo handle and INVALID_HANDLE_VALUE;
	// -2 is the current-thread pseudo handle. Neither is accepted as an
	// ownership input because it is not a stable child-process handle.
	return raw == ^uintptr(0) || raw == ^uintptr(1)
}
