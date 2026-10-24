package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
	"golang.org/x/sys/windows"
)

func realEngine(t *testing.T, tools []Tool) (*Engine, *Journal, string) {
	return realEngineConfigured(t, tools, nil)
}
func realEngineConfigured(t *testing.T, tools []Tool, configure func(*config.Store)) (*Engine, *Journal, string) {
	t.Helper()
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(filepath.Join(dir, "journal"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	cfg := testConfig(t, workspace)
	if configure != nil {
		configure(cfg)
	}
	manager, err := policy.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	source, err := rootfs.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { source.Close() })
	e, err := New(context.Background(), Options{Journal: j, Config: cfg, Manager: manager, Workspace: source, Catalog: Catalog{Version: Version, Tools: tools}, Acquire: func(ctx context.Context, _ Subject) (context.Context, func(), error) { return ctx, func() {}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, j, workspace
}
func approved(t *testing.T, e *Engine, j *Journal, r Request) Summary {
	t.Helper()
	s := Subject{"principal", "connection"}
	p, err := e.Prepare(context.Background(), s, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit(s, p.ID, p.Digest); err != nil {
		t.Fatal(err)
	}
	if err = j.Decide(p.ID, p.Digest, true, true); err != nil {
		t.Fatal(err)
	}
	return p
}
func helperTool(t *testing.T) Tool {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "operation-helper.exe")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", exe, "./testhelper")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v %s", err, output)
	}
	hash, err := hashFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return Tool{ID: "helper", Executable: exe, SHA256: hash, Environment: []string{"LP_OPERATIONS_HELPER=1", "SystemRoot=" + os.Getenv("SystemRoot")}}
}
func TestRealCommandBuildTestCancelOutputAndEnvironment(t *testing.T) {
	t.Setenv("LP_UNAPPROVED_SECRET", "must-not-be-inherited")
	e, j, workspace := realEngine(t, []Tool{helperTool(t)})
	s := Subject{"principal", "connection"}
	for _, kind := range []string{"command", "build", "test"} {
		p := approved(t, e, j, Request{Nonce: kind, Kind: kind, RootID: "workspace", ToolID: "helper", Args: []string{"write"}})
		waitState(t, e, s, p.ID, "succeeded")
		if _, err := e.Submit(s, p.ID, p.Digest); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(workspace, "count.txt"))
	if err != nil || string(b) != "once\nonce\nonce\n" {
		t.Fatalf("wrong actual side effect count: %q %v", b, err)
	}
	for _, action := range []string{"environment", "flood", "wait"} {
		p := approved(t, e, j, Request{Nonce: action, Kind: "command", RootID: "workspace", ToolID: "helper", Args: []string{action}})
		switch action {
		case "environment":
			waitState(t, e, s, p.ID, "succeeded")
			out, err := e.Output(s, p.ID, "stdout", 0, 1024)
			if err != nil || strings.Contains(string(out.Data), "must-not-be-inherited") || !strings.Contains(string(out.Data), "secret=") {
				t.Fatal("unapproved environment inherited")
			}
		case "flood":
			result := waitState(t, e, s, p.ID, "failed")
			if result.ErrorCode != "output_limit" || result.StdoutBytes > 64<<10 {
				t.Fatalf("unbounded output: %+v", result)
			}
		case "wait":
			end := time.Now().Add(5 * time.Second)
			for {
				if _, err = os.Stat(filepath.Join(workspace, "started.txt")); err == nil {
					break
				}
				if time.Now().After(end) {
					t.Fatal("real child did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			result, err := e.Cancel(ctx, s, p.ID)
			cancel()
			if err != nil || result.State != "cancelled" {
				t.Fatalf("real process cancellation failed: %+v %v", result, err)
			}
		}
	}
}

func TestInstalledPythonNodePowerShellScriptsAndVersions(t *testing.T) {
	if os.Getenv("LOCAL_PROBE_OPERATIONS_REAL_TOOLS") != "1" {
		t.Skip("opt-in: explicitly selected installed runtimes")
	}
	for _, runtime := range []struct{ name, env, script string }{
		{"python", "LOCAL_PROBE_OPERATIONS_PYTHON", "from pathlib import Path\nPath('python.txt').write_text('python-ok')\nprint('python-ok')\n"},
		{"node", "LOCAL_PROBE_OPERATIONS_NODE", "require('fs').writeFileSync('node.txt','node-ok'); console.log('node-ok');\n"},
		{"powershell", "LOCAL_PROBE_OPERATIONS_POWERSHELL", "[IO.File]::WriteAllText((Join-Path (Get-Location) 'powershell.txt'),'powershell-ok'); Write-Output 'powershell-ok'\n"},
		{"cmd", "LOCAL_PROBE_OPERATIONS_CMD", "@echo off\r\n>cmd.txt echo cmd-ok\r\necho cmd-ok\r\n"},
	} {
		t.Run(runtime.name, func(t *testing.T) {
			path := os.Getenv(runtime.env)
			if path == "" {
				t.Fatal("explicit runtime path required")
			}
			path, err := filepath.EvalSymlinks(path)
			if err != nil {
				t.Fatal(err)
			}
			hash, err := hashFile(path)
			if err != nil {
				t.Fatal(err)
			}
			e, j, workspace := realEngine(t, []Tool{{ID: runtime.name, Executable: path, SHA256: hash, ScriptType: runtime.name, Environment: []string{"SystemRoot=" + os.Getenv("SystemRoot")}}})
			s := Subject{"principal", "connection"}
			if runtime.name == "cmd" {
				if _, err := e.Prepare(context.Background(), s, Request{Nonce: "cmd-args", Kind: "script", RootID: "workspace", ToolID: "cmd", Script: "echo safe", Args: []string{"&", "echo unexpected"}}); err != ErrInvalid {
					t.Fatal("cmd script accepted shell arguments outside its approved body")
				}
			}
			versionArgs := []string{"--version"}
			if runtime.name == "cmd" {
				versionArgs = []string{"/d", "/c", "ver"}
			}
			version := approved(t, e, j, Request{Nonce: "version", Kind: "version", RootID: "workspace", ToolID: runtime.name, Args: versionArgs})
			waitState(t, e, s, version.ID, "succeeded")
			output, err := e.Output(s, version.ID, "stdout", 0, 2048)
			if err != nil || len(bytes.TrimSpace(output.Data)) == 0 {
				t.Fatal("actual runtime version missing")
			}
			t.Logf("verified runtime version: %s", strings.TrimSpace(string(output.Data)))
			script := approved(t, e, j, Request{Nonce: "script", Kind: "script", RootID: "workspace", ToolID: runtime.name, Script: runtime.script})
			waitState(t, e, s, script.ID, "succeeded")
			b, err := os.ReadFile(filepath.Join(workspace, runtime.name+".txt"))
			if err != nil || strings.TrimSpace(string(b)) != runtime.name+"-ok" {
				t.Fatal("approved runtime did not produce expected real file")
			}
		})
	}
}

func TestRealGoBuildAndTestProject(t *testing.T) {
	if os.Getenv("LOCAL_PROBE_OPERATIONS_REAL_TOOLS") != "1" {
		t.Skip("opt-in: real installed Go compiler")
	}
	goexe := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	hash, err := hashFile(goexe)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "cache")
	gopath := filepath.Join(t.TempDir(), "gopath")
	tool := Tool{ID: "go", Executable: goexe, SHA256: hash, Environment: []string{"SystemRoot=" + os.Getenv("SystemRoot"), "GOROOT=" + runtime.GOROOT(), "GOCACHE=" + cache, "GOPATH=" + gopath, "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0", "GOFLAGS=-p=2", "GOMAXPROCS=2"}}
	e, j, workspace := realEngine(t, []Tool{tool})
	s := Subject{"principal", "connection"}
	for name, body := range map[string]string{"go.mod": "module local-probe-synthetic\n\ngo 1.25.0\n", "main.go": "package main\nfunc add(a,b int)int{return a+b}\nfunc main(){}\n", "main_test.go": "package main\nimport \"testing\"\nfunc TestAdd(t *testing.T){if add(2,3)!=5{t.Fatal(\"wrong result\")}}\n"} {
		if err = os.WriteFile(filepath.Join(workspace, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range []struct {
		kind string
		args []string
	}{{"build", []string{"build", "-o", "application.exe", "."}}, {"test", []string{"test", "-count=1", "."}}} {
		p := approved(t, e, j, Request{Nonce: job.kind, Kind: job.kind, RootID: "workspace", ToolID: "go", Args: job.args, TimeoutSeconds: 180})
		end := time.Now().Add(180 * time.Second)
		for {
			out, err := e.Status(s, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if terminal(out.State) {
				if out.State != "succeeded" {
					stderr, _ := e.Output(s, p.ID, "stderr", 0, 8192)
					t.Fatalf("real %s state=%s code=%s stderr=%q", job.kind, out.State, out.ErrorCode, stderr.Data)
				}
				break
			}
			if time.Now().After(end) {
				t.Fatal("real build/test timed out")
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if info, err := os.Stat(filepath.Join(workspace, "application.exe")); err != nil || info.Size() == 0 {
		t.Fatal("real compiler did not produce executable")
	}
}

func TestLocalCLIApprovalPublishesDurableDecision(t *testing.T) {
	e, j, workspace := realEngine(t, nil)
	s := Subject{"principal", "connection"}
	cli := filepath.Join(t.TempDir(), "local-probe-operations.exe")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go.exe"), "build", "-o", cli, "../../cmd/local-probe-operations")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local control CLI: %v %s", err, output)
	}
	p, err := e.Prepare(context.Background(), s, Request{Nonce: "local-cli", Kind: "file_write", RootID: "workspace", Path: "approved.txt", Content: "local-cli-approved"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit(s, p.ID, p.Digest); err != nil {
		t.Fatal(err)
	}
	base := []string{"-journal", j.Path, "-id", p.ID, "-digest", p.Digest}
	if err = exec.Command(cli, append(append([]string(nil), base...), "-action", "approve")...).Run(); err == nil {
		t.Fatal("CLI accepted missing acknowledgement")
	}
	if _, err = os.Stat(filepath.Join(workspace, "approved.txt")); !os.IsNotExist(err) {
		t.Fatal("file changed before explicit CLI approval")
	}
	if output, err := exec.Command(cli, append(append([]string(nil), base...), "-action", "inspect")...).CombinedOutput(); err != nil {
		t.Fatalf("local inspection failed: %v %s", err, output)
	} else {
		var record Record
		if json.Unmarshal(output, &record) != nil || record.Plan.Request.Content != "local-cli-approved" || record.Summary.Digest != p.Digest {
			t.Fatal("CLI inspection did not show the exact approved plan")
		}
	}
	if output, err := exec.Command(cli, append(append([]string(nil), base...), "-action", "approve", "-ack-trusted-host")...).CombinedOutput(); err != nil {
		t.Fatalf("local approval failed: %v %s", err, output)
	}
	waitState(t, e, s, p.ID, "succeeded")
	content, err := os.ReadFile(filepath.Join(workspace, "approved.txt"))
	if err != nil || string(content) != "local-cli-approved" {
		t.Fatal("local CLI approval did not dispatch actual write")
	}
	replayed, err := e.Prepare(context.Background(), s, Request{Nonce: "local-cli", Kind: "file_write", RootID: "workspace", Path: "approved.txt", Content: "local-cli-approved"})
	if err != nil || replayed.ID != p.ID || replayed.State != "succeeded" {
		t.Fatal("retrying prepare after side effects did not return the existing operation")
	}
}

func TestExactTextPatchRequiresUniqueContextAndProtectsUserChanges(t *testing.T) {
	e, j, workspace := realEngine(t, nil)
	s := Subject{"principal", "connection"}
	target := filepath.Join(workspace, "patch.txt")
	if err := os.WriteFile(target, []byte("before old after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	request := Request{Nonce: "patch", Kind: "file_patch", RootID: "workspace", Path: "patch.txt", Edits: []Edit{{Old: "old", New: "new"}}}
	p := approved(t, e, j, request)
	waitState(t, e, s, p.ID, "succeeded")
	b, err := os.ReadFile(target)
	if err != nil || string(b) != "before new after\n" {
		t.Fatal("exact patch not applied")
	}
	records, err := j.Records()
	if err != nil || records[p.ID].Plan.Before != "before old after\n" || records[p.ID].Plan.PatchedContent != "before new after\n" {
		t.Fatal("approval did not bind before/after patch contents")
	}
	if err = os.WriteFile(target, []byte("old old"), 0600); err != nil {
		t.Fatal(err)
	}
	request.Nonce = "ambiguous"
	if _, err = e.Prepare(context.Background(), s, request); err != ErrConflict {
		t.Fatalf("ambiguous patch context accepted: %v", err)
	}
	if err = os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	request.Nonce = "conflict"
	p, err = e.Prepare(context.Background(), s, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Submit(s, p.ID, p.Digest); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, []byte("user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = j.Decide(p.ID, p.Digest, true, true); err != nil {
		t.Fatal(err)
	}
	result := waitState(t, e, s, p.ID, "failed")
	if result.ErrorCode != "file_conflict" {
		t.Fatalf("patch did not detect intervening edit: %+v", result)
	}
	b, _ = os.ReadFile(target)
	if string(b) != "user edit" {
		t.Fatal("patch overwrote user's intervening edit")
	}
}

func TestTwoConnectionsRunIndependentlyAndCancelOnlyOwnJob(t *testing.T) {
	e, j, workspace := realEngineConfigured(t, []Tool{helperTool(t)}, func(store *config.Store) {
		cfg := store.Snapshot().Config()
		c := config.NewConnection("connection-two", "second", "profile", "credential-two", true)
		var err error
		c, err = c.WithPurpose(config.PurposeAgentRemote)
		if err != nil {
			t.Fatal(err)
		}
		c, err = c.WithExecutionPolicy(config.ExecutionTrustedHost)
		if err != nil {
			t.Fatal(err)
		}
		mode, err := commandprofile.NewDeveloperMode(true, []string{"connection", "connection-two"}, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
		if err != nil {
			t.Fatal(err)
		}
		next, err := config.NewWithCommandProfiles(config.SchemaVersionV2, cfg.Roots(), cfg.Profiles(), append(cfg.Connections(), c), append(cfg.Credentials(), config.NewCredentialRef("credential-two", "runtime")), nil, mode, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(next); err != nil {
			t.Fatal(err)
		}
	})
	subjects := []Subject{{"one", "connection"}, {"two", "connection-two"}}
	ids := make([]string, 2)
	for i, s := range subjects {
		action := "wait"
		if i == 1 {
			action = "wait-two"
		}
		p, err := e.Prepare(context.Background(), s, Request{Nonce: action, Kind: "command", RootID: "workspace", ToolID: "helper", Args: []string{action}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = e.Submit(s, p.ID, p.Digest); err != nil {
			t.Fatal(err)
		}
		if err = j.Decide(p.ID, p.Digest, true, true); err != nil {
			t.Fatal(err)
		}
		ids[i] = p.ID
	}
	end := time.Now().Add(5 * time.Second)
	for {
		_, one := os.Stat(filepath.Join(workspace, "started.txt"))
		_, two := os.Stat(filepath.Join(workspace, "started-two.txt"))
		if one == nil && two == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("both real connection processes did not start concurrently")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := e.Cancel(ctx, subjects[0], ids[1]); err != ErrDenied {
		t.Fatal("cross-connection cancellation was accepted")
	}
	if result, err := e.Cancel(ctx, subjects[0], ids[0]); err != nil || result.State != "cancelled" {
		t.Fatal("first job cancellation failed")
	}
	if result, err := e.Status(subjects[1], ids[1]); err != nil || result.State != "running" {
		t.Fatal("cancelling one connection stopped the other")
	}
	if result, err := e.Cancel(ctx, subjects[1], ids[1]); err != nil || result.State != "cancelled" {
		t.Fatal("second job cleanup failed")
	}
}

func TestOwnerCanCancelRunningJobWithoutAgent(t *testing.T) {
	e, j, workspace := realEngine(t, []Tool{helperTool(t)})
	s := Subject{"principal", "connection"}
	p := approved(t, e, j, Request{Nonce: "owner-cancel", Kind: "command", RootID: "workspace", ToolID: "helper", Args: []string{"wait"}})
	end := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(workspace, "started.txt")); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("real child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := j.CancelLocal(p.ID, p.Digest); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, s, p.ID, "cancelled")
}

func TestSuccessfulParentExitReapsChildWithoutOutputHandles(t *testing.T) {
	e, j, workspace := realEngine(t, []Tool{helperTool(t)})
	s := Subject{"principal", "connection"}
	p := approved(t, e, j, Request{Nonce: "descendant", Kind: "command", RootID: "workspace", ToolID: "helper", Args: []string{"spawn"}})
	waitState(t, e, s, p.ID, "succeeded")
	b, err := os.ReadFile(filepath.Join(workspace, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.ParseUint(string(b), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return
		}
		t.Fatalf("cannot verify descendant termination: %v", err)
	}
	defer windows.CloseHandle(h)
	state, err := windows.WaitForSingleObject(h, 0)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatal("job reported success while its output-detached child was still running")
	}
}
