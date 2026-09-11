package probe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var helperDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "local-probe-probe-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	helperDir = dir
	modes := []string{
		"fake-node", "fake-git", "fake-python", "fake-env", "fake-overoutput",
		"fake-stderroveroutput", "fake-path-output", "fake-timeout",
	}
	for _, mode := range modes {
		name := mode
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		output := filepath.Join(dir, name)
		command := exec.Command("go", "build", "-o", output, "./testhelper")
		command.Stdout = os.Stderr
		command.Stderr = os.Stderr
		if command.Run() != nil {
			_ = os.RemoveAll(dir)
			os.Exit(2)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func helperPath(mode string) string {
	if runtime.GOOS == "windows" {
		mode += ".exe"
	}
	return filepath.Join(helperDir, mode)
}

func requireProcessExecution(t *testing.T) {
	t.Helper()
	if !processExecutionSupported() {
		t.Skip("fixed process probes are Windows-only until the fd-based Unix launcher is implemented")
	}
}

func copyExecutable(t *testing.T, source, name string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), name)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destination, 0700); err != nil {
		t.Fatal(err)
	}
	return destination
}

func TestAuditExistsDoesNotStartProcess(t *testing.T) {
	descriptor, err := AuditExecutable(ToolNode, helperPath("fake-node"))
	if err != nil {
		t.Fatalf("AuditExecutable: %v", err)
	}
	got, err := ToolExists(context.Background(), descriptor)
	if err != nil {
		t.Fatalf("ToolExists: %v", err)
	}
	if got.Tool != ToolNode || !got.Exists {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestCancelledContextDoesNotStartProcess(t *testing.T) {
	descriptor, err := AuditExecutable(ToolNode, helperPath("fake-node"))
	if err != nil {
		t.Fatalf("AuditExecutable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ToolVersion(ctx, descriptor); !errors.Is(err, ErrCancelled) {
		t.Fatalf("want ErrCancelled, got %v", err)
	}
}

func TestFixedVersionArgumentsAndParsers(t *testing.T) {
	requireProcessExecution(t)
	tests := []struct {
		name    string
		tool    ToolID
		helper  string
		version string
	}{
		{name: "node", tool: ToolNode, helper: "fake-node", version: "1.2.3"},
		{name: "git", tool: ToolGit, helper: "fake-git", version: "9.8.7"},
		{name: "python", tool: ToolPython, helper: "fake-python", version: "3.12.4"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			descriptor, err := AuditExecutable(test.tool, helperPath(test.helper))
			if err != nil {
				t.Fatalf("AuditExecutable: %v", err)
			}
			got, err := ToolVersion(context.Background(), descriptor)
			if err != nil {
				t.Fatalf("ToolVersion: %v", err)
			}
			if got.Tool != test.tool || got.Version != test.version {
				t.Fatalf("unexpected result: %+v", got)
			}
		})
	}
}

func TestExactEnvironmentIgnoresCallerInjectionAndUsesPrivateCwd(t *testing.T) {
	requireProcessExecution(t)
	t.Setenv("LOCAL_PROBE_SECRET", "must-not-cross-boundary")
	t.Setenv("PATH", "/tmp/LOCAL_PROBE_MALICIOUS")
	descriptor, err := AuditExecutable(ToolNode, helperPath("fake-env"))
	if err != nil {
		t.Fatalf("AuditExecutable: %v", err)
	}
	got, err := ToolVersion(context.Background(), descriptor)
	if err != nil {
		t.Fatalf("ToolVersion: %v", err)
	}
	if got.Version != "1.2.3" {
		t.Fatalf("unexpected version: %+v", got)
	}
}

func TestOutputLimitsAreBounded(t *testing.T) {
	requireProcessExecution(t)
	for _, helper := range []string{"fake-overoutput", "fake-stderroveroutput"} {
		t.Run(helper, func(t *testing.T) {
			descriptor, err := AuditExecutable(ToolNode, helperPath(helper))
			if err != nil {
				t.Fatalf("AuditExecutable: %v", err)
			}
			_, err = ToolVersion(context.Background(), descriptor)
			if !errors.Is(err, ErrOutputLimit) {
				t.Fatalf("want ErrOutputLimit, got %v", err)
			}
		})
	}
}

func TestTimeoutKillsProcessTree(t *testing.T) {
	requireProcessExecution(t)
	descriptor, err := AuditExecutable(ToolNode, helperPath("fake-timeout"))
	if err != nil {
		t.Fatalf("AuditExecutable: %v", err)
	}
	started := time.Now()
	_, err = ToolVersion(context.Background(), descriptor)
	if !errors.Is(err, ErrDeadlineExceeded) {
		t.Fatalf("want ErrDeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout was not bounded: %s", elapsed)
	}
}

func TestTimeoutKillsDescendant(t *testing.T) {
	requireProcessExecution(t)
	cwd, err := makePrivateWorkdir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(cwd)
	env, err := fixedEnvironment(cwd)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := AuditExecutable(ToolNode, helperPath("fake-timeout"))
	if err != nil {
		t.Fatal(err)
	}
	output, err := runFixedProcess(context.Background(), helperPath("fake-timeout"), []string{"--version"}, cwd, env, descriptor.identity)
	if !errors.Is(err, ErrDeadlineExceeded) {
		t.Fatalf("want ErrDeadlineExceeded, got %v", err)
	}
	if pid := descendantPID(string(output.stdout)); pid != 0 {
		t.Fatalf("active-process job limit allowed a descendant process: %d", pid)
	}
	if !strings.Contains(string(output.stderr), "child-start-failed") {
		t.Fatalf("timeout helper did not report blocked child creation: %q", output.stderr)
	}
}

func descendantPID(output string) int {
	for _, line := range strings.Split(output, "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid > 1 {
			return pid
		}
	}
	return 0
}

func TestIdentityChangeFailsClosed(t *testing.T) {
	requireProcessExecution(t)
	path := copyExecutable(t, helperPath("fake-node"), executableName("target"))
	descriptor, err := AuditExecutable(ToolNode, path)
	if err != nil {
		t.Fatalf("AuditExecutable: %v", err)
	}
	replacement := copyExecutable(t, helperPath("fake-git"), executableName("replacement"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ToolVersion(context.Background(), descriptor); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("want ErrIdentityChanged, got %v", err)
	}
}

func TestInvalidInputsAndPathFreeErrors(t *testing.T) {
	requireProcessExecution(t)
	if _, err := AuditExecutable(ToolNode, "relative-node"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("relative path: want ErrInvalidInput, got %v", err)
	}
	missing := filepath.Join(t.TempDir(), executableName("missing"))
	if _, err := AuditExecutable(ToolNode, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing path: want ErrNotFound, got %v", err)
	} else if strings.Contains(err.Error(), missing) {
		t.Fatalf("error leaked path: %q", err)
	}
	if _, err := AuditExecutable(ToolNode, t.TempDir()); !errors.Is(err, ErrRejected) {
		t.Fatalf("directory: want ErrRejected, got %v", err)
	}
	if _, err := AuditExecutable(ToolID("shell"), helperPath("fake-node")); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown tool: want ErrInvalidInput, got %v", err)
	}
	if _, err := AuditExecutable(ToolNode, helperPath("fake-path-output")); err != nil {
		t.Fatalf("AuditExecutable path-output: %v", err)
	} else {
		descriptor, _ := AuditExecutable(ToolNode, helperPath("fake-path-output"))
		_, err = ToolVersion(context.Background(), descriptor)
		if !errors.Is(err, ErrInvalidOutput) || strings.Contains(err.Error(), helperDir) {
			t.Fatalf("path leaked or wrong error: %v", err)
		}
	}
}

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}
