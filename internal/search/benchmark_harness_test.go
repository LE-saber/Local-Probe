package search_test

// This file is an opt-in evidence harness for the direct filesystem search
// path. It deliberately creates synthetic fixtures below a test temp
// directory and never writes fixture data to the repository. The default test
// uses a small fixture; 10k/100k/1m runs require both an explicit size and
// LOCAL_PROBE_SEARCH_HARNESS_ALLOW_LARGE=1.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
	"github.com/LE-saber/Local-Probe/internal/search"
)

const (
	harnessSizeEnv      = "LOCAL_PROBE_SEARCH_HARNESS_SIZE"
	harnessAllowEnv     = "LOCAL_PROBE_SEARCH_HARNESS_ALLOW_LARGE"
	harnessEvidenceEnv  = "LOCAL_PROBE_SEARCH_HARNESS_EVIDENCE"
	harnessSmallEntries = 256
	harnessNeedleEvery  = 257
	harnessRootID       = "project"
	harnessConnectionID = "connection"
)

type syntheticSpec struct {
	Label       string
	Entries     int
	Directories int
	Files       int
	NeedleFiles int
	NeedleEvery int
	TopDirs     int
	BucketDirs  int
}

func parseSyntheticSpec(label string) (syntheticSpec, bool) {
	label = strings.ToLower(strings.TrimSpace(label))
	spec := syntheticSpec{Label: label, NeedleEvery: harnessNeedleEvery}
	switch label {
	case "", "small":
		spec.Label = "small"
		spec.Entries = harnessSmallEntries
		spec.TopDirs, spec.BucketDirs = 4, 4
	case "10k":
		spec.Entries = 10_000
		spec.TopDirs, spec.BucketDirs = 8, 8
	case "100k":
		spec.Entries = 100_000
		spec.TopDirs, spec.BucketDirs = 16, 16
	case "1m":
		spec.Entries = 1_000_000
		spec.TopDirs, spec.BucketDirs = 32, 32
	default:
		return syntheticSpec{}, false
	}
	spec.Directories = spec.TopDirs + spec.TopDirs*spec.BucketDirs
	spec.Files = spec.Entries - spec.Directories
	if spec.Files < 1 {
		return syntheticSpec{}, false
	}
	spec.NeedleFiles = (spec.Files + spec.NeedleEvery - 1) / spec.NeedleEvery
	return spec, true
}

func selectedSyntheticSpec(tb testing.TB) syntheticSpec {
	tb.Helper()
	label := os.Getenv(harnessSizeEnv)
	spec, ok := parseSyntheticSpec(label)
	if !ok {
		tb.Fatalf("%s=%q is invalid; use small, 10k, 100k, or 1m", harnessSizeEnv, label)
	}
	if spec.Label != "small" && os.Getenv(harnessAllowEnv) != "1" {
		tb.Skipf("%s=%s requires %s=1; large synthetic fixtures are opt-in", harnessSizeEnv, spec.Label, harnessAllowEnv)
	}
	return spec
}

type syntheticFixture struct {
	Root       string
	Spec       syntheticSpec
	CreateTime time.Duration
}

func createSyntheticFixture(tb testing.TB, spec syntheticSpec) syntheticFixture {
	tb.Helper()
	started := time.Now()
	root := tb.TempDir()
	for top := 0; top < spec.TopDirs; top++ {
		for bucket := 0; bucket < spec.BucketDirs; bucket++ {
			path := filepath.Join(root, fmt.Sprintf("shard-%03d", top), fmt.Sprintf("bucket-%03d", bucket))
			if err := os.MkdirAll(path, 0o700); err != nil {
				tb.Fatalf("create synthetic directory %q: %v", path, err)
			}
		}
	}
	for index := 0; index < spec.Files; index++ {
		top := index % spec.TopDirs
		bucket := (index / spec.TopDirs) % spec.BucketDirs
		name := fmt.Sprintf("file-%07d.txt", index)
		path := filepath.Join(root, fmt.Sprintf("shard-%03d", top), fmt.Sprintf("bucket-%03d", bucket), name)
		content := fmt.Sprintf("plain-%07d\n", index)
		if index%spec.NeedleEvery == 0 {
			content = fmt.Sprintf("needle-%07d\n", index)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			tb.Fatalf("create synthetic file %q: %v", path, err)
		}
	}
	return syntheticFixture{Root: root, Spec: spec, CreateTime: time.Since(started)}
}

type searchRuntime struct {
	Service *search.Service
	Bound   policy.BoundScope
	Source  *rootfs.Source
}

