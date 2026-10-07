//go:build windows

package runtimeowner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

type fakeWindowsKernel struct {
	mu sync.Mutex

	jobHandle       windows.Handle
	setJobErr       error
	duplicate       windows.Handle
	pid             uint32
	creationTicks   uint64
	inJob           bool
	assignErr       error
	duplicateErr    error
	queryErr        error
	memberErr       error
	terminateErr    error
	terminateErrors []error
	waitErr         error
	waitResults     []uint32
	closeErrors     []error
	events          []string
	assignCalls     int
	duplicateCalls  int
	terminateCalls  int
	waitCalls       int
	closedHandles   []windows.Handle
	assignedJob     windows.Handle
	assignedProcess windows.Handle
}

func (f *fakeWindowsKernel) createJob() (windows.Handle, error) { return f.jobHandle, nil }
func (f *fakeWindowsKernel) setJobLimits(windows.Handle) error  { return f.setJobErr }

func (f *fakeWindowsKernel) closeHandle(handle windows.Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "close")
	f.closedHandles = append(f.closedHandles, handle)
	if len(f.closeErrors) != 0 {
		err := f.closeErrors[0]
		f.closeErrors = f.closeErrors[1:]
		return err
	}
	return nil
}

func (f *fakeWindowsKernel) assignProcess(job, process windows.Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "assign")
	f.assignCalls++
	f.assignedJob = job
	f.assignedProcess = process
	return f.assignErr
}

func (f *fakeWindowsKernel) duplicateProcess(windows.Handle) (windows.Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "duplicate")
	f.duplicateCalls++
	return f.duplicate, f.duplicateErr
}

func (f *fakeWindowsKernel) processID(windows.Handle) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queryErr != nil {
		return 0, f.queryErr
	}
	return f.pid, nil
}

func (f *fakeWindowsKernel) processCreationTicks(windows.Handle) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.queryErr != nil {
		return 0, f.queryErr
	}
	return f.creationTicks, nil
}

func (f *fakeWindowsKernel) isProcessInJob(windows.Handle, windows.Handle) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.memberErr != nil {
		return false, f.memberErr
	}
	return f.inJob, nil
}

func (f *fakeWindowsKernel) waitProcess(windows.Handle, uint32) (uint32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitCalls++
	if f.waitErr != nil {
		return 0, f.waitErr
	}
	if len(f.waitResults) == 0 {
		return windows.WAIT_OBJECT_0, nil
	}
	result := f.waitResults[0]
	f.waitResults = f.waitResults[1:]
	return result, nil
}

func (f *fakeWindowsKernel) terminateJob(windows.Handle, uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "terminate")
	f.terminateCalls++
	if len(f.terminateErrors) != 0 {
		err := f.terminateErrors[0]
		f.terminateErrors = f.terminateErrors[1:]
		return err
	}
	return f.terminateErr
}

