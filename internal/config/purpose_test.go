package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPurposeMigrationDoesNotGrantCapabilities(t *testing.T) {
	legacy, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	conn, _ := legacy.Connection("account-a")
	if conn.Purpose() != PurposeWebLocal {
		t.Fatal("legacy purpose changed")
	}
	if conn.ExecutionPolicy() != ExecutionIsolatedRequired {
		t.Fatal("legacy execution policy changed")
	}
	data := strings.Replace(validJSON, SchemaVersionV1, SchemaVersionV2, 1)
	data = strings.Replace(data, `"label": "A"`, `"label": "A", "purpose": "agent_remote"`, 1)
	cfg, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	remote, _ := cfg.Connection("account-a")
	if remote.Purpose() != PurposeAgentRemote || remote.ProfileID() != conn.ProfileID() || remote.CredentialRef() != conn.CredentialRef() || remote.Enabled() != conn.Enabled() {
		t.Fatal("migration changed scope")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	remote, _ = again.Connection("account-a")
	if remote.Purpose() != PurposeAgentRemote {
		t.Fatal("purpose lost on roundtrip")
	}
	for _, bad := range []string{
		strings.Replace(data, SchemaVersionV2, SchemaVersionV1, 1),
		strings.Replace(data, "agent_remote", "execute_everything", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
}

func TestTrustedExecutionPolicyRequiresExplicitV2(t *testing.T) {
	data := strings.Replace(validJSON, `"label": "A"`, `"label": "A", "execution_policy": "trusted_host"`, 1)
	if _, err := Parse([]byte(data)); err == nil {
		t.Fatal("v1 accepted execution policy")
	}
	data = strings.Replace(data, SchemaVersionV1, SchemaVersionV2, 1)
	cfg, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := cfg.Connection("account-a")
	if c.ExecutionPolicy() != ExecutionTrustedHost {
		t.Fatal("explicit policy lost")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err = Parse([]byte(strings.Replace(data, "trusted_host", "unrestricted", 1))); err == nil {
		t.Fatal("unknown execution policy accepted")
	}
}
