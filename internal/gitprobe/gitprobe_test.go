package gitprobe

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestPlanStatusUsesOnlyFixedArgumentsAndEnvironment(t *testing.T) {
	plan, err := PlanAction(Request{Action: ActionStatus, RootID: "repo"})
	if err != nil {
		t.Fatalf("PlanAction: %v", err)
	}
	if plan.PreviewExecutableID() != "git" || plan.RootID() != "repo" || plan.Action() != ActionStatus {
		t.Fatalf("plan identity = %#v", plan)
	}
	if plan.PreviewExecutable() || plan.PreviewBlockedReason() != BlockedByRepositoryFilters {
		t.Fatalf("Git plan unexpectedly executable: executable=%v reason=%q", plan.PreviewExecutable(), plan.PreviewBlockedReason())
	}
	args := plan.PreviewArgs()
	wantSuffix := []string{"status", "--porcelain=v1", "--branch", "--untracked-files=all", "--no-renames", "-z", "--"}
	if len(args) < len(wantSuffix) || !reflect.DeepEqual(args[len(args)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("status args = %#v", args)
	}
	if !contains(args, "--no-pager") || !contains(args, "--no-optional-locks") || !contains(args, "--literal-pathspecs") || !contains(args, "core.fsmonitor=") || !containsPrefix(args, "core.hooksPath=") || !contains(args, "core.quotePath=false") || !contains(args, "diff.external=") {
		t.Fatalf("fixed safety arguments missing: %#v", args)
	}
	env := plan.PreviewEnvironment()
	for _, name := range []string{"GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_COUNT", "GIT_OPTIONAL_LOCKS", "GIT_TERMINAL_PROMPT", "GIT_PAGER", "GIT_EXTERNAL_DIFF", "GIT_DIFF_OPTS", "GIT_ATTR_NOSYSTEM"} {
		if !containsEnv(env, name) {
			t.Fatalf("fixed environment missing %q: %#v", name, env)
		}
	}
	if value := envValue(env, "GIT_CONFIG_NOSYSTEM"); value != "1" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM = %q", value)
	}
	if value := envValue(env, "GIT_CONFIG_COUNT"); value != "0" {
		t.Fatalf("GIT_CONFIG_COUNT = %q", value)
	}
	if value := envValue(env, "GIT_EXTERNAL_DIFF"); value != "" {
		t.Fatalf("GIT_EXTERNAL_DIFF = %q", value)
	}
}

func TestPlanDiffTerminatesOptionsBeforeLiteralPaths(t *testing.T) {
	paths := []string{"-leading.txt", "目录/带 空格.txt"}
	plan, err := PlanAction(Request{Action: ActionDiff, RootID: "repo", Paths: paths})
	if err != nil {
		t.Fatalf("PlanAction: %v", err)
	}
	args := plan.PreviewArgs()
	separator := indexOf(args, "--")
	if separator < 0 || separator == len(args)-1 {
		t.Fatalf("diff args have no usable terminator: %#v", args)
	}
	if !reflect.DeepEqual(args[separator+1:], paths) {
		t.Fatalf("paths escaped or reordered: %#v", args[separator+1:])
	}
	if indexOf(args[:separator], "--no-ext-diff") < 0 || indexOf(args[:separator], "--no-textconv") < 0 || indexOf(args[:separator], "--no-color") < 0 || indexOf(args[:separator], "--no-renames") < 0 || indexOf(args[:separator], "--no-prefix") < 0 {
		t.Fatalf("diff safety arguments missing: %#v", args)
	}
	args[separator+1] = "mutated"
	if plan.PreviewArgs()[separator+1] != paths[0] {
		t.Fatal("Plan.PreviewArgs did not return a defensive copy")
	}
}

