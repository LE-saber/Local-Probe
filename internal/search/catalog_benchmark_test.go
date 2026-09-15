package search_test

// This file compares the direct, live filesystem walk with the R7 local-only
// metadata catalog.  The comparison is deliberately test-only: the catalog
// is built from a complete direct manifest, queried as a candidate source, and
// every candidate is opened again through the current rootfs-bound search
// source before it is counted as verified.  A catalog candidate is never
// treated as an existence proof.

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/catalog"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
	"github.com/LE-saber/Local-Probe/internal/search"
)

const (
	catalogComparisonPrefix              = "shard-000"
	catalogComparisonQueryPageSize       = 1024
	catalogComparisonStageTimeout        = 30 * time.Minute
	catalogWarmQueryMaxRatioReportOnly   = 0.5
	catalogWarmQueryTimingRepeats        = 8
	catalogComparisonOperation           = "catalog_candidate_comparison"
	catalogComparisonEvidenceEnvironment = harnessEvidenceEnv
)

// Correctness is a hard gate: the complete direct manifest must be
// reconciled, the same-prefix direct result set and catalog candidate set must
// be equal, and every catalog candidate must pass a fresh rootfs OpenFile plus
// Metadata check under the current BoundScope.  Performance is deliberately a
// report-only signal because host filesystem and antivirus noise make a timing
// failure unsuitable for default CI. The predeclared report-only target is
// warm catalog query <= 0.5x direct prefix scan (at least 2x faster); a
// failure is evidence against enabling the optimization, not a CI failure.
type catalogComparisonStage struct {
	SizeLabel        string `json:"size_label"`
	Platform         string `json:"platform"`
	Operation        string `json:"operation"`
	Stage            string `json:"stage"`
	Phase            string `json:"phase"`
	ElapsedNanos     int64  `json:"elapsed_nanos"`
	AllocBytes       uint64 `json:"alloc_bytes"`
	AllocObjects     uint64 `json:"alloc_objects"`
	RSSAvailable     bool   `json:"rss_available"`
	RSSBeforeBytes   uint64 `json:"rss_before_bytes,omitempty"`
	RSSAfterBytes    uint64 `json:"rss_after_bytes,omitempty"`
	CandidateCount   int    `json:"candidate_count"`
	VerifiedCount    int    `json:"verified_count"`
	ExpectedCount    int    `json:"expected_count"`
	Iterations       int    `json:"iterations"`
	SetMatchesDirect bool   `json:"set_matches_direct,omitempty"`
}

type catalogComparisonReport struct {
	SizeLabel                         string                   `json:"size_label"`
	Platform                          string                   `json:"platform"`
	Operation                         string                   `json:"operation"`
	Prefix                            string                   `json:"prefix"`
	DirectManifestCount               int                      `json:"direct_manifest_count"`
	DirectPrefixCount                 int                      `json:"direct_prefix_count"`
	CatalogPrefixCandidateCount       int                      `json:"catalog_prefix_candidate_count"`
	LiveVerifiedCount                 int                      `json:"live_verified_count"`
	FinalSetMatchesDirect             bool                     `json:"final_set_matches_direct"`
	WarmQueryToDirectRatio            float64                  `json:"warm_query_to_direct_ratio,omitempty"`
	WarmQueryMaxRatioReportOnly       float64                  `json:"warm_query_max_ratio_report_only"`
	WarmQueryAtLeastTwoX              bool                     `json:"warm_query_at_least_two_x"`
	PerformanceGateEnforced           bool                     `json:"performance_gate_enforced"`
	LiveVerificationSeparate          bool                     `json:"live_verification_separate"`
	LiveVerificationMatchesCandidates bool                     `json:"live_verification_matches_candidates"`
	Stages                            []catalogComparisonStage `json:"stages"`
}

func measureCatalogComparisonStage(tb testing.TB, fixture syntheticFixture, stage, phase string, expected int, run func() (int, int, error)) catalogComparisonStage {
	tb.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	rssBefore, rssOKBefore := currentRSSBytes()
	started := time.Now()
	candidates, verified, err := run()
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	rssAfter, rssOKAfter := currentRSSBytes()
	if err != nil {
		tb.Fatalf("catalog comparison stage %s: %v", stage, err)
	}
	return catalogComparisonStage{
		SizeLabel:      fixture.Spec.Label,
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		Operation:      catalogComparisonOperation,
		Stage:          stage,
		Phase:          phase,
		ElapsedNanos:   elapsed.Nanoseconds(),
		AllocBytes:     after.TotalAlloc - before.TotalAlloc,
		AllocObjects:   after.Mallocs - before.Mallocs,
		RSSAvailable:   rssOKBefore && rssOKAfter,
		RSSBeforeBytes: rssBefore,
		RSSAfterBytes:  rssAfter,
		CandidateCount: candidates,
		VerifiedCount:  verified,
		ExpectedCount:  expected,
		Iterations:     1,
	}
}