func openSyntheticRuntime(tb testing.TB, fixture syntheticFixture) searchRuntime {
	tb.Helper()
	root, err := config.NewRoot(harnessRootID, fixture.Root, nil)
	if err != nil {
		tb.Fatalf("configure synthetic root: %v", err)
	}
	profile, err := config.NewProfile("read", []string{harnessRootID}, []string{"find_files", "search_text"}, nil)
	if err != nil {
		tb.Fatalf("configure synthetic profile: %v", err)
	}
	credential := config.NewCredentialRef("credential", "local_token")
	connection := config.NewConnection(harnessConnectionID, "synthetic", profile.ID(), credential.ID(), true)
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credential})
	if err != nil {
		tb.Fatalf("configure synthetic connection: %v", err)
	}
	store, err := config.NewStore(cfg)
	if err != nil {
		tb.Fatalf("create synthetic config store: %v", err)
	}
	manager, err := policy.NewManager(store)
	if err != nil {
		tb.Fatalf("create synthetic policy manager: %v", err)
	}
	bound, err := manager.BindAuthenticated(connection.ID())
	if err != nil {
		tb.Fatalf("bind synthetic scope: %v", err)
	}
	source, err := rootfs.New(store)
	if err != nil {
		tb.Fatalf("open synthetic root: %v", err)
	}
	limits := search.DefaultLimits()
	limits.MaxPageSize = 1024
	limits.MaxScannedEntries = fixture.Spec.Entries
	limits.MaxReadBytes = minInt(fixture.Spec.Files*16+1024, 64<<20)
	limits.MaxOpenFiles = fixture.Spec.Files + 1
	limits.MaxOpenDirectories = fixture.Spec.Directories + 2
	limits.DirectoryBatch = 256
	limits.Timeout = 30 * time.Second
	service, err := search.New(source, limits, []byte("synthetic-search-harness-key-0123456789"))
	if err != nil {
		_ = source.Close()
		tb.Fatalf("create synthetic search service: %v", err)
	}
	return searchRuntime{Service: service, Bound: bound, Source: source}
}

type coverageTotals struct {
	Complete            bool `json:"complete"`
	PagesConsumed       int  `json:"pages_consumed"`
	ScannedEntries      int  `json:"scanned_entries"`
	ReplayedEntries     int  `json:"replayed_entries"`
	ReturnedEntries     int  `json:"returned_entries"`
	OpenedFiles         int  `json:"opened_files"`
	OpenedDirectories   int  `json:"opened_directories"`
	DeniedEntries       int  `json:"denied_entries"`
	IgnoredEntries      int  `json:"ignored_entries"`
	UnsupportedEntries  int  `json:"unsupported_entries"`
	InvalidEncoding     int  `json:"invalid_encoding_entries"`
	DepthLimitedEntries int  `json:"depth_limited_entries"`
	ReadBytes           int  `json:"read_bytes"`
	ReturnedBytes       int  `json:"returned_bytes"`
}

func (c *coverageTotals) add(value search.Coverage) {
	c.ScannedEntries += value.ScannedEntries
	c.ReplayedEntries += value.ReplayedEntries
	c.ReturnedEntries += value.ReturnedEntries
	c.OpenedFiles += value.OpenedFiles
	c.OpenedDirectories += value.OpenedDirectories
	c.DeniedEntries += value.DeniedEntries
	c.IgnoredEntries += value.IgnoredEntries
	c.UnsupportedEntries += value.UnsupportedEntries
	c.InvalidEncoding += value.InvalidEncodingEntries
	c.DepthLimitedEntries += value.DepthLimitedEntries
	c.ReadBytes += value.ReadBytes
	c.ReturnedBytes += value.ReturnedBytes
	c.Complete = value.Complete
}

type syntheticValidator struct {
	Spec syntheticSpec
	Seen []byte
}

func newSyntheticValidator(spec syntheticSpec) *syntheticValidator {
	return &syntheticValidator{Spec: spec, Seen: make([]byte, (spec.Files+7)/8)}
}

func (v *syntheticValidator) mark(path string, requireNeedle bool) error {
	base := filepath.ToSlash(path)
	name := base[strings.LastIndexByte(base, '/')+1:]
	if !strings.HasPrefix(name, "file-") || !strings.HasSuffix(name, ".txt") {
		return fmt.Errorf("unexpected synthetic result path %q", path)
	}
	index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "file-"), ".txt"))
	if err != nil || index < 0 || index >= v.Spec.Files {
		return fmt.Errorf("synthetic result index out of range: %q", path)
	}
	if requireNeedle && index%v.Spec.NeedleEvery != 0 {
		return fmt.Errorf("synthetic needle classification mismatch for %q", path)
	}
	byteIndex, bit := index/8, byte(1<<(index%8))
	if v.Seen[byteIndex]&bit != 0 {
		return fmt.Errorf("duplicate synthetic result path %q", path)
	}
	v.Seen[byteIndex] |= bit
	return nil
}

