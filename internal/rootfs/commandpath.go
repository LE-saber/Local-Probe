package rootfs

import (
	"context"
	"encoding/json"
	"os"

	"github.com/LE-saber/Local-Probe/internal/commandpath"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
)

// commandPathResolver captures one Source and one authenticated scope.  It
// never stores a configured absolute path; the Source's existing *os.Root is
// the only authority used by the platform implementation.
type commandPathResolver struct {
	source   *Source
	bound    policy.BoundScope
	revision string
}

// BindCommandPath returns a local-only resolver tied to this Source's
// configuration revision and the supplied authenticated scope.  The
// resolver can be retained across calls, but every Resolve revalidates both
// the scope and Source state.
//
// Construction hard gate: Source.New currently performs a name-based Lstat
// followed by OpenRoot in separate system calls.  That Lstat-to-OpenRoot
// race is an external gate which this binding does not close.  The binding
// revalidates the already-owned root and final handles, but must not claim
// complete launch security until Source construction closes that gap.
func (s *Source) BindCommandPath(bound policy.BoundScope) (commandpath.TrustedResolver, error) {
	if s == nil {
		return nil, commandpath.ErrClosed
	}
	if err := bound.Validate(); err != nil {
		return nil, commandpath.ErrDenied
	}
	s.mu.RLock()
	closed, revision := s.closed, s.revision
	s.mu.RUnlock()
	if closed {
		return nil, commandpath.ErrClosed
	}
	if revision == "" || bound.Revision() != revision {
		return nil, commandpath.ErrDenied
	}
	return &commandPathResolver{source: s, bound: bound, revision: revision}, nil
}

// ResolveCommandPath is the direct local convenience form of
// BindCommandPath(...).Resolve(...).  The returned binding retains its final
// handle and cannot be serialized as JSON.
func (s *Source) ResolveCommandPath(ctx context.Context, bound policy.BoundScope, rootID, relativePath string) (commandpath.PathBinding, error) {
	resolver, err := s.BindCommandPath(bound)
	if err != nil {
		return nil, err
	}
	return resolver.Resolve(ctx, bound, rootID, relativePath)
}

// MarshalJSON deliberately prevents a resolver from crossing a process or
// network boundary.  Its source and bound scope are process-local state.
func (*commandPathResolver) MarshalJSON() ([]byte, error) {
	return nil, commandpath.ErrLocalOnly
}

// UnmarshalJSON prevents a caller from forging the source/scope association.
func (*commandPathResolver) UnmarshalJSON([]byte) error {
	return commandpath.ErrLocalOnly
}

func validateCommandPathRequest(ctx context.Context, bound policy.BoundScope, rootID, relativePath string) error {
	if ctx == nil {
		return commandpath.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validCommandRootID(rootID) || !readcore.ValidPath(relativePath) {
		return commandpath.ErrInvalid
	}
	if err := bound.Validate(); err != nil {
		return commandpath.ErrDenied
	}
	// Keep the explicit root allowlist and deny decision separate.  In
	// particular, ignore patterns are discovery-only and never participate in
	// this direct command-path authorization.
	if !bound.AllowsRoot(rootID) || !bound.AllowsPath(rootID, relativePath) {
		return commandpath.ErrDenied
	}
	return nil
}

func validCommandRootID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (s *Source) commandRoot(bound policy.BoundScope, rootID string) (*os.Root, error) {
	if s == nil {
		return nil, commandpath.ErrClosed
	}
	s.mu.RLock()
	closed, revision := s.closed, s.revision
	root := s.roots[rootID]
	s.mu.RUnlock()
	if closed {
		return nil, commandpath.ErrClosed
	}
	if revision == "" || revision != bound.Revision() {
		return nil, commandpath.ErrDenied
	}
	if root == nil {
		return nil, commandpath.ErrDenied
	}
	return root, nil
}

var _ json.Marshaler = (*commandPathResolver)(nil)
var _ commandpath.TrustedResolver = (*commandPathResolver)(nil)
