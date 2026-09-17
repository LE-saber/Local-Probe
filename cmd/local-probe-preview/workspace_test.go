package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/previewconnect"
	"github.com/LE-saber/Local-Probe/internal/previewui"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

type fakeWorkspaceReconnecter struct {
	status previewconnect.Status
	err    error
	calls  int
}

func (f *fakeWorkspaceReconnecter) Reconnect(context.Context, previewconnect.ProgressFunc) (previewconnect.Status, error) {
	f.calls++
	return f.status, f.err
}

func newPreviewWorkspaceTestManager(t *testing.T, reconnect workspaceReconnecter) (*previewWorkspaceManager, *config.FileStore, config.Snapshot, string) {
	t.Helper()
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "root")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := config.NewRoot("root", rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("profile", []string{"root"}, []string{"read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	credential := config.NewCredentialRef("credential", "runtime")
	connection := config.NewConnection(workspaceadmin.DefaultConnectionID, "Local", "profile", credential.ID(), true)
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credential})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "local-probe.json")
	store, err := config.NewFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := workspaceadmin.New(store)
	if err != nil {
		t.Fatal(err)
	}
	return newPreviewWorkspaceManager(admin, reconnect), store, snapshot, rootPath
}

func TestPreviewWorkspaceManagerListUsesChatGPTLocalProfile(t *testing.T) {
	reconnect := &fakeWorkspaceReconnecter{}
	manager, _, _, rootPath := newPreviewWorkspaceTestManager(t, reconnect)
	folders, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || folders[0].RootID != "root" || folders[0].Path != rootPath {
		t.Fatalf("folders = %+v", folders)
	}
}

func TestPreviewWorkspaceManagerAddReconnectsAfterCASCommit(t *testing.T) {
	reconnect := &fakeWorkspaceReconnecter{status: previewconnect.Status{Stage: previewconnect.StageReady, Code: previewconnect.CodeNone}}
	manager, store, _, rootPath := newPreviewWorkspaceTestManager(t, reconnect)
	newPath := filepath.Join(filepath.Dir(rootPath), "new-root")
	if err := os.Mkdir(newPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.Add(context.Background(), []string{newPath}); err != nil {
		t.Fatal(err)
	}
	if reconnect.calls != 1 {
		t.Fatalf("reconnect calls = %d, want 1", reconnect.calls)
	}
	current, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := current.Config().Profile("profile")
	if len(profile.RootIDs()) != 2 {
		t.Fatalf("profile roots = %v, want two after add", profile.RootIDs())
	}
}

func TestPreviewWorkspaceManagerReportsSafeReconnectFailureAfterSave(t *testing.T) {
	reconnect := &fakeWorkspaceReconnecter{
		status: previewconnect.Status{Stage: previewconnect.StageFailed, Code: previewconnect.CodeTokenRejected},
		err:    &previewconnect.Problem{Code: previewconnect.CodeTokenRejected, Message: "token text must not be forwarded", Remedy: "rotate token"},
	}
	manager, store, _, rootPath := newPreviewWorkspaceTestManager(t, reconnect)
	newPath := filepath.Join(filepath.Dir(rootPath), "new-root")
	if err := os.Mkdir(newPath, 0o700); err != nil {
		t.Fatal(err)
	}
	err := manager.Add(context.Background(), []string{newPath})
	if err == nil {
		t.Fatal("expected post-save reconnect failure")
	}
	if !strings.Contains(err.Error(), "授权已保存") || !strings.Contains(err.Error(), "连接尚未就绪") || strings.Contains(err.Error(), "token text") || strings.Contains(err.Error(), "rotate") {
		t.Fatalf("unsafe reconnect error = %v", err)
	}
	// The stable wrapper is intentionally keyed by its Code string rather
	// than exposing controller implementation details; assert the safe
	// category through the wrapper below.
	var reconnectErr *workspaceReconnectError
	if !errors.As(err, &reconnectErr) || reconnectErr.code != previewconnect.CodeTokenRejected {
		t.Fatalf("reconnect error category = %v", err)
	}
	current, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	profile, _ := current.Config().Profile("profile")
	if len(profile.RootIDs()) != 2 {
		t.Fatal("configuration was not committed before reporting reconnect failure")
	}
}

func TestPreviewWorkspaceManagerRemoveUsesStableRootIDs(t *testing.T) {
	reconnect := &fakeWorkspaceReconnecter{status: previewconnect.Status{Stage: previewconnect.StageReady, Code: previewconnect.CodeNone}}
	manager, _, _, _ := newPreviewWorkspaceTestManager(t, reconnect)
	folders, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Remove(context.Background(), []string{previewui.WorkspaceFolderSelectionID(folders[0])}); err != nil {
		t.Fatal(err)
	}
	if reconnect.calls != 1 {
		t.Fatalf("reconnect calls = %d, want 1", reconnect.calls)
	}
}

func TestWorkspaceErrorPresentationUsesSafeActionableMessages(t *testing.T) {
	cases := []struct {
		err  error
		code string
		want string
	}{
		{err: &workspaceadmin.Problem{Code: workspaceadmin.CodePathMissing, Remediation: "secret-path"}, code: "path_missing", want: "不存在"},
		{err: &workspaceadmin.Problem{Code: workspaceadmin.CodeRootOverlap, Remediation: "secret-path"}, code: "root_overlap", want: "父子重叠"},
		{err: &workspaceadmin.Problem{Code: workspaceadmin.CodeRevisionConflict, Remediation: "secret-path"}, code: "revision_conflict", want: "其他窗口"},
		{err: &workspaceadmin.Problem{Code: workspaceadmin.CodeMutationBusy, Remediation: "secret-path"}, code: "busy", want: "稍后重试"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := workspaceErrorPresentation(tc.err)
			var safe *safeWorkspaceError
			if !errors.As(got, &safe) || safe.WorkspaceCode() != tc.code || !strings.Contains(safe.WorkspaceMessage(), tc.want) {
				t.Fatalf("presented error = %v code=%q, want code=%q/message containing %q", got, safeCode(got), tc.code, tc.want)
			}
			if strings.Contains(got.Error(), "secret-path") {
				t.Fatalf("presentation leaked remediation detail: %v", got)
			}
			message, remediation := previewui.WorkspaceErrorPresentation(got)
			if !strings.Contains(message, tc.want) || remediation == "" {
				t.Fatalf("UI presentation = %q / %q, want specific safe text containing %q", message, remediation, tc.want)
			}
		})
	}
}

func safeCode(err error) string {
	var safe *safeWorkspaceError
	if errors.As(err, &safe) {
		return safe.WorkspaceCode()
	}
	return ""
}
