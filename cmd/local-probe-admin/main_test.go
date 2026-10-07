package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/workspaceadmin"
)

func adminFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := config.NewRoot("project", dir, []string{".env"})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfileWithIgnore("local", []string{"project"}, []string{"read_file"}, []string{"*.key"}, []string{"vendor/**"})
	if err != nil {
		t.Fatal(err)
	}
	credential := config.NewCredentialRef("local-credential-ref", "runtime")
	connection := config.NewConnection("local", "Local", "local", credential.ID(), true)
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{credential})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "local-probe.json")
	store, err := config.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Save(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return path, snapshot.Revision()
}

func TestAdminShowPreviewApplyAndReload(t *testing.T) {
	path, revision := adminFixture(t)
	args := []string{"-config", path, "-connection-id", "local"}
	var output, diagnostics bytes.Buffer
	if err := run(context.Background(), args, nil, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var rules workspaceadmin.Rules
	if err := json.Unmarshal(output.Bytes(), &rules); err != nil || rules.Revision != revision {
		t.Fatalf("show: %s %v", output.String(), err)
	}
	if strings.Contains(output.String(), filepath.Dir(path)) || strings.Contains(output.String(), "credential") {
		t.Fatal("show leaked path/credential")
	}
	request := `{"update":{"deny_patterns":["*.key","private/**"]},"samples":[{"root_id":"project","path":"private/a.txt"},{"root_id":"project","path":"vendor/a.go"}]}`
	previewArgs := append(append([]string{}, args...), "-action", "preview", "-revision", revision, "-request", "-")
	output.Reset()
	if err := run(context.Background(), previewArgs, strings.NewReader(request), &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var preview workspaceadmin.RulePreview
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Changed || preview.Decisions[0].Allowed || !preview.Decisions[1].Ignored {
		t.Fatalf("wrong preview: %+v", preview)
	}
	applyArgs := append(append([]string{}, args...), "-action", "apply", "-revision", revision, "-request", "-", "-offline", "-approve", "-ack-connections", "local")
	output.Reset()
	if err := run(context.Background(), applyArgs, strings.NewReader(request), &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &preview); err != nil || preview.Revision == revision {
		t.Fatalf("apply failed: %v %+v", err, preview)
	}
	output.Reset()
	if err := run(context.Background(), args, nil, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "private/**") {
		t.Fatal("reloaded config lacks saved rule")
	}
	output.Reset()
	if err := run(context.Background(), applyArgs, strings.NewReader(request), &output, &diagnostics); err == nil || !strings.Contains(err.Error(), "revision_conflict") {
		t.Fatalf("stale apply: %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("failed operation printed success result")
	}
}

func TestAdminApplyGatesBeforeAnyIO(t *testing.T) {
	path, revision := adminFixture(t)
	base := []string{"-config", path, "-connection-id", "local", "-action", "apply", "-revision", revision, "-request", "-"}
	before, _ := os.ReadFile(path)
	for _, flags := range [][]string{nil, {"-offline"}, {"-approve"}, {"-offline", "-approve", "-ack-connections", "other"}} {
		var output, diagnostics bytes.Buffer
		args := append(append([]string{}, base...), flags...)
		err := run(context.Background(), args, strings.NewReader(`{"update":{"deny_patterns":["**"]}}`), &output, &diagnostics)
		if err == nil || output.Len() != 0 {
			t.Fatalf("unapproved update succeeded: %v", flags)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) {
			t.Fatal("gate failure changed config")
		}
	}
}

func TestAdminStrictRequestParsing(t *testing.T) {
	for _, payload := range []string{
		`null`, `[]`, `{`, `{} {}`,
		`{"update":{},"Update":{}}`,
		`{"update":{"deny_patterns":[],"deny_patterns":["**"]}}`,
		`{"update":{"unknown":true}}`,
		`{"update":{"path":"secret"}}`,
		`{"update":{"deny_patterns":"**"}}`,
		`{"samples":[{"root_id":"project","path":"a","path":"b"}]}`,
		strings.Repeat(" ", maxRequestBytes+1),
	} {
		if _, err := loadRequest("-", strings.NewReader(payload)); err == nil {
			t.Fatalf("invalid payload accepted: %.120s", payload)
		}
	}
	if _, err := loadRequest("relative.json", nil); err == nil {
		t.Fatal("relative request path accepted")
	}
	if _, err := loadRequest("-", nil); err == nil {
		t.Fatal("missing stdin accepted")
	}
	valid, err := loadRequest("-", strings.NewReader(`{"update":{"ignore_patterns":[]}}`))
	if err != nil || valid.Update.Ignore == nil || len(*valid.Update.Ignore) != 0 {
		t.Fatalf("explicit clear not preserved: %+v %v", valid, err)
	}
}

func TestAdminHelpDoesNotRequireConfig(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if err := run(context.Background(), []string{"-help"}, nil, &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostics.String(), "-offline") || output.Len() != 0 {
		t.Fatal("help omitted the offline contract")
	}
}

func FuzzLoadRuleRequest(f *testing.F) {
	for _, seed := range []string{
		`{"update":{"deny_patterns":["*.key"]}}`,
		`{"update":{"ignore_patterns":[]},"samples":[{"root_id":"a","path":"b","directory":false}]}`,
		`{"update":{},"update":{}}`, "null", "[]", "{",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > maxRequestBytes+1 {
			return
		}
		_, _ = loadRequest("-", strings.NewReader(input))
	})
}

func TestAdminCLIProcessFlow(t *testing.T) {
	path, revision := adminFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	call := func(args []string, input string) (string, error) {
		command := exec.CommandContext(ctx, executable, append([]string{"-test.run=^TestAdminCLIProcessHelper$", "--", "admin-cli-helper"}, args...)...)
		command.Stdin = strings.NewReader(input)
		data, err := command.CombinedOutput()
		return string(data), err
	}
	args := []string{"-config", path, "-connection-id", "local"}
	output, err := call(args, "")
	if err != nil {
		t.Fatalf("show subprocess: %s %v", output, err)
	}
	var view workspaceadmin.Rules
	if err := json.Unmarshal([]byte(output), &view); err != nil || view.Revision != revision {
		t.Fatalf("show JSON: %s %v", output, err)
	}
	request := `{"update":{"deny_patterns":["private/**"]}}`
	applyArgs := append(append([]string{}, args...), "-action", "apply", "-revision", revision, "-request", "-", "-offline", "-approve", "-ack-connections", "local")
	if output, err := call(applyArgs, request); err != nil {
		t.Fatalf("apply subprocess: %s %v", output, err)
	}
	output, err = call(args, "")
	if err != nil || !strings.Contains(output, "private/**") {
		t.Fatalf("persisted subprocess result: %s %v", output, err)
	}
	output, err = call(applyArgs, request)
	if err == nil || !strings.Contains(output, "revision_conflict") {
		t.Fatalf("stale subprocess: %s %v", output, err)
	}
}

func TestAdminCLIProcessHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "admin-cli-helper" {
			os.Args = append([]string{"local-probe-admin"}, os.Args[i+1:]...)
			main()
			os.Exit(0)
		}
	}
}

func TestAdminRequestFileAndCancellation(t *testing.T) {
	path, revision := adminFixture(t)
	requestPath := filepath.Join(filepath.Dir(path), "rules.json")
	if err := os.WriteFile(requestPath, []byte(`{"update":{"ignore_patterns":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRequest(requestPath, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRequest(filepath.Dir(path), nil); err == nil {
		t.Fatal("directory request accepted")
	}
	var output, diagnostics bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, []string{"-config", path, "-connection-id", "local", "-action", "preview", "-revision", revision, "-request", requestPath}, nil, &output, &diagnostics)
	if err == nil || !strings.Contains(err.Error(), "cancelled") || output.Len() != 0 {
		t.Fatalf("cancelled operation: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("test context")
	}
}
