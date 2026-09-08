package readcore

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type fixture struct {
	text      string
	size      int64
	synthetic bool
	change    bool
	readErr   error
	shortRead bool
	closeErr  bool
	afterRead func()
}
type fixtureSource struct {
	files   map[string]fixture
	opens   atomic.Int64
	closes  atomic.Int64
	bytes   atomic.Int64
	active  atomic.Int64
	peak    atomic.Int64
	delay   time.Duration
	openErr error
}
type fixtureHandle struct {
	source *fixtureSource
	file   fixture
	reads  int
	once   sync.Once
}

func (s *fixtureSource) Open(ctx context.Context, scope Scope, ref FileRef) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.openErr != nil {
		return nil, s.openErr
	}
	f, ok := s.files[ref.Path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	s.opens.Add(1)
	active := s.active.Add(1)
	for {
		old := s.peak.Load()
		if active <= old || s.peak.CompareAndSwap(old, active) {
			break
		}
	}
	return &fixtureHandle{source: s, file: f}, nil
}
func (h *fixtureHandle) ReadAt(p []byte, off int64) (int, error) {
	if h.source.delay > 0 {
		time.Sleep(h.source.delay)
	}
	h.reads++
	size := int64(len(h.file.text))
	if h.file.synthetic {
		size = h.file.size
	}
	n := int(min(int64(len(p)), max(int64(0), size-off)))
	if h.file.shortRead && n > 0 {
		n--
	}
	if h.file.synthetic {
		for i := 0; i < n; i++ {
			p[i] = 'x'
		}
	} else if n > 0 {
		copy(p[:n], h.file.text[off:off+int64(n)])
	}
	h.source.bytes.Add(int64(n))
	if h.file.afterRead != nil {
		h.file.afterRead()
	}
	if h.file.readErr != nil {
		return n, h.file.readErr
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
func (h *fixtureHandle) Metadata(ctx context.Context) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	size := int64(len(h.file.text))
	if h.file.synthetic {
		size = h.file.size
	}
	token := "v1"
	if h.file.change && h.reads > 0 {
		token = "v2"
	}
	return Metadata{Size: size, Version: Version{Token: token, Strength: "metadata"}}, nil
}
func (h *fixtureHandle) Close() error {
	h.once.Do(func() { h.source.closes.Add(1); h.source.active.Add(-1) })
	if h.file.closeErr {
		return errors.New("secret internal close detail")
	}
	return nil
}

func scopeFor(t testing.TB) Scope {
	t.Helper()
	scope, err := NewScope("account-a", "policy-v1", []string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
func engineFor(t testing.TB, src Source, limits Limits) *Engine {
	t.Helper()
	engine, err := New(src, limits)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}
func request(path string, offset int64, count int) Request {
	return Request{File: FileRef{RootID: "project", Path: path}, Offset: offset, MaxBytes: count}
}
func read(t testing.TB, engine *Engine, ctx context.Context, requests ...Request) BatchResult {
	t.Helper()
	result, err := engine.ReadBatch(ctx, scopeFor(t), requests)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func expectCode(t testing.TB, result Result, code string) {
	t.Helper()
	if result.Error == nil || result.Error.Code != code {
		t.Fatalf("want %s, got %+v", code, result)
	}
	if result.Content != "" || result.NextOffset != nil || result.EOF {
		t.Fatalf("failed result leaked content/continuation: %+v", result)
	}
}

func TestByteRangeAndContinuation(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"f.txt": {text: "abcdefghijkl"}}}
	engine := engineFor(t, source, DefaultLimits())
	first := read(t, engine, context.Background(), request("f.txt", 2, 4)).Items[0]
	if first.Error != nil || first.Content != "cdef" || first.EndOffset != 6 || first.EOF || first.NextOffset == nil || *first.NextOffset != 6 {
		t.Fatalf("bad first page: %+v", first)
	}
	req := request("f.txt", *first.NextOffset, 10)
	req.ExpectedVersion = first.Version.Token
	last := read(t, engine, context.Background(), req).Items[0]
	if last.Error != nil || last.Content != "ghijkl" || !last.EOF || last.NextOffset != nil {
		t.Fatalf("bad last page: %+v", last)
	}
	if source.opens.Load() != 2 || source.closes.Load() != 2 {
		t.Fatal("handle leak")
	}
}

func TestFairBatchPreservesOrderAndBounds(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"f.txt": {text: strings.Repeat("a", 256)}}, delay: time.Millisecond}
	limits := DefaultLimits()
	limits.MaxOutputBytes, limits.MaxReadBytes = 128, 256
	engine := engineFor(t, source, limits)
	requests := make([]Request, 32)
	for i := range requests {
		requests[i] = request("f.txt", int64(i), 32)
	}
	for repeat := 0; repeat < 3; repeat++ {
		result := read(t, engine, context.Background(), requests...)
		if result.Failed != 0 || result.ReturnedBytes != 128 || result.BytesRead != 128 {
			t.Fatalf("bad totals: %+v", result)
		}
		for i, item := range result.Items {
			if item.Offset != int64(i) || item.AllocatedBytes != 4 || len(item.Content) != 4 {
				t.Fatalf("bad item %d: %+v", i, item)
			}
		}
	}
	if source.peak.Load() > int64(limits.Workers) || source.peak.Load() < 1 {
		t.Fatalf("bad worker peak %d", source.peak.Load())
	}
	if source.opens.Load() != source.closes.Load() || source.active.Load() != 0 {
		t.Fatal("handle leak")
	}
}

