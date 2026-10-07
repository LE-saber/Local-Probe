package previewconnect

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenAIRunProfileContainsOnlySecretFileReferences(t *testing.T) {
	cfg := openAITunnelConfig{
		tunnelID:     "tunnel_0123456789abcdef0123456789abcdef",
		mcpToken:     "local-mcp-secret-value-0123456789",
		apiKeyPath:   filepath.Join(t.TempDir(), "api key.yaml"),
		mcpTokenPath: filepath.Join(t.TempDir(), "mcp token.txt"),
	}
	const apiKey = "sk-runtime-secret-value-0123456789"
	profile, err := openAIRunProfile(cfg, filepath.Join(t.TempDir(), "health url.txt"), "127.0.0.1:8787")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{apiKey, cfg.mcpToken} {
		if strings.Contains(profile, secret) {
			t.Fatalf("generated profile contains a secret value: %q", secret)
		}
	}
	for _, expected := range []string{
		"config_version: 1",
		"api_key: 'file:",
		"url_file: '",
		"channel: main",
		"url: 'http://127.0.0.1:8787/mcp'",
		"extra_headers:",
		"discovery_extra_headers:",
		"X-Local-Probe-Token: 'file:",
	} {
		if !strings.Contains(profile, expected) {
			t.Fatalf("generated profile is missing %q", expected)
		}
	}
	if !strings.Contains(profile, filepath.ToSlash(cfg.apiKeyPath)) || !strings.Contains(profile, filepath.ToSlash(cfg.mcpTokenPath)) {
		t.Fatal("generated profile did not reference both protected secret files")
	}
}

func TestReadOwnedHealthURLPendingInvalidAndStale(t *testing.T) {
	path, info, err := createHealthURLFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if _, err := readOwnedHealthURL(path, info); !errors.Is(err, errHealthURLPending) {
		t.Fatalf("empty URL file error = %v, want pending", err)
	}
	for _, invalid := range []string{
		"https://127.0.0.1:1234",
		"http://localhost:1234",
		"http://127.0.0.1",
		"http://127.0.0.1:1234/ui",
		"http://127.0.0.1:1234?next=https://example.com",
		"http://example.com:1234",
	} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readOwnedHealthURL(path, info); !errors.Is(err, errHealthURLInvalid) {
			t.Fatalf("URL %q error = %v, want invalid", invalid, err)
		}
	}

	stalePath := path + ".old"
	if err := os.Rename(path, stalePath); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(stalePath)
	if err := os.WriteFile(path, []byte("http://127.0.0.1:1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOwnedHealthURL(path, info); !errors.Is(err, errHealthURLInvalid) {
		t.Fatalf("replaced health file error = %v, want stale-file rejection", err)
	}
}

