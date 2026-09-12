//go:build !windows

package probe

import (
	"context"
	"errors"
	"testing"
)

func TestToolVersionFailsClosedOutsideWindows(t *testing.T) {
	descriptor, err := AuditExecutable(ToolNode, helperPath("fake-node"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ToolVersion(context.Background(), descriptor); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
	_, outcome, err := ToolVersionWithPolicyOutcome(context.Background(), descriptor, []string{"--version"}, DefaultVersionExecutionPolicy())
	if !errors.Is(err, ErrUnsupported) || outcome.ErrorCode != CodeUnsupported || outcome.ExitCode != nil || outcome.TimedOut || outcome.Cancelled {
		t.Fatalf("unsupported outcome = %#v, err=%v", outcome, err)
	}
}