func TestPlanRejectsRawLikeAndUnsafeInputs(t *testing.T) {
	tests := []string{
		"",
		"/absolute",
		`\\server\\share`,
		`dir\\file`,
		"C:drive",
		"dir/../file",
		"dir//file",
		".",
		"..",
		"dir/.",
		"dir/..",
		"dir/*",
		"dir/?",
		"dir/[x]",
		":(exclude)secret",
		"CON.txt",
		"dir/trailing.",
		"dir/trailing ",
		"dir\x00file",
		"dir\nfile",
	}
	for _, path := range tests {
		t.Run(strings.ReplaceAll(path, "\\", "slash"), func(t *testing.T) {
			_, err := PlanAction(Request{Action: ActionDiff, RootID: "repo", Paths: []string{path}})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("path %q error = %v, want invalid_input", path, err)
			}
		})
	}
	invalidUTF8 := string([]byte{'d', '/', 0xff})
	_, err := PlanAction(Request{Action: ActionDiff, RootID: "repo", Paths: []string{invalidUTF8}})
	if !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	if _, err := PlanAction(Request{Action: ActionStatus, RootID: "repo", Paths: []string{"file"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("status paths error = %v", err)
	}
}

func TestPlanRejectsConfigurationAndBudgetInjection(t *testing.T) {
	if _, err := PlanAction(Request{Action: "git_status; evil", RootID: "repo"}); !errors.Is(err, ErrUnsupportedAction) {
		t.Fatalf("unsupported action error = %v", err)
	}
	if _, err := PlanAction(Request{Action: ActionStatus, RootID: "repo", MaxOutputBytes: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative output budget error = %v", err)
	}
	if _, err := PlanAction(Request{Action: ActionStatus, RootID: "repo", MaxOutputBytes: maxOutputBytes + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("over-hard output budget error = %v", err)
	}
	if _, err := PlanAction(Request{Action: ActionStatus, RootID: "repo", MaxEntries: maxEntries + 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("over-hard entry budget error = %v", err)
	}
	plan, err := PlanAction(Request{Action: ActionStatus, RootID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.PreviewEnvironment() {
		if strings.HasPrefix(item.Name, "GIT_CONFIG_KEY_") || strings.HasPrefix(item.Name, "GIT_CONFIG_VALUE_") || item.Name == "GIT_CONFIG_PARAMETERS" {
			t.Fatalf("config injection variable present: %#v", item)
		}
	}
}

func TestParseStatusStructuredAndBounded(t *testing.T) {
	// Git's documented porcelain-v1 --branch -z shape is one NUL-delimited
	// `## ...` branch header followed by NUL-delimited `XY path` entries.
	data := []byte("## main...origin/main [ahead 2, behind 1]\x00 M -leading.txt\x00?? 目录/文件.go\x00")
	result, err := ParseStatus(data, Limits{MaxOutputBytes: len(data), MaxEntries: 2}, true, false, true)
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	if result.SchemaVersion != "gitprobe.v1" || result.Action != ActionStatus || result.Branch.Head != "main" || result.Branch.Upstream != "origin/main" || result.Branch.Ahead != 2 || result.Branch.Behind != 1 || !result.Branch.HasAheadBehind {
		t.Fatalf("status metadata = %#v", result)
	}
	if len(result.Entries) != 2 || result.Entries[0].Path != "-leading.txt" || result.Entries[1].Worktree != "?" || result.Coverage.OutputBytes != len(data) || !result.Coverage.Complete {
		t.Fatalf("status result = %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "oid") || strings.Contains(string(encoded), "## main") {
		t.Fatalf("structured JSON unexpectedly contains v1 header/OID: %s (%v)", encoded, err)
	}
	for _, fixture := range []struct {
		name     string
		data     string
		head     string
		initial  bool
		detached bool
		upstream string
	}{
		{name: "no-upstream", data: "## main\x00", head: "main"},
		{name: "initial", data: "## No commits yet on main\x00", head: "main", initial: true},
		{name: "detached", data: "## HEAD (no branch)\x00", detached: true},
		{name: "detached-at", data: "## HEAD detached at abc123\x00", detached: true},
		{name: "ahead-only", data: "## main...origin/main [ahead 2]\x00", head: "main", upstream: "origin/main"},
		{name: "behind-only", data: "## main...origin/main [behind 3]\x00", head: "main", upstream: "origin/main"},
		{name: "gone", data: "## main...origin/main [gone]\x00", head: "main", upstream: "origin/main"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			result, err := ParseStatus([]byte(fixture.data), Limits{}, true, false, true)
			if err != nil || result.Branch.Head != fixture.head || result.Branch.Upstream != fixture.upstream || result.Branch.Initial != fixture.initial || result.Branch.Detached != fixture.detached {
				t.Fatalf("branch fixture = %#v, err=%v", result, err)
			}
		})
	}
	if result, err := ParseStatus([]byte("## main\x00 M file.go\x00"), Limits{}, true, false, true); err != nil || result.Branch.Head != "main" || len(result.Entries) != 1 {
		t.Fatalf("NUL-delimited branch records = %#v, err=%v", result, err)
	}
	if _, err := ParseStatus([]byte("## main\n M file.go\x00"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("LF-delimited branch record accepted: %v", err)
	}
}

func TestParseStatusRejectsMalformedEncodingAndLimits(t *testing.T) {
	if _, err := ParseStatus([]byte("?? bad\x00"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing branch still accepted as output shape error: %v", err)
	}
	if _, err := ParseStatus([]byte("?? ok\x00"), Limits{MaxOutputBytes: 4}, true, false, true); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("status output limit error = %v", err)
	}
	if _, err := ParseStatus([]byte{'?', '?', ' ', 'x', 0xff, 0}, Limits{}, true, false, true); !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("status invalid UTF-8 error = %v", err)
	}
	if _, err := ParseStatus([]byte("## main\x00?? -leading\x00"), Limits{}, true, false, true); err != nil {
		t.Fatalf("leading-dash status path rejected: %v", err)
	}
	if _, err := ParseStatus([]byte("## main\x00 M ../escape\x00"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unsafe status path error = %v", err)
	}
	if _, err := ParseStatus([]byte("## main\x00 M a\x00 M b\x00"), Limits{MaxEntries: 1}, true, false, true); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("entry limit error = %v", err)
	}
	if _, err := ParseStatus([]byte("# branch.head main\x00"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("porcelain-v2 branch header error = %v", err)
	}
	for _, record := range []string{"R  renamed.txt\x00", "C  copied.txt\x00"} {
		if _, err := ParseStatus([]byte("## main\x00"+record), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("rename/copy status record accepted: %q (%v)", record, err)
		}
	}
	for _, fixture := range []string{
		"## main [ahead 1]\x00",
		"## main...origin/main [ahead 1, ahead 2]\x00",
		"## main...origin/main [behind 1, behind 2]\x00",
		"## HEAD detached \x00",
	} {
		if _, err := ParseStatus([]byte(fixture), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("malformed branch fixture %q accepted: %v", fixture, err)
		}
	}
	if result, err := ParseStatus([]byte("## main\x00T? type-changed\x00"), Limits{}, true, false, true); err != nil || len(result.Entries) != 1 || result.Entries[0].Index != "T" {
		t.Fatalf("type-change status record = %#v, err=%v", result, err)
	}
}

func TestParseStatusCaptureCompleteness(t *testing.T) {
	data := []byte("## main\x00 M file.go\x00")
	result, err := ParseStatus(data, Limits{}, false, false, true)
	if err != nil || result.Coverage.Complete {
		t.Fatalf("unconfirmed EOF reported complete: %#v, err=%v", result, err)
	}
	result, err = ParseStatus(data, Limits{}, false, true, true)
	if !errors.Is(err, ErrOutputLimit) || result.Coverage.Complete {
		t.Fatalf("truncated capture was not bounded: %#v, err=%v", result, err)
	}
	if _, err := ParseStatus(data, Limits{}, true, true, true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("contradictory capture flags accepted: %v", err)
	}
	result, err = ParseStatus(data, Limits{}, true, false, false)
	if err != nil || result.Coverage.Complete {
		t.Fatalf("unsuccessful process reported complete: %#v, err=%v", result, err)
	}
}

func TestParseDiffStructured(t *testing.T) {
	data := []byte("diff --git -leading.txt -leading.txt\nindex 1111111..2222222 100644\n--- -leading.txt\n+++ -leading.txt\n@@ -1,2 +1,2 @@ heading\n-old\n+new\n---- body-starts-with-dashes\n+++ body-starts-with-pluses\n")
	result, err := ParseDiff(data, Limits{MaxOutputBytes: len(data), MaxEntries: 1, MaxHunks: 1, MaxDiffLines: 4}, true, false, true)
	if err != nil {
		t.Fatalf("ParseDiff: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].OldPath != "-leading.txt" || result.Files[0].NewPath != "-leading.txt" || result.Files[0].AddedLines != 2 || result.Files[0].RemovedLines != 2 || len(result.Files[0].Hunks) != 1 || len(result.Files[0].Hunks[0].Lines) != 4 {
		t.Fatalf("diff result = %#v", result)
	}
	if result.Files[0].Hunks[0].Lines[0].Kind != "-" || result.Files[0].Hunks[0].Lines[1].Kind != "+" || result.Coverage.ReturnedEntries != 1 || !result.Coverage.Complete {
		t.Fatalf("diff structure = %#v", result)
	}
}

func TestParseDiffBinaryAndInvalidOutput(t *testing.T) {
	binary := []byte("diff --git image.bin image.bin\nindex 1111111..2222222\nBinary files image.bin and image.bin differ\n")
	result, err := ParseDiff(binary, Limits{}, true, false, true)
	if err != nil || len(result.Files) != 1 || result.Files[0].OldPath != "image.bin" || !result.Files[0].Binary {
		t.Fatalf("binary diff = %#v, err=%v", result, err)
	}
	binaryWithSeparatorInPath := []byte("diff --git \"a and b\" \"a and b\"\nindex 1111111..2222222\nBinary files \"a and b\" and \"a and b\" differ\n")
	if result, err := ParseDiff(binaryWithSeparatorInPath, Limits{}, true, false, true); err != nil || len(result.Files) != 1 || result.Files[0].OldPath != "a and b" || result.Files[0].NewPath != "a and b" || !result.Files[0].Binary {
		t.Fatalf("quoted binary diff = %#v, err=%v", result, err)
	}
	if _, err := ParseDiff([]byte("diff --git a b\n--- a\n+++ b\n@@ malformed\n"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("malformed hunk error = %v", err)
	}
	if _, err := ParseDiff([]byte("diff --git a b\n--- ../a\n+++ b\n"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unsafe diff path error = %v", err)
	}
	if _, err := ParseDiff([]byte{'d', 'i', 'f', 'f', 0xff}, Limits{}, true, false, true); !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("diff invalid UTF-8 error = %v", err)
	}
	if _, err := ParseDiff([]byte("diff --git a b\n"), Limits{MaxOutputBytes: 1}, true, false, true); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("diff output limit error = %v", err)
	}
	modeOnly := []byte("diff --git mode.txt mode.txt\nold mode 100644\nnew mode 100755\n")
	if result, err := ParseDiff(modeOnly, Limits{}, true, false, true); err != nil || len(result.Files) != 1 || result.Files[0].OldPath != "mode.txt" || result.Files[0].NewPath != "mode.txt" {
		t.Fatalf("mode-only diff = %#v, err=%v", result, err)
	}
	quoted := []byte("diff --git \"space name.txt\" \"space name.txt\"\nold mode 100644\nnew mode 100755\n")
	if result, err := ParseDiff(quoted, Limits{}, true, false, true); err != nil || len(result.Files) != 1 || result.Files[0].OldPath != "space name.txt" {
		t.Fatalf("quoted mode-only diff = %#v, err=%v", result, err)
	}
	literalAB := []byte("diff --git a/dir/file.txt a/dir/file.txt\nindex 1111111..2222222 100644\n--- a/dir/file.txt\n+++ a/dir/file.txt\n@@ -1 +1 @@\n-old\n+new\n")
	if result, err := ParseDiff(literalAB, Limits{}, true, false, true); err != nil || len(result.Files) != 1 || result.Files[0].OldPath != "a/dir/file.txt" || result.Files[0].NewPath != "a/dir/file.txt" {
		t.Fatalf("literal a/b directories were stripped: %#v, err=%v", result, err)
	}
	if _, err := ParseDiff([]byte("diff --git a b\n--- a\n+++ b\n@@ -1 +1 @@\n-x\x00\n"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("diff NUL error = %v", err)
	}
	if _, err := ParseDiff([]byte("diff --git old new\nsimilarity index 90%\nrename from old\nrename to new\n"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unexpected rename metadata error = %v", err)
	}
	if _, err := ParseDiff([]byte("diff --git old new\nGIT binary patch\nliteral 3\nabc\n"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("opaque binary patch accepted: %v", err)
	}
	if _, err := ParseDiff([]byte("diff --git old new\nunexpected metadata\n"), Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unknown diff metadata accepted: %v", err)
	}
}

func TestParseDiffDecodesQuotedFileMarkers(t *testing.T) {
	data := []byte("diff --git \"space name.txt\" \"space name.txt\"\nindex 1111111..2222222 100644\n--- \"space name.txt\"\n+++ \"space name.txt\"\n@@ -1 +1 @@\n-old\n+new\n")
	result, err := ParseDiff(data, Limits{}, true, false, true)
	if err != nil || len(result.Files) != 1 || result.Files[0].OldPath != "space name.txt" || result.Files[0].NewPath != "space name.txt" {
		t.Fatalf("quoted file markers = %#v, err=%v", result, err)
	}
}

func TestParseGitHeaderKeepsLiteralABDirectories(t *testing.T) {
	oldPath, newPath, ok := parseGitHeaderPaths("a/dir/file.txt b/dir/file.txt")
	if !ok || oldPath != "a/dir/file.txt" || newPath != "b/dir/file.txt" {
		t.Fatalf("header paths were normalized as prefixes: old=%q new=%q ok=%v", oldPath, newPath, ok)
	}
}

func TestParseDiffMetadataDirections(t *testing.T) {
	newFile := []byte("diff --git new.txt new.txt\nnew file mode 100644\nindex 0000000..1111111\n--- /dev/null\n+++ new.txt\n@@ -0,0 +1 @@\n+new\n")
	if result, err := ParseDiff(newFile, Limits{}, true, false, true); err != nil || len(result.Files) != 1 || result.Files[0].OldPath != "/dev/null" || result.Files[0].NewPath != "new.txt" {
		t.Fatalf("new-file diff = %#v, err=%v", result, err)
	}
	deletedFile := []byte("diff --git old.txt old.txt\ndeleted file mode 100644\nindex 1111111..0000000\n--- old.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-old\n")
	if result, err := ParseDiff(deletedFile, Limits{}, true, false, true); err != nil || len(result.Files) != 1 || result.Files[0].OldPath != "old.txt" || result.Files[0].NewPath != "/dev/null" {
		t.Fatalf("deleted-file diff = %#v, err=%v", result, err)
	}
	for _, fixture := range [][]byte{
		[]byte("diff --git new.txt new.txt\nnew file mode 100644\nindex 0000000..1111111\n--- new.txt\n+++ /dev/null\n"),
		[]byte("diff --git old.txt old.txt\ndeleted file mode 100644\nindex 1111111..0000000\n--- /dev/null\n+++ old.txt\n"),
		[]byte("diff --git file.txt file.txt\nold mode 100644\n"),
		[]byte("diff --git file.txt file.txt\nnew mode 100755\nold mode 100644\n"),
		[]byte("diff --git file.txt file.txt\n--- file.txt\n+++ file.txt\n"),
	} {
		if _, err := ParseDiff(fixture, Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("inconsistent diff metadata accepted: %q (%v)", fixture, err)
		}
	}
	binaryNew := []byte("diff --git image.bin image.bin\nnew file mode 100644\nindex 0000000..1111111\nBinary files /dev/null and image.bin differ\n")
	if result, err := ParseDiff(binaryNew, Limits{}, true, false, true); err != nil || len(result.Files) != 1 || !result.Files[0].Binary {
		t.Fatalf("new binary diff = %#v, err=%v", result, err)
	}
	wrongBinaryDirection := []byte("diff --git image.bin image.bin\nnew file mode 100644\nindex 0000000..1111111\nBinary files image.bin and /dev/null differ\n")
	if _, err := ParseDiff(wrongBinaryDirection, Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("wrong binary direction accepted: %v", err)
	}
}

func TestParseDiffCaptureCompleteness(t *testing.T) {
	data := []byte("diff --git file.txt file.txt\nindex 1111111..2222222 100644\n--- file.txt\n+++ file.txt\n@@ -1 +1 @@\n-old\n+new\n")
	result, err := ParseDiff(data, Limits{}, false, false, true)
	if err != nil || result.Coverage.Complete {
		t.Fatalf("unconfirmed EOF reported complete: %#v, err=%v", result, err)
	}
	result, err = ParseDiff(data, Limits{}, false, true, true)
	if !errors.Is(err, ErrOutputLimit) || result.Coverage.Complete {
		t.Fatalf("truncated capture was not bounded: %#v, err=%v", result, err)
	}
	if _, err := ParseDiff(data, Limits{}, true, true, true); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("contradictory capture flags accepted: %v", err)
	}
	result, err = ParseDiff(data, Limits{}, true, false, false)
	if err != nil || result.Coverage.Complete {
		t.Fatalf("unsuccessful process reported complete: %#v, err=%v", result, err)
	}
}

func TestParseDiffRejectsHeaderMarkerDirectionAndHunkCountMismatch(t *testing.T) {
	fixtures := [][]byte{
		[]byte("diff --git old.txt new.txt\nindex 1111111..2222222\n--- new.txt\n+++ old.txt\n@@ -1 +1 @@\n-old\n+new\n"),
		[]byte("diff --git old.txt new.txt\nindex 1111111..2222222\n--- old.txt\n+++ new.txt\n@@ -1,2 +1 +1 @@\n-old\n+new\n"),
		[]byte("diff --git old.txt new.txt\nindex 1111111..2222222\n--- old.txt\n+++ new.txt\n@@ -1 +1,2 @@\n-old\n+new\n"),
	}
	for _, fixture := range fixtures {
		if _, err := ParseDiff(fixture, Limits{}, true, false, true); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("inconsistent diff accepted: %q (%v)", fixture, err)
		}
	}
}

func TestPlanEnvironmentDefensiveCopy(t *testing.T) {
	plan, err := PlanAction(Request{Action: ActionStatus, RootID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	env := plan.PreviewEnvironment()
	env[0].Name = "MUTATED"
	if plan.PreviewEnvironment()[0].Name == "MUTATED" {
		t.Fatal("Plan.PreviewEnvironment did not return a defensive copy")
	}
}

func contains(values []string, want string) bool { return indexOf(values, want) >= 0 }

func containsPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

func containsEnv(values []EnvironmentEntry, name string) bool { return envValuePresent(values, name) }

func envValuePresent(values []EnvironmentEntry, name string) bool {
	for _, value := range values {
		if value.Name == name {
			return true
		}
	}
	return false
}

func envValue(values []EnvironmentEntry, name string) string {
	for _, value := range values {
		if value.Name == name {
			return value.Value
		}
	}
	return ""
}