// collectDirectEntries is a direct FindFiles baseline that retains the
// relative-path manifest.  It checks continuation completeness and exact
// duplicate-free paths, while leaving authorization and actual opening to the
// existing search service.
func collectDirectEntries(ctx context.Context, runtime searchRuntime, spec syntheticSpec, path string) ([]search.Entry, coverageTotals, error) {
	var totals coverageTotals
	request := search.FindFilesRequest{
		RootID:     harnessRootID,
		Path:       path,
		Pattern:    "**/*.txt",
		PageSize:   1024,
		MaxEntries: spec.Entries,
	}
	entries := make([]search.Entry, 0)
	seen := make(map[string]struct{})
	for pages := 0; pages < harnessPageLimit(spec, "find_files"); pages++ {
		result, err := runtime.Service.FindFiles(ctx, runtime.Bound, request)
		if err != nil {
			return nil, totals, fmt.Errorf("direct find_files page %d: %w", pages, err)
		}
		totals.PagesConsumed = pages + 1
		totals.add(result.Coverage)
		for _, entry := range result.Entries {
			if _, duplicate := seen[entry.Path]; duplicate {
				return nil, totals, fmt.Errorf("direct find_files duplicate path %q", entry.Path)
			}
			seen[entry.Path] = struct{}{}
			entries = append(entries, entry)
		}
		if result.Coverage.Complete {
			if result.Continuation != "" {
				return nil, totals, fmt.Errorf("direct find_files complete with continuation")
			}
			return entries, totals, nil
		}
		if result.Continuation == "" {
			return nil, totals, fmt.Errorf("direct find_files stopped without continuation")
		}
		request.Cursor = result.Continuation
	}
	return nil, totals, fmt.Errorf("direct find_files pagination exceeded explicit page bound %d", harnessPageLimit(spec, "find_files"))
}

func catalogComparisonCandidates(entries []search.Entry) ([]catalog.Candidate, error) {
	candidates := make([]catalog.Candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != search.EntryRegular {
			return nil, fmt.Errorf("direct manifest returned non-regular entry %q (%s)", entry.Path, entry.Type)
		}
		candidates = append(candidates, catalog.Candidate{
			RootID: harnessRootID,
			Path:   entry.Path,
			Kind:   catalog.KindRegular,
			Metadata: catalog.Metadata{
				SizeBytes:    entry.SizeBytes,
				VersionToken: entry.ModTime,
			},
		})
	}
	return candidates, nil
}

func collectCatalogCandidates(ctx context.Context, value *catalog.Catalog, scope policy.BoundScope, req catalog.QueryRequest, maxPages int) ([]catalog.Candidate, error) {
	if maxPages < 1 {
		return nil, fmt.Errorf("invalid catalog page bound %d", maxPages)
	}
	result := make([]catalog.Candidate, 0)
	seen := make(map[string]struct{})
	for page := 0; page < maxPages; page++ {
		current, err := value.Query(ctx, scope, req)
		if err != nil {
			return nil, fmt.Errorf("catalog query page %d: %w", page, err)
		}
		for _, candidate := range current.Candidates {
			key := candidate.RootID + "\x00" + candidate.Path
			if _, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("catalog query duplicate candidate %q", candidate.Path)
			}
			seen[key] = struct{}{}
			result = append(result, candidate)
		}
		if current.Next == nil {
			return result, nil
		}
		req.Cursor = current.Next
	}
	return nil, fmt.Errorf("catalog query pagination exceeded explicit page bound %d", maxPages)
}

func catalogComparisonMaxPages(spec syntheticSpec) int {
	return (spec.Files+catalogComparisonQueryPageSize-1)/catalogComparisonQueryPageSize + 2
}

