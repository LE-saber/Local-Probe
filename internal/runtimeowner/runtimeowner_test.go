package runtimeowner

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func testIdentity(tag [16]byte, pid uint32, creation uint64) Identity {
	return Identity{pid: pid, creationTicks: creation, jobTag: tag, valid: true}
}

type fakeRuntimeOps struct {
	mu              sync.Mutex
	snapshotValue   Snapshot
	snapshotErr     error
	terminateErr    error
	waitExitedErr   error
	closeErrors     []error
	closed          bool
	terminateCalls  int
	waitExitedCalls int
	closeCalls      int
}

func (f *fakeRuntimeOps) snapshot(context.Context, Identity) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshotValue, f.snapshotErr
}

func (f *fakeRuntimeOps) terminate(context.Context, Identity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminateCalls++
	return f.terminateErr
}

func (f *fakeRuntimeOps) waitExited(context.Context, Identity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waitExitedCalls++
	return f.waitExitedErr
}

func (f *fakeRuntimeOps) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	if len(f.closeErrors) != 0 {
		err := f.closeErrors[0]
		f.closeErrors = f.closeErrors[1:]
		return err
	}
	f.closed = true
	return nil
}

type fakeJobOps struct {
	mu          sync.Mutex
	attachment  runtimeAttachment
	err         error
	closeErrors []error
	closed      bool
	calls       int
	closeCalls  int
}

func (f *fakeJobOps) ownProcess(uintptr, [16]byte) (runtimeAttachment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.attachment, f.err
}

func (f *fakeJobOps) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	if len(f.closeErrors) != 0 {
		err := f.closeErrors[0]
		f.closeErrors = f.closeErrors[1:]
		return err
	}
	f.closed = true
	return nil
}

