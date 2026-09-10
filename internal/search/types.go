// Package search implements bounded, read-only filesystem discovery and
// literal text search. It receives an already authenticated policy scope and
// never opens an operating-system path by itself.
package search

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
)

const SchemaVersion = "local-probe.search.v1"

const (
	EntryRegular   EntryType = "regular"
	EntryDirectory EntryType = "directory"
	EntrySymlink   EntryType = "symlink"
	EntryReparse   EntryType = "reparse"
	EntryOther     EntryType = "other"
	EntryUnknown   EntryType = "unknown"
)

var (
	ErrInvalidRequest    = errors.New("invalid search request")
	ErrInvalidCursor     = errors.New("invalid search cursor")
	ErrGenerationChanged = errors.New("search directory generation changed")
	ErrDenied            = errors.New("search access denied")
	ErrUnavailable       = errors.New("search source unavailable")
)

// EntryType is deliberately smaller than os.FileMode so the source adapter
// cannot accidentally expose platform-specific flags or an absolute path.
type EntryType string

// DirEntry is the source-facing metadata returned by one bounded directory
// read. Name is a single directory component, never a relative or OS path.
type DirEntry struct {
	Name      string
	Type      EntryType
	SizeBytes int64
	ModTime   time.Time
}

// Directory is an iterator over a directory opened inside an authorized root.
// ReadDir must return at most n entries when n > 0 and may return io.EOF only
// after the final entries. Generation is a bounded metadata fingerprint used
// to invalidate live-listing cursors when the directory changes.
type Directory interface {
	ReadDir(n int) ([]DirEntry, error)
	Generation() (string, error)
	Close() error
}

// Source is implemented by the rootfs adapter. Search never accepts an
// absolute path and always supplies the trusted BoundScope to both methods.
type Source interface {
	OpenDirectory(context.Context, policy.BoundScope, string, string) (Directory, error)
	OpenFile(context.Context, policy.BoundScope, readcore.FileRef) (readcore.Handle, error)
}

// Binder lets the service obtain a source tied to the same configuration
// revision as the authenticated scope. A source must revalidate that scope on
// every operation.
type Binder interface {
	BindSearch(policy.BoundScope) (Source, error)
}

// Limits are per-call safety limits. They bound memory, traversal work,
// directory reads, text I/O and cursor size independently of MCP wire limits.
type Limits struct {
	MaxPageSize       int
	MaxDepth          int
	MaxScannedEntries int
	MaxReadBytes      int
	MaxLineBytes      int
	MaxContextBytes   int
	DirectoryBatch    int
	MaxOutputBytes    int
	MaxCursorBytes    int
	Timeout           time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxPageSize:       128,
		MaxDepth:          32,
		MaxScannedEntries: 4096,
		MaxReadBytes:      8 << 20,
		MaxLineBytes:      64 << 10,
		MaxContextBytes:   512,
		DirectoryBatch:    64,
		MaxOutputBytes:    256 << 10,
		MaxCursorBytes:    64 << 10,
		Timeout:           10 * time.Second,
	}
}

func (l Limits) validate() error {
	if l.MaxPageSize < 1 || l.MaxPageSize > 1024 ||
		l.MaxDepth < 0 || l.MaxDepth > 256 ||
		l.MaxScannedEntries < 1 || l.MaxScannedEntries > 1<<20 ||
		l.MaxReadBytes < 1 || l.MaxReadBytes > 64<<20 ||
		l.MaxLineBytes < 1 || l.MaxLineBytes > 1<<20 ||
		l.MaxContextBytes < 0 || l.MaxContextBytes > 64<<10 ||
		l.DirectoryBatch < 1 || l.DirectoryBatch > 1024 ||
		l.MaxOutputBytes < 1<<10 || l.MaxOutputBytes > 8<<20 ||
		l.MaxCursorBytes < 1024 || l.MaxCursorBytes > 256<<10 ||
		l.Timeout <= 0 || l.Timeout > 30*time.Second {
		return fmt.Errorf("%w: search limits exceed safety bounds", ErrInvalidRequest)
	}
	return nil
}

type ListDirectoryRequest struct {
	RootID     string `json:"root_id"`
	Path       string `json:"path,omitempty"`
	PageSize   int    `json:"page_size,omitempty"`
	MaxEntries int    `json:"max_entries,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
}

// TreeDirectoryRequest describes one stateless, bounded pre-order traversal.
// Path is always relative to the configured root; it is a traversal start
// point, not a process working directory.
type TreeDirectoryRequest struct {
	RootID     string `json:"root_id"`
	Path       string `json:"path,omitempty"`
	MaxDepth   int    `json:"max_depth,omitempty"`
	PageSize   int    `json:"page_size,omitempty"`
	MaxEntries int    `json:"max_entries,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
}

