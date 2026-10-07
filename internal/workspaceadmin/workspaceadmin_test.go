package workspaceadmin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/config"
)

type testWorkspace struct {
	manager       *Manager
	store         *config.FileStore
	initial       config.Snapshot
	config        config.Config
	shared        string
	project       string
	second        string
	third         string
	configPath    string
	preservedJSON []byte
}

func newTestWorkspace(t *testing.T, sharedByBoth bool) testWorkspace {
	t.Helper()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	second := filepath.Join(dir, "second")
	third := filepath.Join(dir, "third")
	shared := filepath.Join(dir, "shared")
	for _, path := range []string{project, second, third, shared} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	projectRoot, err := config.NewRoot("project", project, []string{".env"})
	if err != nil {
		t.Fatal(err)
	}
	sharedRoot, err := config.NewRoot("shared", shared, nil)
	if err != nil {
		t.Fatal(err)
	}
	profileRoots := []string{"project"}
	otherRoots := []string{"shared"}
	if sharedByBoth {
		profileRoots = append(profileRoots, "shared")
	}
	profileA, err := config.NewProfileWithIgnore("profile-a", profileRoots, []string{"read_file", "list_directory"}, []string{".git/**"}, []string{"vendor/**"})
	if err != nil {
		t.Fatal(err)
	}
	profileB, err := config.NewProfile("profile-b", otherRoots, []string{"read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	credentialA := config.NewCredentialRef("cred-a", "runtime")
	credentialB := config.NewCredentialRef("cred-b", "runtime")
	connectionA := config.NewConnection("chatgpt-local", "Local", "profile-a", credentialA.ID(), true)
	connectionB := config.NewConnection("other-connection", "Other", "profile-b", credentialB.ID(), true)
	environmentTool, err := config.NewEnvironmentTool("git", []string{filepath.Join(dir, "git.exe")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	developerMode, err := commandprofile.NewDeveloperMode(true, []string{"chatgpt-local"}, commandprofile.ConfirmationPerCall, commandprofile.NetworkDeny)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.NewWithCommandProfiles(
		config.SchemaVersionV1,
		[]config.Root{projectRoot, sharedRoot},
		[]config.Profile{profileA, profileB},
		[]config.Connection{connectionA, connectionB},
		[]config.CredentialRef{credentialA, credentialB},
		[]config.EnvironmentTool{environmentTool},
		developerMode,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "local-probe.json")
	store, err := config.NewFileStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return testWorkspace{manager: manager, store: store, initial: initial, config: cfg, shared: shared, project: project, second: second, third: third, configPath: configPath, preservedJSON: encoded}
}

func TestListRequiresExplicitConnectionAndNeverProjectsOtherProfiles(t *testing.T) {
	env := newTestWorkspace(t, false)
	ctx := context.Background()
	if _, err := env.manager.List(ctx, ""); !errors.Is(err, ErrConnectionMissing) {
		t.Fatalf("empty connection error = %v, want ErrConnectionMissing", err)
	}
	workspace, err := env.manager.List(ctx, DefaultConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.ConnectionID != DefaultConnectionID || workspace.ProfileID != "profile-a" || len(workspace.Roots) != 1 || workspace.Roots[0].ID != "project" {
		t.Fatalf("unexpected selected workspace: %+v", workspace)
	}
}

func TestProfileReferenceCountDetectsSharedConnectionProfile(t *testing.T) {
	env := newTestWorkspace(t, false)
	initial, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := initial.Config()
	connections := append(cfg.Connections(), config.NewConnection("second-local", "Second", "profile-a", "cred-a", true))
	next, err := config.NewWithCommandProfiles(cfg.SchemaVersion(), cfg.Roots(), cfg.Profiles(), connections, cfg.Credentials(), cfg.EnvironmentTools(), cfg.DeveloperMode(), cfg.CommandProfiles())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.SaveIfRevision(initial.Revision(), next); err != nil {
		t.Fatal(err)
	}
	revision, references, err := env.manager.ProfileReferenceCount(context.Background(), DefaultConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if references != 2 {
		t.Fatalf("profile reference count = %d, want 2", references)
	}
	if current, err := env.store.Load(); err != nil || current.Revision() != revision {
		t.Fatalf("reference count revision = %q, current=%q, err=%v", revision, current.Revision(), err)
	}
}

func TestUpdatePauseAndResumeAreProfileScoped(t *testing.T) {
	env := newTestWorkspace(t, true)
	pausedValue := false
	paused, err := env.manager.Update(context.Background(), DefaultConnectionID, env.initial.Revision(), "shared", RootUpdate{Enabled: &pausedValue})
	if err != nil {
		t.Fatal(err)
	}
	var shared WorkspaceRoot
	for _, root := range paused.Roots {
		if root.ID == "shared" {
			shared = root
		}
	}
	if shared.ID == "" || shared.Enabled {
		t.Fatalf("paused workspace projection = %+v", paused.Roots)
	}
	stored, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	profileA, _ := stored.Config().Profile("profile-a")
	profileB, _ := stored.Config().Profile("profile-b")
	if reflect.DeepEqual(profileA.RootIDs(), []string{"project", "shared"}) || !reflect.DeepEqual(profileB.RootIDs(), []string{"shared"}) {
		t.Fatalf("profile scopes after pause: A=%v B=%v", profileA.RootIDs(), profileB.RootIDs())
	}
	if _, ok := stored.Config().Root("shared"); !ok {
		t.Fatal("pausing profile A deleted its shared root entity")
	}
	other, err := env.manager.List(context.Background(), "other-connection")
	if err != nil || len(other.Roots) != 1 || !other.Roots[0].Enabled {
		t.Fatalf("pause in profile A affected profile B: roots=%+v err=%v", other.Roots, err)
	}

	resumedValue := true
	resumed, err := env.manager.Update(context.Background(), DefaultConnectionID, paused.Revision, "shared", RootUpdate{Enabled: &resumedValue})
	if err != nil {
		t.Fatal(err)
	}
	profileA, _ = mustLoadConfig(t, env.store).Profile("profile-a")
	if !reflect.DeepEqual(profileA.RootIDs(), []string{"project", "shared"}) {
		t.Fatalf("profile A did not resume: %v (revision %s)", profileA.RootIDs(), resumed.Revision)
	}
}

func TestRemovingPausedRegistrationPreservesOtherProfileRootReference(t *testing.T) {
	env := newTestWorkspace(t, true)
	pausedValue := false
	paused, err := env.manager.Update(context.Background(), DefaultConnectionID, env.initial.Revision(), "shared", RootUpdate{Enabled: &pausedValue})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := env.manager.Remove(context.Background(), DefaultConnectionID, paused.Revision, []string{"shared"})
	if err != nil {
		t.Fatal(err)
	}
	stored := mustLoadConfig(t, env.store)
	if _, ok := stored.Root("shared"); !ok {
		t.Fatal("removing paused registration deleted a root still authorized by another profile")
	}
	last, err := env.manager.Remove(context.Background(), "other-connection", removed.Revision, []string{"shared"})
	if err != nil {
		t.Fatal(err)
	}
	stored = mustLoadConfig(t, env.store)
	if _, ok := stored.Root("shared"); ok {
		t.Fatal("root entity remained after every profile and command reference was removed")
	}
	if len(last.Roots) != 0 {
		t.Fatalf("last profile still lists removed root: %+v", last.Roots)
	}
}

func TestUpdateDisplayNameDoesNotChangeEffectiveAuthorization(t *testing.T) {
	env := newTestWorkspace(t, false)
	name := "Codebase"
	updated, err := env.manager.Update(context.Background(), DefaultConnectionID, env.initial.Revision(), "project", RootUpdate{DisplayName: &name})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := mustLoadConfig(t, env.store).Profile("profile-a")
	if !reflect.DeepEqual(profile.RootIDs(), []string{"project"}) || len(updated.Roots) != 1 || updated.Roots[0].DisplayName != name {
		t.Fatalf("label update changed authorization or projection: profile=%v roots=%+v", profile.RootIDs(), updated.Roots)
	}
}

func mustLoadConfig(t *testing.T, store *config.FileStore) config.Config {
	t.Helper()
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Config()
}

func TestAddMultipleFoldersIsIdempotentAndScopedToOneConnection(t *testing.T) {
	env := newTestWorkspace(t, false)
	result, err := env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{env.second, env.third, env.second})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changed) != 2 || len(result.Roots) != 3 {
		t.Fatalf("add result = %+v, want two changes and three authorized roots", result)
	}
	if result.Roots[1].ID != workspaceRootID(strings.ToLower(filepath.Clean(env.second))) && runtime.GOOS == "windows" {
		t.Fatalf("second root id = %q, want stable digest id", result.Roots[1].ID)
	}
	other, err := env.manager.List(context.Background(), "other-connection")
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Roots) != 1 || other.Roots[0].ID != "shared" {
		t.Fatalf("other connection was changed: %+v", other)
	}
	secondRevision := result.Revision
	idempotent, err := env.manager.Add(context.Background(), DefaultConnectionID, secondRevision, []string{env.second, env.second})
	if err != nil {
		t.Fatal(err)
	}
	if len(idempotent.Changed) != 0 || idempotent.Revision != secondRevision {
		t.Fatalf("duplicate add was not idempotent: %+v", idempotent)
	}
}

func TestDeleteSharedRootOnlyRemovesItAfterLastProfileReference(t *testing.T) {
	env := newTestWorkspace(t, true)
	first, err := env.manager.Remove(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{"shared"})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Roots) != 1 || first.Roots[0].ID != "project" {
		t.Fatalf("profile A roots after removal = %+v", first.Roots)
	}
	stored, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.Config().Root("shared"); !ok {
		t.Fatal("shared root entity was deleted while profile B still referenced it")
	}
	second, err := env.manager.Remove(context.Background(), "other-connection", first.Revision, []string{"shared"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Roots) != 0 {
		t.Fatalf("profile B roots after removal = %+v", second.Roots)
	}
	stored, err = env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.Config().Root("shared"); ok {
		t.Fatal("unreferenced shared root entity was retained")
	}
}

func TestRevisionConflictDoesNotOverwriteNewerConfiguration(t *testing.T) {
	env := newTestWorkspace(t, false)
	first, err := env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{env.second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{env.third})
	if !errors.Is(err, config.ErrRevisionConflict) || ProblemCode(err) != CodeRevisionConflict {
		t.Fatalf("stale mutation error = %v (code %s), want revision conflict", err, ProblemCode(err))
	}
	current, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision() != first.Revision {
		t.Fatalf("stale mutation changed revision from %q to %q", first.Revision, current.Revision())
	}
	workspace, err := env.manager.List(context.Background(), DefaultConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Roots) != 2 {
		t.Fatalf("stale mutation changed roots: %+v", workspace.Roots)
	}
}

func TestRejectsParentChildOverlapAgainstConfiguredAndBatchRoots(t *testing.T) {
	env := newTestWorkspace(t, false)
	child := filepath.Join(env.project, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{child})
	if !errors.Is(err, ErrRootOverlap) || ProblemCode(err) != CodeRootOverlap {
		t.Fatalf("configured-root overlap error = %v code=%s, want root_overlap", err, ProblemCode(err))
	}
	current, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision() != env.initial.Revision() {
		t.Fatalf("configured-root overlap changed revision to %q", current.Revision())
	}
	batchParent := filepath.Join(filepath.Dir(env.project), "batch-parent")
	batchChild := filepath.Join(batchParent, "child")
	if err := os.MkdirAll(batchChild, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{batchParent, batchChild})
	if !errors.Is(err, ErrRootOverlap) || ProblemCode(err) != CodeRootOverlap {
		t.Fatalf("batch overlap error = %v code=%s, want root_overlap", err, ProblemCode(err))
	}
	current, err = env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision() != env.initial.Revision() {
		t.Fatalf("batch overlap changed revision to %q", current.Revision())
	}
}

type heldMutationLocker struct{}

func (heldMutationLocker) acquire(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestMutationWaitTimeoutIsStableBusyError(t *testing.T) {
	env := newTestWorkspace(t, false)
	env.manager.locker = heldMutationLocker{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := env.manager.Add(ctx, DefaultConnectionID, env.initial.Revision(), []string{env.second})
	if !errors.Is(err, ErrMutationBusy) || ProblemCode(err) != CodeMutationBusy {
		t.Fatalf("busy error = %v code=%s, want stable busy", err, ProblemCode(err))
	}
	current, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision() != env.initial.Revision() {
		t.Fatalf("busy mutation changed revision to %q", current.Revision())
	}
}

func TestMutationPreservesNonWorkspaceFieldsAndWritesBackup(t *testing.T) {
	env := newTestWorkspace(t, false)
	before, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	result, err := env.manager.Add(context.Background(), DefaultConnectionID, before.Revision(), []string{env.second})
	if err != nil {
		t.Fatal(err)
	}
	after, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != after.Revision() || result.Revision == before.Revision() {
		t.Fatalf("unexpected revisions before=%q after=%q result=%q", before.Revision(), after.Revision(), result.Revision)
	}
	if !reflect.DeepEqual(after.Config().Connections(), before.Config().Connections()) ||
		!reflect.DeepEqual(after.Config().Credentials(), before.Config().Credentials()) ||
		!reflect.DeepEqual(after.Config().EnvironmentTools(), before.Config().EnvironmentTools()) ||
		after.Config().DeveloperMode().Enabled() != before.Config().DeveloperMode().Enabled() ||
		!reflect.DeepEqual(after.Config().DeveloperMode().AllowedConnections(), before.Config().DeveloperMode().AllowedConnections()) {
		t.Fatal("non-workspace configuration fields were not preserved")
	}
	backupData, err := os.ReadFile(env.store.BackupPath())
	if err != nil {
		t.Fatal(err)
	}
	var backupJSON, beforeJSON map[string]any
	if err := json.Unmarshal(backupData, &backupJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.preservedJSON, &beforeJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backupJSON, beforeJSON) {
		t.Fatal("FileStore backup does not contain the complete previous configuration")
	}
}

func TestWorkspacePathSafetyRejectsUnsafeSelections(t *testing.T) {
	env := newTestWorkspace(t, false)
	cases := []struct {
		name string
		path string
		code Code
		base error
	}{
		{name: "relative", path: "relative-folder", code: CodeInvalidPath, base: ErrInvalidPath},
		{name: "missing", path: filepath.Join(filepath.Dir(env.project), "missing"), code: CodePathMissing, base: ErrPathMissing},
	}
	file := filepath.Join(filepath.Dir(env.project), "not-a-directory.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, struct {
		name string
		path string
		code Code
		base error
	}{name: "file", path: file, code: CodeNotDirectory, base: ErrNotDirectory})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{tc.path})
			if !errors.Is(err, tc.base) || ProblemCode(err) != tc.code {
				t.Fatalf("error = %v code=%s, want %v/%s", err, ProblemCode(err), tc.base, tc.code)
			}
		})
	}
	rootPath := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		rootPath = filepath.VolumeName(env.project) + `\`
	}
	_, err := env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{rootPath})
	if !errors.Is(err, ErrDriveRoot) || ProblemCode(err) != CodeDriveRoot {
		t.Fatalf("drive root error = %v code=%s, want drive_root", err, ProblemCode(err))
	}

	link := filepath.Join(filepath.Dir(env.project), "link")
	if err := os.Symlink(env.project, link); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	_, err = env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{link})
	if !errors.Is(err, ErrPathLink) || ProblemCode(err) != CodePathLink {
		t.Fatalf("symlink error = %v code=%s, want symlink_or_reparse", err, ProblemCode(err))
	}

	remote := `//localhost/share`
	if runtime.GOOS == "windows" {
		remote = `\\localhost\share`
	}
	_, err = env.manager.Add(context.Background(), DefaultConnectionID, env.initial.Revision(), []string{remote})
	if !errors.Is(err, ErrRemotePath) || ProblemCode(err) != CodeRemotePath {
		t.Fatalf("remote path error = %v code=%s, want remote_path", err, ProblemCode(err))
	}
}

func TestCancelContextDoesNotTouchConfiguration(t *testing.T) {
	env := newTestWorkspace(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := env.manager.Add(ctx, DefaultConnectionID, env.initial.Revision(), []string{env.second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v, want context.Canceled", err)
	}
	current, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision() != env.initial.Revision() {
		t.Fatalf("cancelled mutation changed revision to %q", current.Revision())
	}
}
