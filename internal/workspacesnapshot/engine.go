package workspacesnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/search"
)

// Source is the already-bound rootfs/search adapter. Snapshot does not accept
// an OS path opener and therefore cannot escape the caller's policy boundary.
type Source interface {
	search.Source
}

type binder struct{ source Source }

func (b binder) BindSearch(policy.BoundScope) (search.Source, error) {
	if b.source == nil {
		return nil, ErrUnavailable
	}
	return b.source, nil
}

type Engine struct {
	service *search.Service
	limits  Limits
}

func New(source Source, limits Limits) (*Engine, error) {
	if source == nil {
		return nil, ErrUnavailable
	}
	return newWithBinder(binder{source: source}, limits)
}

// NewWithBinder keeps the snapshot core on the same bound-source seam as the
// existing search service. The binder must revalidate the supplied scope and
// return a source for that exact configuration revision.
func NewWithBinder(source search.Binder, limits Limits) (*Engine, error) {
	if source == nil {
		return nil, ErrUnavailable
	}
	return newWithBinder(source, limits)
}

func newWithBinder(source search.Binder, limits Limits) (*Engine, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	searchLimits := search.DefaultLimits()
	searchLimits.MaxPageSize = limits.MaxEntries
	searchLimits.MaxDepth = limits.MaxDepth
	searchLimits.MaxScannedEntries = limits.MaxEntries
	searchLimits.MaxReadBytes = limits.MaxReadBytes
	searchLimits.DirectoryBatch = limits.DirectoryBatch
	searchLimits.MaxOutputBytes = limits.MaxOutputBytes
	searchLimits.MaxCursorBytes = limits.MaxCursorBytes
	searchLimits.MaxOpenFiles = limits.MaxOpenFiles
	searchLimits.MaxOpenDirectories = limits.MaxOpenDirectories
	searchLimits.Timeout = limits.Timeout
	service, err := search.New(source, searchLimits, nil)
	if err != nil {
		return nil, err
	}
	return &Engine{service: service, limits: limits}, nil
}

// Snapshot returns one bounded page. When continuation is non-empty, the
// caller may repeat the same request with that cursor; each page's facts are
// intentionally mergeable by path. There is no hidden second pass or content
// read, so language statistics and manifest candidates are path-derived facts.
func (e *Engine) Snapshot(ctx context.Context, bound policy.BoundScope, req Request) (Result, error) {
	out := Result{
		SchemaVersion: SchemaVersion, RootID: req.RootID, Path: req.Path,
		Outline: []OutlineEntry{}, ManifestCandidates: []ManifestCandidate{},
		LanguageStats: []LanguageStat{}, EvidencePaths: []EvidencePath{},
	}
	if e == nil || e.service == nil || bound.Validate() != nil {
		return out, ErrDenied
	}
	if ctx == nil {
		ctx = context.Background()
	}
	depth := req.MaxDepth
	if depth == 0 {
		depth = e.limits.MaxDepth
	}
	entries := req.MaxEntries
	if entries == 0 {
		entries = e.limits.MaxEntries
	}
	if depth < 0 || depth > e.limits.MaxDepth || entries < 1 || entries > e.limits.MaxEntries {
		return out, ErrInvalidRequest
	}
	// TreeDirectory enforces the engine-wide serialized-entry budget before it
	// advances its cursor. A smaller per-request override would require a
	// second post-hoc page boundary and could otherwise discard entries without
	// a recoverable cursor. Keep one fixed budget until snapshot paging has a
	// first-class envelope budget.
	if req.MaxOutputBytes != 0 && req.MaxOutputBytes != e.limits.MaxOutputBytes {
		return out, ErrInvalidRequest
	}
	output := e.limits.MaxOutputBytes
	readBytes := req.MaxReadBytes
	if readBytes == 0 {
		readBytes = e.limits.MaxReadBytes
	}
	if readBytes < 1 || readBytes > e.limits.MaxReadBytes {
		return out, ErrInvalidRequest
	}
	out.Budget = Budget{MaxDepth: depth, MaxEntries: entries, MaxReadBytes: readBytes, MaxOutputBytes: output, MaxOpenFiles: e.limits.MaxOpenFiles, MaxOpenDirectories: e.limits.MaxOpenDirectories}
	if err := ctx.Err(); err != nil {
		return out, mapContextError(err)
	}

	tree, err := e.service.TreeDirectory(ctx, bound, search.TreeDirectoryRequest{
		RootID: req.RootID, Path: req.Path, MaxDepth: depth, PageSize: entries,
		MaxEntries: entries, Cursor: req.Cursor,
	})
	if err != nil {
		return out, mapSearchError(err)
	}
	out.Coverage = Coverage{
		Complete:            tree.Coverage.Complete,
		ScannedEntries:      tree.Coverage.ScannedEntries,
		ReturnedEntries:     tree.Coverage.ReturnedEntries,
		OpenedFiles:         tree.Coverage.OpenedFiles,
		OpenedDirectories:   tree.Coverage.OpenedDirectories,
		ReadBytes:           tree.Coverage.ReadBytes,
		ReturnedBytes:       tree.Coverage.ReturnedBytes,
		DepthLimitedEntries: tree.Coverage.DepthLimitedEntries,
		DeniedEntries:       tree.Coverage.DeniedEntries,
		IgnoredEntries:      tree.Coverage.IgnoredEntries,
		UnsupportedEntries:  tree.Coverage.UnsupportedEntries,
	}
	out.Warnings = append(out.Warnings, tree.Warnings...)
	out.Continuation = tree.Continuation

	stats := make(map[string]LanguageStat)
	derivedOverflow := false
	for _, entry := range tree.Entries {
		public := OutlineEntry{Path: entry.Path, Name: entry.Name, Type: string(entry.Type), Depth: entry.Depth, SizeBytes: entry.SizeBytes}
		out.Outline = append(out.Outline, public)
		if entry.Type != search.EntryRegular {
			continue
		}
		if language, ok := languageFor(entry.Path); ok {
			stat := stats[language]
			stat.Language = language
			stat.Files++
			stat.Bytes += maxInt64(entry.SizeBytes, 0)
			stats[language] = stat
		}
		if kind, ok := manifestFor(entry.Path); ok {
			if len(out.ManifestCandidates) < 64 {
				out.ManifestCandidates = append(out.ManifestCandidates, ManifestCandidate{Path: entry.Path, Kind: kind, Evidence: "path"})
			} else {
				derivedOverflow = true
			}
			if len(out.EvidencePaths) < 64 {
				out.EvidencePaths = append(out.EvidencePaths, EvidencePath{Path: entry.Path, Reason: "manifest:" + kind})
			} else {
				derivedOverflow = true
			}
		}
		if isEvidenceName(entry.Name) && len(out.EvidencePaths) < 64 {
			out.EvidencePaths = append(out.EvidencePaths, EvidencePath{Path: entry.Path, Reason: "project-marker"})
		} else if isEvidenceName(entry.Name) {
			derivedOverflow = true
		}
	}
	if derivedOverflow {
		// The tree cursor has already advanced in this call, so returning a
		// truncated derived view with no resumable derived-state cursor would
		// lose facts. Fail closed and expose no continuation instead.
		return Result{}, ErrBudgetExceeded
	}
	for _, stat := range stats {
		out.LanguageStats = append(out.LanguageStats, stat)
	}
	sort.Slice(out.LanguageStats, func(i, j int) bool { return out.LanguageStats[i].Language < out.LanguageStats[j].Language })
	sort.Slice(out.ManifestCandidates, func(i, j int) bool { return out.ManifestCandidates[i].Path < out.ManifestCandidates[j].Path })
	sort.Slice(out.EvidencePaths, func(i, j int) bool {
		if out.EvidencePaths[i].Path == out.EvidencePaths[j].Path {
			return out.EvidencePaths[i].Reason < out.EvidencePaths[j].Reason
		}
		return out.EvidencePaths[i].Path < out.EvidencePaths[j].Path
	})
	if err := fitResultBudget(&out, output); err != nil {
		return Result{}, err
	}
	return out, nil
}