func TestAllocationWaterFilling(t *testing.T) {
	cases := []struct {
		demand []int
		total  int
		want   []int
	}{
		{[]int{1, 100, 100}, 9, []int{1, 4, 4}},
		{[]int{0, 10, 10}, 5, []int{0, 3, 2}},
		{[]int{5, 5, 5}, 2, []int{1, 1, 0}},
		{[]int{1, 2}, 100, []int{1, 2}},
	}
	for _, c := range cases {
		if got := allocate(c.demand, c.total); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%v/%d -> %v != %v", c.demand, c.total, got, c.want)
		}
	}
}

func TestIOBudgetAndNoRedistribution(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"short.txt": {text: "x"}, "long.txt": {text: strings.Repeat("y", 200)}}}
	limits := DefaultLimits()
	limits.MaxOutputBytes, limits.MaxReadBytes = 100, 10
	result := read(t, engineFor(t, source, limits), context.Background(), request("short.txt", 0, 100), request("long.txt", 0, 100))
	if result.Items[0].AllocatedBytes != 5 || result.Items[1].AllocatedBytes != 5 || result.BytesRead != 6 || result.ReturnedBytes != 6 {
		t.Fatalf("unexpected allocation: %+v", result)
	}
}

func TestErrorsDoNotFailOtherItems(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"ok.txt": {text: "works"}}}
	result := read(t, engineFor(t, source, DefaultLimits()), context.Background(), request("missing.txt", 0, 20), request("ok.txt", 0, 20))
	expectCode(t, result.Items[0], "not_found")
	if result.Failed != 1 || result.Items[1].Content != "works" {
		t.Fatalf("partial failure was not isolated: %+v", result)
	}
}

func TestDeniedScopeNeverTouchesSource(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"secret.txt": {text: "secret"}}}
	req := request("secret.txt", 0, 20)
	req.File.RootID = "private"
	result := read(t, engineFor(t, source, DefaultLimits()), context.Background(), req)
	expectCode(t, result.Items[0], "denied")
	if source.opens.Load() != 0 {
		t.Fatal("denied request opened a file")
	}
}

func TestInvalidPathsNeverTouchSource(t *testing.T) {
	paths := []string{"../secret", "/abs", "a/../b", "a//b", "./file", "C:/secret", "a\\b", "file:stream", "NUL.txt", "com1", "LPT\u00b9.txt", "a/", "trail.", "space ", "bad\x00name", "bad\nname", "*", string([]byte{0xff}), strings.Repeat("x", 4097)}
	for _, path := range paths {
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			source := &fixtureSource{files: map[string]fixture{path: {text: "secret"}}}
			result := read(t, engineFor(t, source, DefaultLimits()), context.Background(), request(path, 0, 10))
			expectCode(t, result.Items[0], "invalid_request")
			if source.opens.Load() != 0 || result.Items[0].File.Path != "" {
				t.Fatal("invalid request touched source or echoed path")
			}
		})
	}
	for _, path := range []string{"src/file.go", ".gitignore", "a b/c-d.txt", "\u76ee\u5f55/\u6587\u4ef6.txt", "composer.json"} {
		if !ValidPath(path) {
			t.Fatalf("rejected valid lexical path %q", path)
		}
	}
}

