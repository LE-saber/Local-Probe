package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
)

type countingWorkspace struct {
	writes    atomic.Int32
	directory string
}
type previewWorkspace struct {
	*countingWorkspace
	before []byte
}

func (w previewWorkspace) FilePreview(policy.BoundScope, string, string) (string, []byte, error) {
	return "existing", append([]byte(nil), w.before...), nil
}

func (w *countingWorkspace) WithDirectory(_ policy.BoundScope, _, _ string, call func(string) error) error {
	return call(w.directory)
}
func (w *countingWorkspace) FilePreview(policy.BoundScope, string, string) (string, []byte, error) {
	return "absent", nil, nil
}
func (w *countingWorkspace) WriteFile(_ policy.BoundScope, _, _, _ string, _ []byte, backup func([]byte) error) error {
	if err := backup(nil); err != nil {
		return err
	}
	w.writes.Add(1)
	return nil
}

func testConfig(t *testing.T, workspace string) *config.Store {
	t.Helper()
	root, err := config.NewRoot("workspace", workspace, []string{"*.key"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("profile", []string{"workspace"}, []string{"server_info", "read_file", "prepare_operation", "submit_operation", "operation_status", "operation_output", "cancel_operation", "prepare_file_change", "commit_file_change", "probe_tools"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile = profile.WithWriteAccess()
	c := config.NewConnection("connection", "test", profile.ID(), "credential", true)
	c, err = c.WithPurpose(config.PurposeAgentRemote)
	if err != nil {
		t.Fatal(err)
	}
	c, err = c.WithExecutionPolicy(config.ExecutionTrustedHost)
	if err != nil {
		t.Fatal(err)
	}
	mode, err := commandprofile.NewDeveloperMode(true, []string{c.ID()}, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.NewWithCommandProfiles(config.SchemaVersionV2, []config.Root{root}, []config.Profile{profile}, []config.Connection{c}, []config.CredentialRef{config.NewCredentialRef("credential", "runtime")}, nil, mode, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func newTestEngine(t *testing.T) (*Engine, *Journal, *countingWorkspace) {
	t.Helper()
	dir := t.TempDir()
	workspace := &countingWorkspace{directory: filepath.Join(dir, "workspace")}
	if err := os.Mkdir(workspace.directory, 0700); err != nil {
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
	store := testConfig(t, workspace.directory)
	manager, err := policy.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(context.Background(), Options{Journal: j, Config: store, Manager: manager, Workspace: workspace, Catalog: Catalog{Version: Version}, Acquire: func(ctx context.Context, _ Subject) (context.Context, func(), error) { return ctx, func() {}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, j, workspace
}
func waitState(t *testing.T, e *Engine, s Subject, id string, want string) Summary {
	t.Helper()
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		out, err := e.Status(s, id)
		if err != nil {
			t.Fatal(err)
		}
		if out.State == want {
			return out
		}
		if terminal(out.State) {
			output, _ := e.Output(s, id, "stderr", 0, 1024)
			t.Fatalf("state=%s code=%s exit=%d stderr=%q, want %s", out.State, out.ErrorCode, out.ExitCode, output.Data, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("operation did not reach expected state")
	return Summary{}
}
func prepareFile(t *testing.T, e *Engine, s Subject, nonce string) Summary {
	t.Helper()
	r, err := e.Prepare(context.Background(), s, Request{Nonce: nonce, Kind: "file_write", RootID: "workspace", Path: "test.txt", Content: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestApprovalIdempotencyAndSubjectIsolation(t *testing.T) {
	e, j, w := newTestEngine(t)
	subject := Subject{"principal", "connection"}
	p := prepareFile(t, e, subject, "once")
	if _, err := e.Submit(subject, p.ID, p.Digest); err != nil {
		t.Fatal(err)
	}
	if w.writes.Load() != 0 {
		t.Fatal("unapproved job wrote files")
	}
	if err := j.Decide(p.ID, strings.Repeat("0", 64), true, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed digest accepted: %v", err)
	}
	if err := j.Decide(p.ID, p.Digest, true, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing trusted-host acknowledgement accepted: %v", err)
	}
	if _, err := e.Status(Subject{"different-principal", "connection"}, p.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("cross-principal status exposed")
	}
	if err := j.Decide(p.ID, p.Digest, true, true); err != nil {
		t.Fatal(err)
	}
	waitState(t, e, subject, p.ID, "succeeded")
	if _, err := e.Submit(subject, p.ID, p.Digest); err != nil {
		t.Fatal(err)
	}
	if w.writes.Load() != 1 {
		t.Fatal("idempotent submission repeated side effects")
	}
	_, err := e.Prepare(context.Background(), subject, Request{Nonce: "once", Kind: "file_write", RootID: "workspace", Path: "test.txt", Content: "different"})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("same nonce accepted a different plan")
	}
	if err = j.Decide(p.ID, p.Digest, true, true); err == nil {
		t.Fatal("terminal job was re-approved")
	}
}
func TestRejectCancelAndPolicyChangeHaveNoEffects(t *testing.T) {
	for _, action := range []string{"reject", "cancel", "policy"} {
		t.Run(action, func(t *testing.T) {
			e, j, w := newTestEngine(t)
			s := Subject{"principal", "connection"}
			p := prepareFile(t, e, s, action)
			if _, err := e.Submit(s, p.ID, p.Digest); err != nil {
				t.Fatal(err)
			}
			state := "cancelled"
			switch action {
			case "reject":
				state = "rejected"
				if err := j.Decide(p.ID, p.Digest, false, false); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err := e.Cancel(context.Background(), s, p.ID); err != nil {
					t.Fatal(err)
				}
			case "policy":
				if _, err := e.opts.Config.Replace(e.opts.Config.Snapshot().Config()); err != nil {
					t.Fatal(err)
				}
			}
			waitState(t, e, s, p.ID, state)
			if w.writes.Load() != 0 {
				t.Fatal("unapproved/revoked/cancelled operation executed")
			}
		})
	}
}
func TestJournalOwnershipCorruptionAndRestartRecovery(t *testing.T) {
	e, j, w := newTestEngine(t)
	s := Subject{"principal", "connection"}
	p := prepareFile(t, e, s, "restart")
	if _, err := New(context.Background(), e.opts); !errors.Is(err, ErrConflict) {
		t.Fatalf("second engine obtained the same journal: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	records, err := j.Records()
	if err != nil {
		t.Fatal(err)
	}
	r := records[p.ID]
	r.Sequence++
	r.Summary.State = "running"
	if err = j.save(r); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(context.Background(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if out, err := restarted.Status(s, p.ID); err != nil || out.State != "unknown" {
		t.Fatalf("restart incorrectly resolved an in-flight operation: %+v %v", out, err)
	}
	if w.writes.Load() != 0 {
		t.Fatal("restart replayed uncertain side effect")
	}
	if err = os.WriteFile(filepath.Join(j.Path, p.ID+".000003.record"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Records(); err == nil {
		t.Fatal("corrupt persisted record was ignored")
	}
}

func TestJournalPublicationIsCompleteAndCannotReplace(t *testing.T) {
	_, j, _ := newTestEngine(t)
	name := "publication.bin"
	body := strings.Repeat("test-payload-", 100000)
	done := make(chan error, 1)
	go func() { done <- j.write(name, struct{ Body string }{body}) }()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			var result struct{ Body string }
			if j.read(name, &result) != nil || result.Body != body {
				t.Fatal("published record corrupted")
			}
			if j.write(name, struct{ Body string }{"replaced"}) == nil {
				t.Fatal("write-once record replaced")
			}
			return
		default:
			if _, err := j.root.Lstat(name); err == nil {
				var result struct{ Body string }
				if j.read(name, &result) != nil || result.Body != body {
					t.Fatal("reader observed a partial published record")
				}
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestEscapedPatchBudgetIsRejectedWithoutDisablingEngine(t *testing.T) {
	e, _, w := newTestEngine(t)
	old := strings.Repeat("\x01", MaxContent/2)
	other := strings.Repeat("\x02", MaxContent/2)
	e.opts.Workspace = previewWorkspace{w, []byte(old + other)}
	_, err := e.Prepare(context.Background(), Subject{"principal", "connection"}, Request{Nonce: "oversize", Kind: "file_patch", RootID: "workspace", Path: "test.txt", Edits: []Edit{{Old: old, New: strings.Repeat("\x03", MaxContent/2)}}})
	if !errors.Is(err, ErrInvalid) || !e.Allows(Subject{"principal", "connection"}) {
		t.Fatal("oversized serialized patch damaged operation admission")
	}
}

func TestReadOnlyProfileCannotEnableOperationTools(t *testing.T) {
	e, _, _ := newTestEngine(t)
	cfg := e.opts.Config.Snapshot().Config()
	old, _ := cfg.Profile("profile")
	readonly, err := config.NewProfile(old.ID(), old.RootIDs(), old.Tools(), old.DenyPatterns())
	if err != nil {
		t.Fatal(err)
	}
	next, err := config.NewWithCommandProfiles(config.SchemaVersionV2, cfg.Roots(), []config.Profile{readonly}, cfg.Connections(), cfg.Credentials(), nil, cfg.DeveloperMode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.opts.Config.Replace(next); err != nil {
		t.Fatal(err)
	}
	s := Subject{"principal", "connection"}
	if e.Allows(s) {
		t.Fatal("read-only profile exposed operation tools")
	}
	if _, err = e.Prepare(context.Background(), s, Request{Nonce: "read-only", Kind: "file_write", RootID: "workspace", Path: "test.txt", Content: "bad"}); !errors.Is(err, ErrDenied) {
		t.Fatal("read-only profile admitted an operation")
	}
}
