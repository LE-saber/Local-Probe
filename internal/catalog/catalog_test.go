package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type testScope struct {
	mu            sync.RWMutex
	connection    string
	profile       string
	revision      string
	valid         bool
	roots         map[string]bool
	denyPaths     map[string]bool
	validateHook  func(int)
	validateCalls int
}

func newTestScope(connection, profile, revision string) *testScope {
	return &testScope{connection: connection, profile: profile, revision: revision, valid: true, roots: map[string]bool{"workspace": true}, denyPaths: map[string]bool{}}
}

func (s *testScope) Validate() error {
	s.mu.Lock()
	s.validateCalls++
	callNumber := s.validateCalls
	valid := s.valid
	hook := s.validateHook
	s.mu.Unlock()
	if hook != nil {
		hook(callNumber)
	}
	if !valid {
		return errors.New("revoked")
	}
	return nil
}
func (s *testScope) ConnectionID() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.connection }
func (s *testScope) ProfileID() string    { s.mu.RLock(); defer s.mu.RUnlock(); return s.profile }
func (s *testScope) Revision() string     { s.mu.RLock(); defer s.mu.RUnlock(); return s.revision }
func (s *testScope) AllowsRoot(root string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.valid && s.roots[root]
}
func (s *testScope) AllowsPath(root, path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.valid && s.roots[root] && !s.denyPaths[root+"/"+path]
}
func (s *testScope) revoke() { s.mu.Lock(); s.valid = false; s.mu.Unlock() }
func (s *testScope) setRevision(revision string) {
	s.mu.Lock()
	s.revision = revision
	s.valid = true
	s.mu.Unlock()
}
func (s *testScope) denyPath(root, path string) {
	s.mu.Lock()
	s.denyPaths[root+"/"+path] = true
	s.mu.Unlock()
}
func (s *testScope) setValidateHook(hook func(int)) {
	s.mu.Lock()
	s.validateHook = hook
	s.validateCalls = 0
	s.mu.Unlock()
}

func candidate(path string, size int64) Candidate {
	return Candidate{RootID: "workspace", Path: path, Kind: KindRegular, Metadata: Metadata{SizeBytes: size, VersionToken: "m1.token"}}
}

func newTestCatalog(t *testing.T, scope *testScope, limits Limits) *Catalog {
	t.Helper()
	c, err := New(scope, limits)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func reconcile(t *testing.T, c *Catalog, scope *testScope, generation string, entries ...Candidate) {
	t.Helper()
	if err := c.Reconcile(context.Background(), scope, ReconcileInput{SourceGeneration: generation, Candidates: entries}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileStoresOnlyBoundedCandidatesAndSortsDeterministically(t *testing.T) {
	scope := newTestScope("account-a", "profile-a", "r1")
	c := newTestCatalog(t, scope, DefaultLimits())
	reconcile(t, c, scope, "dir-gen-1", candidate("z.txt", 3), candidate("src/main.go", 7), candidate("a.txt", 1))
	first, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Generation == 0 || first.SourceGeneration != "dir-gen-1" {
		t.Fatalf("generation evidence = %#v", first)
	}
	if len(first.Candidates) != 2 || first.Candidates[0].Path != "a.txt" || first.Candidates[1].Path != "src/main.go" {
		t.Fatalf("stable order = %#v", first.Candidates)
	}
	if first.Next == nil {
		t.Fatal("missing typed continuation")
	}
	second, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 2, Cursor: first.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Candidates) != 1 || second.Candidates[0].Path != "z.txt" || second.Next != nil {
		t.Fatalf("second page = %#v", second)
	}
	state, err := c.Stats(scope)
	if err != nil {
		t.Fatal(err)
	}
	if state.Dirty || !state.Reconciled || state.Entries != 3 || state.ApproxMemoryBytes <= 0 {
		t.Fatalf("state = %#v", state)
	}
}

func TestCursorAndQueryResultAreLocalOnly(t *testing.T) {
	scope := newTestScope("account-a", "profile-a", "r1")
	c := newTestCatalog(t, scope, DefaultLimits())
	reconcile(t, c, scope, "g1", candidate("one.txt", 1), candidate("two.txt", 2))
	result, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 1})
	if err != nil || result.Next == nil {
		t.Fatalf("expected local cursor: %#v, %v", result, err)
	}
	if _, err := json.Marshal(result.Next); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("cursor JSON error = %v", err)
	}
	if _, err := json.Marshal(result); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("query result JSON error = %v", err)
	}
	last, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 2})
	if err != nil || last.Next != nil {
		t.Fatalf("expected last page without cursor: %#v, %v", last, err)
	}
	if _, err := json.Marshal(last); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("last-page query result JSON error = %v", err)
	}
	var decoded Cursor
	if err := json.Unmarshal([]byte(`{}`), &decoded); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("cursor JSON decode error = %v", err)
	}
	var decodedResult QueryResult
	if err := json.Unmarshal([]byte(`{}`), &decodedResult); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("query result JSON decode error = %v", err)
	}
}

