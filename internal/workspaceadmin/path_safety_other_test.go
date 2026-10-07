//go:build !windows

package workspaceadmin

import (
	"errors"
	"testing"
)

func TestUnixRemotePrefixRejectedBeforeNormalization(t *testing.T) {
	for _, raw := range []string{"//localhost/share", "//host/share/../folder", "///host/share", "//"} {
		t.Run(raw, func(t *testing.T) {
			_, _, err := validateExistingWorkspaceDirectory(raw)
			if !errors.Is(err, ErrRemotePath) || ProblemCode(err) != CodeRemotePath {
				t.Fatalf("error = %v code=%s, want remote_path before any filesystem lookup", err, ProblemCode(err))
			}
		})
	}
}
