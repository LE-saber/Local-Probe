package workspacesnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
	"github.com/LE-saber/Local-Probe/internal/search"
)

type fakeSource struct {
	dirs  map[string][]search.DirEntry
	opens atomic.Int64
}

func (s *fakeSource) OpenDirectory(_ context.Context, _ policy.BoundScope, rootID, path string) (search.Directory, error) {
	if rootID != "project" {
		return nil, errors.New("wrong root")
	}
	entries, ok := s.dirs[path]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	s.opens.Add(1)
	return &fakeDirectory{entries: append([]search.DirEntry(nil), entries...)}, nil
}

func (s *fakeSource) OpenFile(context.Context, policy.BoundScope, readcore.FileRef) (readcore.Handle, error) {
	return nil, errors.New("snapshot must not open files")
}

type fakeDirectory struct {
	entries []search.DirEntry
	index   int
}

func (d *fakeDirectory) ReadDir(n int) ([]search.DirEntry, error) {
	if d.index >= len(d.entries) {
		return nil, io.EOF
	}
	end := len(d.entries)
	if n > 0 && d.index+n < end {
		end = d.index + n
	}
	out := append([]search.DirEntry(nil), d.entries[d.index:end]...)
	d.index = end
	return out, nil
}
func (d *fakeDirectory) Generation() (string, error) { return "generation-1", nil }
func (d *fakeDirectory) Close() error                { return nil }

