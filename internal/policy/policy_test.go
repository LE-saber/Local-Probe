package policy

import (
	"errors"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/config"
)

const policyConfigJSON = `{
  "schema_version": "local-probe.config.v1",
  "roots": [
    {"id": "project", "path": "C:/work/project", "deny_patterns": [".env", "*.key"]},
    {"id": "private", "path": "C:/work/private"}
  ],
  "profiles": [
		{"id": "project-read", "roots": ["project"], "tools": ["read_file", "batch_read"], "deny_patterns": ["secrets/**"]},
    {"id": "private-read", "roots": ["private"], "tools": ["read_file"]}
  ],
  "connections": [
    {"id": "account-a", "profile_id": "project-read", "credential_ref": "cred-a", "enabled": true},
    {"id": "account-b", "profile_id": "private-read", "credential_ref": "cred-b", "enabled": true}
  ],
  "credentials": [
    {"id": "cred-a", "kind": "runtime"},
    {"id": "cred-b", "kind": "runtime"}
  ]
}`

func policyStore(t *testing.T, data string) *config.Store {
	t.Helper()
	cfg, err := config.Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestBoundScopesAreIsolatedAndDenyWins(t *testing.T) {
	manager, err := NewManager(policyStore(t, policyConfigJSON))
	if err != nil {
		t.Fatal(err)
	}
	a, err := manager.BindAuthenticated("account-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := manager.BindAuthenticated("account-b")
	if err != nil {
		t.Fatal(err)
	}
	if !a.AllowsRoot("project") || a.AllowsRoot("private") || b.AllowsRoot("project") || !b.AllowsRoot("private") {
		t.Fatal("cross-profile root access was allowed")
	}
	if !a.AllowsTool("read_file") || !a.AllowsTool("batch_read") || a.AllowsTool("git_diff") {
		t.Fatal("tool allowlist is incorrect")
	}
	if !a.AllowsPath("project", "src/main.go") || a.AllowsPath("project", ".env") || a.AllowsPath("project", ".ENV") || a.AllowsPath("project", "nested/api.key") || a.AllowsPath("project", "nested/secret.KEY") || a.AllowsPath("project", "secrets/token.txt") || a.AllowsPath("project", "secrets/nested/token.txt") || a.AllowsPath("project", "secrets") {
		t.Fatal("deny pattern or path policy was bypassed")
	}
	if a.AllowsPath("private", "notes.txt") || b.AllowsPath("project", "src/main.go") {
		t.Fatal("profile root isolation was bypassed")
	}
	if _, err := a.Scope(); err != nil {
		t.Fatalf("active scope rejected: %v", err)
	}
}

func TestDirectoryPolicySeparatesIgnoreFromDeny(t *testing.T) {
	const input = `{
  "schema_version":"local-probe.config.v1",
  "roots":[{"id":"project","path":"C:/project","deny_patterns":["secrets/**"],"ignore_patterns":["vendor/**"]}],
  "profiles":[{"id":"read","roots":["project"],"tools":["find_files"],"deny_patterns":["*.key"],"ignore_patterns":["*.tmp"]}],
  "connections":[{"id":"connection","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials":[{"id":"credential","kind":"local_token"}]
}`
	store := policyStore(t, input)
	manager, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.BindAuthenticated("connection")
	if err != nil {
		t.Fatal(err)
	}
	if !bound.AllowsDirectory("project", "") || !bound.AllowsDirectory("project", "src") {
		t.Fatal("authorized directory was denied")
	}
	if !bound.IsIgnoredPath("project", "vendor/pkg/file.go") || !bound.IsIgnoredPath("project", "build.tmp") {
		t.Fatal("ignore pattern was not applied")
	}
	if !bound.IsDeniedPath("project", "secrets/token.txt") || !bound.IsDeniedPath("project", "config.key") {
		t.Fatal("deny pattern was not applied")
	}
	if bound.IsIgnoredPath("project", "secrets/vendor.tmp") {
		t.Fatal("deny must take precedence over ignore")
	}
}

func TestDisabledAndReplacedConnectionsRevokeOldScopes(t *testing.T) {
	store := policyStore(t, policyConfigJSON)
	manager, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	old, err := manager.BindAuthenticated("account-a")
	if err != nil {
		t.Fatal(err)
	}
	updatedJSON := `{
  "schema_version": "local-probe.config.v1",
  "roots": [{"id": "project", "path": "C:/work/project"}],
  "profiles": [{"id": "project-read", "roots": ["project"], "tools": ["read_file"]}],
  "connections": [{"id": "account-a", "profile_id": "project-read", "credential_ref": "cred-a", "enabled": false}],
  "credentials": [{"id": "cred-a", "kind": "runtime"}]
}`
	updated, err := config.Parse([]byte(updatedJSON))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Replace(updated); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(old.Validate(), ErrRevoked) {
		t.Fatal("old scope remained valid after revision/disable")
	}
	if old.AllowsRoot("project") || old.AllowsTool("read_file") {
		t.Fatal("revoked scope still authorized operations")
	}
	if _, err := manager.BindAuthenticated("account-a"); !errors.Is(err, ErrDenied) {
		t.Fatalf("disabled connection was accepted: %v", err)
	}
}

func TestScopeCannotBeUsedWithAnotherManager(t *testing.T) {
	store := policyStore(t, policyConfigJSON)
	m1, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := m1.BindAuthenticated("account-a")
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(m2.Validate(bound), ErrRevoked) {
		t.Fatal("scope accepted by a different manager")
	}
}