func TestIdentityAndRuntimeAreNotJSONCapabilities(t *testing.T) {
	tag := [16]byte{1}
	identity := testIdentity(tag, 17, 99)
	if !identity.Valid() || identity.PID() != 17 || identity.CreationTimeTicks() != 99 {
		t.Fatalf("identity accessors lost valid identity: %#v", identity)
	}
	if _, err := json.Marshal(identity); !errors.Is(err, ErrSerializationBlocked) {
		t.Fatalf("identity marshal error = %v", err)
	}
	var decoded Identity
	if err := json.Unmarshal([]byte(`{"pid":17,"creationTicks":99}`), &decoded); !errors.Is(err, ErrSerializationBlocked) {
		t.Fatalf("identity unmarshal error = %v", err)
	}
	if decoded.Valid() {
		t.Fatal("JSON manufactured a valid identity")
	}

	ops := &fakeRuntimeOps{snapshotValue: Snapshot{identity: identity, ancestorMember: true}}
	job := &Job{
		state: &jobState{
			ops:   &fakeJobOps{attachment: runtimeAttachment{identity: identity, ops: ops}},
			token: tag,
		},
	}
	runtime, err := job.OwnProcess(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(runtime); !errors.Is(err, ErrSerializationBlocked) {
		t.Fatalf("runtime marshal error = %v", err)
	}
	if _, err := json.Marshal(job); !errors.Is(err, ErrSerializationBlocked) {
		t.Fatalf("job marshal error = %v", err)
	}
	var decodedRuntime OwnedRuntime
	if err := json.Unmarshal([]byte(`{"pid":17}`), &decodedRuntime); !errors.Is(err, ErrSerializationBlocked) {
		t.Fatalf("runtime unmarshal error = %v", err)
	}
	if err := decodedRuntime.Revalidate(context.Background()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero runtime revalidate error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnProcessBindsTypedIdentityAndRejectsSecondClaim(t *testing.T) {
	tag := [16]byte{2}
	identity := testIdentity(tag, 21, 101)
	ops := &fakeRuntimeOps{snapshotValue: Snapshot{identity: identity, ancestorMember: true}}
	jobOps := &fakeJobOps{attachment: runtimeAttachment{identity: identity, ops: ops}}
	job := &Job{state: &jobState{ops: jobOps, token: tag}}

	if _, err := job.OwnProcess(0); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero handle error = %v", err)
	}
	runtime, err := job.OwnProcess(123)
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.Identity(); !got.equal(identity) {
		t.Fatalf("runtime identity = %#v, want %#v", got, identity)
	}
	if _, err := job.OwnProcess(456); !errors.Is(err, ErrAlreadyOwned) {
		t.Fatalf("second claim error = %v", err)
	}
	jobOps.mu.Lock()
	calls := jobOps.calls
	jobOps.mu.Unlock()
	if calls != 1 {
		t.Fatalf("job own calls = %d, want 1", calls)
	}
	if err := runtime.Revalidate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Revalidate(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed runtime revalidate error = %v", err)
	}
}

func TestRevalidateAndTerminateFailClosedOnForeignOrChangedIdentity(t *testing.T) {
	tests := []struct {
		name     string
		snapshot Snapshot
		want     error
	}{
		{
			name:     "foreign job tree",
			snapshot: Snapshot{identity: testIdentity([16]byte{3}, 30, 300), ancestorMember: false},
			want:     ErrNotOwned,
		},
		{
			name:     "pid reuse",
			snapshot: Snapshot{identity: testIdentity([16]byte{3}, 31, 301), ancestorMember: true},
			want:     ErrIdentityChanged,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tag := [16]byte{3}
			expected := testIdentity(tag, 30, 300)
			ops := &fakeRuntimeOps{snapshotValue: test.snapshot}
			runtime := &OwnedRuntime{state: &ownedRuntimeState{identity: expected, ops: ops}}
			if err := runtime.Revalidate(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("revalidate error = %v, want %v", err, test.want)
			}
			if err := runtime.Terminate(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("terminate error = %v, want %v", err, test.want)
			}
			ops.mu.Lock()
			calls := ops.terminateCalls
			ops.mu.Unlock()
			if calls != 0 {
				t.Fatalf("terminate calls = %d, want 0", calls)
			}
		})
	}
}

func TestWaitExitedSeparatesDispatchFromConfirmedExit(t *testing.T) {
	tag := [16]byte{4}
	identity := testIdentity(tag, 40, 400)
	ops := &fakeRuntimeOps{snapshotValue: Snapshot{identity: identity, ancestorMember: true}}
	runtime := &OwnedRuntime{state: &ownedRuntimeState{identity: identity, ops: ops}}
	if runtime.Exited() {
		t.Fatal("runtime initially marked exited")
	}
	if err := runtime.Terminate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runtime.Exited() {
		t.Fatal("Terminate incorrectly confirmed exit")
	}
	if err := runtime.WaitExited(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !runtime.Exited() {
		t.Fatal("WaitExited did not record confirmed exit")
	}
	ops.mu.Lock()
	terminateCalls, waitCalls := ops.terminateCalls, ops.waitExitedCalls
	ops.mu.Unlock()
	if terminateCalls != 1 || waitCalls != 1 {
		t.Fatalf("calls terminate=%d wait=%d", terminateCalls, waitCalls)
	}
}

func TestCleanupFailureRemainsRetryableAndSharedAcrossCopies(t *testing.T) {
	jobOps := &fakeJobOps{closeErrors: []error{ErrUnavailable}}
	job := &Job{state: &jobState{ops: jobOps, token: [16]byte{5}}}
	jobCopy := *job
	if err := job.Close(); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("first Job.Close = %v, want cleanup failure", err)
	}
	if _, err := jobCopy.OwnProcess(9); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("claim during cleanup failure = %v", err)
	}
	if err := jobCopy.Close(); err != nil {
		t.Fatalf("retry Job.Close = %v", err)
	}
	if err := job.Close(); err != nil {
		t.Fatalf("closed Job.Close = %v", err)
	}

	tag := [16]byte{6}
	identity := testIdentity(tag, 60, 600)
	runtimeOps := &fakeRuntimeOps{
		snapshotValue: Snapshot{identity: identity, ancestorMember: true},
		closeErrors:   []error{ErrUnavailable},
	}
	runtime := &OwnedRuntime{state: &ownedRuntimeState{identity: identity, ops: runtimeOps}}
	runtimeCopy := *runtime
	if err := runtime.Close(); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("first Runtime.Close = %v, want cleanup failure", err)
	}
	if err := runtimeCopy.Revalidate(context.Background()); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("revalidate during cleanup failure = %v", err)
	}
	if err := runtimeCopy.Close(); err != nil {
		t.Fatalf("retry Runtime.Close = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("closed Runtime.Close = %v", err)
	}
}

func TestForeignAttachmentIsRejectedAndClosed(t *testing.T) {
	jobTag := [16]byte{7}
	foreignOps := &fakeRuntimeOps{snapshotValue: Snapshot{identity: testIdentity([16]byte{8}, 70, 700), ancestorMember: true}}
	job := &Job{state: &jobState{
		ops:   &fakeJobOps{attachment: runtimeAttachment{identity: testIdentity([16]byte{8}, 70, 700), ops: foreignOps}},
		token: jobTag,
	}}
	if _, err := job.OwnProcess(1); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("foreign attachment error = %v", err)
	}
	foreignOps.mu.Lock()
	closed := foreignOps.closed
	foreignOps.mu.Unlock()
	if !closed {
		t.Fatal("foreign attachment was not closed")
	}
}

func TestConcurrentRevalidationAndCloseIsSafe(t *testing.T) {
	tag := [16]byte{9}
	identity := testIdentity(tag, 90, 900)
	ops := &fakeRuntimeOps{snapshotValue: Snapshot{identity: identity, ancestorMember: true}}
	runtime := &OwnedRuntime{state: &ownedRuntimeState{identity: identity, ops: ops}}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = runtime.Revalidate(context.Background())
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = runtime.Close()
	}()
	wg.Wait()
	if err := runtime.Revalidate(context.Background()); !errors.Is(err, ErrClosed) && !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("post-close revalidate error = %v", err)
	}
}