func TestUnicodePageBoundaries(t *testing.T) {
	text := "\u4f60\u597d\U0001f600abc"
	source := &fixtureSource{files: map[string]fixture{"f.txt": {text: text}}}
	engine := engineFor(t, source, DefaultLimits())
	var joined strings.Builder
	offset := int64(0)
	for i := 0; i < 10; i++ {
		result := read(t, engine, context.Background(), request("f.txt", offset, 5)).Items[0]
		if result.Error != nil || !utf8.ValidString(result.Content) {
			t.Fatalf("bad UTF-8 page: %+v", result)
		}
		joined.WriteString(result.Content)
		if result.EOF {
			break
		}
		if result.NextOffset == nil || *result.NextOffset <= offset {
			t.Fatal("continuation did not progress")
		}
		offset = *result.NextOffset
	}
	if joined.String() != text {
		t.Fatalf("bad reconstruction %q", joined.String())
	}
}

func TestEncodingFailuresDoNotRewriteContent(t *testing.T) {
	cases := []struct {
		name, text, code string
		offset           int64
		count            int
	}{
		{"mid-rune", "\u4f60\u597d", "unsupported_encoding", 1, 5},
		{"small-page", "\u4f60\u597d", "budget_exhausted", 0, 2},
		{"invalid", "a\xffb", "unsupported_encoding", 0, 10},
		{"nul", "a\x00b", "unsupported_encoding", 0, 10},
		{"incomplete-eof", "a\xe4\xbd", "unsupported_encoding", 0, 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			source := &fixtureSource{files: map[string]fixture{"f": {text: c.text}}}
			result := read(t, engineFor(t, source, DefaultLimits()), context.Background(), request("f", c.offset, c.count))
			expectCode(t, result.Items[0], c.code)
		})
	}
}

func TestEmptyFileAndEOF(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"empty": {}, "text": {text: "abc"}}}
	result := read(t, engineFor(t, source, DefaultLimits()), context.Background(), request("empty", 0, 10), request("text", 3, 10))
	for _, item := range result.Items {
		if item.Error != nil || !item.EOF || item.BytesRead != 0 || item.NextOffset != nil {
			t.Fatalf("bad EOF: %+v", item)
		}
	}
}

func TestVersionMismatchAndConcurrentChange(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"stable": {text: "abc"}, "changes": {text: "secret", change: true}}}
	engine := engineFor(t, source, DefaultLimits())
	req := request("stable", 0, 10)
	req.ExpectedVersion = "old"
	mismatch := read(t, engine, context.Background(), req).Items[0]
	expectCode(t, mismatch, "stale_version")
	if mismatch.BytesRead != 0 {
		t.Fatal("stale expected version still read content")
	}
	changed := read(t, engine, context.Background(), request("changes", 0, 10)).Items[0]
	expectCode(t, changed, "stale_version")
	if changed.BytesRead != 6 {
		t.Fatal("discarded I/O not counted")
	}
}

func TestTenGiBFileReadsOnlyRequestedRange(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"large.log": {synthetic: true, size: 10 << 30}}}
	offset := int64(9 << 30)
	result := read(t, engineFor(t, source, DefaultLimits()), context.Background(), request("large.log", offset, 4096))
	if result.Failed != 0 || result.BytesRead != 4096 || source.bytes.Load() != 4096 || len(result.Items[0].Content) != 4096 || result.Items[0].EndOffset != offset+4096 {
		t.Fatalf("unbounded/wrong read: %+v", result)
	}
}