func TestDirtyAndGenerationChangesFailClosed(t *testing.T) {
	scope := newTestScope("account-a", "profile-a", "r1")
	c := newTestCatalog(t, scope, DefaultLimits())
	reconcile(t, c, scope, "g1", candidate("one.txt", 1), candidate("two.txt", 2))
	page, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MarkDirty(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	dirty, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 1, Cursor: page.Next})
	if !errors.Is(err, ErrReconcileRequired) || len(dirty.Candidates) != 0 {
		t.Fatalf("dirty query = %#v, %v", dirty, err)
	}
	reconcile(t, c, scope, "g2", candidate("new.txt", 4))
	if _, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 1, Cursor: page.Next}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("stale cursor = %v", err)
	}
	current, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace"})
	if err != nil || len(current.Candidates) != 1 || current.Candidates[0].Path != "new.txt" {
		t.Fatalf("reconciled query = %#v, %v", current, err)
	}
}

func TestQueryLiveVerifiesEveryCandidateOutsideCatalogLock(t *testing.T) {
	scope := newTestScope("account-a", "profile-a", "r1")
	c := newTestCatalog(t, scope, DefaultLimits())
	reconcile(t, c, scope, "g1", candidate("public.txt", 1), candidate("secret.txt", 2))
	// The revision remains unchanged: the query must still re-check the
	// current authorization decision for every candidate before returning it.
	scope.denyPath("workspace", "secret.txt")
	result, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 2})
	if !errors.Is(err, ErrDenied) || len(result.Candidates) != 0 || result.Next != nil {
		t.Fatalf("dynamic deny leaked a candidate page: %#v, %v", result, err)
	}
}

func TestCancelledWhileWaitingForCommitDoesNotApply(t *testing.T) {
	testCases := []struct {
		name string
		run  func(context.Context, *Catalog, *testScope) error
	}{
		{name: "reconcile", run: func(ctx context.Context, c *Catalog, scope *testScope) error {
			return c.Reconcile(ctx, scope, ReconcileInput{SourceGeneration: "g2", Candidates: []Candidate{candidate("new.txt", 2)}})
		}},
		{name: "upsert", run: func(ctx context.Context, c *Catalog, scope *testScope) error {
			return c.Upsert(ctx, scope, []Candidate{candidate("new.txt", 2)})
		}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scope := newTestScope("account-a", "profile-a", "r1")
			c := newTestCatalog(t, scope, DefaultLimits())
			reconcile(t, c, scope, "g1", candidate("old.txt", 1))
			before, err := c.Stats(scope)
			if err != nil {
				t.Fatal(err)
			}

			validated := make(chan struct{})
			allowCommitCheck := make(chan struct{})
			var once sync.Once
			scope.setValidateHook(func(call int) {
				// The first validation belongs to the initial authorize call. The
				// second is the final check immediately before c.mu.Lock. Block
				// there so the test can acquire the write lock deterministically.
				if call != 2 {
					return
				}
				once.Do(func() { close(validated) })
				<-allowCommitCheck
			})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx, c, scope) }()
			select {
			case <-validated:
			case <-time.After(2 * time.Second):
				cancel()
				close(allowCommitCheck)
				t.Fatal("operation did not reach the pre-lock validation")
			}
			c.mu.Lock()
			cancel()
			close(allowCommitCheck)
			c.mu.Unlock()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled operation error = %v", err)
			}
			scope.setValidateHook(nil)
			after, err := c.Stats(scope)
			if err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("cancelled operation changed state: before=%#v after=%#v", before, after)
			}
			result, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace"})
			if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Path != "old.txt" {
				t.Fatalf("old snapshot lost after cancellation: %#v, %v", result, err)
			}
		})
	}
}

func TestRevocationRevisionAndABIsolationFailClosed(t *testing.T) {
	a := newTestScope("account-a", "profile-a", "r1")
	b := newTestScope("account-b", "profile-b", "r1")
	ca := newTestCatalog(t, a, DefaultLimits())
	cb := newTestCatalog(t, b, DefaultLimits())
	reconcile(t, ca, a, "a1", candidate("a.txt", 1))
	reconcile(t, cb, b, "b1", candidate("b.txt", 2))
	if _, err := ca.Query(context.Background(), b, QueryRequest{RootID: "workspace"}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("A accepted B scope: %v", err)
	}
	if _, err := cb.Query(context.Background(), a, QueryRequest{RootID: "workspace"}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("B accepted A scope: %v", err)
	}
	a.revoke()
	result, err := ca.Query(context.Background(), a, QueryRequest{RootID: "workspace"})
	if !errors.Is(err, ErrStale) || len(result.Candidates) != 0 {
		t.Fatalf("revoked query = %#v, %v", result, err)
	}
	a.setRevision("r2")
	if _, err := ca.Query(context.Background(), a, QueryRequest{RootID: "workspace"}); !errors.Is(err, ErrStale) {
		t.Fatalf("revision change query = %v", err)
	}
	result, err = cb.Query(context.Background(), b, QueryRequest{RootID: "workspace"})
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Path != "b.txt" {
		t.Fatalf("B was affected by A: %#v, %v", result, err)
	}
}

