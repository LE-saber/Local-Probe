package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const validJSON = `{
  "schema_version": "local-probe.config.v1",
  "roots": [
    {"id": "project", "path": "C:/work/project", "deny_patterns": [".env", "*.key"]},
    {"id": "private", "path": "C:/work/private"}
  ],
  "profiles": [
    {"id": "read-project", "roots": ["project"], "tools": ["read_file", "batch_read"], "deny_patterns": ["secrets/*"]},
    {"id": "read-private", "roots": ["private"], "tools": ["read_file"]}
  ],
  "connections": [
    {"id": "account-a", "label": "A", "profile_id": "read-project", "credential_ref": "cred-a", "enabled": true},
    {"id": "account-b", "label": "B", "profile_id": "read-private", "credential_ref": "cred-b", "enabled": true}
  ],
  "credentials": [
    {"id": "cred-a", "kind": "runtime"},
    {"id": "cred-b", "kind": "runtime"}
  ],
  "environment_tools": [
    {"id": "git", "candidate_files": ["D:/replace/with/an/absolute/path/to/git.exe"], "candidate_dirs": []},
    {"id": "node", "candidate_files": [], "candidate_dirs": ["D:/replace/with/an/absolute/tool-directory"]},
    {"id": "codex", "candidate_files": ["D:/replace/with/an/absolute/path/to/codex.exe"], "candidate_dirs": []}
  ]
}`

func TestParseStrictAndReferences(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion() != SchemaVersionV1 || len(c.Roots()) != 2 || len(c.Profiles()) != 2 || len(c.Connections()) != 2 || len(c.Credentials()) != 2 || len(c.EnvironmentTools()) != 3 {
		t.Fatalf("unexpected parsed config: %+v", c)
	}
	if got := c.EnvironmentTools()[0].CandidateFiles(); !reflect.DeepEqual(got, []string{"D:/replace/with/an/absolute/path/to/git.exe"}) {
		t.Fatalf("git candidate files = %#v", got)
	}
	profile, ok := c.Profile("read-project")
	if !ok || !profile.ReadOnly() || len(profile.RootIDs()) != 1 {
		t.Fatalf("profile lookup failed: %+v", profile)
	}
	for _, input := range []string{
		strings.Replace(validJSON, `"credentials":`, `"unexpected": [], "credentials":`, 1),
		strings.Replace(validJSON, `"credentials":`, `"credentials": [], "CREDENTIALS":`, 1),
		strings.Replace(validJSON, `{"id": "project", "path": "C:/work/project", "deny_patterns": [".env", "*.key"]}`, `{"id": "project", "ID": "project", "path": "C:/work/project", "deny_patterns": [".env", "*.key"]}`, 1),
		strings.Replace(validJSON, `"deny_patterns": [".env", "*.key"]`, `"deny_patterns": ["[broken"]`, 1),
		strings.Replace(validJSON, `"schema_version": "local-probe.config.v1"`, `"schema_version": "local-probe.config.v0"`, 1),
		validJSON + ` {"extra": true}`,
	} {
		if _, err := Parse([]byte(input)); err == nil {
			t.Fatalf("accepted invalid config")
		} else if !errors.Is(err, ErrInvalid) {
			t.Fatalf("wrong error for invalid config: %v", err)
		}
	}
}

func TestWorkspaceRootMetadataIsProfileScopedAndLegacyConfigRemainsActive(t *testing.T) {
	legacy, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := legacy.Profile("read-project")
	if !reflect.DeepEqual(profile.RootIDs(), []string{"project"}) || profile.WorkspaceRoots() != nil {
		t.Fatalf("legacy root scope changed: roots=%v metadata=%v", profile.RootIDs(), profile.WorkspaceRoots())
	}
	encodedLegacy, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encodedLegacy, []byte(`"workspace_roots"`)) {
		t.Fatalf("legacy config unexpectedly gained metadata: %s", encodedLegacy)
	}

	active, err := NewWorkspaceRootMetadata("project", "Project", true)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := NewWorkspaceRootMetadata("private", "Private", false)
	if err != nil {
		t.Fatal(err)
	}
	profileA, err := NewProfileWithWorkspaceRoots("read-project", []string{"project"}, []string{"read_file"}, nil, nil, []WorkspaceRootMetadata{active, paused})
	if err != nil {
		t.Fatal(err)
	}
	profiles := legacy.Profiles()
	profiles[0] = profileA
	next, err := NewWithCommandProfiles(legacy.SchemaVersion(), legacy.Roots(), profiles, legacy.Connections(), legacy.Credentials(), legacy.EnvironmentTools(), legacy.DeveloperMode(), legacy.CommandProfiles())
	if err != nil {
		t.Fatal(err)
	}
	if got := next.Profiles()[0].RootIDs(); !reflect.DeepEqual(got, []string{"project"}) {
		t.Fatalf("paused profile scope = %v, want only project", got)
	}
	if got := next.Profiles()[1].RootIDs(); !reflect.DeepEqual(got, []string{"private"}) {
		t.Fatalf("other profile scope changed = %v", got)
	}
	data, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	parsedA, _ := parsed.Profile("read-project")
	if len(parsedA.WorkspaceRoots()) != 2 || parsedA.WorkspaceRoots()[1].Enabled() {
		t.Fatalf("paused metadata did not round trip: %+v", parsedA.WorkspaceRoots())
	}
}

