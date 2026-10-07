// Package workspacesnapshot builds a bounded, path-evidenced workspace
// outline. It never executes project files and does not claim snapshot-level
// consistency: the underlying rootfs generation is still live metadata.
package workspacesnapshot

import (
	"errors"
	"fmt"
	"time"
)

const SchemaVersion = "local-probe.workspace-snapshot.v1"

var (
	ErrInvalidRequest = errors.New("invalid workspace snapshot request")
	ErrDenied         = errors.New("workspace snapshot access denied")
	ErrUnavailable    = errors.New("workspace snapshot unavailable")
	ErrStale          = errors.New("workspace snapshot generation changed")
	ErrBudgetExceeded = errors.New("workspace snapshot output budget exceeded")
)

type Limits struct {
	MaxDepth           int
	MaxEntries         int
	MaxReadBytes       int
	MaxOutputBytes     int
	DirectoryBatch     int
	MaxOpenFiles       int
	MaxOpenDirectories int
	MaxCursorBytes     int
	Timeout            time.Duration
}

func DefaultLimits() Limits {
	return Limits{
		MaxDepth: 4, MaxEntries: 512, MaxReadBytes: 1 << 20,
		MaxOutputBytes: 128 << 10, DirectoryBatch: 64,
		MaxOpenFiles: 1, MaxOpenDirectories: 128, MaxCursorBytes: 64 << 10,
		Timeout: 10 * time.Second,
	}
}

func (l Limits) validate() error {
	if l.MaxDepth < 0 || l.MaxDepth > 32 || l.MaxEntries < 1 || l.MaxEntries > 1024 ||
		l.MaxReadBytes < 1 || l.MaxReadBytes > 64<<20 || l.MaxOutputBytes < 1024 || l.MaxOutputBytes > 8<<20 ||
		l.DirectoryBatch < 1 || l.DirectoryBatch > 1024 || l.MaxOpenFiles < 1 || l.MaxOpenFiles > 1<<20 ||
		l.MaxOpenDirectories < 1 || l.MaxOpenDirectories > 1<<20 || l.MaxCursorBytes < 1024 || l.MaxCursorBytes > 256<<10 ||
		l.Timeout <= 0 || l.Timeout > 30*time.Second {
		return fmt.Errorf("%w: limits exceed snapshot safety bounds", ErrInvalidRequest)
	}
	return nil
}

type Request struct {
	RootID         string `json:"root_id"`
	Path           string `json:"path,omitempty"`
	MaxDepth       int    `json:"max_depth,omitempty"`
	MaxEntries     int    `json:"max_entries,omitempty"`
	MaxReadBytes   int    `json:"max_read_bytes,omitempty"`
	MaxOutputBytes int    `json:"max_output_bytes,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
}

type OutlineEntry struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Depth     int    `json:"depth"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type ManifestCandidate struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Evidence string `json:"evidence"`
}

type LanguageStat struct {
	Language string `json:"language"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
}

type EvidencePath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Coverage struct {
	Complete            bool `json:"complete"`
	ScannedEntries      int  `json:"scanned_entries"`
	ReturnedEntries     int  `json:"returned_entries"`
	OpenedFiles         int  `json:"opened_files"`
	OpenedDirectories   int  `json:"opened_directories"`
	ReadBytes           int  `json:"read_bytes"`
	ReturnedBytes       int  `json:"returned_bytes"`
	DepthLimitedEntries int  `json:"depth_limited_entries"`
	DeniedEntries       int  `json:"denied_entries"`
	IgnoredEntries      int  `json:"ignored_entries"`
	UnsupportedEntries  int  `json:"unsupported_entries"`
}

type Budget struct {
	MaxDepth           int `json:"max_depth"`
	MaxEntries         int `json:"max_entries"`
	MaxReadBytes       int `json:"max_read_bytes"`
	MaxOutputBytes     int `json:"max_output_bytes"`
	MaxOpenFiles       int `json:"max_open_files"`
	MaxOpenDirectories int `json:"max_open_directories"`
}

type Result struct {
	SchemaVersion      string              `json:"schema_version"`
	RootID             string              `json:"root_id"`
	Path               string              `json:"path"`
	Outline            []OutlineEntry      `json:"outline"`
	ManifestCandidates []ManifestCandidate `json:"manifest_candidates"`
	LanguageStats      []LanguageStat      `json:"language_stats"`
	EvidencePaths      []EvidencePath      `json:"evidence_paths"`
	Coverage           Coverage            `json:"coverage"`
	Warnings           []string            `json:"warnings,omitempty"`
	Budget             Budget              `json:"budget"`
	Continuation       string              `json:"continuation,omitempty"`
}