type FindFilesRequest struct {
	RootID        string `json:"root_id"`
	Path          string `json:"path,omitempty"`
	Pattern       string `json:"pattern"`
	PageSize      int    `json:"page_size,omitempty"`
	MaxDepth      int    `json:"max_depth,omitempty"`
	MaxEntries    int    `json:"max_entries,omitempty"`
	CaseSensitive *bool  `json:"case_sensitive,omitempty"`
	Cursor        string `json:"cursor,omitempty"`
}

type SearchTextRequest struct {
	RootID        string   `json:"root_id"`
	Path          string   `json:"path,omitempty"`
	Query         string   `json:"query"`
	Globs         []string `json:"globs,omitempty"`
	PageSize      int      `json:"page_size,omitempty"`
	MaxDepth      int      `json:"max_depth,omitempty"`
	MaxEntries    int      `json:"max_entries,omitempty"`
	MaxReadBytes  int      `json:"max_read_bytes,omitempty"`
	ContextBytes  int      `json:"context_bytes,omitempty"`
	CaseSensitive *bool    `json:"case_sensitive,omitempty"`
	Cursor        string   `json:"cursor,omitempty"`
}

// Entry is a relative-path discovery result. ModTime is RFC3339Nano text so
// zero values can be omitted without exposing platform-specific FileInfo.
type Entry struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Type      EntryType `json:"type"`
	SizeBytes int64     `json:"size_bytes,omitempty"`
	ModTime   string    `json:"mod_time,omitempty"`
}

// TreeEntry is a flat pre-order tree item. Depth is relative to the requested
// start directory, which itself is emitted at depth zero.
type TreeEntry struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Type      EntryType `json:"type"`
	Depth     int       `json:"depth"`
	SizeBytes int64     `json:"size_bytes,omitempty"`
	ModTime   string    `json:"mod_time,omitempty"`
}

type Coverage struct {
	Complete               bool `json:"complete"`
	ScannedEntries         int  `json:"scanned_entries"`
	ReturnedEntries        int  `json:"returned_entries"`
	DeniedEntries          int  `json:"denied_entries"`
	IgnoredEntries         int  `json:"ignored_entries"`
	UnsupportedEntries     int  `json:"unsupported_entries"`
	InvalidEncodingEntries int  `json:"invalid_encoding_entries"`
	DepthLimitedEntries    int  `json:"depth_limited_entries"`
	ReadBytes              int  `json:"read_bytes"`
	ReturnedBytes          int  `json:"returned_bytes"`
}

type Budget struct {
	PageSize       int `json:"page_size"`
	MaxDepth       int `json:"max_depth,omitempty"`
	MaxEntries     int `json:"max_entries"`
	MaxReadBytes   int `json:"max_read_bytes,omitempty"`
	MaxOutputBytes int `json:"max_output_bytes"`
}

type ListDirectoryResult struct {
	SchemaVersion string   `json:"schema_version"`
	RootID        string   `json:"root_id"`
	Path          string   `json:"path"`
	Entries       []Entry  `json:"entries"`
	Coverage      Coverage `json:"coverage"`
	Warnings      []string `json:"warnings,omitempty"`
	Budget        Budget   `json:"budget"`
	Continuation  string   `json:"continuation,omitempty"`
}

type TreeDirectoryResult struct {
	SchemaVersion string      `json:"schema_version"`
	RootID        string      `json:"root_id"`
	Path          string      `json:"path"`
	Entries       []TreeEntry `json:"entries"`
	Coverage      Coverage    `json:"coverage"`
	Warnings      []string    `json:"warnings,omitempty"`
	Budget        Budget      `json:"budget"`
	Continuation  string      `json:"continuation,omitempty"`
}

type FindFilesResult struct {
	SchemaVersion string   `json:"schema_version"`
	RootID        string   `json:"root_id"`
	Path          string   `json:"path"`
	Pattern       string   `json:"pattern"`
	Entries       []Entry  `json:"entries"`
	Coverage      Coverage `json:"coverage"`
	Warnings      []string `json:"warnings,omitempty"`
	Budget        Budget   `json:"budget"`
	Continuation  string   `json:"continuation,omitempty"`
}

type Match struct {
	Path           string `json:"path"`
	Line           int    `json:"line"`
	LineStartByte  int64  `json:"line_start_byte"`
	MatchStartByte int64  `json:"match_start_byte"`
	MatchEndByte   int64  `json:"match_end_byte"`
	LineText       string `json:"line_text,omitempty"`
	Context        string `json:"context,omitempty"`
	LineTruncated  bool   `json:"line_truncated,omitempty"`
}

type SearchTextResult struct {
	SchemaVersion string   `json:"schema_version"`
	RootID        string   `json:"root_id"`
	Path          string   `json:"path"`
	Query         string   `json:"query"`
	Matches       []Match  `json:"matches"`
	Coverage      Coverage `json:"coverage"`
	Warnings      []string `json:"warnings,omitempty"`
	Budget        Budget   `json:"budget"`
	Continuation  string   `json:"continuation,omitempty"`
}

func defaultCaseSensitive(explicit *bool) bool {
	if explicit != nil {
		return *explicit
	}
	return runtime.GOOS != "windows"
}

func validText(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value)
}