func TestWindowsNewJobCreatesOnlyAnEmptyConfiguredJob(t *testing.T) {
	if ProductionReady() {
		t.Fatal("production readiness must remain closed until broker integration")
	}
	job, err := NewJob()
	if err != nil {
		t.Fatalf("NewJob: %v", err)
	}
	if job == nil {
		t.Fatal("NewJob returned nil job")
	}
	if err := job.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestWindowsNewJobLimitFailureRetainsHandleWhenCleanupFails(t *testing.T) {
	closeErr := errors.New("synthetic close failure")
	api := &fakeWindowsKernel{
		jobHandle: windows.Handle(9), setJobErr: errors.New("synthetic limit failure"),
		closeErrors: []error{closeErr, nil},
	}
	job, err := newJobWithKernel(api, [16]byte{1})
	if !errors.Is(err, ErrCleanupFailed) || job == nil {
		t.Fatalf("newJobWithKernel = job %#v, error %v", job, err)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("retry Close: %v", err)
	}
}

func TestWindowsFakeDuplicatesBeforeAssignAndBindsPIDCreationAndJob(t *testing.T) {
	tag := [16]byte{7}
	api := &fakeWindowsKernel{
		jobHandle:     windows.Handle(10),
		duplicate:     windows.Handle(11),
		pid:           101,
		creationTicks: 202,
		inJob:         true,
	}
	jobOps := &winJobOps{api: api, handle: api.jobHandle}
	job := &Job{state: &jobState{ops: jobOps, token: tag}}
	runtime, err := job.OwnProcess(22)
	if err != nil {
		t.Fatalf("OwnProcess: %v", err)
	}
	api.mu.Lock()
	events := append([]string(nil), api.events...)
	api.mu.Unlock()
	if len(events) < 2 || events[0] != "duplicate" || events[1] != "assign" {
		t.Fatalf("ownership syscall order = %#v, want duplicate then assign", events)
	}
	api.mu.Lock()
	assignedJob, assignedProcess := api.assignedJob, api.assignedProcess
	api.mu.Unlock()
	if assignedJob != api.jobHandle || assignedProcess != api.duplicate {
		t.Fatalf("AssignProcess handles = job %#x/process %#x, want job %#x/duplicate %#x", assignedJob, assignedProcess, api.jobHandle, api.duplicate)
	}
	identity := runtime.Identity()
	if identity.PID() != 101 || identity.CreationTimeTicks() != 202 || !identity.Valid() {
		t.Fatalf("identity = %#v", identity)
	}
	snapshot, err := runtime.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !snapshot.InOwnedJobTree() || snapshot.PID() != 101 || snapshot.CreationTimeTicks() != 202 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if err := runtime.Terminate(context.Background()); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	api.mu.Lock()
	assignCalls, duplicateCalls, terminateCalls := api.assignCalls, api.duplicateCalls, api.terminateCalls
	api.mu.Unlock()
	if assignCalls != 1 || duplicateCalls != 1 || terminateCalls != 1 {
		t.Fatalf("calls assign=%d duplicate=%d terminate=%d", assignCalls, duplicateCalls, terminateCalls)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("runtime Close: %v", err)
	}
}

func TestWindowsFakePIDReuseAndForeignJobNeverTerminate(t *testing.T) {
	tests := []struct {
		name  string
		inJob bool
		pid   uint32
		want  error
	}{
		{name: "foreign job tree", inJob: false, pid: 303, want: ErrNotOwned},
		{name: "pid reuse", inJob: true, pid: 304, want: ErrIdentityChanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tag := [16]byte{8}
			api := &fakeWindowsKernel{
				jobHandle:     windows.Handle(20),
				duplicate:     windows.Handle(21),
				pid:           303,
				creationTicks: 404,
				inJob:         true,
			}
			jobOps := &winJobOps{api: api, handle: api.jobHandle}
			job := &Job{state: &jobState{ops: jobOps, token: tag}}
			runtime, err := job.OwnProcess(30)
			if err != nil {
				t.Fatalf("OwnProcess: %v", err)
			}
			api.mu.Lock()
			api.inJob, api.pid = test.inJob, test.pid
			api.mu.Unlock()
			if err := runtime.Revalidate(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("Revalidate = %v, want %v", err, test.want)
			}
			if err := runtime.Terminate(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("Terminate = %v, want %v", err, test.want)
			}
			api.mu.Lock()
			terminateCalls := api.terminateCalls
			api.mu.Unlock()
			if terminateCalls != 0 {
				t.Fatalf("terminate calls = %d, want 0", terminateCalls)
			}
			_ = runtime.Close()
		})
	}
}

func TestWindowsFakeWaitExitedRequiresMembershipAndSeparatesDispatch(t *testing.T) {
	tag := [16]byte{9}
	api := &fakeWindowsKernel{
		jobHandle:     windows.Handle(30),
		duplicate:     windows.Handle(31),
		pid:           505,
		creationTicks: 606,
		inJob:         true,
		waitResults:   []uint32{waitTimeout, windows.WAIT_OBJECT_0},
	}
	jobOps := &winJobOps{api: api, handle: api.jobHandle}
	job := &Job{state: &jobState{ops: jobOps, token: tag}}
	runtime, err := job.OwnProcess(32)
	if err != nil {
		t.Fatalf("OwnProcess: %v", err)
	}
	if err := runtime.Terminate(context.Background()); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	if runtime.Exited() {
		t.Fatal("Terminate incorrectly confirmed exit")
	}
	if err := runtime.WaitExited(context.Background()); err != nil {
		t.Fatalf("WaitExited: %v", err)
	}
	if !runtime.Exited() {
		t.Fatal("WaitExited did not record confirmed exit")
	}
	api.mu.Lock()
	waitCalls := api.waitCalls
	api.mu.Unlock()
	if waitCalls != 2 {
		t.Fatalf("wait calls = %d, want 2", waitCalls)
	}
	_ = runtime.Close()
}

func TestWindowsFakeWaitExitedAcceptsSignaledProcessAfterJobDeparture(t *testing.T) {
	api := &fakeWindowsKernel{
		jobHandle: windows.Handle(33), duplicate: windows.Handle(34),
		pid: 507, creationTicks: 608, inJob: true,
		waitResults: []uint32{windows.WAIT_OBJECT_0},
	}
	job := &Job{state: &jobState{ops: &winJobOps{api: api, handle: api.jobHandle}, token: [16]byte{15}}}
	runtime, err := job.OwnProcess(35)
	if err != nil {
		t.Fatalf("OwnProcess: %v", err)
	}
	api.mu.Lock()
	api.inJob = false
	api.mu.Unlock()
	if err := runtime.WaitExited(context.Background()); err != nil {
		t.Fatalf("WaitExited after Job departure: %v", err)
	}
	if !runtime.Exited() {
		t.Fatal("signaled process was not recorded as exited")
	}
	_ = runtime.Close()
}

