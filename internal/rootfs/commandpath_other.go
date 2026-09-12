//go:build !windows

package rootfs

import (
	"context"

	"github.com/LE-saber/Local-Probe/internal/commandpath"
	"github.com/LE-saber/Local-Probe/internal/policy"
)

// Resolve intentionally remains unsupported on non-Windows targets in this
// increment.  The command path contract requires a platform implementation
// that can prove final identity and local-volume semantics; returning a
// joined path here would create a false security guarantee.
func (r *commandPathResolver) Resolve(ctx context.Context, bound policy.BoundScope, rootID, relativePath string) (commandpath.PathBinding, error) {
	if r == nil || r.source == nil {
		return nil, commandpath.ErrUnavailable
	}
	if err := validateCommandPathRequest(ctx, bound, rootID, relativePath); err != nil {
		return nil, err
	}
	return nil, commandpath.ErrUnsupported
}