func TestBoundsAreAtomicAndOverflowFailsClosed(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxEntries = 2
	limits.MaxMemoryBytes = 4 << 10
	scope := newTestScope("account-a", "profile-a", "r1")
	c := newTestCatalog(t, scope, limits)
	if err := c.Reconcile(context.Background(), scope, ReconcileInput{SourceGeneration: "g1", Candidates: []Candidate{candidate("one.txt", 1), candidate("two.txt", 2), candidate("three.txt", 3)}}); !errors.Is(err, ErrLimit) {
		t.Fatalf("entry overflow = %v", err)
	}
	state, err := c.Stats(scope)
	if err != nil {
		t.Fatal(err)
	}
	if state.Entries != 0 || !state.Dirty || state.Reconciled {
		t.Fatalf("failed reconcile changed state = %#v", state)
	}
	if err := c.Reconcile(context.Background(), scope, ReconcileInput{SourceGeneration: "g1", Candidates: []Candidate{candidate("one.txt", 1), candidate("two.txt", 2)}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Upsert(context.Background(), scope, []Candidate{candidate("three.txt", 3)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("upsert overflow = %v", err)
	}
	state, err = c.Stats(scope)
	if err != nil {
		t.Fatal(err)
	}
	if state.Entries != 2 {
		t.Fatalf("failed upsert changed entries = %#v", state)
	}
	if _, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", Prefix: "../escape"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid prefix = %v", err)
	}
}

func TestCancelledReconcileLeavesPreviousSnapshot(t *testing.T) {
	scope := newTestScope("account-a", "profile-a", "r1")
	c := newTestCatalog(t, scope, DefaultLimits())
	reconcile(t, c, scope, "g1", candidate("old.txt", 1))
	stateBefore, err := c.Stats(scope)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Reconcile(ctx, scope, ReconcileInput{SourceGeneration: "g2", Candidates: []Candidate{candidate("new.txt", 2)}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled reconcile = %v", err)
	}
	stateAfter, err := c.Stats(scope)
	if err != nil {
		t.Fatal(err)
	}
	if stateAfter != stateBefore {
		t.Fatalf("cancelled reconcile changed state: before=%#v after=%#v", stateBefore, stateAfter)
	}
	result, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace"})
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Path != "old.txt" {
		t.Fatalf("old snapshot lost = %#v, %v", result, err)
	}
}

func TestConcurrentMutationsAndQueries(t *testing.T) {
	scope := newTestScope("account-a", "profile-a", "r1")
	c := newTestCatalog(t, scope, DefaultLimits())
	reconcile(t, c, scope, "g0", candidate("seed.txt", 1))
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				path := fmt.Sprintf("worker-%d/%03d.txt", worker, i)
				_ = c.Upsert(context.Background(), scope, []Candidate{candidate(path, int64(i))})
				if i%9 == 0 {
					_ = c.MarkDirty(context.Background(), scope)
				}
				_, _ = c.Query(context.Background(), scope, QueryRequest{RootID: "workspace", PageSize: 8})
			}
		}()
	}
	wg.Wait()
	// Dirty state is expected after watcher-style hints; a full reconcile is
	// the only operation that reopens the query surface.
	reconcile(t, c, scope, "g-final", candidate("final.txt", 9))
	result, err := c.Query(context.Background(), scope, QueryRequest{RootID: "workspace"})
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Path != "final.txt" {
		t.Fatalf("final snapshot = %#v, %v", result, err)
	}
}

func TestInvalidCandidateAndDuplicateRejected(t *testing.T) {
	scope := newTestScope("account-a", "profile-a", "r1")
	c := newTestCatalog(t, scope, DefaultLimits())
	bad := []Candidate{{RootID: "workspace", Path: `C:\escape`, Kind: KindRegular}}
	if err := c.Reconcile(context.Background(), scope, ReconcileInput{SourceGeneration: "g1", Candidates: bad}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("bad path = %v", err)
	}
	dup := []Candidate{candidate("same.txt", 1), candidate("same.txt", 2)}
	if err := c.Reconcile(context.Background(), scope, ReconcileInput{SourceGeneration: "g1", Candidates: dup}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate = %v", err)
	}
}