func testBound(t *testing.T) policy.BoundScope {
	t.Helper()
	root, err := config.NewRootWithIgnore("project", `C:\workspace`, []string{"secrets/**"}, []string{"vendor/**"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("read", []string{"project"}, []string{"workspace_snapshot"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{config.NewConnection("connection", "test", "read", "credential", true)}, []config.CredentialRef{config.NewCredentialRef("credential", "bearer")})
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := policy.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.BindAuthenticated("connection")
	if err != nil {
		t.Fatal(err)
	}
	return bound
}

func testSource() *fakeSource {
	return &fakeSource{dirs: map[string][]search.DirEntry{
		"": {
			{Name: "go.mod", Type: search.EntryRegular, SizeBytes: 64},
			{Name: "README.md", Type: search.EntryRegular, SizeBytes: 128},
			{Name: "src", Type: search.EntryDirectory},
			{Name: "secrets", Type: search.EntryDirectory},
			{Name: "vendor", Type: search.EntryDirectory},
		},
		"src": {
			{Name: "main.go", Type: search.EntryRegular, SizeBytes: 256},
			{Name: "util.go", Type: search.EntryRegular, SizeBytes: 128},
		},
		"secrets": {{Name: "token.txt", Type: search.EntryRegular, SizeBytes: 10}},
		"vendor":  {{Name: "third_party.go", Type: search.EntryRegular, SizeBytes: 10}},
	}}
}

func denseManifestSource() *fakeSource {
	source := &fakeSource{dirs: map[string][]search.DirEntry{"": {}}}
	for i := 0; i < 70; i++ {
		directory := fmt.Sprintf("package-%03d", i)
		source.dirs[""] = append(source.dirs[""], search.DirEntry{Name: directory, Type: search.EntryDirectory})
		source.dirs[directory] = []search.DirEntry{{Name: "package.json", Type: search.EntryRegular, SizeBytes: 32}}
	}
	return source
}

func densePathSource() *fakeSource {
	source := &fakeSource{dirs: map[string][]search.DirEntry{"": {}}}
	for i := 0; i < 4; i++ {
		name := fmt.Sprintf("%s-%03d.go", strings.Repeat("long-path-segment", 20), i)
		source.dirs[""] = append(source.dirs[""], search.DirEntry{Name: name, Type: search.EntryRegular, SizeBytes: 1})
	}
	return source
}

func TestSnapshotReturnsBoundedFactsAndEvidence(t *testing.T) {
	source := testSource()
	engine, err := New(source, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	out, err := engine.Snapshot(context.Background(), testBound(t), Request{RootID: "project", MaxDepth: 3, MaxEntries: 32})
	if err != nil {
		t.Fatal(err)
	}
	if out.SchemaVersion != SchemaVersion || !out.Coverage.Complete || len(out.Outline) == 0 {
		t.Fatalf("bad snapshot envelope: %+v", out)
	}
	for _, entry := range out.Outline {
		if strings.Contains(entry.Path, "secret") || strings.Contains(entry.Path, "vendor") {
			t.Fatalf("denied/ignored path leaked: %+v", entry)
		}
	}
	if len(out.ManifestCandidates) != 1 || out.ManifestCandidates[0].Path != "go.mod" {
		t.Fatalf("bad manifest evidence: %+v", out.ManifestCandidates)
	}
	if len(out.LanguageStats) != 2 || out.LanguageStats[0].Language != "Go" || out.LanguageStats[0].Files != 2 {
		t.Fatalf("bad language facts: %+v", out.LanguageStats)
	}
	if len(out.EvidencePaths) < 2 || source.opens.Load() > int64(DefaultLimits().MaxOpenDirectories) {
		t.Fatalf("bad evidence/open budget: %+v opens=%d", out.EvidencePaths, source.opens.Load())
	}
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > out.Budget.MaxOutputBytes || out.Coverage.ReturnedBytes != len(encoded) {
		t.Fatalf("snapshot exceeded exact output budget: bytes=%d coverage=%d budget=%d", len(encoded), out.Coverage.ReturnedBytes, out.Budget.MaxOutputBytes)
	}
}

func TestSnapshotIsBoundedAndCursorContinuesPages(t *testing.T) {
	source := testSource()
	limits := DefaultLimits()
	limits.MaxEntries = 2
	engine, err := New(source, limits)
	if err != nil {
		t.Fatal(err)
	}
	bound := testBound(t)
	first, err := engine.Snapshot(context.Background(), bound, Request{RootID: "project", MaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Coverage.Complete || first.Continuation == "" {
		t.Fatalf("bounded snapshot did not expose continuation: %+v", first)
	}
	second, err := engine.Snapshot(context.Background(), bound, Request{RootID: "project", MaxEntries: 2, Cursor: first.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Outline) == 0 || second.Coverage.OpenedDirectories == 0 {
		t.Fatalf("cursor page was not readable: %+v", second)
	}
	if source.opens.Load() > 2*int64(limits.MaxOpenDirectories) {
		t.Fatalf("snapshot opened unbounded directories: %d", source.opens.Load())
	}
}

func TestSnapshotRejectsInvalidBoundsAndDoesNotRunFiles(t *testing.T) {
	source := testSource()
	engine, err := New(source, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	bound := testBound(t)
	for _, req := range []Request{
		{RootID: "missing"},
		{RootID: "project", MaxDepth: -1},
		{RootID: "project", MaxEntries: 0, MaxOutputBytes: 512},
		{RootID: "project", MaxOutputBytes: DefaultLimits().MaxOutputBytes - 1},
	} {
		if _, err := engine.Snapshot(context.Background(), bound, req); !errors.Is(err, ErrInvalidRequest) && !errors.Is(err, ErrUnavailable) {
			t.Fatalf("request %+v returned unexpected error %v", req, err)
		}
	}
	if source.opens.Load() != 0 {
		t.Fatal("invalid snapshot request opened a directory")
	}
}

func TestSnapshotFailsClosedForDerivedManifestOverflow(t *testing.T) {
	engine, err := New(denseManifestSource(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	out, err := engine.Snapshot(context.Background(), testBound(t), Request{RootID: "project", MaxEntries: 512})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("manifest overflow was not rejected: err=%v result=%+v", err, out)
	}
	if out.Continuation != "" || len(out.Outline) != 0 || len(out.ManifestCandidates) != 0 {
		t.Fatalf("overflow returned an unrecoverable partial result: %+v", out)
	}
}

func TestSnapshotExactBudgetRejectsDensePathsWithoutAdvancingCursor(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOutputBytes = 1024
	engine, err := New(densePathSource(), limits)
	if err != nil {
		t.Fatal(err)
	}
	out, err := engine.Snapshot(context.Background(), testBound(t), Request{RootID: "project", MaxEntries: 16})
	if err == nil {
		encoded, marshalErr := json.Marshal(out)
		if marshalErr != nil || len(encoded) > limits.MaxOutputBytes || out.Coverage.ReturnedBytes != len(encoded) {
			t.Fatalf("successful result exceeded exact budget: bytes=%d coverage=%d err=%v", len(encoded), out.Coverage.ReturnedBytes, marshalErr)
		}
		return
	}
	if !errors.Is(err, ErrBudgetExceeded) || out.Continuation != "" || len(out.Outline) != 0 {
		t.Fatalf("dense path budget did not fail closed: err=%v result=%+v", err, out)
	}
}

func TestFitResultBudgetFixedPointAcrossDecimalBoundaries(t *testing.T) {
	for pathLength := 0; pathLength <= 4096; pathLength++ {
		candidate := Result{
			SchemaVersion: SchemaVersion,
			RootID:        "project",
			Path:          strings.Repeat("p", pathLength),
			Outline:       []OutlineEntry{}, ManifestCandidates: []ManifestCandidate{},
			LanguageStats: []LanguageStat{}, EvidencePaths: []EvidencePath{},
			Warnings: []string{}, Budget: Budget{MaxDepth: 4, MaxEntries: 512, MaxReadBytes: 1 << 20, MaxOutputBytes: 1 << 20, MaxOpenFiles: 1, MaxOpenDirectories: 128},
		}
		if err := fitResultBudget(&candidate, 1<<20); err != nil {
			t.Fatalf("path length %d did not converge: %v", pathLength, err)
		}
		encoded, err := json.Marshal(candidate)
		if err != nil || len(encoded) != candidate.Coverage.ReturnedBytes || len(encoded) > 1<<20 {
			t.Fatalf("path length %d violated fixed point: bytes=%d coverage=%d err=%v", pathLength, len(encoded), candidate.Coverage.ReturnedBytes, err)
		}
		if pathLength%257 == 0 {
			tooSmall := candidate
			tooSmall.Coverage.ReturnedBytes = 0
			if err := fitResultBudget(&tooSmall, len(encoded)-1); !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("path length %d accepted one-byte-short budget: %v", pathLength, err)
			}
		}
	}
}
