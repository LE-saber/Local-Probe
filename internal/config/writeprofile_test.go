package config

import (
	"strings"
	"testing"
)

func TestExplicitWriteProfileRequiresV2AndSurvivesRoundTrip(t *testing.T) {
	data := strings.Replace(validJSON, `"id": "read-project",`, `"id": "read-project", "read_only": false,`, 1)
	if _, err := Parse([]byte(data)); err == nil {
		t.Fatal("v1 silently accepted write permission")
	}
	data = strings.Replace(data, SchemaVersionV1, SchemaVersionV2, 1)
	cfg, err := Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Profile("read-project")
	if !ok || p.ReadOnly() {
		t.Fatal("explicit write profile was lost")
	}
	b, err := cfg.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	p, _ = again.Profile("read-project")
	if p.ReadOnly() {
		t.Fatal("write permission was not preserved")
	}
	readonly, _ := again.Profile("read-private")
	if !readonly.ReadOnly() {
		t.Fatal("unrelated default profile became writable")
	}
}
