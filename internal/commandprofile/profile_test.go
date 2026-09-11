package commandprofile

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func validSpec() Spec {
	return Spec{
		ID:         "codex_version",
		Kind:       KindVersionProbe,
		Platform:   []Platform{PlatformWindows},
		Executable: `C:\Program Files\Codex\codex.exe`,
		Identity:   IdentitySpec{RequireRegular: true, RejectReparse: true},
		Variants: []VariantSpec{
			{ID: "short", Exact: []string{"-v"}},
			{ID: "long", Exact: []string{"--version"}},
		},
		CWD:          CWDSpec{Kind: CWDPrivateEmpty},
		Environment:  EnvironmentSpec{Inherit: false},
		Limits:       LimitsSpec{WallTimeout: 2 * time.Second, StdoutBytes: 8192, StderrBytes: 4096, MaxProcesses: 1, MaxChildren: 0},
		Network:      NetworkSpec{Mode: NetworkDeny, RequireEnforcement: true},
		Confirmation: ConfirmationSpec{Mode: ConfirmationPerCall, LocalOnly: true},
		Result:       ResultSpec{Type: ResultVersion, ReturnRawOutput: false},
	}
}

func TestNewVersionProbeIsImmutableAndExact(t *testing.T) {
	spec := validSpec()
	profile, err := New(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Variants[0].Exact[0] = "changed"
	variant, ok := profile.Variant("short")
	if !ok || !reflect.DeepEqual(variant.Exact(), []string{"-v"}) {
		t.Fatalf("variant was not copied: %#v", variant.Exact())
	}
	variants := profile.Variants()
	variants[0].Exact()[0] = "caller mutation does not alter copy"
	if got, _ := profile.Variant("short"); !reflect.DeepEqual(got.Exact(), []string{"-v"}) {
		t.Fatalf("variant accessor leaked mutable data: %#v", got.Exact())
	}
	if profile.Executable() != spec.Executable || profile.Kind() != KindVersionProbe {
		t.Fatalf("unexpected profile metadata: %#v", profile)
	}
}

func TestVersionProbeRejectsUnsafeShapes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"relative executable", func(s *Spec) { s.Executable = `codex.exe` }},
		{"wrapper executable", func(s *Spec) { s.Executable = `C:\tools\codex.cmd` }},
		{"alternate data stream", func(s *Spec) { s.Executable = `C:\tools\codex.exe:stream` }},
		{"parent path", func(s *Spec) { s.Executable = `C:\tools\..\codex.exe` }},
		{"non-windows platform", func(s *Spec) { s.Platform = []Platform{"linux"} }},
		{"reparse allowed", func(s *Spec) { s.Identity.RejectReparse = false }},
		{"custom env", func(s *Spec) { s.Environment.Fixed = map[string]string{"PATH": "C:\\Windows"} }},
		{"inherit env", func(s *Spec) { s.Environment.Inherit = true }},
		{"free args", func(s *Spec) { s.Variants[0].Exact = nil }},
		{"children", func(s *Spec) { s.Limits.MaxChildren = 1 }},
		{"network not verified", func(s *Spec) { s.Network.RequireEnforcement = false }},
		{"raw output", func(s *Spec) { s.Result.ReturnRawOutput = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			tc.mutate(&spec)
			if _, err := New(spec); err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted unsafe profile: %v", err)
			}
		})
	}
}

func TestDeveloperModeAndNetworkAdmission(t *testing.T) {
	profile, err := New(validSpec())
	if err != nil {
		t.Fatal(err)
	}
	mode, err := NewDeveloperMode(true, []string{"connection-a"}, ConfirmationPerCall, NetworkDeny)
	if err != nil {
		t.Fatal(err)
	}
	if err := profile.Admit(mode, "connection-a", EnforcementCapability{}); !errors.Is(err, ErrNetworkEnforcementRequired) {
		t.Fatalf("unverified network admitted: %v", err)
	}
	if err := profile.Admit(mode, "connection-b", testEnforcementCapability()); !errors.Is(err, ErrConnectionNotAllowed) {
		t.Fatalf("unallowed connection admitted: %v", err)
	}
	if err := profile.Admit(mode, "connection-a", testEnforcementCapability()); err != nil {
		t.Fatalf("verified local enforcement rejected: %v", err)
	}
	disabled := DefaultDeveloperMode()
	if err := profile.Admit(disabled, "connection-a", testEnforcementCapability()); !errors.Is(err, ErrDeveloperModeDisabled) {
		t.Fatalf("disabled developer mode admitted: %v", err)
	}
	if _, err := json.Marshal(testEnforcementCapability()); err == nil {
		t.Fatal("enforcement capability was serializable")
	}
}

func testEnforcementCapability() EnforcementCapability {
	return EnforcementCapability{marker: &struct{}{}}
}

func TestDeveloperModeValidation(t *testing.T) {
	if _, err := NewDeveloperMode(true, []string{"connection-a", "connection-a"}, ConfirmationPerCall, NetworkDeny); err == nil {
		t.Fatal("duplicate allowed connection accepted")
	}
	if _, err := NewDeveloperMode(true, []string{"connection-a"}, "always", NetworkDeny); err == nil {
		t.Fatal("non per-call confirmation accepted")
	}
	if _, err := NewDeveloperMode(true, []string{"connection-a"}, ConfirmationPerCall, "allow"); err == nil {
		t.Fatal("non-deny network default accepted")
	}
}