func TestInvalidWorkspaceRootMetadataFailsClosed(t *testing.T) {
	invalid := []string{
		strings.Replace(validJSON, `{"id": "read-project", "roots": ["project"],`, `{"id": "read-project", "roots": ["project"], "workspace_roots": [{"root_id":"project","display_name":"bad/name","enabled":true}],`, 1),
		strings.Replace(validJSON, `{"id": "read-project", "roots": ["project"],`, `{"id": "read-project", "roots": ["project"], "workspace_roots": [{"root_id":"project","display_name":"","enabled":true}],`, 1),
		strings.Replace(validJSON, `{"id": "read-project", "roots": ["project"],`, `{"id": "read-project", "roots": ["project"], "workspace_roots": [{"root_id":"project","enabled":false}],`, 1),
		strings.Replace(validJSON, `{"id": "read-project", "roots": ["project"],`, `{"id": "read-project", "roots": ["project"], "workspace_roots": [{"root_id":"project","enabled":true},{"root_id":"project","enabled":true}],`, 1),
		strings.Replace(validJSON, `{"id": "read-project", "roots": ["project"],`, `{"id": "read-project", "roots": ["project"], "workspace_roots": [],`, 1),
	}
	for i, input := range invalid {
		if _, err := Parse([]byte(input)); err == nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid metadata case %d accepted or returned wrong error: %v", i, err)
		}
	}
}

func TestEnvironmentToolsValidationAndLegacyCompatibility(t *testing.T) {
	base, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	legacyJSON := strings.Replace(validJSON, `,
  "environment_tools": [
    {"id": "git", "candidate_files": ["D:/replace/with/an/absolute/path/to/git.exe"], "candidate_dirs": []},
    {"id": "node", "candidate_files": [], "candidate_dirs": ["D:/replace/with/an/absolute/tool-directory"]},
    {"id": "codex", "candidate_files": ["D:/replace/with/an/absolute/path/to/codex.exe"], "candidate_dirs": []}
  ]`, "", 1)
	legacy, err := Parse([]byte(legacyJSON))
	if err != nil {
		t.Fatalf("legacy config without environment_tools rejected: %v", err)
	}
	if got := legacy.EnvironmentTools(); len(got) != 0 {
		t.Fatalf("legacy environment_tools = %#v, want empty", got)
	}

	validTool, err := NewEnvironmentTool("git", []string{"relative/tool"}, nil)
	if err != nil {
		t.Fatalf("relative candidate should remain a core/platform concern: %v", err)
	}
	if _, err := NewWithEnvironmentTools(base.SchemaVersion(), base.Roots(), base.Profiles(), base.Connections(), base.Credentials(), []EnvironmentTool{validTool}); err != nil {
		t.Fatalf("valid environment tool rejected: %v", err)
	}

	cases := []struct {
		name  string
		tools []EnvironmentTool
	}{
		{
			name:  "duplicate logical id",
			tools: []EnvironmentTool{mustEnvironmentTool(t, "git"), mustEnvironmentTool(t, "git")},
		},
		{
			name:  "too many tools",
			tools: makeEnvironmentTools(t, MaxEnvironmentTools+1),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWithEnvironmentTools(base.SchemaVersion(), base.Roots(), base.Profiles(), base.Connections(), base.Credentials(), tc.tools); err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid environment tools: %v", err)
			}
		})
	}

	tooManyCandidates := make([]string, MaxEnvironmentCandidatesPerTool+1)
	for i := range tooManyCandidates {
		tooManyCandidates[i] = "D:/replace/candidate-" + strconv.Itoa(i)
	}
	if _, err := NewEnvironmentTool("too_many_candidates", tooManyCandidates, nil); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted too many candidates: %v", err)
	}

	invalidCases := []struct {
		name string
		id   string
		files,
		dirs []string
	}{
		{name: "invalid logical id", id: "工具", files: []string{"D:/replace/tool"}},
		{name: "empty candidates", id: "empty", files: nil, dirs: nil},
		{name: "empty candidate file", id: "empty_file", files: []string{""}},
		{name: "empty candidate directory", id: "empty_dir", dirs: []string{""}},
		{name: "invalid utf8", id: "invalid_utf8", files: []string{string([]byte{0xff})}},
		{name: "nul candidate", id: "nul", files: []string{"D:/private/secret\x00tool"}},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewEnvironmentTool(tc.id, tc.files, tc.dirs)
			if err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid environment tool: %v", err)
			}
			if strings.Contains(err.Error(), "D:/private/secret") {
				t.Fatalf("validation error echoed candidate path: %v", err)
			}
		})
	}

	invalidJSONUTF8 := []byte(validJSON)
	marker := []byte("git.exe")
	markerIndex := bytes.Index(invalidJSONUTF8, marker)
	if markerIndex < 0 {
		t.Fatal("test marker not found")
	}
	invalidJSONUTF8[markerIndex] = 0xff
	if _, err := Parse(invalidJSONUTF8); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted invalid JSON UTF-8: %v", err)
	}
}