func TestWindowsFakeWaitExitedClosesExitDuringMembershipWindow(t *testing.T) {
	api := &fakeWindowsKernel{
		jobHandle: windows.Handle(36), duplicate: windows.Handle(37),
		pid: 509, creationTicks: 610, inJob: true,
		waitResults: []uint32{waitTimeout, windows.WAIT_OBJECT_0},
	}
	job := &Job{state: &jobState{ops: &winJobOps{api: api, handle: api.jobHandle}, token: [16]byte{16}}}
	runtime, err := job.OwnProcess(38)
	if err != nil {
		t.Fatalf("OwnProcess: %v", err)
	}
	api.mu.Lock()
	api.inJob = false
	api.mu.Unlock()
	if err := runtime.WaitExited(context.Background()); err != nil {
		t.Fatalf("WaitExited during membership window: %v", err)
	}
	if !runtime.Exited() {
		t.Fatal("membership-window exit was not recorded")
	}
	_ = runtime.Close()
}

func TestWindowsCleanupFailureRetainsHandlesForRetry(t *testing.T) {
	closeErr := errors.New("synthetic close failure")
	api := &fakeWindowsKernel{
		jobHandle:     windows.Handle(40),
		duplicate:     windows.Handle(41),
		pid:           707,
		creationTicks: 808,
		inJob:         true,
		closeErrors:   []error{closeErr, nil},
	}
	jobOps := &winJobOps{api: api, handle: api.jobHandle}
	job := &Job{state: &jobState{ops: jobOps, token: [16]byte{10}}}
	runtime, err := job.OwnProcess(42)
	if err != nil {
		t.Fatalf("OwnProcess: %v", err)
	}
	if err := runtime.Close(); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("first runtime Close = %v, want cleanup_failed", err)
	}
	api.mu.Lock()
	processHandleAfterFailure := runtime.state.ops.(*winRuntimeOps).process
	api.mu.Unlock()
	if processHandleAfterFailure != api.duplicate {
		t.Fatalf("process handle cleared after failed cleanup: %#x", processHandleAfterFailure)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("retry runtime Close = %v", err)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("job Close after runtime cleanup = %v", err)
	}
}

func TestWindowsClaimFailureCleanupCanBeRetried(t *testing.T) {
	closeErr := errors.New("synthetic close failure")
	api := &fakeWindowsKernel{
		jobHandle:     windows.Handle(50),
		duplicate:     windows.Handle(51),
		pid:           909,
		creationTicks: 1001,
		inJob:         true,
		queryErr:      errors.New("query failure"),
		closeErrors:   []error{closeErr, nil},
	}
	jobOps := &winJobOps{api: api, handle: api.jobHandle}
	job := &Job{state: &jobState{ops: jobOps, token: [16]byte{11}}}
	if _, err := job.OwnProcess(52); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("claim cleanup error = %v, want cleanup_failed", err)
	}
	api.mu.Lock()
	handleAfterFailure := jobOps.handle
	pending := len(jobOps.pendingHandles)
	api.mu.Unlock()
	if handleAfterFailure != windows.InvalidHandle || pending != 1 {
		t.Fatalf("claim cleanup state handle=%#x pending=%d", handleAfterFailure, pending)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("retry claim cleanup = %v", err)
	}
}

func TestWindowsClaimTerminationFailureRetainsJobAndDuplicate(t *testing.T) {
	terminateErr := errors.New("synthetic termination failure")
	api := &fakeWindowsKernel{
		jobHandle:       windows.Handle(55),
		duplicate:       windows.Handle(56),
		pid:             1009,
		creationTicks:   1101,
		inJob:           true,
		queryErr:        errors.New("query failure"),
		terminateErrors: []error{terminateErr, nil},
	}
	jobOps := &winJobOps{api: api, handle: api.jobHandle}
	job := &Job{state: &jobState{ops: jobOps, token: [16]byte{13}}}
	if _, err := job.OwnProcess(57); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("claim cleanup error = %v, want cleanup_failed", err)
	}
	api.mu.Lock()
	handleAfterFailure := jobOps.handle
	pending := len(jobOps.pendingHandles)
	api.mu.Unlock()
	if handleAfterFailure != api.jobHandle || pending != 1 {
		t.Fatalf("termination failure discarded owner state handle=%#x pending=%d", handleAfterFailure, pending)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("retry termination cleanup = %v", err)
	}
}

func TestWindowsRejectsPseudoAndInvalidProcessHandles(t *testing.T) {
	tag := [16]byte{12}
	api := &fakeWindowsKernel{jobHandle: windows.Handle(60), duplicate: windows.Handle(61), pid: 1, creationTicks: 1, inJob: true}
	job := &Job{state: &jobState{ops: &winJobOps{api: api, handle: api.jobHandle}, token: tag}}
	for _, handle := range []uintptr{0, ^uintptr(0), ^uintptr(1)} {
		if _, err := job.OwnProcess(handle); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("handle %#x: error = %v, want %v", handle, err, ErrInvalidInput)
		}
	}
}
