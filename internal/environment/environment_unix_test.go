//go:build aix || android || darwin || dragonfly || freebsd || hurd || illumos || linux || netbsd || openbsd || solaris

package environment

import (
	"errors"
	"os"
	"testing"
)

func TestDiscoverToolsRejectsDeviceCandidate(t *testing.T) {
	const device = "/dev/null"
	if _, err := os.Lstat(device); err != nil {
		t.Skipf("device fixture unavailable: %v", err)
	}
	_, err := DiscoverTools([]ToolSpec{{
		ID:             "tool",
		CandidateFiles: []string{device},
	}}, DiscoveryOptions{})
	if !errors.Is(err, ErrRejectedCandidate) {
		t.Fatalf("device candidate error = %v", err)
	}
}