func TestOffsetAndRequestBounds(t *testing.T) {
	source := &fixtureSource{files: map[string]fixture{"f": {text: "abc"}}}
	engine := engineFor(t, source, DefaultLimits())
	for _, req := range []Request{request("f", -1, 10), request("f", math.MaxInt64, 10), request("f", 0, -1), request("f", 0, 1<<30)} {
		expectCode(t, read(t, engine, context.Background(), req).Items[0], "invalid_request")
	}
	if _, err := engine.ReadBatch(context.Background(), scopeFor(t), nil); err == nil {
		t.Fatal("accepted empty batch")
	}
	if _, err := engine.ReadBatch(context.Background(), scopeFor(t), make([]Request, 33)); err == nil {
		t.Fatal("accepted oversized batch")
	}
	if _, err := engine.ReadBatch(context.Background(), Scope{}, []Request{request("f", 0, 1)}); err == nil {
		t.Fatal("accepted unauthenticated zero scope")
	}
}

func TestCancellationBeforeAndDuringRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &fixtureSource{files: map[string]fixture{"f": {text: "secret"}}}
	expectCode(t, read(t, engineFor(t, source, DefaultLimits()), ctx, request("f", 0, 10)).Items[0], "cancelled")
	if source.opens.Load() != 0 {
		t.Fatal("opened after cancellation")
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	source = &fixtureSource{files: map[string]fixture{"f": {text: "secret", afterRead: cancel}}}
	expectCode(t, read(t, engineFor(t, source, DefaultLimits()), ctx, request("f", 0, 10)).Items[0], "cancelled")
	if source.closes.Load() != 1 {
		t.Fatal("cancelled handle not closed")
	}
}

func TestExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	source := &fixtureSource{files: map[string]fixture{"f": {text: "secret"}}}
	expectCode(t, read(t, engineFor(t, source, DefaultLimits()), ctx, request("f", 0, 10)).Items[0], "deadline_exceeded")
	if source.opens.Load() != 0 {
		t.Fatal("opened after deadline")
	}
}

func TestSourceAndCloseFailuresAreSanitized(t *testing.T) {
	for _, f := range []fixture{{text: "secret", readErr: errors.New("secret source error")}, {text: "secret", shortRead: true}, {text: "secret", closeErr: true}} {
		source := &fixtureSource{files: map[string]fixture{"f": f}}
		item := read(t, engineFor(t, source, DefaultLimits()), context.Background(), request("f", 0, 10)).Items[0]
		expectCode(t, item, "unavailable")
		if strings.Contains(item.Error.Message, "secret") || source.closes.Load() != 1 {
			t.Fatal("detail leak/handle leak")
		}
	}
	for _, c := range []struct {
		err  error
		code string
	}{{fs.ErrPermission, "denied"}, {fs.ErrNotExist, "not_found"}, {errors.New("private absolute path"), "unavailable"}} {
		source := &fixtureSource{openErr: c.err}
		expectCode(t, read(t, engineFor(t, source, DefaultLimits()), context.Background(), request("f", 0, 10)).Items[0], c.code)
	}
}

func TestTypedSourceErrorsUseOnlyStableCodeAndMessage(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		code    string
		message string
	}{
		{
			name:    "known",
			err:     &ItemError{Code: "unsupported_type", Message: "file type is not supported"},
			code:    "unsupported_type",
			message: "file type is not supported",
		},
		{
			name:    "unknown code",
			err:     &ItemError{Code: "secret_path", Message: "C:/private/secret.txt"},
			code:    "unavailable",
			message: "source operation failed",
		},
		{
			name:    "unknown message",
			err:     &ItemError{Code: "unsupported_type", Message: "C:/private/secret.txt"},
			code:    "unavailable",
			message: "source operation failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := &fixtureSource{openErr: tc.err}
			item := read(t, engineFor(t, source, DefaultLimits()), context.Background(), request("f", 0, 10)).Items[0]
			expectCode(t, item, tc.code)
			if item.Error.Message != tc.message {
				t.Fatalf("message = %q, want %q", item.Error.Message, tc.message)
			}
		})
	}
}

