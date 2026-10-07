package workspaceadmin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
)

func patterns(values ...string) *[]string { return &values }

func TestFileRulesViewIsScopedAndDetached(t *testing.T) {
	env := newTestWorkspace(t, false)
	view, err := env.manager.FileRules(context.Background(), "chatgpt-local")
	if err != nil {
		t.Fatal(err)
	}
	if view.ProfileID != "profile-a" || view.Revision != env.initial.Revision() || len(view.Roots) != 1 || view.Roots[0].RootID != "project" {
		t.Fatalf("bad view: %+v", view)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), env.project) || strings.Contains(string(encoded), "cred-a") {
		t.Fatal("local rules view leaked path/credential")
	}
	view.Profile.Deny[0] = "**"
	view.Roots[0].Deny[0] = "**"
	again, err := env.manager.FileRules(context.Background(), "chatgpt-local")
	if err != nil || again.Profile.Deny[0] != ".git/**" || again.Roots[0].Deny[0] != ".env" {
		t.Fatalf("view mutation escaped: %+v %v", again, err)
	}
	if _, err := env.manager.FileRules(context.Background(), ""); !errors.Is(err, ErrConnectionMissing) {
		t.Fatalf("empty connection: %v", err)
	}
}

func TestRulePreviewUsesRealDenyAndIgnoreWithoutPersistence(t *testing.T) {
	env := newTestWorkspace(t, false)
	before, _ := os.ReadFile(env.configPath)
	update := RuleUpdate{Deny: patterns(".git/**", "*.key"), Ignore: patterns("vendor/**", "*.key")}
	samples := []RuleSample{
		{RootID: "project", Path: "src/main.go"},
		{RootID: "project", Path: "SRC/PRIVATE.KEY"},
		{RootID: "project", Path: "vendor/lib.go"},
		{RootID: "project", Path: ".env"},
		{RootID: "project", Path: ".git", Directory: true},
		{RootID: "project", Directory: true},
	}
	preview, err := env.manager.PreviewRules(context.Background(), "chatgpt-local", env.initial.Revision(), update, samples)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Changed || !slices.Equal(preview.AffectedConnections, []string{"chatgpt-local"}) {
		t.Fatalf("impact: %+v", preview)
	}
	for i, expected := range []struct{ allowed, ignored bool }{{true, false}, {false, false}, {true, true}, {false, false}, {false, false}, {true, false}} {
		got := preview.Decisions[i]
		if got.Allowed != expected.allowed || got.Ignored != expected.ignored {
			t.Fatalf("decision[%d]=%+v", i, got)
		}
	}
	after, _ := os.ReadFile(env.configPath)
	if string(before) != string(after) {
		t.Fatal("preview changed persisted config")
	}
}

