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
}
