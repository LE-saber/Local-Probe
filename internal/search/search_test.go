package search

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
)

func TestListDirectoryIsBoundedAndCursorIsSigned(t *testing.T) {
	bound, store := searchBound(t, `{
  "schema_version":"local-probe.config.v1",
  "roots":[{"id":"project","path":"C:/project","deny_patterns":["secret/**"],"ignore_patterns":["*.tmp"]}],
  "profiles":[{"id":"read","roots":["project"],"tools":["list_directory"],"deny_patterns":[],"ignore_patterns":[]}],
  "connections":[{"id":"connection","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials":[{"id":"credential","kind":"local_token"}]
}`)
	source := newFakeSource(map[string][]DirEntry{
		"": {
			{Name: "a.txt", Type: EntryRegular, SizeBytes: 1},
			{Name: "secret", Type: EntryDirectory},
			{Name: "b.txt", Type: EntryRegular, SizeBytes: 2},
			{Name: "ignored.tmp", Type: EntryRegular, SizeBytes: 3},
			{Name: "c.txt", Type: EntryRegular, SizeBytes: 4},
		},
	})
	service, err := New(source, DefaultLimits(), []byte("search-test-cursor-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ListDirectory(context.Background(), bound, ListDirectoryRequest{RootID: "project", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 1 || first.Entries[0].Path != "a.txt" || first.Coverage.Complete || first.Continuation == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := service.ListDirectory(context.Background(), bound, ListDirectoryRequest{RootID: "project", PageSize: 1, Cursor: first.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 1 || second.Entries[0].Path != "b.txt" {
		t.Fatalf("second page = %#v", second)
	}
	tampered := first.Continuation[:len(first.Continuation)-1] + "!"
	if _, err := service.ListDirectory(context.Background(), bound, ListDirectoryRequest{RootID: "project", PageSize: 1, Cursor: tampered}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered cursor error = %v", err)
	}
	updated, err := config.Parse([]byte(`{
  "schema_version":"local-probe.config.v1",
  "roots":[{"id":"project","path":"C:/project"}],
  "profiles":[{"id":"read","roots":["project"],"tools":["list_directory"]}],
  "connections":[{"id":"connection","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials":[{"id":"credential","kind":"local_token"}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(updated); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListDirectory(context.Background(), bound, ListDirectoryRequest{RootID: "project", PageSize: 1, Cursor: first.Continuation}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("revoked cursor error = %v", err)
	}
}

func TestTreeDirectoryReturnsFlatBoundedTreeAndHidesDeniedSubtrees(t *testing.T) {
	bound, store := searchBound(t, `{
  "schema_version":"local-probe.config.v1",
  "roots":[{"id":"project","path":"C:/project","deny_patterns":["private/**"],"ignore_patterns":["ignored/**"]}],
  "profiles":[{"id":"read","roots":["project"],"tools":["tree_directory"],"deny_patterns":[],"ignore_patterns":[]}],
  "connections":[{"id":"connection","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials":[{"id":"credential","kind":"local_token"}]
}`)
	source := newFakeSource(map[string][]DirEntry{
		"": {
			{Name: "src", Type: EntryDirectory},
			{Name: "private", Type: EntryDirectory},
			{Name: "ignored", Type: EntryDirectory},
			{Name: "README.md", Type: EntryRegular},
			{Name: "link", Type: EntrySymlink},
		},
		"src": {
			{Name: "main.go", Type: EntryRegular},
			{Name: "nested", Type: EntryDirectory},
		},
		"src/nested": {{Name: "util.go", Type: EntryRegular}},
		"private":    {{Name: "secret.txt", Type: EntryRegular}},
		"ignored":    {{Name: "ignored.txt", Type: EntryRegular}},
	})
	service, err := New(source, DefaultLimits(), []byte("tree-test-cursor-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.TreeDirectory(context.Background(), bound, TreeDirectoryRequest{RootID: "project", PageSize: 3, MaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.Coverage.Complete || first.Continuation == "" || first.Entries[0].Path != "" || first.Entries[0].Depth != 0 {
		t.Fatalf("first tree page = %#v", first)
	}
	if first.Entries[1].Path != "src" || first.Entries[1].Depth != 1 || first.Entries[2].Path != "src/main.go" || first.Entries[2].Depth != 2 {
		t.Fatalf("first tree entries = %#v", first.Entries)
	}
	for _, entry := range first.Entries {
		if strings.HasPrefix(entry.Path, "private") || strings.HasPrefix(entry.Path, "ignored") {
			t.Fatalf("tree leaked filtered entry: %#v", entry)
		}
	}

	second, err := service.TreeDirectory(context.Background(), bound, TreeDirectoryRequest{RootID: "project", PageSize: 3, MaxEntries: 2, Cursor: first.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) == 0 || second.Entries[0].Path == "" {
		t.Fatalf("tree cursor repeated root or returned nothing: %#v", second)
	}
	for _, entry := range second.Entries {
		if strings.HasPrefix(entry.Path, "private") || strings.HasPrefix(entry.Path, "ignored") {
			t.Fatalf("tree leaked filtered entry after cursor: %#v", entry)
		}
		if entry.Path == "link" && entry.Type != EntrySymlink {
			t.Fatalf("tree symlink type = %#v", entry)
		}
	}
	if second.Coverage.Complete {
		t.Fatalf("tree marked partial cursor result complete: %#v", second)
	}
	all, err := service.TreeDirectory(context.Background(), bound, TreeDirectoryRequest{RootID: "project", PageSize: 32, MaxEntries: 32})
	if err != nil {
		t.Fatal(err)
	}
	if all.Coverage.DeniedEntries == 0 || all.Coverage.IgnoredEntries == 0 || all.Coverage.Complete {
		t.Fatalf("tree filter coverage = %#v warnings=%v", all.Coverage, all.Warnings)
	}
	for _, entry := range all.Entries {
		if strings.HasPrefix(entry.Path, "private") || strings.HasPrefix(entry.Path, "ignored") {
			t.Fatalf("tree leaked filtered entry in complete traversal: %#v", entry)
		}
	}
	if _, err := service.TreeDirectory(context.Background(), bound, TreeDirectoryRequest{RootID: "project", PageSize: 3, MaxEntries: 3, Cursor: first.Continuation}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tree accepted cursor with changed budget: %v", err)
	}

	updated, err := config.Parse([]byte(`{
  "schema_version":"local-probe.config.v1",
  "roots":[{"id":"project","path":"C:/project"}],
  "profiles":[{"id":"read","roots":["project"],"tools":["tree_directory"]}],
  "connections":[{"id":"connection","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials":[{"id":"credential","kind":"local_token"}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(updated); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TreeDirectory(context.Background(), bound, TreeDirectoryRequest{RootID: "project", PageSize: 3, MaxEntries: 2, Cursor: first.Continuation}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tree accepted revoked cursor: %v", err)
	}
}

func TestTreeDirectoryDepthLimitIsExplicitAndRootOnlyIsAllowed(t *testing.T) {
	bound, _ := searchBound(t, `{
  "schema_version":"local-probe.config.v1",
  "roots":[{"id":"project","path":"C:/project"}],
  "profiles":[{"id":"read","roots":["project"],"tools":["tree_directory"]}],
  "connections":[{"id":"connection","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials":[{"id":"credential","kind":"local_token"}]
}`)
	source := newFakeSource(map[string][]DirEntry{
		"":        {{Name: "one", Type: EntryDirectory}},
		"one":     {{Name: "two", Type: EntryDirectory}},
		"one/two": {{Name: "file.txt", Type: EntryRegular}},
	})
	service, err := New(source, DefaultLimits(), []byte("tree-test-cursor-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.TreeDirectory(context.Background(), bound, TreeDirectoryRequest{RootID: "project", MaxDepth: 1, PageSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || result.Entries[0].Path != "" || result.Entries[1].Path != "one" || result.Entries[1].Depth != 1 {
		t.Fatalf("depth-limited tree = %#v", result)
	}
	if result.Coverage.Complete || !contains(result.Warnings, "depth_limit") || result.Continuation != "" {
		t.Fatalf("depth-limited coverage = %#v warnings=%v cursor=%q", result.Coverage, result.Warnings, result.Continuation)
	}

	rootOnly, err := service.TreeDirectory(context.Background(), bound, TreeDirectoryRequest{RootID: "project", MaxDepth: 0, PageSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	// max_depth=0 uses the service default, matching the other P06 walkers.
	if len(rootOnly.Entries) != 4 || rootOnly.Entries[2].Path != "one/two" || rootOnly.Entries[3].Path != "one/two/file.txt" {
		t.Fatalf("default-depth tree = %#v", rootOnly)
	}
}

func TestFindFilesAndSearchTextUseLiteralUTF8AndCoverage(t *testing.T) {
	bound, _ := searchBound(t, `{
  "schema_version":"local-probe.config.v1",
  "roots":[{"id":"project","path":"C:/project","deny_patterns":["secret/**"],"ignore_patterns":["vendor/**"]}],
  "profiles":[{"id":"read","roots":["project"],"tools":["find_files","search_text"],"deny_patterns":[],"ignore_patterns":["*.tmp"]}],
  "connections":[{"id":"connection","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials":[{"id":"credential","kind":"local_token"}]
}`)
	source := newFakeSource(map[string][]DirEntry{
		"":           {{Name: "src", Type: EntryDirectory}, {Name: "vendor", Type: EntryDirectory}, {Name: "secret", Type: EntryDirectory}, {Name: "notes.txt", Type: EntryRegular}},
		"src":        {{Name: "main.go", Type: EntryRegular}, {Name: "main.tmp", Type: EntryRegular}, {Name: "nested", Type: EntryDirectory}},
		"src/nested": {{Name: "util.go", Type: EntryRegular}},
		"vendor":     {{Name: "third.go", Type: EntryRegular}},
		"secret":     {{Name: "hidden.go", Type: EntryRegular}},
	}, map[string]string{
		"notes.txt":          "literal regex [a-z]+ should be found\n第二行没有\n",
		"src/main.go":        "package main\nneedle = \"literal\"\n",
		"src/main.tmp":       "needle in ignored file\n",
		"src/nested/util.go": "没有 needle\nneedle 在 UTF-8 行\n",
		"vendor/third.go":    "needle in ignored directory\n",
		"secret/hidden.go":   "needle in denied directory\n",
	})
	service, err := New(source, DefaultLimits(), []byte("search-test-cursor-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	found, err := service.FindFiles(context.Background(), bound, FindFilesRequest{RootID: "project", Pattern: "**/*.go", PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(found.Entries))
	for _, entry := range found.Entries {
		paths = append(paths, entry.Path)
	}
	sort.Strings(paths)
	if strings.Join(paths, ",") != "src/main.go,src/nested/util.go" {
		t.Fatalf("find_files paths = %v; coverage=%#v warnings=%v", paths, found.Coverage, found.Warnings)
	}
	result, err := service.SearchText(context.Background(), bound, SearchTextRequest{RootID: "project", Query: "[a-z]+", Globs: []string{"*.txt"}, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "notes.txt" || result.Matches[0].Line != 1 {
		t.Fatalf("literal search = %#v", result)
	}
	if result.Coverage.ReturnedEntries != len(result.Matches) {
		t.Fatalf("literal search returned_entries = %d, matches = %d", result.Coverage.ReturnedEntries, len(result.Matches))
	}
	utf8Result, err := service.SearchText(context.Background(), bound, SearchTextRequest{RootID: "project", Query: "needle", PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(utf8Result.Matches) != 3 || utf8Result.Coverage.DeniedEntries == 0 || utf8Result.Coverage.IgnoredEntries == 0 {
		t.Fatalf("utf8 search = %#v", utf8Result)
	}
	if utf8Result.Coverage.ReturnedEntries != len(utf8Result.Matches) {
		t.Fatalf("utf8 search returned_entries = %d, matches = %d", utf8Result.Coverage.ReturnedEntries, len(utf8Result.Matches))
	}
}

func TestSearchTextHonorsCancellationAndLongLineBudget(t *testing.T) {
	bound, _ := searchBound(t, `{
  "schema_version":"local-probe.config.v1",
  "roots":[{"id":"project","path":"C:/project"}],
  "profiles":[{"id":"read","roots":["project"],"tools":["search_text"]}],
  "connections":[{"id":"connection","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials":[{"id":"credential","kind":"local_token"}]
}`)
	long := strings.Repeat("x", 2<<20) + "needle\n"
	source := newFakeSource(map[string][]DirEntry{"": {{Name: "large.log", Type: EntryRegular}}}, map[string]string{"large.log": long})
	limits := DefaultLimits()
	limits.MaxReadBytes = 256 << 10
	limits.MaxLineBytes = 1024
	service, err := New(source, limits, []byte("search-test-cursor-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := service.SearchText(ctx, bound, SearchTextRequest{RootID: "project", Query: "needle", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.Complete || !contains(result.Warnings, "cancelled") {
		t.Fatalf("cancelled result = %#v", result)
	}
	result, err = service.SearchText(context.Background(), bound, SearchTextRequest{RootID: "project", Query: "needle", PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.Complete || !contains(result.Warnings, "read_limit") || result.Continuation == "" {
		t.Fatalf("bounded long line result = %#v", result)
	}
	if len(result.Matches) > 1 || result.Coverage.ReadBytes > limits.MaxReadBytes {
		t.Fatalf("long line exceeded bounds = %#v", result)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func searchBound(t *testing.T, data string) (policy.BoundScope, *config.Store) {
	t.Helper()
	cfg, err := config.Parse([]byte(data))
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
	return bound, store
}

type fakeBinder struct{ source Source }

func (b *fakeBinder) BindSearch(policy.BoundScope) (Source, error) { return b.source, nil }

type fakeSource struct {
	mu    sync.RWMutex
	dirs  map[string][]DirEntry
	files map[string]string
}

func newFakeSource(dirs map[string][]DirEntry, files ...map[string]string) *fakeSource {
	allFiles := map[string]string{}
	if len(files) != 0 {
		allFiles = files[0]
	}
	return &fakeSource{dirs: dirs, files: allFiles}
}

func (s *fakeSource) BindSearch(policy.BoundScope) (Source, error) { return s, nil }

func (s *fakeSource) OpenDirectory(_ context.Context, _ policy.BoundScope, _, path string) (Directory, error) {
	s.mu.RLock()
	entries, ok := s.dirs[path]
	s.mu.RUnlock()
	if !ok {
		return nil, errors.New("directory missing")
	}
	return &fakeDirectory{entries: append([]DirEntry(nil), entries...), generation: "stable"}, nil
}

func (s *fakeSource) OpenFile(_ context.Context, _ policy.BoundScope, ref readcore.FileRef) (readcore.Handle, error) {
	s.mu.RLock()
	data, ok := s.files[ref.Path]
	s.mu.RUnlock()
	if !ok {
		return nil, errors.New("file missing")
	}
	return &fakeHandle{data: []byte(data), version: "v1"}, nil
}

type fakeDirectory struct {
	entries    []DirEntry
	offset     int
	generation string
}

func (d *fakeDirectory) ReadDir(n int) ([]DirEntry, error) {
	if d.offset >= len(d.entries) {
		return nil, io.EOF
	}
	if n <= 0 || n > len(d.entries)-d.offset {
		n = len(d.entries) - d.offset
	}
	out := append([]DirEntry(nil), d.entries[d.offset:d.offset+n]...)
	d.offset += n
	return out, nil
}

func (d *fakeDirectory) Generation() (string, error) { return d.generation, nil }
func (d *fakeDirectory) Close() error                { return nil }

type fakeHandle struct {
	data    []byte
	version string
}

func (h *fakeHandle) ReadAt(p []byte, offset int64) (int, error) {
	if offset >= int64(len(h.data)) {
		return 0, io.EOF
	}
	n := copy(p, h.data[offset:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (h *fakeHandle) Metadata(context.Context) (readcore.Metadata, error) {
	return readcore.Metadata{Size: int64(len(h.data)), Version: readcore.Version{Token: h.version, Strength: "metadata"}}, nil
}
func (h *fakeHandle) Close() error { return nil }

var _ Binder = (*fakeBinder)(nil)
var _ Source = (*fakeSource)(nil)
var _ readcore.Handle = (*fakeHandle)(nil)