func (v *syntheticValidator) count() int {
	count := 0
	for _, value := range v.Seen {
		for value != 0 {
			value &= value - 1
			count++
		}
	}
	return count
}

func collectFind(ctx context.Context, runtime searchRuntime, spec syntheticSpec, validate bool) (coverageTotals, int, error) {
	var totals coverageTotals
	var validator *syntheticValidator
	if validate {
		validator = newSyntheticValidator(spec)
	}
	request := search.FindFilesRequest{RootID: harnessRootID, Pattern: "**/*.txt", PageSize: 1024, MaxEntries: spec.Entries}
	maxPages := harnessPageLimit(spec, "find_files")
	for pages := 0; pages < maxPages; pages++ {
		result, err := runtime.Service.FindFiles(ctx, runtime.Bound, request)
		if err != nil {
			return totals, 0, fmt.Errorf("find_files page %d: %w", pages, err)
		}
		totals.PagesConsumed = pages + 1
		totals.add(result.Coverage)
		if validate {
			for _, entry := range result.Entries {
				if err := validator.mark(entry.Path, false); err != nil {
					return totals, 0, err
				}
			}
		}
		if result.Coverage.Complete {
			if result.Continuation != "" {
				return totals, 0, errors.New("find_files reported complete with continuation")
			}
			if validate && validator.count() != spec.Files {
				return totals, 0, fmt.Errorf("find_files returned %d files, want %d", validator.count(), spec.Files)
			}
			return totals, totals.ReturnedEntries, nil
		}
		if result.Continuation == "" {
			return totals, 0, errors.New("find_files stopped without continuation")
		}
		request.Cursor = result.Continuation
	}
	return totals, 0, fmt.Errorf("find_files pagination exceeded explicit page bound %d", maxPages)
}

func collectSearch(ctx context.Context, runtime searchRuntime, spec syntheticSpec, validate bool) (coverageTotals, int, error) {
	var totals coverageTotals
	var validator *syntheticValidator
	if validate {
		validator = newSyntheticValidator(spec)
	}
	// Search has to scan every regular file before it can report a match. A
	// bounded per-call scan keeps a large Windows fixture from hitting the
	// service's 30-second operation deadline; the signed continuation then
	// consumes the remaining entries. This is a harness pagination choice, not
	// an assertion about a production default.
	request := search.SearchTextRequest{RootID: harnessRootID, Query: "needle", PageSize: 1024, MaxEntries: searchEntriesPerPage(spec), MaxReadBytes: minInt(spec.Files*16+1024, 64<<20)}
	maxPages := harnessPageLimit(spec, "search_text")
	for pages := 0; pages < maxPages; pages++ {
		result, err := runtime.Service.SearchText(ctx, runtime.Bound, request)
		if err != nil {
			return totals, 0, fmt.Errorf("search_text page %d: %w", pages, err)
		}
		totals.PagesConsumed = pages + 1
		totals.add(result.Coverage)
		if validate {
			for _, match := range result.Matches {
				if err := validator.mark(match.Path, true); err != nil {
					return totals, 0, err
				}
			}
		}
		if result.Coverage.Complete {
			if result.Continuation != "" {
				return totals, 0, errors.New("search_text reported complete with continuation")
			}
			if validate && validator.count() != spec.NeedleFiles {
				return totals, 0, fmt.Errorf("search_text returned %d matches, want %d", validator.count(), spec.NeedleFiles)
			}
			return totals, totals.ReturnedEntries, nil
		}
		if result.Continuation == "" {
			return totals, 0, errors.New("search_text stopped without continuation")
		}
		request.Cursor = result.Continuation
	}
	return totals, 0, fmt.Errorf("search_text pagination exceeded explicit page bound %d", maxPages)
}

func searchEntriesPerPage(spec syntheticSpec) int {
	if spec.Files <= 512 {
		return spec.Entries
	}
	return 256
}

