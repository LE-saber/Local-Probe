package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
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
    "identity": {"require_regular": true, "reject_reparse": true, "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
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

func TestVersionProbeEmptySlotsAndTemplateRemainCompatible(t *testing.T) {
	withEmptySlots := strings.Replace(string(commandProfileJSON()), `"variants": [`, `"slots": [], "variants": [`, 1)
	if _, err := Parse([]byte(withEmptySlots)); err != nil {
		t.Fatalf("version_probe with empty slots was rejected: %v", err)
	}
	withEmptyTemplate := strings.Replace(string(commandProfileJSON()), `{"variant_id": "short", "exact": ["-v"]}`, `{"variant_id": "short", "exact": ["-v"], "template": []}`, 1)
	if _, err := Parse([]byte(withEmptyTemplate)); err != nil {
		t.Fatalf("version_probe with empty template was rejected: %v", err)
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
			return strings.Replace(value, `"variants": [`, `"slots": [{"slot_id":"extra","kind":"enum","enum_values":["x"]}], "variants": [`, 1)
		}},
		{"custom environment", func(value string) string {
			return strings.Replace(value, `"fixed": {}`, `"fixed": {"PATH": "C:\\\\Windows"}`, 1)
		}},
		{"wrapper executable", func(value string) string {
			return strings.Replace(value, `C:\\Program Files\\Codex\\codex.exe`, `C:\\Program Files\\Codex\\codex.cmd`, 1)
		}},
		{"missing executable sha256", func(value string) string {
			return strings.Replace(value, `"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"sha256": ""`, 1)
		}},
		{"uppercase executable sha256", func(value string) string {
			return strings.Replace(value, `"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"sha256": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`, 1)
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

func fixedCommandProfileJSON() []byte {
	base := strings.TrimSpace(validJSON)
	base = strings.TrimSuffix(base, "}")
	return []byte(base + `,
  "command_profiles": [{
    "id": "tool_command",
    "kind": "fixed_command",
    "platform": ["windows"],
    "executable": "C:\\Tools\\tool.exe",
    "identity": {"require_regular": true, "reject_reparse": true, "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
    "argv": {
      "slots": [
        {"slot_id": "format", "kind": "enum", "enum_values": ["json", "text"]},
        {"slot_id": "count", "kind": "bounded_integer", "min": 1, "max": 4},
        {"slot_id": "file", "kind": "root_relative_path", "root_ids": ["project", "private"]}
      ],
      "variants": [{"variant_id": "run", "template": [
        {"literal": "--format"}, {"slot_id": "format"},
        {"literal": "--count"}, {"slot_id": "count"}, {"slot_id": "file"}
      ]}]
    },
    "cwd": {"kind": "private_empty"},
    "env": {"inherit": false, "allow": [], "fixed": {}},
    "limits": {"wall_timeout_ms": 2000, "stdout_bytes": 8192, "stderr_bytes": 4096, "max_processes": 1, "max_children": 0},
    "network": {"mode": "deny", "require_enforcement": true},
    "confirmation": {"mode": "per_call", "local_only": true},
    "result": {"type": "exit_status", "return_raw_output": false}
  }]
}`)
}

func TestParseFixedCommandSlotsRoundTripAndRootCrossValidation(t *testing.T) {
	c, err := Parse(fixedCommandProfileJSON())
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := c.CommandProfile("tool_command")
	if !ok || profile.Kind() != commandprofile.KindFixedCommand || len(profile.Slots()) != 3 {
		t.Fatalf("unexpected fixed command profile: %#v", profile)
	}
	variant, ok := profile.Variant("run")
	if !ok || len(variant.Template()) != 5 || variant.Template()[1].SlotID != "format" {
		t.Fatalf("unexpected fixed command template: %#v", variant)
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded); err != nil {
		t.Fatalf("fixed command marshal roundtrip failed: %v", err)
	}
	unknownRoot := strings.Replace(string(fixedCommandProfileJSON()), `"root_ids": ["project", "private"]`, `"root_ids": ["missing"]`, 1)
	if _, err := Parse([]byte(unknownRoot)); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown root id accepted: %v", err)
	}
	unknownField := strings.Replace(string(fixedCommandProfileJSON()), `{"literal": "--format"}`, `{"literal": "--format", "unexpected": true}`, 1)
	if _, err := Parse([]byte(unknownField)); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown template field accepted: %v", err)
	}
}

func TestFixedCommandSlotsRemainImmutableAfterConfigCopies(t *testing.T) {
	c, err := Parse(fixedCommandProfileJSON())
	if err != nil {
		t.Fatal(err)
	}
	profiles := c.CommandProfiles()
	slots := profiles[0].Slots()
	enumValues := slots[0].EnumValues()
	rootIDs := slots[2].RootIDs()
	enumValues[0] = "changed"
	rootIDs[0] = "changed"
	if got := c.CommandProfiles()[0].Slots(); got[0].EnumValues()[0] != "json" || got[2].RootIDs()[0] != "project" {
		t.Fatalf("config slot accessors leaked mutable data: %#v", got)
	}
}