func TestReadOwnedHealthURLAcceptsTunnelClientV014InPlaceWrite(t *testing.T) {
	// OpenAI tunnel-client v0.0.14 writes the resolved URL with os.WriteFile
	// at the configured path (not an atomic rename):
	// https://github.com/openai/tunnel-client/blob/v0.0.14/pkg/localproxy/localproxy.go
	path, info, err := createHealthURLFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if err := os.WriteFile(path, []byte("http://127.0.0.1:39127\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readOwnedHealthURL(path, info)
	if err != nil || got != "http://127.0.0.1:39127" {
		t.Fatalf("resolved URL = %q, err=%v", got, err)
	}
}

type retryStopChild struct {
	kills int
	done  chan struct{}
}

func (c *retryStopChild) Wait() error { return nil }
func (c *retryStopChild) Kill() error {
	c.kills++
	if c.kills == 1 {
		return errors.New("injected transient kill failure")
	}
	close(c.done)
	return nil
}

func TestStopRetryRetainsTunnelHandleAndRuntimeFiles(t *testing.T) {
	runtimeRoot := t.TempDir()
	healthPath, healthInfo, err := createHealthURLFile(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	profilePath, profileInfo, err := createOwnedRuntimeFile(runtimeRoot, ".tunnel-client-run-*.yaml", []byte("safe profile"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	child := &retryStopChild{done: done}
	handle := &childHandle{process: child, done: done}
	controller := &Controller{
		now:                time.Now,
		tunnel:             handle,
		healthURLFile:      healthPath,
		healthURLFileInfo:  healthInfo,
		runProfileFile:     profilePath,
		runProfileFileInfo: profileInfo,
	}

	if err := controller.Stop(); err == nil {
		t.Fatal("first Stop succeeded despite injected failure")
	}
	if controller.tunnel != handle {
		t.Fatal("Stop discarded the tunnel handle before its exit was confirmed")
	}
	for _, path := range []string{healthPath, profilePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed Stop removed owned runtime file %q: %v", filepath.Base(path), err)
		}
	}
	if err := controller.Stop(); err != nil {
		t.Fatalf("retry Stop failed: %v", err)
	}
	if controller.tunnel != nil || controller.healthURLFile != "" || controller.runProfileFile != "" {
		t.Fatal("successful retry did not clear owned lifecycle state")
	}
	for _, path := range []string{healthPath, profilePath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("successful Stop kept owned runtime file %q", filepath.Base(path))
		}
	}
}

func TestSafeOutputRedactsEveryOpenAIRuntimeSecret(t *testing.T) {
	apiKey := "sk-runtime-secret-value-0123456789"
	token := "local-mcp-secret-value-0123456789"
	output := newSafeOutputWithSecrets(1024, sha256.Sum256([]byte(apiKey)), sha256.Sum256([]byte(token)))
	_, _ = output.Write([]byte("api=" + apiKey + " token=" + token))
	if strings.Contains(output.String(), apiKey) || strings.Contains(output.String(), token) {
		t.Fatalf("safe output leaked an OpenAI secret: %q", output.String())
	}
}

func TestValidateOpenAIClassifiesEveryExternalInput(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{
		"client": filepath.Join(dir, "tunnel-client.exe"),
		"api":    filepath.Join(dir, "control-plane-api-key.txt"),
		"id":     filepath.Join(dir, "tunnel-id.txt"),
		"token":  filepath.Join(dir, "mcp-bearer-token.txt"),
	}
	controller := &Controller{opts: Options{
		TunnelClientBinary: paths["client"], OpenAIAPIKeyFile: paths["api"],
		OpenAITunnelIDFile: paths["id"], MCPTokenFile: paths["token"],
	}}
	if _, err := controller.validateOpenAI(); asProblem(err).Code != CodeOpenAIClientMissing {
		t.Fatalf("missing client code = %q", asProblem(err).Code)
	}
	for name, value := range map[string]string{
		"client": "test executable", "api": "sk-runtime-test-0123456789",
		"id": "tunnel_0123456789abcdef0123456789abcdef", "token": "local-hop-token-0123456789",
	} {
		if err := os.WriteFile(paths[name], []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := controller.validateOpenAI(); err != nil {
		t.Fatalf("valid OpenAI inputs rejected: %v", err)
	}
	if err := os.Remove(paths["token"]); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.validateOpenAI(); asProblem(err).Code != CodeOpenAIMCPTokenMissing {
		t.Fatalf("missing MCP token code = %q", asProblem(err).Code)
	}
	if err := os.WriteFile(paths["token"], nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.validateOpenAI(); asProblem(err).Code != CodeOpenAIMCPTokenEmpty {
		t.Fatalf("empty MCP token code = %q", asProblem(err).Code)
	}
}

func TestFilteredOpenAIEnvironmentRemovesCredentialVariables(t *testing.T) {
	for name, value := range map[string]string{
		"OPENAI_API_KEY": "must-not-pass", "CONTROL_PLANE_API_KEY": "must-not-pass",
		"MCP_TOKEN": "must-not-pass", "HEALTH_URL": "must-not-pass",
	} {
		t.Setenv(name, value)
	}
	for _, entry := range filteredOpenAIEnvironment() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "CONTROL_PLANE_") || strings.HasPrefix(upper, "MCP_") || strings.HasPrefix(upper, "HEALTH_") {
			t.Fatalf("credential-bearing environment variable remained: %q", name)
		}
	}
}