func TestEnvironmentToolsAreDeeplyImmutable(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	tools := c.EnvironmentTools()
	files := tools[0].CandidateFiles()
	files[0] = "changed"
	tools[0] = EnvironmentTool{}
	if got := c.EnvironmentTools()[0].CandidateFiles()[0]; got != "D:/replace/with/an/absolute/path/to/git.exe" {
		t.Fatalf("caller mutation changed config candidate: %q", got)
	}

	clone := c.Clone()
	cloneTools := clone.EnvironmentTools()
	cloneFiles := cloneTools[0].CandidateFiles()
	cloneFiles[0] = "changed-clone"
	cloneTools[0] = EnvironmentTool{}
	if got := c.EnvironmentTools()[0].CandidateFiles()[0]; got != "D:/replace/with/an/absolute/path/to/git.exe" {
		t.Fatalf("clone mutation changed original candidate: %q", got)
	}
}

func TestExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "local-probe.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("example config is invalid: %v", err)
	}
	if got := c.EnvironmentTools(); len(got) != 3 {
		t.Fatalf("example environment_tools = %d, want 3", len(got))
	}
	profile, ok := c.Profile("read_only")
	if !ok {
		t.Fatal("example read_only profile is missing")
	}
	for _, want := range []string{"get_environment", "discover_tools"} {
		found := false
		for _, tool := range profile.Tools() {
			if tool == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("example read_only profile does not allow %q", want)
		}
	}
}

