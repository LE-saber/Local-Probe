//go:build windows

package probe

import (
	"context"
	"os"
	"testing"
	"time"
)

// Local fixed-version diagnostic, not a remote execution/isolation proof.
// Explicit installed paths, fixed flags, existing PE guard/Job/output limits;
// no PATH discovery, project code, dependency download, or new account.
func TestInstalledCurrentAccountVersionDiagnostics(t *testing.T) {
	if os.Getenv("LOCAL_PROBE_INSTALLED_VERSION_TEST") != "1" {
		t.Skip("opt-in installed tool diagnostic")
	}
	for _, tool := range []struct {
		name, pathEnv string
		id            ToolID
		args          []string
	}{
		// Supply physical installed paths explicitly; do not relax the PE guard
		// or commit a developer's personal installation directory.
		{"node", "LOCAL_PROBE_TEST_NODE_PATH", ToolNode, []string{"--version"}},
		{"python", "LOCAL_PROBE_TEST_PYTHON_PATH", ToolPython, []string{"--version"}},
		{"powershell", "LOCAL_PROBE_TEST_POWERSHELL_PATH", ToolVersionGeneric, []string{"-v"}},
	} {
		t.Run(tool.name, func(t *testing.T) {
			toolPath := os.Getenv(tool.pathEnv)
			if toolPath == "" {
				t.Skip("set " + tool.pathEnv + " to an explicitly approved physical executable path")
			}
			descriptor, err := AuditExecutable(tool.id, toolPath)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			policy := DefaultVersionExecutionPolicy()
			policy.WallTimeout = 5 * time.Second
			result, outcome, err := ToolVersionWithPolicyOutcome(ctx, descriptor, tool.args, policy)
			if err != nil {
				t.Fatal(err)
			}
			if result.Version == "" || outcome.ExitCode == nil || *outcome.ExitCode != 0 || outcome.TimedOut || outcome.Cancelled {
				t.Fatalf("invalid actual version outcome: %+v", outcome)
			}
			t.Logf("actual installed version=%s exit=%d (local diagnostic; not network/file isolation)", result.Version, *outcome.ExitCode)
		})
	}
}