func liveVerifyCatalogCandidates(ctx context.Context, source search.Source, scope policy.BoundScope, candidates []catalog.Candidate) (int, error) {
	if source == nil {
		return 0, fmt.Errorf("catalog live verification source is nil")
	}
	verified := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return verified, err
		}
		if candidate.Kind != catalog.KindRegular {
			return verified, fmt.Errorf("catalog candidate %q is not regular", candidate.Path)
		}
		handle, err := source.OpenFile(ctx, scope, readcore.FileRef{RootID: candidate.RootID, Path: candidate.Path})
		if err != nil {
			return verified, fmt.Errorf("live verify %q: %w", candidate.Path, err)
		}
		_, metadataErr := handle.Metadata(ctx)
		closeErr := handle.Close()
		if metadataErr != nil {
			return verified, fmt.Errorf("live metadata %q: %w", candidate.Path, metadataErr)
		}
		if closeErr != nil {
			return verified, fmt.Errorf("live close %q: %w", candidate.Path, closeErr)
		}
		verified++
	}
	return verified, nil
}

func exactDirectPathSet(entries []search.Entry) map[string]struct{} {
	set := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		set[entry.Path] = struct{}{}
	}
	return set
}

func exactCatalogPathSet(entries []catalog.Candidate) map[string]struct{} {
	set := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		set[entry.Path] = struct{}{}
	}
	return set
}

func equalPathSets(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for path := range left {
		if _, ok := right[path]; !ok {
			return false
		}
	}
	return true
}

func catalogComparisonLimits(spec syntheticSpec) catalog.Limits {
	limits := catalog.DefaultLimits()
	limits.MaxEntries = spec.Files
	limits.MaxPageSize = catalogComparisonQueryPageSize
	if spec.Files > 100_000 {
		// The catalog's normal 64 MiB bound is intentionally retained for the
		// default/100k evidence.  One-million-entry evidence must opt in to a
		// larger, still bounded test budget explicitly.
		limits.MaxMemoryBytes = 1 << 30
	}
	return limits
}

func comparisonStageContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), catalogComparisonStageTimeout)
}

