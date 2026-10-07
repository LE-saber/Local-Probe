package commandprofile

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func validSpec() Spec {
	return Spec{
		ID:         "codex_version",
		Kind:       KindVersionProbe,
		Platform:   []Platform{PlatformWindows},
		Executable: `C:\Program Files\Codex\codex.exe`,
		Identity:   IdentitySpec{RequireRegular: true, RejectReparse: true, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
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
		{"missing sha256", func(s *Spec) { s.Identity.SHA256 = "" }},
		{"uppercase sha256", func(s *Spec) { s.Identity.SHA256 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" }},
		{"custom env", func(s *Spec) { s.Environment.Fixed = map[string]string{"PATH": "C:\\Windows"} }},
		{"inherit env", func(s *Spec) { s.Environment.Inherit = true }},
		{"free args", func(s *Spec) { s.Variants[0].Exact = nil }},
		{"unsafe version args", func(s *Spec) { s.Variants[0].Exact = []string{"-c", "side-effect"} }},
		{"unknown version flag", func(s *Spec) { s.Variants[0].Exact = []string{"/run"} }},
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

func fixedCommandSpec() Spec {
	return Spec{
		ID: "fixed_command", Kind: KindFixedCommand, Platform: []Platform{PlatformWindows},
		Executable: `C:\Tools\tool.exe`,
		Identity:   IdentitySpec{RequireRegular: true, RejectReparse: true, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		Slots: []SlotSpec{
			{ID: "format", Kind: SlotEnum, EnumValues: []string{"json", "text"}},
			{ID: "count", Kind: SlotBoundedInteger, Min: 1, Max: 10},
			{ID: "file", Kind: SlotRootRelativePath, RootIDs: []string{"project", "private"}},
		},
		Variants: []VariantSpec{{ID: "run", Template: []TemplateItemSpec{
			{Literal: "--format"}, {SlotID: "format"}, {Literal: "--count"}, {SlotID: "count"}, {SlotID: "file"},
		}}},
		CWD: CWDSpec{Kind: CWDPrivateEmpty}, Environment: EnvironmentSpec{Inherit: false},
		Limits:       LimitsSpec{WallTimeout: time.Second, StdoutBytes: 4096, StderrBytes: 4096, MaxProcesses: 1, MaxChildren: 0},
		Network:      NetworkSpec{Mode: NetworkDeny, RequireEnforcement: true},
		Confirmation: ConfirmationSpec{Mode: ConfirmationPerCall, LocalOnly: true},
		Result:       ResultSpec{Type: ResultExitStatus},
	}
}

func TestFixedCommandResolveVariantUsesTypedSlotsInOrder(t *testing.T) {
	spec := fixedCommandSpec()
	profile, err := New(spec)
	if err != nil {
		t.Fatal(err)
	}
	resolverCalls := 0
	argv, err := profile.ResolveVariant("run", map[string]SlotValue{
		"format": {Text: "json"}, "count": {Integer: 3}, "file": {RootID: "project", RelativePath: "src/main.go"},
	}, func(rootID, relative string) (string, error) {
		resolverCalls++
		return `C:\work\` + rootID + `\` + relative, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--format", "json", "--count", "3", `C:\work\project\src/main.go`}
	if !reflect.DeepEqual(argv, want) || resolverCalls != 1 {
		t.Fatalf("argv = %#v, resolver calls = %d, want %#v and 1", argv, resolverCalls, want)
	}
	argv[0] = "changed"
	again, err := profile.ResolveVariant("run", map[string]SlotValue{
		"format": {Text: "text"}, "count": {Integer: 1}, "file": {RootID: "private", RelativePath: "x.txt"},
	}, func(string, string) (string, error) { return "resolved", nil })
	if err != nil || again[0] != "--format" {
		t.Fatalf("resolved argv leaked mutable state: %#v, %v", again, err)
	}
}

func TestFixedCommandTemplateMayReuseASlot(t *testing.T) {
	spec := fixedCommandSpec()
	spec.Variants[0].Template = append(spec.Variants[0].Template, TemplateItemSpec{SlotID: "format"})
	profile, err := New(spec)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := profile.ResolveVariant("run", map[string]SlotValue{
		"format": {Text: "json"}, "count": {Integer: 2}, "file": {RootID: "project", RelativePath: "x.txt"},
	}, func(string, string) (string, error) { return "resolved", nil })
	if err != nil {
		t.Fatal(err)
	}
	if got := argv[len(argv)-1]; got != "json" {
		t.Fatalf("reused slot result = %q, want json; argv=%#v", got, argv)
	}
}

func TestFixedCommandReusedPathSlotIsResolvedOnce(t *testing.T) {
	spec := fixedCommandSpec()
	spec.Variants[0].Template = append(spec.Variants[0].Template, TemplateItemSpec{SlotID: "file"})
	profile, err := New(spec)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	argv, err := profile.ResolveVariant("run", map[string]SlotValue{
		"format": {Text: "json"}, "count": {Integer: 2}, "file": {RootID: "project", RelativePath: "x.txt"},
	}, func(string, string) (string, error) {
		calls++
		return `C:\resolved\x.txt`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || argv[len(argv)-1] != argv[len(argv)-2] {
		t.Fatalf("reused path resolved calls=%d argv=%#v, want one call and equal tokens", calls, argv)
	}
}

func TestFixedCommandExactAndVersionCompatibility(t *testing.T) {
	spec := fixedCommandSpec()
	spec.Slots = nil
	spec.Variants = []VariantSpec{{ID: "exact", Exact: []string{"--status", "ready"}}}
	profile, err := New(spec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := profile.ResolveVariant("exact", map[string]SlotValue{}, nil)
	if err != nil || !reflect.DeepEqual(got, []string{"--status", "ready"}) {
		t.Fatalf("fixed exact resolve = %#v, %v", got, err)
	}
	legacy, err := New(validSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ResolveVariant("short", map[string]SlotValue{}, nil); err != nil {
		t.Fatalf("version exact compatibility failed: %v", err)
	}
}

func TestFixedCommandRejectsInvalidSlotInputs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Spec)
	}{
		{"duplicate slot", func(s *Spec) { s.Slots = append(s.Slots, s.Slots[0]) }},
		{"duplicate enum", func(s *Spec) { s.Slots[0].EnumValues = []string{"json", "json"} }},
		{"undeclared reference", func(s *Spec) { s.Variants[0].Template[1] = TemplateItemSpec{SlotID: "missing"} }},
		{"unreferenced slot", func(s *Spec) {
			s.Slots = append(s.Slots, SlotSpec{ID: "unused", Kind: SlotEnum, EnumValues: []string{"x"}})
		}},
		{"ambiguous template item", func(s *Spec) { s.Variants[0].Template[0] = TemplateItemSpec{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := fixedCommandSpec()
			tc.mutate(&spec)
			if _, err := New(spec); err == nil || !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted invalid slot shape: %v", err)
			}
		})
	}
	profile, err := New(fixedCommandSpec())
	if err != nil {
		t.Fatal(err)
	}
	validValues := map[string]SlotValue{
		"format": {Text: "json"}, "count": {Integer: 3}, "file": {RootID: "project", RelativePath: "ok.txt"},
	}
	for name, values := range map[string]map[string]SlotValue{
		"missing": {"format": validValues["format"], "count": validValues["count"]},
		"extra":   {"format": validValues["format"], "count": validValues["count"], "file": validValues["file"], "extra": {Text: "x"}},
	} {
		if _, err := profile.ResolveVariant("run", values, func(string, string) (string, error) { return "ok", nil }); err == nil {
			t.Fatalf("accepted %s slot values", name)
		}
	}
	invalidPaths := []string{"/absolute", `dir\\file`, "dir//file", "dir/../file", "dir/file.", "dir/file ", "dir/*.txt", "C:drive", "CON.txt", "prn", "AUX.log", "NUL", "CLOCK$.tmp", "COM1.txt", "COM¹.txt", "COM²", "COM³.log", "LPT9", "LPT¹.txt", "LPT²", "LPT³.log", "dir<file", "dir>file", "dir\"file", "dir|file"}
	for _, relative := range invalidPaths {
		values := map[string]SlotValue{"format": validValues["format"], "count": validValues["count"], "file": {RootID: "project", RelativePath: relative}}
		if _, err := profile.ResolveVariant("run", values, func(string, string) (string, error) { return "ok", nil }); err == nil {
			t.Fatalf("accepted invalid relative path %q", relative)
		}
	}
	if _, err := profile.ResolveVariant("run", validValues, nil); !errors.Is(err, ErrPathResolverRequired) {
		t.Fatalf("missing path resolver = %v", err)
	}
	if _, err := profile.ResolveVariant("run", validValues, func(string, string) (string, error) { return "", errors.New("denied") }); !errors.Is(err, ErrPathResolution) {
		t.Fatalf("resolver denial = %v", err)
	}
	mixed := []map[string]SlotValue{
		{"format": {Text: "json", Integer: 1}, "count": validValues["count"], "file": validValues["file"]},
		{"format": validValues["format"], "count": {Text: "3"}, "file": validValues["file"]},
		{"format": validValues["format"], "count": validValues["count"], "file": {Text: "x", RootID: "project", RelativePath: "x.txt"}},
	}
	for _, values := range mixed {
		if _, err := profile.ResolveVariant("run", values, func(string, string) (string, error) { return "ok", nil }); err == nil {
			t.Fatalf("accepted mixed typed slot fields: %#v", values)
		}
	}
	longResolved := strings.Repeat("a", 30000)
	values := map[string]SlotValue{"format": validValues["format"], "count": validValues["count"], "file": validValues["file"]}
	longArgv, longErr := profile.ResolveVariant("run", values, func(string, string) (string, error) { return longResolved, nil })
	if longErr != nil || len(longArgv[len(longArgv)-1]) != len(longResolved) {
		t.Fatalf("long resolved path rejected: len=%d err=%v", len(longArgv), longErr)
	}
	tooLong := strings.Repeat("a", 32768)
	if _, err := profile.ResolveVariant("run", values, func(string, string) (string, error) { return tooLong, nil }); err == nil {
		t.Fatal("oversized resolver path accepted")
	}
	tooLongRelative := strings.Repeat("a", 4097)
	values["file"] = SlotValue{RootID: "project", RelativePath: tooLongRelative}
	if _, err := profile.ResolveVariant("run", values, func(string, string) (string, error) { return "ok", nil }); err == nil {
		t.Fatal("oversized relative path accepted")
	}
}

func TestFixedCommandSlotDataIsDeeplyImmutable(t *testing.T) {
	spec := fixedCommandSpec()
	profile, err := New(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Slots[0].EnumValues[0] = "changed"
	spec.Slots[2].RootIDs[0] = "changed"
	slots := profile.Slots()
	slots[0].EnumValues()[0] = "changed-accessor"
	slots[2].RootIDs()[0] = "changed-accessor"
	got := profile.Slots()
	if got[0].EnumValues()[0] != "json" || got[2].RootIDs()[0] != "project" {
		t.Fatalf("slot data was not deeply copied: %#v", got)
	}
}

func TestResolvedInputDigestIsDeterministicAndImmutable(t *testing.T) {
	profile, err := New(validSpec())
	if err != nil {
		t.Fatal(err)
	}
	first, err := profile.ResolveInput("short", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := profile.ResolveInput("short", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Digest()) != 64 || first.Digest() != strings.ToLower(first.Digest()) || first.Digest() != second.Digest() {
		t.Fatalf("unexpected deterministic digest: %q vs %q", first.Digest(), second.Digest())
	}
	argv := first.Argv()
	argv[0] = "changed"
	if got := first.Argv(); !reflect.DeepEqual(got, []string{"-v"}) {
		t.Fatalf("resolved input argv leaked mutable state: %#v", got)
	}

	changedVariant := validSpec()
	changedVariant.Variants[0].Exact = []string{"--version"}
	other, err := New(changedVariant)
	if err != nil {
		t.Fatal(err)
	}
	changedInput, err := other.ResolveInput("short", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedInput.Digest() == first.Digest() {
		t.Fatal("argv token change did not change resolved input digest")
	}

	changedVariantID := validSpec()
	changedVariantID.Variants[0].ID = "short_alt"
	other, err = New(changedVariantID)
	if err != nil {
		t.Fatal(err)
	}
	changedInput, err = other.ResolveInput("short_alt", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedInput.Digest() == first.Digest() {
		t.Fatal("variant id change did not change resolved input digest")
	}

	changedIdentity := validSpec()
	changedIdentity.Identity.SHA256 = strings.Repeat("b", 64)
	other, err = New(changedIdentity)
	if err != nil {
		t.Fatal(err)
	}
	changedInput, err = other.ResolveInput("short", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedInput.Digest() == first.Digest() {
		t.Fatal("identity change did not change resolved input digest")
	}

	changedProfile := validSpec()
	changedProfile.ID = "other_version"
	other, err = New(changedProfile)
	if err != nil {
		t.Fatal(err)
	}
	changedInput, err = other.ResolveInput("short", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedInput.Digest() == first.Digest() {
		t.Fatal("profile id change did not change resolved input digest")
	}

	changedKind := validSpec()
	changedKind.Kind = KindFixedCommand
	changedKind.Result = ResultSpec{Type: ResultExitStatus}
	other, err = New(changedKind)
	if err != nil {
		t.Fatal(err)
	}
	changedInput, err = other.ResolveInput("short", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changedInput.Digest() == first.Digest() {
		t.Fatal("profile kind change did not change resolved input digest")
	}
}

func TestResolvedInputDigestSeparatesArgvBoundariesAndMetadata(t *testing.T) {
	base := fixedCommandSpec()
	base.Slots = nil
	base.Variants = []VariantSpec{{ID: "same", Exact: []string{"ab", "c"}}}
	profile, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	first, err := profile.ResolveInput("same", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base.Variants[0].Exact = []string{"a", "bc"}
	other, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := other.ResolveInput("same", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() == second.Digest() {
		t.Fatal("argv token boundary change did not change digest")
	}
	base.Variants[0].Exact = []string{"ab"}
	other, err = New(base)
	if err != nil {
		t.Fatal(err)
	}
	third, err := other.ResolveInput("same", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() == third.Digest() {
		t.Fatal("argc change did not change digest")
	}
}

func TestResolveVariantRemainsCompatibilityWrapper(t *testing.T) {
	profile, err := New(validSpec())
	if err != nil {
		t.Fatal(err)
	}
	input, err := profile.ResolveInput("long", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	argv, err := profile.ResolveVariant("long", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(argv, input.Argv()) {
		t.Fatalf("compatibility wrapper argv = %#v, resolved input = %#v", argv, input.Argv())
	}
}
