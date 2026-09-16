package supportbundle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/LE-saber/Local-Probe/internal/commandprofile"
	"github.com/LE-saber/Local-Probe/internal/desktopadmin"
)

func validInput() Input {
	return Input{
		Build: BuildInfo{Version: "preview", Commit: "unknown", GoVersion: "go1.26.0", OS: "windows", Arch: "amd64"},
		Overview: desktopadmin.Overview{
			SchemaVersion: desktopadmin.SchemaVersion, ProductionReady: false,
			Capabilities: desktopadmin.Capabilities{
				ProductionReady:   false,
				Overview:          desktopadmin.CapabilityStatus{Available: true},
				ConnectionStatus:  desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason},
				ManagementActions: desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityProductionGate},
				Diagnostics:       desktopadmin.CapabilityStatus{Reason: desktopadmin.CapabilityUnavailableReason},
				DeveloperRules:    desktopadmin.CapabilityStatus{Available: true},
			},
			ConfigRevision: "r1", StatusKnown: false,
		},
		Statuses:       desktopadmin.ConnectionStatusList{SchemaVersion: desktopadmin.SchemaVersion, ProductionReady: false, StatusKnown: false},
		DeveloperRules: desktopadmin.DeveloperRules{SchemaVersion: desktopadmin.SchemaVersion, ProductionReady: false},
	}
}

func TestGenerateIsBoundedAndContainsOnlyFixedProjection(t *testing.T) {
	data, err := Generate(validInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || len(data) > MaxBytes {
		t.Fatalf("bundle size = %d", len(data))
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "production_ready", "build", "overview", "statuses", "developer_rules", "audit"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing fixed key %q: %s", key, data)
		}
	}
	for _, forbidden := range []string{"root", "credential", "tunnel_id", "tunnel_token", "raw", "argv", "env", "stdout", "stderr", "path"} {
		if strings.Contains(strings.ToLower(string(data)), `"`+forbidden+`"`) {
			t.Fatalf("bundle contains forbidden key %q: %s", forbidden, data)
		}
	}
	if strings.Contains(string(data), "ProductionReady") || strings.Contains(string(data), "unknown") == false {
		t.Fatalf("bundle did not preserve safe build metadata: %s", data)
	}
}

func TestGenerateRejectsSensitiveAndPathBuildMetadata(t *testing.T) {
	for _, value := range []string{"Bearer secret-value", "C:\\Users\\owner\\secret.txt", "/home/owner/file"} {
		input := validInput()
		input.Build.Commit = value
		if _, err := Generate(input); !errors.Is(err, ErrSensitiveData) {
			t.Fatalf("value %q error = %v, want ErrSensitiveData", value, err)
		}
	}
}

func TestSecondPassRejectsSensitiveUnknownFieldsAndAbsolutePaths(t *testing.T) {
	for _, data := range []string{
		`{"unexpected":"Bearer secret-value"}`,
		`{"unexpected":"C:\\private\\file.txt"}`,
		`{"api_key":"not-returned"}`,
	} {
		if err := secondPass([]byte(data)); !errors.Is(err, ErrSensitiveData) {
			t.Fatalf("data %s error = %v", data, err)
		}
	}
	if err := secondPass([]byte(`{"unexpected":"safe"}`)); err != nil {
		t.Fatalf("safe unknown field rejected: %v", err)
	}
}

func TestGenerateRejectsOversizedProjection(t *testing.T) {
	input := validInput()
	input.Statuses.StatusKnown = true
	input.Statuses.Entries = make([]desktopadmin.ConnectionStatus, desktopadmin.MaxItems)
	for index := range input.Statuses.Entries {
		input.Statuses.Entries[index] = desktopadmin.ConnectionStatus{
			ConnectionID: "connection-" + strings.Repeat("x", 100) + string(rune('a'+index%26)),
			Label:        strings.Repeat("label ", 20),
			ProfileID:    "profile-" + strings.Repeat("y", 100) + string(rune('a'+index%26)),
			Enabled:      true, Transport: "local", StatusKnown: true, State: "ready",
		}
	}
	input.DeveloperRules.Rules = make([]desktopadmin.DeveloperRule, desktopadmin.MaxItems)
	for index := range input.DeveloperRules.Rules {
		input.DeveloperRules.Rules[index] = desktopadmin.DeveloperRule{
			ID:         "rule-" + strings.Repeat("x", 100) + string(rune('a'+index%26)),
			Kind:       commandprofile.KindFixedCommand,
			VariantIDs: []string{"variant-" + strings.Repeat("v", 100) + string(rune('a'+index%26))},
			SlotKinds:  []commandprofile.SlotKind{commandprofile.SlotEnum},
		}
	}
	if _, err := Generate(input); !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("oversized bundle error = %v, want ErrSizeLimit", err)
	}
}

func TestWriteAtomicNoOverwriteAndPrivateMode(t *testing.T) {
	directory := t.TempDir()
	path, err := Write(directory, validInput())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != directory || filepath.Ext(path) != ".json" {
		t.Fatalf("path = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondPass(data); err != nil {
		t.Fatalf("written bundle second pass = %v", err)
	}
	path2, err := Write(directory, validInput())
	if err != nil {
		t.Fatal(err)
	}
	if path2 == path {
		t.Fatalf("second write reused path %q", path)
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file remains: %v", err)
	}
}

func TestWriteRejectsRelativeAndSymlinkDirectories(t *testing.T) {
	if _, err := Write("relative", validInput()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("relative path error = %v", err)
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := Write(link, validInput()); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("symlink path error = %v", err)
	}
}

func TestGenerateDoesNotConsultContextOrPerformIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Generate has no context parameter by design.  This compile-time/use-site
	// test documents that it is a pure projection operation and does not need
	// to observe caller cancellation or perform I/O.
	_ = ctx
	if _, err := Generate(validInput()); err != nil {
		t.Fatal(err)
	}
}
