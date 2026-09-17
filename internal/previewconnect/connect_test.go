package previewconnect

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultOptionsUsePackagedMCPAndExternalCloudflared(t *testing.T) {
	opts := DefaultOptions(`C:\work\Local-Probe\repo`)
	if want := `C:\work\Local-Probe\repo\bin\local-probe-mcp.exe`; opts.MCPBinary != want {
		t.Fatalf("MCPBinary = %q, want %q", opts.MCPBinary, want)
	}
	if want := `C:\work\Local-Probe\_tools\tunnel-client-v0.0.14-windows-amd64\bin\cloudflared.exe`; opts.CloudflaredBinary != want {
		t.Fatalf("CloudflaredBinary = %q, want %q", opts.CloudflaredBinary, want)
	}
	if want := `.secrets/cloudflared-tunnel-token.txt`; credentialHint(opts) != want {
		t.Fatalf("credential hint = %q, want %q", credentialHint(opts), want)
	}
}

func TestValidateTokenClassifiesMissingEmptyAndMultilineWithoutReturningContent(t *testing.T) {
	dir := t.TempDir()
	if _, err := validateToken(filepath.Join(dir, "missing")); asProblem(err).Code != CodeTokenMissing {
		t.Fatalf("missing token classification failed: %v", err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateToken(empty); asProblem(err).Code != CodeTokenEmpty {
		t.Fatalf("empty token classification failed: %v", err)
	}
	multiline := filepath.Join(dir, "multiline")
	if err := os.WriteFile(multiline, []byte("abcdefghijklmnop\nsecond\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateToken(multiline); asProblem(err).Code != CodeTokenInvalidFormat {
		t.Fatalf("multiline token classification failed: %v", err)
	}
	valid := filepath.Join(dir, "valid")
	if err := os.WriteFile(valid, []byte("eyJhbGciOiJIUzI1NiJ9.payload.signature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateToken(valid); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}

func TestTunnelFailureClassificationAndRedaction(t *testing.T) {
	tokenLike := "eyJhbGciOiJIUzI1NiJ9.abcdefghijklmnop.signaturevalue"
	output := "ERR token=" + tokenLike + " invalid tunnel credentials"
	p := asProblem(classifyTunnelFailure(output))
	if p.Code != CodeTokenRejected {
		t.Fatalf("code = %q, want %q", p.Code, CodeTokenRejected)
	}
	if strings.Contains(p.Error(), tokenLike) {
		t.Fatalf("problem leaked token: %q", p.Error())
	}
	clean := sanitizeOutput(output)
	if strings.Contains(clean, tokenLike) || strings.Contains(clean, "invalid tunnel credentials") == false {
		t.Fatalf("sanitized output = %q", clean)
	}
	if !strings.Contains(clean, "<redacted>") && !strings.Contains(clean, "<redacted-token>") {
		t.Fatalf("sanitized output did not redact: %q", clean)
	}
	digest := sha256.Sum256([]byte(tokenLike))
	standalone := sanitizeOutputWithDigest("cloudflared echoed "+tokenLike, digest)
	if strings.Contains(standalone, tokenLike) {
		t.Fatalf("standalone token leaked: %q", standalone)
	}
}

func TestLoadTunnelConfigBindsOriginToMCP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tunnel.json")
	valid := `{"public_host":"mcp.example.test","origin_url":"http://127.0.0.1:8788","metrics_addr":"127.0.0.1:49300","transport_protocol":"auto"}`
	if err := os.WriteFile(path, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTunnelConfig(path, "127.0.0.1:8788"); err != nil {
		t.Fatalf("valid tunnel config rejected: %v", err)
	}
	if _, err := loadTunnelConfig(path, "127.0.0.1:8787"); asProblem(err).Code != CodeTunnelConfigInvalid {
		t.Fatalf("mismatched origin code = %v", asProblem(err).Code)
	}
	ipv6 := strings.Replace(valid, "127.0.0.1:8788", "[::1]:8788", 1)
	if err := os.WriteFile(path, []byte(ipv6), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTunnelConfig(path, "127.0.0.1:8788"); asProblem(err).Code != CodeTunnelConfigInvalid {
		t.Fatalf("different loopback identity accepted: %v", err)
	}
}

func TestParseHAConnectionsTakesMaximum(t *testing.T) {
	body := []byte("# HELP cloudflared_tunnel_ha_connections\ncloudflared_tunnel_ha_connections{location=\"nrt\"} 2\ncloudflared_tunnel_ha_connections{location=\"hkg\"} 4\n")
	if got := parseHAConnections(body); got != 4 {
		t.Fatalf("HA connections = %d, want 4", got)
	}
}

func TestFilteredEnvironmentRemovesStaleTransport(t *testing.T) {
	t.Setenv("TUNNEL_TRANSPORT_PROTOCOL", "quic")
	t.Setenv("TUNNEL_TOKEN", "stale-token-must-not-be-inherited")
	t.Setenv("TUNNEL_TOKEN_FILE", "stale-token-file-must-not-be-inherited")
	env := filteredEnvironment(nil)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "TUNNEL_TRANSPORT_PROTOCOL", "TUNNEL_TOKEN", "TUNNEL_TOKEN_FILE":
			t.Fatalf("stale tunnel variable remained: %q", entry)
		}
	}
	if got := filteredEnvironment([]string{"TUNNEL_TRANSPORT_PROTOCOL=http2"}); !contains(got, "TUNNEL_TRANSPORT_PROTOCOL=http2") {
		t.Fatal("explicit transport override was not added")
	}
}

func TestProblemIsSafeAndTyped(t *testing.T) {
	err := problem(CodeTokenMissing)
	var typed *Problem
	if !errors.As(err, &typed) || typed.Code != CodeTokenMissing {
		t.Fatalf("problem type/code = %#v", err)
	}
	if strings.Contains(typed.Remedy, "cloudflared-tunnel-token.txt") == false {
		t.Fatalf("setup hint missing from token remedy: %q", typed.Remedy)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
