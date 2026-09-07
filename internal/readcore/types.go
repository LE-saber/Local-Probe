// Package readcore implements a bounded, transport-independent read kernel.
// It deliberately does not open paths, authenticate callers, or serve MCP.
package readcore

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Version describes the Source's consistency evidence, not a repository snapshot.
type Version struct {
	Token    string `json:"token"`
	Strength string `json:"strength"` // "metadata" or "snapshot"
}

type Metadata struct {
	Size    int64
	Version Version
}

// Handle must refer to the SAME opened regular file for its entire lifetime.
// Metadata must be bounded and must not hash/read the entire file on every call.
// Implementations must honor context cancellation where the OS permits it.
type Handle interface {
	io.ReaderAt
	Metadata(context.Context) (Metadata, error)
	Close() error
}

// Source is a TRUSTED boundary, not a convenience path opener. It must enforce
// revocation, root containment, secret denies, regular-file checks and platform
// path semantics on the actual opened handle. Lexical validation is not enough.
// Open is concurrent; on success the caller owns the returned handle.
type Source interface {
	Open(context.Context, Scope, FileRef) (Handle, error)
}

type FileRef struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
}

// Scope must be constructed by trusted ingress authentication, never decoded
// from tool arguments. Its root set is copied and not exposed for mutation.
type Scope struct {
	connection string
	revision   string
	roots      map[string]struct{}
}

func NewScope(connection, revision string, roots []string) (Scope, error) {
	if !validID(connection) || !validID(revision) || len(roots) == 0 || len(roots) > 128 {
		return Scope{}, fmt.Errorf("invalid trusted scope")
	}
	s := Scope{connection: connection, revision: revision, roots: make(map[string]struct{}, len(roots))}
	for _, root := range roots {
		if !validID(root) {
			return Scope{}, fmt.Errorf("invalid root id")
		}
		s.roots[root] = struct{}{}
	}
	return s, nil
}

func (s Scope) ConnectionID() string    { return s.connection }
func (s Scope) ProfileRevision() string { return s.revision }
func (s Scope) Allows(root string) bool { _, ok := s.roots[root]; return ok }

type Request struct {
	File            FileRef `json:"file"`
	Offset          int64   `json:"offset"`
	MaxBytes        int     `json:"max_bytes"` // zero uses the configured per-item maximum
	ExpectedVersion string  `json:"expected_version,omitempty"`
}

type ItemError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ItemError) Error() string { return e.Code + ": " + e.Message }

type Result struct {
	File           FileRef    `json:"file"`
	Offset         int64      `json:"offset"`
	EndOffset      int64      `json:"end_offset"`
	SizeBytes      int64      `json:"size_bytes"`
	Version        Version    `json:"version"`
	Content        string     `json:"content,omitempty"`
	EOF            bool       `json:"eof"`
	NextOffset     *int64     `json:"next_offset,omitempty"`
	AllocatedBytes int        `json:"allocated_bytes"`
	BytesRead      int        `json:"bytes_read"`
	Error          *ItemError `json:"error,omitempty"`
}

type BatchResult struct {
	SchemaVersion string   `json:"schema_version"`
	Items         []Result `json:"items"`
	ReturnedBytes int      `json:"returned_bytes"`
	BytesRead     int      `json:"bytes_read"`
	Failed        int      `json:"failed"`
}

// Limits constrain kernel payload/I/O only. MCP must separately bound request
// size and the serialized response, including JSON escaping and metadata.
type Limits struct {
	MaxItems       int
	Workers        int
	MaxItemBytes   int
	MaxOutputBytes int
	MaxReadBytes   int
	Timeout        time.Duration
}

func DefaultLimits() Limits {
	return Limits{MaxItems: 32, Workers: 4, MaxItemBytes: 32 << 10,
		MaxOutputBytes: 128 << 10, MaxReadBytes: 8 << 20, Timeout: 10 * time.Second}
}

func (l Limits) validate() error {
	if l.MaxItems < 1 || l.MaxItems > 128 || l.Workers < 1 || l.Workers > 16 ||
		l.MaxItemBytes < 1 || l.MaxItemBytes > 1<<20 ||
		l.MaxOutputBytes < 1 || l.MaxOutputBytes > 8<<20 ||
		l.MaxReadBytes < 1 || l.MaxReadBytes > 16<<20 ||
		l.Timeout <= 0 || l.Timeout > 30*time.Second {
		return fmt.Errorf("limits exceed kernel safety bounds")
	}
	return nil
}