func mustEnvironmentTool(t *testing.T, id string) EnvironmentTool {
	t.Helper()
	tool, err := NewEnvironmentTool(id, []string{"D:/replace/with/an/absolute/path/to/" + id + ".exe"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func makeEnvironmentTools(t *testing.T, count int) []EnvironmentTool {
	t.Helper()
	tools := make([]EnvironmentTool, count)
	for i := range tools {
		tools[i] = mustEnvironmentTool(t, "tool-"+strconv.Itoa(i))
	}
	return tools
}

func TestIgnorePatternsAreConfigurableAndPreserved(t *testing.T) {
	data := []byte(`{
  "schema_version": "local-probe.config.v1",
  "roots": [{"id":"project","path":"C:/project","deny_patterns":[".env"],"ignore_patterns":["vendor/**","*.tmp"]}],
  "profiles": [{"id":"read","roots":["project"],"tools":["list_directory"],"deny_patterns":["secrets/**"],"ignore_patterns":["node_modules/**"]}],
  "connections": [{"id":"connection","label":"","profile_id":"read","credential_ref":"credential","enabled":true}],
  "credentials": [{"id":"credential","kind":"local_token"}]
}`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	root, ok := cfg.Root("project")
	if !ok || !reflect.DeepEqual(root.IgnorePatterns(), []string{"vendor/**", "*.tmp"}) {
		t.Fatalf("root ignore patterns = %#v", root.IgnorePatterns())
	}
	profile, ok := cfg.Profile("read")
	if !ok || !reflect.DeepEqual(profile.IgnorePatterns(), []string{"node_modules/**"}) {
		t.Fatalf("profile ignore patterns = %#v", profile.IgnorePatterns())
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"ignore_patterns"`)) {
		t.Fatalf("marshal dropped ignore_patterns: %s", encoded)
	}
}

func TestConfigSizeAndExplicitEnabled(t *testing.T) {
	if _, err := Parse(bytes.Repeat([]byte{' '}, MaxConfigBytes+1)); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted oversized Parse input: %v", err)
	}
	if _, err := Load(bytes.NewReader(bytes.Repeat([]byte{' '}, MaxConfigBytes+1))); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted oversized Load input: %v", err)
	}
	withoutEnabled := strings.Replace(validJSON, `, "enabled": true`, "", 1)
	if _, err := Parse([]byte(withoutEnabled)); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("accepted connection without enabled: %v", err)
	}
}

func TestDuplicateAndDanglingReferencesRejected(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"duplicate root", strings.Replace(validJSON, `{"id": "private", "path": "C:/work/private"}`, `{"id": "project", "path": "C:/work/private"}`, 1)},
		{"duplicate profile", strings.Replace(validJSON, `{"id": "read-private", "roots": ["private"], "tools": ["read_file"]}`, `{"id": "read-project", "roots": ["private"], "tools": ["read_file"]}`, 1)},
		{"duplicate connection", strings.Replace(validJSON, `{"id": "account-b", "label": "B", "profile_id": "read-private", "credential_ref": "cred-b", "enabled": true}`, `{"id": "account-a", "label": "B", "profile_id": "read-private", "credential_ref": "cred-b", "enabled": true}`, 1)},
		{"duplicate credential", strings.Replace(validJSON, `{"id": "cred-b", "kind": "runtime"}`, `{"id": "cred-a", "kind": "runtime"}`, 1)},
		{"unknown root", strings.Replace(validJSON, `"roots": ["project"]`, `"roots": ["missing"]`, 1)},
		{"unknown profile", strings.Replace(validJSON, `"profile_id": "read-project"`, `"profile_id": "missing"`, 1)},
		{"unknown credential", strings.Replace(validJSON, `"credential_ref": "cred-a"`, `"credential_ref": "missing"`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.json)); err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid references: %v", err)
			}
		})
	}
}

func TestConfigAndSnapshotAreDeeplyImmutable(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(c)
	if err != nil {
		t.Fatal(err)
	}
	roots := c.Roots()
	patterns := roots[0].DenyPatterns()
	patterns[0] = "changed"
	roots[0] = Root{}
	profiles := store.Snapshot().Config().Profiles()
	rootIDs := profiles[0].RootIDs()
	rootIDs[0] = "private"
	if got, _ := store.Snapshot().Config().Root("project"); got.ID() != "project" {
		t.Fatal("caller mutation changed stored roots")
	}
	if got, _ := store.Snapshot().Config().Profile("read-project"); got.RootIDs()[0] != "project" || got.DenyPatterns()[0] != "secrets/*" {
		t.Fatal("caller mutation changed stored profile")
	}
}

func TestStoreAtomicRevisionReplacement(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(c)
	if err != nil {
		t.Fatal(err)
	}
	first := store.Snapshot()
	if first.Revision() != "r1" {
		t.Fatalf("unexpected initial revision %q", first.Revision())
	}
	updatedJSON := strings.Replace(validJSON, `"enabled": true`, `"enabled": false`, 1)
	updated, err := Parse([]byte(updatedJSON))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ReplaceIfRevision(first.Revision(), updated)
	if err != nil || second.Revision() != "r2" {
		t.Fatalf("replace failed: %v, %+v", err, second)
	}
	if _, err := store.ReplaceIfRevision(first.Revision(), c); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale replace accepted: %v", err)
	}
	if got, _ := store.Snapshot().Config().Connection("account-a"); got.Enabled() {
		t.Fatal("replacement was not atomic")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = store.Snapshot().Revision()
		}()
	}
	wg.Wait()
}

func TestStoreRevisionLeaseRejectsWrongRevisionWithoutLock(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(c)
	if err != nil {
		t.Fatal(err)
	}
	if release, ok := store.AcquireRevisionLease("r999"); ok || release != nil {
		t.Fatal("wrong revision acquired a lease")
	}
	done := make(chan error, 1)
	go func() {
		_, replaceErr := store.ReplaceIfRevision("r1", c)
		done <- replaceErr
	}()
	select {
	case replaceErr := <-done:
		if replaceErr != nil {
			t.Fatalf("replace after rejected lease failed: %v", replaceErr)
		}
	case <-time.After(time.Second):
		t.Fatal("rejected lease unexpectedly held the store")
	}
}

func TestStoreRevisionLeaseBlocksReplaceUntilIdempotentRelease(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(c)
	if err != nil {
		t.Fatal(err)
	}
	release, ok := store.AcquireRevisionLease("r1")
	if !ok || release == nil {
		t.Fatal("current revision lease was not acquired")
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, replaceErr := store.ReplaceIfRevision("r1", c)
		done <- replaceErr
	}()
	<-started
	select {
	case replaceErr := <-done:
		t.Fatalf("replace completed while revision lease was held: %v", replaceErr)
	case <-time.After(50 * time.Millisecond):
	}
	release()
	release()
	select {
	case replaceErr := <-done:
		if replaceErr != nil {
			t.Fatalf("replace after lease release failed: %v", replaceErr)
		}
	case <-time.After(time.Second):
		t.Fatal("replace remained blocked after idempotent release")
	}
	if got := store.Snapshot().Revision(); got != "r2" {
		t.Fatalf("unexpected revision after released replacement: %s", got)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(b); err != nil {
		t.Fatalf("marshal output is not valid config: %v", err)
	}
}