func harnessPageLimit(spec syntheticSpec, operation string) int {
	entriesPerPage := 1024
	if operation == "search_text" {
		entriesPerPage = searchEntriesPerPage(spec)
	}
	// A complete fixture should consume roughly entries/page calls. The
	// directory allowance covers cursor replay through the fixed three-level
	// synthetic tree; the fixed slack makes the guard fail closed if pagination
	// behavior changes. This is a harness termination bound, not a performance
	// claim.
	return (spec.Entries+entriesPerPage-1)/entriesPerPage + spec.Directories + 1024
}

type harnessEvidence struct {
	SizeLabel           string         `json:"size_label"`
	Platform            string         `json:"platform"`
	Operation           string         `json:"operation"`
	Phase               string         `json:"phase"`
	OSCacheState        string         `json:"os_cache_state"`
	FixtureEntries      int            `json:"fixture_entries"`
	FixtureFiles        int            `json:"fixture_files"`
	FixtureDirectories  int            `json:"fixture_directories"`
	FixtureCreateNanos  int64          `json:"fixture_create_nanos,omitempty"`
	ElapsedNanos        int64          `json:"elapsed_nanos"`
	ResultCount         int            `json:"result_count"`
	ExpectedResults     int            `json:"expected_results"`
	AllocBytes          uint64         `json:"alloc_bytes"`
	AllocObjects        uint64         `json:"alloc_objects"`
	RSSAvailable        bool           `json:"rss_available"`
	RSSBeforeBytes      uint64         `json:"rss_before_bytes,omitempty"`
	RSSAfterBytes       uint64         `json:"rss_after_bytes,omitempty"`
	Coverage            coverageTotals `json:"coverage"`
	MaxPages            int            `json:"max_pages"`
	PerCallTimeoutNanos int64          `json:"per_call_timeout_nanos"`
}

func measureEvidence(tb testing.TB, fixture syntheticFixture, operation, phase string, run func() (coverageTotals, int, error)) harnessEvidence {
	tb.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rssBefore, rssOKBefore := currentRSSBytes()
	started := time.Now()
	coverage, resultCount, err := run()
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	rssAfter, rssOKAfter := currentRSSBytes()
	if err != nil {
		tb.Fatalf("%s %s: %v", operation, phase, err)
	}
	evidence := harnessEvidence{
		SizeLabel:           fixture.Spec.Label,
		Platform:            runtime.GOOS + "/" + runtime.GOARCH,
		Operation:           operation,
		Phase:               phase,
		OSCacheState:        "unknown",
		FixtureEntries:      fixture.Spec.Entries,
		FixtureFiles:        fixture.Spec.Files,
		FixtureDirectories:  fixture.Spec.Directories,
		FixtureCreateNanos:  fixture.CreateTime.Nanoseconds(),
		ElapsedNanos:        elapsed.Nanoseconds(),
		ResultCount:         resultCount,
		ExpectedResults:     fixture.Spec.Files,
		AllocBytes:          after.TotalAlloc - before.TotalAlloc,
		AllocObjects:        after.Mallocs - before.Mallocs,
		RSSAvailable:        rssOKBefore && rssOKAfter,
		RSSBeforeBytes:      rssBefore,
		RSSAfterBytes:       rssAfter,
		Coverage:            coverage,
		MaxPages:            harnessPageLimit(fixture.Spec, operation),
		PerCallTimeoutNanos: int64(30 * time.Second),
	}
	if operation == "search_text" {
		evidence.ExpectedResults = fixture.Spec.NeedleFiles
	}
	tb.Logf("search_harness_evidence=%s", mustJSON(evidence))
	return evidence
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("json_error:%v", err)
	}
	return string(data)
}

func TestSyntheticSearchHarnessSizeLabels(t *testing.T) {
	cases := map[string]int{"small": 256, "10k": 10_000, "100k": 100_000, "1m": 1_000_000}
	for label, want := range cases {
		spec, ok := parseSyntheticSpec(label)
		if !ok || spec.Entries != want || spec.Files+spec.Directories != want || spec.NeedleFiles < 1 {
			t.Fatalf("parseSyntheticSpec(%q) = %#v, want %d total entries", label, spec, want)
		}
	}
	if _, ok := parseSyntheticSpec("bogus"); ok {
		t.Fatal("invalid fixture size accepted")
	}
}

