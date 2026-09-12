// Package commandpath defines the local-only handoff used when a fixed
// command profile needs a file selected relative to an authorized root.
//
// A PathBinding is deliberately more than an absolute path string: its
// implementation owns the opened file and the identity commitment that was
// checked while resolving it.  PreviewToken is available only as a local
// presentation for diagnostics; it is not launch-ready argv, an
// authorization proof, or a value that may be handed to an executor.  A
// future launcher must accept the binding itself and perform revalidation and
// launch in one trusted operation.  No such launcher integration exists in
// this package, so the binding is not by itself a complete process-launch
// security boundary.  The interface is also explicitly a JSON marshaler so
// an implementation cannot accidentally become a wire value.
package commandpath

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LE-saber/Local-Probe/internal/policy"
)

var (
	// ErrDenied covers authorization, deny rules, links and other policy
	// failures.  None of these errors contain a filesystem path.
	ErrDenied = errors.New("command path denied")
	// ErrUnsupported is used when the current platform or filesystem cannot
	// provide the required handle/identity guarantees.
	ErrUnsupported = errors.New("command path unsupported")
	// ErrUnavailable covers an indeterminate operating-system lookup.  It is
	// intentionally less specific than an os.PathError to avoid path leaks.
	ErrUnavailable = errors.New("command path unavailable")
	// ErrIdentityChanged means that a previously resolved object no longer
	// matches its bound final identity or canonical location.
	ErrIdentityChanged = errors.New("command path identity changed")
	// ErrClosed identifies a binding or resolver whose owning source has been
	// closed.  It is safe to compare with errors.Is.
	ErrClosed = errors.New("command path binding closed")
	// ErrInvalid is returned for malformed root IDs, relative names or nil
	// contexts before any filesystem operation is attempted.
	ErrInvalid = errors.New("invalid command path")
	// ErrLocalOnly prevents accidental JSON/IPC serialization of a binding or
	// resolver.  A future authenticated local IPC protocol must define its own
	// explicit representation.
	ErrLocalOnly = errors.New("command path object is local-only")
)

// PathBinding is the result of trusted local resolution.  PreviewToken is
// derived from an already opened final handle for local preview/logging only;
// it is not a request path that the implementation joins or trusts, and must
// never be passed to a launcher.  Commitment is an opaque, stable identity
// commitment and never contains the path in clear text.  Callers are
// responsible for Close; a future launcher must retain that responsibility
// while it revalidates and launches from the binding in one trusted operation.
//
// MarshalJSON is part of the contract rather than an optional convenience:
// implementations must reject JSON serialization.  The concrete binding in
// rootfs also rejects unmarshaling, so a wire payload cannot forge one.
type PathBinding interface {
	json.Marshaler
	PreviewToken() (string, error)
	Commitment() [32]byte
	Revalidate(context.Context) error
	Close() error
}

// TrustedResolver resolves only a root-relative regular file under the
// supplied authenticated scope.  Implementations must bind the scope, root
// ID and relative path into the returned PathBinding and must not return a
// path string without retaining the final handle and identity commitment.
//
// MarshalJSON makes resolver values local-only as well; callers should pass
// the resolver directly inside the process that owns the root source.
type TrustedResolver interface {
	json.Marshaler
	Resolve(context.Context, policy.BoundScope, string, string) (PathBinding, error)
}

var _ json.Marshaler = (PathBinding)(nil)
var _ json.Marshaler = (TrustedResolver)(nil)