func TestSetRulesPersistsAndRealPolicyEnforces(t *testing.T) {
	env := newTestWorkspace(t, false)
	result, err := env.manager.SetRules(context.Background(), "chatgpt-local", env.initial.Revision(), RuleUpdate{RootID: "project", Deny: patterns(".env", "private/**"), Ignore: patterns("build/**")}, []string{"chatgpt-local"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Revision == env.initial.Revision() {
		t.Fatal("no persisted revision change")
	}
	saved, err := env.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg := saved.Config()
	root, _ := cfg.Root("project")
	if !slices.Equal(root.DenyPatterns(), []string{".env", "private/**"}) || !slices.Equal(root.IgnorePatterns(), []string{"build/**"}) {
		t.Fatal("rules not saved")
	}
	if !reflect.DeepEqual(cfg.Credentials(), env.config.Credentials()) || !reflect.DeepEqual(cfg.Connections(), env.config.Connections()) || !reflect.DeepEqual(cfg.DeveloperMode(), env.config.DeveloperMode()) || !reflect.DeepEqual(cfg.CommandProfiles(), env.config.CommandProfiles()) || !reflect.DeepEqual(cfg.EnvironmentTools(), env.config.EnvironmentTools()) {
		t.Fatal("unrelated configuration changed")
	}
	oldProfile, _ := env.config.Profile("profile-a")
	newProfile, _ := cfg.Profile("profile-a")
	if !reflect.DeepEqual(oldProfile, newProfile) {
		t.Fatal("root update rewrote profile")
	}
	store, _ := config.NewStore(cfg)
	manager, _ := policy.NewManager(store)
	bound, err := manager.BindAuthenticated("chatgpt-local")
	if err != nil {
		t.Fatal(err)
	}
	if bound.AllowsPath("project", "private/a.txt") || !bound.AllowsPath("project", "build/a.txt") || !bound.IsIgnoredPath("project", "build/a.txt") {
		t.Fatal("actual policy differs from persisted rules")
	}
	if _, err := env.manager.SetRules(context.Background(), "chatgpt-local", env.initial.Revision(), RuleUpdate{Deny: patterns("*.key")}, []string{"chatgpt-local"}); !errors.Is(err, config.ErrRevisionConflict) {
		t.Fatalf("stale write: %v", err)
	}
}

func TestSharedRulesRequireExactImpactIncludingPausedAndDisabled(t *testing.T) {
	env := newTestWorkspace(t, true)
	cfg := env.config
	profile, _ := cfg.Profile("profile-b")
	metadata, _ := config.NewWorkspaceRootMetadata("shared", "Shared", false)
	paused, err := config.NewProfileWithWorkspaceRoots(profile.ID(), nil, profile.Tools(), profile.DenyPatterns(), profile.IgnorePatterns(), []config.WorkspaceRootMetadata{metadata})
	if err != nil {
		t.Fatal(err)
	}
	profiles := cfg.Profiles()
	profiles[profileIndex(profiles, paused.ID())] = paused
	connections := cfg.Connections()
	connections[1] = config.NewConnection(connections[1].ID(), "Other", paused.ID(), connections[1].CredentialRef(), false)
	next, err := config.NewWithCommandProfiles(cfg.SchemaVersion(), cfg.Roots(), profiles, connections, cfg.Credentials(), cfg.EnvironmentTools(), cfg.DeveloperMode(), cfg.CommandProfiles())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := env.store.Save(next)
	if err != nil {
		t.Fatal(err)
	}
	update := RuleUpdate{RootID: "shared", Deny: patterns("secret/**")}
	preview, err := env.manager.PreviewRules(context.Background(), "chatgpt-local", saved.Revision(), update, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"chatgpt-local", "other-connection"}
	if !slices.Equal(preview.AffectedConnections, want) {
		t.Fatalf("impact=%v", preview.AffectedConnections)
	}
	for _, ack := range [][]string{nil, {"chatgpt-local"}, {"chatgpt-local", "other-connection", "unknown"}, {"chatgpt-local", "other-connection", "other-connection"}} {
		if _, err := env.manager.SetRules(context.Background(), "chatgpt-local", saved.Revision(), update, ack); !errors.Is(err, ErrImpactRequired) {
			t.Fatalf("bad ack %v: %v", ack, err)
		}
	}
	if _, err := env.manager.SetRules(context.Background(), "chatgpt-local", saved.Revision(), update, []string{"other-connection", "chatgpt-local"}); err != nil {
		t.Fatal(err)
	}
}

func TestSharedProfileImpactDoesNotIncludeOnlySharedRoot(t *testing.T) {
	env := newTestWorkspace(t, true)
	cfg := env.config
	connections := append(cfg.Connections(), config.NewConnection("same-profile", "Same", "profile-a", "cred-a", true))
	next, err := config.NewWithCommandProfiles(cfg.SchemaVersion(), cfg.Roots(), cfg.Profiles(), connections, cfg.Credentials(), cfg.EnvironmentTools(), cfg.DeveloperMode(), cfg.CommandProfiles())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := env.store.Save(next)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := env.manager.PreviewRules(context.Background(), "chatgpt-local", saved.Revision(), RuleUpdate{Deny: patterns("*.key")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(preview.AffectedConnections, []string{"chatgpt-local", "same-profile"}) {
		t.Fatalf("profile impact=%v", preview.AffectedConnections)
	}
}

func TestRuleRejectionsNeverWrite(t *testing.T) {
	tests := []struct {
		name    string
		update  RuleUpdate
		samples []RuleSample
		cause   error
	}{
		{"missing_update", RuleUpdate{}, nil, ErrInvalidRules},
		{"foreign_root", RuleUpdate{RootID: "shared", Deny: patterns("*.key")}, nil, ErrRootMissing},
		{"traversal", RuleUpdate{Deny: patterns("../secret/**")}, nil, ErrInvalidRules},
		{"glob", RuleUpdate{Deny: patterns("[")}, nil, ErrInvalidRules},
		{"backslash", RuleUpdate{Deny: patterns(`dir\secret`)}, nil, ErrInvalidRules},
		{"absolute", RuleUpdate{Deny: patterns("/etc/**")}, nil, ErrInvalidRules},
		{"control", RuleUpdate{Ignore: patterns("foo\nbar")}, nil, ErrInvalidRules},
		{"duplicate", RuleUpdate{Deny: patterns("*.key", "*.key")}, nil, ErrInvalidRules},
		{"invalid_sample", RuleUpdate{Deny: patterns("*.key")}, []RuleSample{{RootID: "project", Path: "../secret"}}, ErrInvalidRules},
		{"foreign_sample", RuleUpdate{Deny: patterns("*.key")}, []RuleSample{{RootID: "shared", Path: "a.txt"}}, ErrRootMissing},
		{"empty_file_path", RuleUpdate{Deny: patterns("*.key")}, []RuleSample{{RootID: "project"}}, ErrInvalidRules},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestWorkspace(t, false)
			before, _ := os.ReadFile(env.configPath)
			_, err := env.manager.PreviewRules(context.Background(), "chatgpt-local", env.initial.Revision(), tt.update, tt.samples)
			if !errors.Is(err, tt.cause) {
				t.Fatalf("preview error=%v want=%v", err, tt.cause)
			}
			if len(tt.samples) == 0 {
				_, err = env.manager.SetRules(context.Background(), "chatgpt-local", env.initial.Revision(), tt.update, []string{"chatgpt-local"})
				if !errors.Is(err, tt.cause) {
					t.Fatalf("set error=%v want=%v", err, tt.cause)
				}
			}
			after, _ := os.ReadFile(env.configPath)
			if string(before) != string(after) {
				t.Fatal("rejected operation changed file")
			}
		})
	}
}

func TestRuleBudgets(t *testing.T) {
	tooMany := make([]string, MaxRulePatterns+1)
	for i := range tooMany {
		tooMany[i] = strings.Repeat("x", i+1)
	}
	tooLarge := make([]string, 20)
	for i := range tooLarge {
		tooLarge[i] = strings.Repeat("x", 3500+i)
	}
	for _, list := range [][]string{tooMany, tooLarge} {
		env := newTestWorkspace(t, false)
		_, err := env.manager.PreviewRules(context.Background(), "chatgpt-local", env.initial.Revision(), RuleUpdate{Deny: &list}, nil)
		if !errors.Is(err, ErrInvalidRules) {
			t.Fatalf("unbounded rules accepted: %v", err)
		}
	}
	env := newTestWorkspace(t, false)
	_, err := env.manager.PreviewRules(context.Background(), "chatgpt-local", env.initial.Revision(), RuleUpdate{Deny: patterns("*.key")}, make([]RuleSample, MaxRuleSamples+1))
	if !errors.Is(err, ErrInvalidRules) {
		t.Fatalf("unbounded samples: %v", err)
	}
}

func TestClearUnchangedAndCancelledRules(t *testing.T) {
	env := newTestWorkspace(t, false)
	rev := env.initial.Revision()
	noop, err := env.manager.SetRules(context.Background(), "chatgpt-local", rev, RuleUpdate{Deny: patterns(".git/**")}, nil)
	if err != nil || noop.Changed || noop.Revision != rev {
		t.Fatalf("noop=%+v %v", noop, err)
	}
	empty := []string{}
	cleared, err := env.manager.SetRules(context.Background(), "chatgpt-local", rev, RuleUpdate{Ignore: &empty}, []string{"chatgpt-local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleared.Profile.Ignore) != 0 || !slices.Equal(cleared.Profile.Deny, []string{".git/**"}) {
		t.Fatal("clear also removed unchanged deny")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := env.manager.SetRules(ctx, "chatgpt-local", cleared.Revision, RuleUpdate{Deny: patterns("*.key")}, []string{"chatgpt-local"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := env.manager.PreviewRules(context.Background(), "chatgpt-local", "", RuleUpdate{Deny: patterns("*.key")}, nil); !errors.Is(err, ErrRevisionRequired) {
		t.Fatalf("missing revision=%v", err)
	}
	current, _ := env.store.Load()
	if current.Revision() != cleared.Revision {
		t.Fatal("cancelled operation changed revision")
	}
}

func TestConcurrentRuleWritersUseOneCASWinner(t *testing.T) {
	env := newTestWorkspace(t, false)
	second, err := New(env.store)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, manager := range []*Manager{env.manager, second} {
		wg.Add(1)
		go func(m *Manager) {
			defer wg.Done()
			<-start
			_, err := m.SetRules(context.Background(), "chatgpt-local", env.initial.Revision(), RuleUpdate{Deny: patterns("*.key")}, []string{"chatgpt-local"})
			results <- err
		}(manager)
	}
	close(start)
	wg.Wait()
	close(results)
	winners, conflicts := 0, 0
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, config.ErrRevisionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
}