func TestCatalogCandidateComparisonEvidence(t *testing.T) {
	spec := syntheticSpec{Label: "small", Entries: harnessSmallEntries, TopDirs: 4, BucketDirs: 4, NeedleEvery: harnessNeedleEvery}
	spec.Directories = spec.TopDirs + spec.TopDirs*spec.BucketDirs
	spec.Files = spec.Entries - spec.Directories
	spec.NeedleFiles = (spec.Files + spec.NeedleEvery - 1) / spec.NeedleEvery
	if os.Getenv(catalogComparisonEvidenceEnvironment) == "1" {
		spec = selectedSyntheticSpec(t)
	}
	fixture := createSyntheticFixture(t, spec)
	searchRuntimeValue := openSyntheticRuntime(t, fixture)
	t.Cleanup(func() { _ = searchRuntimeValue.Source.Close() })

	var manifest []search.Entry
	manifestStage := measureCatalogComparisonStage(t, fixture, "direct_manifest_scan", "service_cold_same_process", spec.Files, func() (int, int, error) {
		ctx, cancel := comparisonStageContext()
		defer cancel()
		entries, _, err := collectDirectEntries(ctx, searchRuntimeValue, spec, "")
		if err == nil {
			manifest = entries
		}
		return len(entries), 0, err
	})
	if len(manifest) != spec.Files {
		t.Fatalf("direct manifest count=%d want=%d", len(manifest), spec.Files)
	}

	prefix := catalogComparisonPrefix
	var directPrefix []search.Entry
	directStage := measureCatalogComparisonStage(t, fixture, "direct_prefix_scan", "same_fixture_runtime", 0, func() (int, int, error) {
		ctx, cancel := comparisonStageContext()
		defer cancel()
		entries, _, err := collectDirectEntries(ctx, searchRuntimeValue, spec, prefix)
		if err == nil {
			directPrefix = entries
		}
		return len(entries), 0, err
	})
	if len(directPrefix) == 0 {
		t.Fatal("direct prefix baseline returned no files")
	}
	directStage.ExpectedCount = len(directPrefix)

	candidates, err := catalogComparisonCandidates(manifest)
	if err != nil {
		t.Fatal(err)
	}
	value, err := catalog.New(searchRuntimeValue.Bound, catalogComparisonLimits(spec))
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}
	t.Cleanup(func() { _ = value.Close(searchRuntimeValue.Bound) })

	buildStage := measureCatalogComparisonStage(t, fixture, "catalog_build_reconcile", "same_fixture_runtime", len(manifest), func() (int, int, error) {
		ctx, cancel := comparisonStageContext()
		defer cancel()
		err := value.Reconcile(ctx, searchRuntimeValue.Bound, catalog.ReconcileInput{SourceGeneration: "synthetic-direct-manifest-v1", Candidates: candidates})
		if err != nil {
			return 0, 0, err
		}
		return len(candidates), 0, nil
	})

	queryRequest := catalog.QueryRequest{RootID: harnessRootID, Prefix: prefix, PageSize: catalogComparisonQueryPageSize}
	// Prime once so the measured query is a genuinely warm in-memory catalog
	// query.  The measured repeat remains candidate-only; no filesystem claim
	// is made until the separate live verification stage below.
	{
		ctx, cancel := comparisonStageContext()
		_, err := collectCatalogCandidates(ctx, value, searchRuntimeValue.Bound, queryRequest, catalogComparisonMaxPages(spec))
		cancel()
		if err != nil {
			t.Fatalf("prime catalog query: %v", err)
		}
	}
	var catalogPrefix []catalog.Candidate
	warmStage := measureCatalogComparisonStage(t, fixture, "catalog_warm_candidate_query", "warm_same_catalog_repeat", len(directPrefix), func() (int, int, error) {
		ctx, cancel := comparisonStageContext()
		defer cancel()
		for repeat := 0; repeat < catalogWarmQueryTimingRepeats; repeat++ {
			entries, err := collectCatalogCandidates(ctx, value, searchRuntimeValue.Bound, queryRequest, catalogComparisonMaxPages(spec))
			if err != nil {
				return 0, 0, err
			}
			catalogPrefix = entries
		}
		return len(catalogPrefix), 0, nil
	})
	warmStage.Iterations = catalogWarmQueryTimingRepeats

	if !equalPathSets(exactDirectPathSet(directPrefix), exactCatalogPathSet(catalogPrefix)) {
		t.Fatalf("catalog candidate set differs from direct baseline: direct=%d catalog=%d", len(directPrefix), len(catalogPrefix))
	}
	warmStage.SetMatchesDirect = true

	boundSearch, err := searchRuntimeValue.Source.BindSearch(searchRuntimeValue.Bound)
	if err != nil {
		t.Fatalf("bind live verification source: %v", err)
	}
	var verified int
	verifyStage := measureCatalogComparisonStage(t, fixture, "catalog_live_verify", "same_fixture_rootfs_reopen", len(catalogPrefix), func() (int, int, error) {
		ctx, cancel := comparisonStageContext()
		defer cancel()
		count, err := liveVerifyCatalogCandidates(ctx, boundSearch, searchRuntimeValue.Bound, catalogPrefix)
		verified = count
		return len(catalogPrefix), count, err
	})
	if verified != len(catalogPrefix) {
		t.Fatalf("live verified=%d want catalog candidates=%d", verified, len(catalogPrefix))
	}
	verifyStage.SetMatchesDirect = true

	ratio := 0.0
	if directStage.ElapsedNanos > 0 && warmStage.Iterations > 0 {
		ratio = (float64(warmStage.ElapsedNanos) / float64(warmStage.Iterations)) / float64(directStage.ElapsedNanos)
	}
	report := catalogComparisonReport{
		SizeLabel:                         fixture.Spec.Label,
		Platform:                          runtime.GOOS + "/" + runtime.GOARCH,
		Operation:                         catalogComparisonOperation,
		Prefix:                            prefix,
		DirectManifestCount:               len(manifest),
		DirectPrefixCount:                 len(directPrefix),
		CatalogPrefixCandidateCount:       len(catalogPrefix),
		LiveVerifiedCount:                 verified,
		FinalSetMatchesDirect:             true,
		WarmQueryToDirectRatio:            ratio,
		WarmQueryMaxRatioReportOnly:       catalogWarmQueryMaxRatioReportOnly,
		WarmQueryAtLeastTwoX:              ratio <= catalogWarmQueryMaxRatioReportOnly,
		PerformanceGateEnforced:           false,
		LiveVerificationSeparate:          true,
		LiveVerificationMatchesCandidates: verified == len(catalogPrefix),
		Stages:                            []catalogComparisonStage{manifestStage, directStage, buildStage, warmStage, verifyStage},
	}
	t.Logf("catalog_comparison_evidence=%s", mustJSON(report))
	if !report.WarmQueryAtLeastTwoX {
		t.Logf("catalog comparison report-only target missed: warm_query_to_direct_ratio=%.3f max_ratio_for_two_x=%.3f", ratio, catalogWarmQueryMaxRatioReportOnly)
	}
}