func TestTinyBatchBudgetDoesNotOpenUnfundedItems(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxOutputBytes = 1
	source := &fixtureSource{files: map[string]fixture{"f": {text: "abc"}}}
	result := read(t, engineFor(t, source, limits), context.Background(), request("f", 0, 2), request("f", 0, 2))
	if result.Items[0].Content != "a" || source.opens.Load() != 1 {
		t.Fatal("wrong budget behavior")
	}
	expectCode(t, result.Items[1], "budget_exhausted")
}

func TestScopeCopiesRootsAndChecksIDs(t *testing.T) {
	roots := []string{"project"}
	scope, err := NewScope("a", "v1", roots)
	if err != nil {
		t.Fatal(err)
	}
	roots[0] = "private"
	if !scope.Allows("project") || scope.Allows("private") || scope.ConnectionID() != "a" || scope.ProfileRevision() != "v1" {
		t.Fatal("scope mutation or accessor failure")
	}
	for _, args := range []struct {
		c, r  string
		roots []string
	}{{"", "v1", []string{"x"}}, {"a", "bad/rev", []string{"x"}}, {"a", "v1", nil}, {"a", "v1", []string{"bad/root"}}} {
		if _, err := NewScope(args.c, args.r, args.roots); err == nil {
			t.Fatal("accepted invalid scope")
		}
	}
}

func TestInvalidLimits(t *testing.T) {
	if _, err := New(nil, DefaultLimits()); err == nil {
		t.Fatal("accepted nil source")
	}
	for _, modify := range []func(*Limits){
		func(l *Limits) { l.MaxItems = 0 }, func(l *Limits) { l.Workers = 100 },
		func(l *Limits) { l.MaxItemBytes = 1 << 30 }, func(l *Limits) { l.MaxOutputBytes = -1 },
		func(l *Limits) { l.MaxReadBytes = 1 << 30 }, func(l *Limits) { l.Timeout = 0 },
	} {
		limits := DefaultLimits()
		modify(&limits)
		if _, err := New(&fixtureSource{}, limits); err == nil {
			t.Fatal("accepted unsafe limits")
		}
	}
}

func FuzzValidPath(f *testing.F) {
	for _, seed := range []string{"src/a.go", "../secret", "C:/foo", "NUL", "\u6587\u4ef6.txt", "a\\b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		if ValidPath(p) && (p == "" || len(p) > 4096 || !utf8.ValidString(p) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, ":")) {
			t.Fatalf("invalid accepted path %q", p)
		}
	})
}

func FuzzUTF8Pagination(f *testing.F) {
	f.Add("abc", uint8(4))
	f.Add("\u4f60\u597d\U0001f600", uint8(5))
	f.Fuzz(func(t *testing.T, text string, size uint8) {
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text) > 8192 {
			t.Skip()
		}
		pageSize := 4 + int(size)%60
		source := &fixtureSource{files: map[string]fixture{"f": {text: text}}}
		engine := engineFor(t, source, DefaultLimits())
		var joined strings.Builder
		offset := int64(0)
		for i := 0; i <= len(text)+1; i++ {
			result := read(t, engine, context.Background(), request("f", offset, pageSize))
			item := result.Items[0]
			if item.Error != nil || item.BytesRead > pageSize || !utf8.ValidString(item.Content) {
				t.Fatalf("bad page %+v", item)
			}
			joined.WriteString(item.Content)
			if item.EOF {
				if joined.String() != text {
					t.Fatal("content mismatch")
				}
				return
			}
			if item.NextOffset == nil || *item.NextOffset <= offset {
				t.Fatal("non-progressing cursor")
			}
			offset = *item.NextOffset
		}
		t.Fatal("pagination failed to terminate")
	})
}

func BenchmarkLargeRange(b *testing.B) {
	source := &fixtureSource{files: map[string]fixture{"large": {synthetic: true, size: 10 << 30}}}
	engine := engineFor(b, source, DefaultLimits())
	scope := scopeFor(b)
	requests := []Request{request("large", 9<<30, 4096)}
	b.ReportAllocs()
	b.SetBytes(4096)
	for i := 0; i < b.N; i++ {
		result, err := engine.ReadBatch(context.Background(), scope, requests)
		if err != nil || result.Failed != 0 || result.BytesRead != 4096 {
			b.Fatal("read failed")
		}
	}
}