// fitResultBudget measures the exact JSON core envelope, including derived
// arrays, field names and escaping. returned_bytes is itself part of the
// envelope, so iterate to a fixed point before accepting the result.
func fitResultBudget(out *Result, maxBytes int) error {
	out.Coverage.ReturnedBytes = 0
	for i := 0; i < 64; i++ {
		encoded, err := json.Marshal(out)
		if err != nil {
			return ErrUnavailable
		}
		size := len(encoded)
		if size > maxBytes {
			return ErrBudgetExceeded
		}
		if out.Coverage.ReturnedBytes == size {
			confirmed, confirmErr := json.Marshal(out)
			if confirmErr != nil {
				return ErrUnavailable
			}
			if len(confirmed) != size || len(confirmed) != out.Coverage.ReturnedBytes {
				return ErrUnavailable
			}
			return nil
		}
		out.Coverage.ReturnedBytes = size
	}
	return ErrUnavailable
}

func mapContextError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrUnavailable
}

func mapSearchError(err error) error {
	switch {
	case errors.Is(err, search.ErrInvalidRequest), errors.Is(err, search.ErrInvalidCursor):
		return ErrInvalidRequest
	case errors.Is(err, search.ErrDenied):
		return ErrDenied
	case errors.Is(err, search.ErrGenerationChanged):
		return ErrStale
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return ErrUnavailable
	}
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func languageFor(path string) (string, bool) {
	name := strings.ToLower(path)
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		switch name[dot:] {
		case ".go":
			return "Go", true
		case ".rs":
			return "Rust", true
		case ".py":
			return "Python", true
		case ".js", ".mjs", ".cjs":
			return "JavaScript", true
		case ".ts", ".tsx":
			return "TypeScript", true
		case ".jsx":
			return "JSX", true
		case ".java":
			return "Java", true
		case ".kt", ".kts":
			return "Kotlin", true
		case ".cs":
			return "C#", true
		case ".c", ".h":
			return "C", true
		case ".cc", ".cpp", ".cxx", ".hpp":
			return "C++", true
		case ".json":
			return "JSON", true
		case ".yaml", ".yml":
			return "YAML", true
		case ".toml":
			return "TOML", true
		case ".md", ".markdown":
			return "Markdown", true
		case ".sql":
			return "SQL", true
		case ".sh", ".bash":
			return "Shell", true
		case ".ps1":
			return "PowerShell", true
		}
	}
	return "", false
}

func manifestFor(path string) (string, bool) {
	name := strings.ToLower(path)
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	switch name {
	case "go.mod", "go.sum":
		return "go", true
	case "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock":
		return "node", true
	case "cargo.toml", "cargo.lock":
		return "rust", true
	case "pyproject.toml", "requirements.txt", "pipfile", "poetry.lock":
		return "python", true
	case "pom.xml", "build.gradle", "build.gradle.kts":
		return "jvm", true
	case "cmakelists.txt", "makefile":
		return "native-build", true
	case "dockerfile", "compose.yaml", "compose.yml", "docker-compose.yml":
		return "container", true
	}
	if strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".csproj") {
		return "dotnet", true
	}
	return "", false
}

func isEvidenceName(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "readme") || strings.HasPrefix(name, "license") || name == "contributing.md"
}