func TestSyntheticSearchHarnessCorrectnessAndEvidence(t *testing.T) {
	if value := os.Getenv(harnessEvidenceEnv); value != "" && value != "1" {
		t.Skipf("%s must be 1 to run evidence mode", harnessEvidenceEnv)
	}
	spec := syntheticSpec{Label: "small", Entries: harnessSmallEntries, TopDirs: 4, BucketDirs: 4, NeedleEvery: harnessNeedleEvery}
	spec.Directories = spec.TopDirs + spec.TopDirs*spec.BucketDirs
	spec.Files = spec.Entries - spec.Directories
	spec.NeedleFiles = (spec.Files + spec.NeedleEvery - 1) / spec.NeedleEvery
	if os.Getenv(harnessEvidenceEnv) == "1" {
		spec = selectedSyntheticSpec(t)
	}
	fixture := createSyntheticFixture(t, spec)
	ctx := context.Background()

	findRuntime := openSyntheticRuntime(t, fixture)
	t.Cleanup(func() { _ = findRuntime.Source.Close() })
	findCold := measureEvidence(t, fixture, "find_files", "service_cold_same_process", func() (coverageTotals, int, error) {
		return collectFind(ctx, findRuntime, spec, true)
	})
	findWarm := measureEvidence(t, fixture, "find_files", "warm_same_fixture_repeat", func() (coverageTotals, int, error) {
		return collectFind(ctx, findRuntime, spec, true)
	})
	if err := findRuntime.Source.Close(); err != nil {
		t.Fatalf("close synthetic find source: %v", err)
	}

	searchRuntime := openSyntheticRuntime(t, fixture)
	t.Cleanup(func() { _ = searchRuntime.Source.Close() })
	searchCold := measureEvidence(t, fixture, "search_text", "service_cold_same_process", func() (coverageTotals, int, error) {
		return collectSearch(ctx, searchRuntime, spec, true)
	})
	searchWarm := measureEvidence(t, fixture, "search_text", "warm_same_fixture_repeat", func() (coverageTotals, int, error) {
		return collectSearch(ctx, searchRuntime, spec, true)
	})
	if err := searchRuntime.Source.Close(); err != nil {
		t.Fatalf("close synthetic search source: %v", err)
	}
	for _, evidence := range []harnessEvidence{findCold, findWarm, searchCold, searchWarm} {
		if !evidence.Coverage.Complete || evidence.Coverage.ScannedEntries < spec.Entries {
			t.Fatalf("incomplete synthetic evidence: %#v", evidence)
		}
	}
}

func BenchmarkDirectFindSynthetic(b *testing.B) {
	spec := selectedSyntheticSpec(b)
	fixture := createSyntheticFixture(b, spec)
	b.Logf("synthetic fixture size=%s entries=%d files=%d dirs=%d create=%s; OS cache state=unknown", spec.Label, spec.Entries, spec.Files, spec.Directories, fixture.CreateTime)

	b.Run("service_cold_same_process", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			runtime := openSyntheticRuntime(b, fixture)
			b.StartTimer()
			coverage, _, err := collectFind(context.Background(), runtime, spec, false)
			b.StopTimer()
			_ = runtime.Source.Close()
			if err != nil || !coverage.Complete {
				b.Fatalf("cold find coverage=%#v err=%v", coverage, err)
			}
		}
	})

	b.Run("warm_same_fixture_repeat", func(b *testing.B) {
		runtime := openSyntheticRuntime(b, fixture)
		defer runtime.Source.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			coverage, _, err := collectFind(context.Background(), runtime, spec, false)
			if err != nil || !coverage.Complete {
				b.Fatalf("warm find coverage=%#v err=%v", coverage, err)
			}
		}
	})
}

func BenchmarkDirectSearchSynthetic(b *testing.B) {
	spec := selectedSyntheticSpec(b)
	fixture := createSyntheticFixture(b, spec)
	b.Logf("synthetic fixture size=%s entries=%d files=%d dirs=%d create=%s; OS cache state=unknown", spec.Label, spec.Entries, spec.Files, spec.Directories, fixture.CreateTime)

	b.Run("service_cold_same_process", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			runtime := openSyntheticRuntime(b, fixture)
			b.StartTimer()
			coverage, _, err := collectSearch(context.Background(), runtime, spec, false)
			b.StopTimer()
			_ = runtime.Source.Close()
			if err != nil || !coverage.Complete {
				b.Fatalf("cold search coverage=%#v err=%v", coverage, err)
			}
		}
	})

	b.Run("warm_same_fixture_repeat", func(b *testing.B) {
		runtime := openSyntheticRuntime(b, fixture)
		defer runtime.Source.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			coverage, _, err := collectSearch(context.Background(), runtime, spec, false)
			if err != nil || !coverage.Complete {
				b.Fatalf("warm search coverage=%#v err=%v", coverage, err)
			}
		}
	})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
