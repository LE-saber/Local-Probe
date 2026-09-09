package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
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
  ]
}`

func TestParseStrictAndReferences(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion() != SchemaVersionV1 || len(c.Roots()) != 2 || len(c.Profiles()) != 2 || len(c.Connections()) != 2 || len(c.Credentials()) != 2 {
		t.Fatalf("unexpected parsed config: %+v", c)
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
