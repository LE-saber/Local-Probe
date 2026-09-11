package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func commandProfileJSON() []byte {
	base := strings.TrimSpace(validJSON)
	base = strings.TrimSuffix(base, "}")
	return []byte(base + `,
  "developer_mode": {
    "enabled": true,
    "allowed_connections": ["account-a"],
    "default_confirmation": "per_call",
    "network_default": "deny"
  },
  "command_profiles": [{
    "id": "codex_version",
    "kind": "version_probe",
    "platform": ["windows"],
    "executable": "C:\\Program Files\\Codex\\codex.exe",
    "identity": {"require_regular": true, "reject_reparse": true, "sha256": ""},
    "argv": {"variants": [
      {"variant_id": "short", "exact": ["-v"]},
      {"variant_id": "long", "exact": ["--version"]}
    ]},
    "cwd": {"kind": "private_empty"},
    "env": {"inherit": false, "allow": [], "fixed": {}},
    "limits": {"wall_timeout_ms": 2000, "stdout_bytes": 8192, "stderr_bytes": 4096, "max_processes": 1, "max_children": 0},
    "network": {"mode": "deny", "require_enforcement": true},
    "confirmation": {"mode": "per_call", "local_only": true},
    "result": {"type": "version", "return_raw_output": false}
  }]
}`)
}

func TestParseCommandProfilesAndDeveloperMode(t *testing.T) {
	c, err := Parse(commandProfileJSON())
	if err != nil {
		t.Fatal(err)
	}
	mode := c.DeveloperMode()
	if !mode.Enabled() || !mode.AllowsConnection("account-a") || mode.AllowsConnection("account-b") {
		t.Fatalf("unexpected developer mode: %#v", mode)
	}
	profile, ok := c.CommandProfile("codex_version")
	if !ok || profile.Kind() != "version_probe" || profile.Executable() != `C:\Program Files\Codex\codex.exe` {
		t.Fatalf("unexpected command profile: %#v", profile)
	}
	if _, ok := profile.Variant("short"); !ok {
		t.Fatal("short variant missing")
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded); err != nil {
		t.Fatalf("marshal roundtrip failed: %v", err)
	}
}

func TestDeveloperModeDefaultsClosed(t *testing.T) {
	c, err := Parse([]byte(validJSON))
	if err != nil {
		t.Fatal(err)
	}
	mode := c.DeveloperMode()
	if mode.Enabled() || mode.AllowsConnection("account-a") {
		t.Fatal("developer mode defaulted open")
	}
}

func TestCommandProfileConfigRejectsSelfCertificationAndEscapeHatches(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
	}{
		{"network not enforced", func(value string) string {
			return strings.Replace(value, `"require_enforcement": true`, `"require_enforcement": false`, 1)
		}},
		{"model confirmation field", func(value string) string {
			return strings.Replace(value, `"local_only": true`, `"local_only": true, "confirmed": true`, 1)
		}},
		{"argv slots", func(value string) string {
			return strings.Replace(value, `"variants": [`, `"slots": [], "variants": [`, 1)
		}},
		{"custom environment", func(value string) string {
			return strings.Replace(value, `"fixed": {}`, `"fixed": {"PATH": "C:\\\\Windows"}`, 1)
		}},
		{"wrapper executable", func(value string) string {
			return strings.Replace(value, `C:\\Program Files\\Codex\\codex.exe`, `C:\\Program Files\\Codex\\codex.cmd`, 1)
		}},
		{"verified field", func(value string) string {
			return strings.Replace(value, `"mode": "deny", "require_enforcement": true`, `"mode": "deny", "require_enforcement": true, "verified": true`, 1)
		}},
		{"unknown connection", func(value string) string {
			return strings.Replace(value, `"allowed_connections": ["account-a"]`, `"allowed_connections": ["missing"]`, 1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.edit(string(commandProfileJSON())))); err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("unsafe command profile accepted: %v", err)
			}
		})
	}
}

func TestCommandProfileAccessorsAreImmutable(t *testing.T) {
	c, err := Parse(commandProfileJSON())
	if err != nil {
		t.Fatal(err)
	}
	profiles := c.CommandProfiles()
	variants := profiles[0].Variants()
	args := variants[0].Exact()
	args[0] = "changed"
	allowed := c.DeveloperMode().AllowedConnections()
	allowed[0] = "changed"
	profile, _ := c.CommandProfile("codex_version")
	variant, _ := profile.Variant("short")
	if variant.Exact()[0] != "-v" || c.DeveloperMode().AllowedConnections()[0] != "account-a" {
		t.Fatal("command profile or developer mode leaked mutable state")
	}
}
