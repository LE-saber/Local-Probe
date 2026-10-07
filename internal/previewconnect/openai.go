package previewconnect

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const openAIConnectionID = "chatgpt-local"

var tunnelIDPattern = regexp.MustCompile(`^tunnel_[0-9a-f]{32}$`)

var (
	errMCPProbeUnavailable  = errors.New("local MCP probe unavailable")
	errMCPProbeUnauthorized = errors.New("local MCP probe unauthorized")
	errMCPProbeInvalid      = errors.New("local MCP probe contract invalid")
	errHealthURLPending     = errors.New("tunnel health URL pending")
	errHealthURLInvalid     = errors.New("tunnel health URL invalid")
)

type openAITunnelConfig struct {
	tunnelID       string
	mcpToken       string
	mcpTokenPath   string
	apiKeyPath     string
	mcpTokenDigest [sha256.Size]byte
	apiKeyDigest   [sha256.Size]byte
}

func (c *Controller) validateOpenAI() (validatedConfig, error) {
	if !safeRegularFile(c.opts.TunnelClientBinary) {
		return validatedConfig{}, problem(CodeOpenAIClientMissing)
	}
	for _, file := range []struct {
		path        string
		missingCode Code
		invalidCode Code
	}{
		{c.opts.OpenAIAPIKeyFile, CodeOpenAIKeyMissing, CodeOpenAIKeyInvalid},
		{c.opts.OpenAITunnelIDFile, CodeOpenAITunnelMissing, CodeOpenAITunnelInvalid},
		{c.opts.MCPTokenFile, CodeOpenAIMCPTokenMissing, CodeOpenAIMCPTokenInvalid},
	} {
		if err := validateCredentialFile(file.path, file.missingCode, file.invalidCode); err != nil {
			return validatedConfig{}, err
		}
	}
	_, apiKeyDigest, err := readSecretFile(c.opts.OpenAIAPIKeyFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return validatedConfig{}, problem(CodeOpenAIKeyMissing)
		}
		return validatedConfig{}, problem(CodeOpenAIKeyInvalid)
	}
	tunnelID, err := readSingleLineFile(c.opts.OpenAITunnelIDFile, 256)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return validatedConfig{}, problem(CodeOpenAITunnelMissing)
		}
		return validatedConfig{}, problem(CodeOpenAITunnelInvalid)
	}
	if !tunnelIDPattern.MatchString(tunnelID) {
		return validatedConfig{}, problem(CodeOpenAITunnelInvalid)
	}
	mcpToken, mcpTokenDigest, err := readSecretFile(c.opts.MCPTokenFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return validatedConfig{}, problem(CodeOpenAIMCPTokenMissing)
		}
		if errors.Is(err, errSecretEmpty) {
			return validatedConfig{}, problem(CodeOpenAIMCPTokenEmpty)
		}
		return validatedConfig{}, problem(CodeOpenAIMCPTokenInvalid)
	}
	return validatedConfig{
		tokenConfigured: true,
		tokenDigest:     mcpTokenDigest,
		openAI: openAITunnelConfig{
			tunnelID:       tunnelID,
			mcpToken:       mcpToken,
			mcpTokenPath:   c.opts.MCPTokenFile,
			apiKeyPath:     c.opts.OpenAIAPIKeyFile,
			mcpTokenDigest: mcpTokenDigest,
			apiKeyDigest:   apiKeyDigest,
		},
	}, nil
}

func (c *Controller) mcpArgs() []string {
	args := []string{"-config", c.opts.MCPConfig}
	if c.opts.Transport == config.TransportOpenAIRuntime {
		connectionID := c.opts.ConnectionID
		if connectionID == "" {
			connectionID = openAIConnectionID
		}
		return append(args,
			"-ingress", "local-token",
			"-connection-id", connectionID,
			"-token-file", c.opts.MCPTokenFile,
			"-listen-addr", c.opts.MCPListenAddr,
		)
	}
	return append(args,
		"-ingress", "cloudflare-access",
		"-cloudflare-access-config", c.opts.AccessConfig,
		"-listen-addr", c.opts.MCPListenAddr,
	)
}

func (c *Controller) startOpenAITunnel(cfg openAITunnelConfig) (*childHandle, error) {
	path, info, err := createHealthURLFile(c.opts.RuntimeRoot)
	if err != nil {
		return nil, problem(CodeOpenAIProfileInvalid)
	}
	c.mu.Lock()
	c.healthURLFile, c.healthURLFileInfo = path, info
	c.mu.Unlock()
	profile, err := openAIRunProfile(cfg, path, c.opts.MCPListenAddr)
	if err != nil {
		return nil, problem(CodeOpenAIProfileInvalid)
	}
	profilePath, profileInfo, err := createOwnedRuntimeFile(c.opts.RuntimeRoot, ".tunnel-client-run-*.yaml", []byte(profile))
	if err != nil {
		return nil, problem(CodeOpenAIProfileInvalid)
	}
	c.mu.Lock()
	c.runProfileFile, c.runProfileFileInfo = profilePath, profileInfo
	c.mu.Unlock()
	args := openAITunnelRunArgs(profilePath, cfg.tunnelID, path, c.opts.MCPListenAddr)
	child, err := c.startChildWithSecrets(c.opts.TunnelClientBinary, args, filteredOpenAIEnvironment(), cfg.apiKeyDigest, cfg.mcpTokenDigest)
	if err != nil {
		return nil, err
	}
	return child, nil
}

func (c *Controller) waitAuthenticatedMCP(ctx context.Context, h *childHandle, base Status, progress ProgressFunc, token string) error {
	deadline := time.NewTimer(c.opts.Timeout)
	defer deadline.Stop()
	endpoint := "http://" + c.opts.MCPListenAddr + "/mcp"
	for {
		if c.tcpReachable(ctx, c.opts.MCPListenAddr) {
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			err := authenticatedMCPProbe(probeCtx, endpoint, token)
			cancel()
			if err == nil {
				return nil
			}
			if errors.Is(err, errMCPProbeUnauthorized) {
				return problem(CodeOpenAIAuthRejected)
			}
			if errors.Is(err, errMCPProbeInvalid) {
				return problem(CodeOpenAIProfileInvalid)
			}
		}
		select {
		case <-h.done:
			if h.exitError() != nil {
				return problem(CodeMCPStartFailed)
			}
			return problem(CodeMCPNotReady)
		case <-ctx.Done():
			return problem(CodeCancelled)
		case <-deadline.C:
			return problem(CodeMCPNotReady)
		case <-time.After(c.opts.PollInterval):
			base.UpdatedAt = c.now()
			c.publish(progress, base)
		}
	}
}

func authenticatedMCPProbe(ctx context.Context, endpoint, token string) error {
	hostPort, ok := validateLoopbackMCPEndpoint(endpoint)
	if !ok {
		return errMCPProbeInvalid
	}
	baseTransport := loopbackOnlyTransport()
	defer baseTransport.CloseIdleConnections()
	transport := &localTokenRoundTripper{base: baseTransport, token: token, expectedHostPort: hostPort}
	client := mcp.NewClient(&mcp.Implementation{Name: "local-probe-preview-preflight", Version: "0.1.0"}, nil)
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient}, nil)
	if err != nil {
		if transport.unauthorized.Load() {
			return errMCPProbeUnauthorized
		}
		return errMCPProbeUnavailable
	}
	defer func() { _ = session.Close() }()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		if transport.unauthorized.Load() {
			return errMCPProbeUnauthorized
		}
		return errMCPProbeUnavailable
	}
	foundPing := false
	for _, tool := range tools.Tools {
		if tool.Name == mcpserver.ToolPing {
			foundPing = true
			break
		}
	}
	if !foundPing {
		return errMCPProbeInvalid
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: mcpserver.ToolPing, Arguments: map[string]any{}})
	if err != nil {
		if transport.unauthorized.Load() {
			return errMCPProbeUnauthorized
		}
		return errMCPProbeUnavailable
	}
	if result == nil || result.IsError {
		return errMCPProbeUnavailable
	}
	return nil
}

type localTokenRoundTripper struct {
	base             http.RoundTripper
	token            string
	expectedHostPort string
	unauthorized     atomic.Bool
}

func (t *localTokenRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL == nil || !strings.EqualFold(request.URL.Scheme, "http") || !strings.EqualFold(request.URL.Host, t.expectedHostPort) {
		return nil, errors.New("MCP preflight target is not the configured loopback endpoint")
	}
	base := t.base
	if base == nil {
		base = loopbackOnlyTransport()
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set(mcpserver.LocalTokenHeader, t.token)
	response, err := base.RoundTrip(clone)
	if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
		t.unauthorized.Store(true)
	}
	return response, err
}

func validateLoopbackMCPEndpoint(endpoint string) (string, bool) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "/mcp" || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" || !isLoopbackHost(u.Hostname()) || u.Port() == "" {
		return "", false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	return u.Host, true
}

func loopbackOnlyTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond, KeepAlive: 15 * time.Second}
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil || !isLoopbackHost(strings.Trim(host, "[]")) {
				return nil, errors.New("outbound connection is not loopback")
			}
			return dialer.DialContext(ctx, network, address)
		},
	}
}

func openAIHealthHTTPClient() *http.Client {
	return &http.Client{
		Transport: loopbackOnlyTransport(),
		Timeout:   900 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func validateCredentialFile(path string, missingCode, invalidCode Code) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return problem(missingCode)
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return problem(invalidCode)
	}
	return nil
}

func (c *Controller) waitOpenAITunnel(ctx context.Context, h *childHandle, base Status, progress ProgressFunc) (Status, error) {
	status := base
	deadline := time.NewTimer(c.opts.Timeout)
	defer deadline.Stop()
	healthClient := openAIHealthHTTPClient()
	defer healthClient.CloseIdleConnections()
	for {
		c.mu.Lock()
		path, info := c.healthURLFile, c.healthURLFileInfo
		c.mu.Unlock()
		if path == "" || info == nil {
			return status, problem(CodeOpenAIHealthURLInvalid)
		}
		baseURL, err := readOwnedHealthURL(path, info)
		if err != nil && !errors.Is(err, errHealthURLPending) {
			return status, problem(CodeOpenAIHealthURLInvalid)
		}
		if err == nil {
			select {
			case <-h.done:
				return status, problem(CodeOpenAITunnelNotReady)
			default:
			}
			if ctx.Err() != nil {
				return status, problem(CodeCancelled)
			}
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
			if requestErr == nil {
				response, requestErr := healthClient.Do(request)
				if requestErr == nil {
					_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
					_ = response.Body.Close()
					if response.StatusCode == http.StatusOK {
						select {
						case <-h.done:
							return status, problem(CodeOpenAITunnelNotReady)
						default:
						}
						if ctx.Err() != nil {
							return status, problem(CodeCancelled)
						}
						status.TunnelReady = true
						return status, nil
					}
				}
			}
		}
		select {
		case <-h.done:
			return status, problem(CodeOpenAITunnelNotReady)
		case <-ctx.Done():
			return status, problem(CodeCancelled)
		case <-deadline.C:
			return status, problem(CodeOpenAITunnelNotReady)
		case <-time.After(c.opts.PollInterval):
			status.UpdatedAt = c.now()
			c.publish(progress, status)
		}
	}
}

func readSecretFile(path string) (string, [sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	value, err := readSingleLineFile(path, 4096)
	if err != nil {
		return "", zero, err
	}
	if value == "" {
		return "", zero, errSecretEmpty
	}
	if len(value) < 16 {
		return "", zero, errors.New("secret is too short")
	}
	return value, sha256.Sum256([]byte(value)), nil
}

var errSecretEmpty = errors.New("secret is empty")

func readSingleLineFile(path string, maxBytes int) (string, error) {
	lstat, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !lstat.Mode().IsRegular() || lstat.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("file is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(lstat, opened) {
		return "", errors.New("file identity changed")
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil || len(b) > maxBytes {
		return "", errors.New("file is invalid")
	}
	text := strings.TrimSpace(string(b))
	if strings.ContainsAny(text, "\r\n") {
		return "", errors.New("value must be a single line")
	}
	return text, nil
}

func readBoundedFile(path string, maxBytes int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil || len(b) > maxBytes {
		return nil, errors.New("file is invalid")
	}
	return b, nil
}

func filteredOpenAIEnvironment() []string {
	filtered := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "OPENAI_") ||
			strings.HasPrefix(upper, "CONTROL_PLANE_") ||
			strings.HasPrefix(upper, "TUNNEL_CLIENT_") ||
			strings.HasPrefix(upper, "MCP_") ||
			strings.HasPrefix(upper, "HEALTH_") ||
			strings.HasPrefix(upper, "ADMIN_UI_") ||
			strings.HasPrefix(upper, "HARPOON_") ||
			upper == "TUNNEL_TOKEN" || upper == "TUNNEL_TOKEN_FILE" {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func createHealthURLFile(runtimeRoot string) (string, os.FileInfo, error) {
	return createOwnedRuntimeFile(runtimeRoot, ".tunnel-client-health-*.url", nil)
}

func createOwnedRuntimeFile(runtimeRoot, pattern string, content []byte) (string, os.FileInfo, error) {
	if err := os.MkdirAll(runtimeRoot, 0700); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(runtimeRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("runtime root is not a local directory")
	}
	f, err := os.CreateTemp(runtimeRoot, pattern)
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	if len(content) > 0 {
		if _, err = f.Write(content); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return "", nil, err
		}
	}
	createdInfo, statErr := f.Stat()
	closeErr := f.Close()
	if statErr != nil || closeErr != nil || !createdInfo.Mode().IsRegular() {
		_ = os.Remove(path)
		return "", nil, errors.New("cannot claim readiness file")
	}
	return path, createdInfo, nil
}

func openAIRunProfile(cfg openAITunnelConfig, healthURLPath, mcpListenAddr string) (string, error) {
	apiKeyRef, err := yamlSingleQuoted("file:" + filepath.ToSlash(filepath.Clean(cfg.apiKeyPath)))
	if err != nil {
		return "", err
	}
	tokenRef, err := yamlSingleQuoted("file:" + filepath.ToSlash(filepath.Clean(cfg.mcpTokenPath)))
	if err != nil {
		return "", err
	}
	healthPath, err := yamlSingleQuoted(filepath.ToSlash(filepath.Clean(healthURLPath)))
	if err != nil {
		return "", err
	}
	endpoint, err := yamlSingleQuoted("http://" + mcpListenAddr + "/mcp")
	if err != nil {
		return "", err
	}
	tunnelID, err := yamlSingleQuoted(cfg.tunnelID)
	if err != nil {
		return "", err
	}
	profile := "config_version: 1\n" +
		"control_plane:\n  base_url: https://api.openai.com\n  tunnel_id: " + tunnelID + "\n  api_key: " + apiKeyRef + "\n" +
		"health:\n  listen_addr: 127.0.0.1:0\n  url_file: " + healthPath + "\n" +
		"admin_ui:\n  open_browser: false\n" +
		"log:\n  level: info\n  format: struct-text\n" +
		"mcp:\n  server_urls:\n    - channel: main\n      url: " + endpoint + "\n" +
		"  extra_headers:\n    X-Local-Probe-Token: " + tokenRef + "\n" +
		"  discovery_extra_headers:\n    X-Local-Probe-Token: " + tokenRef + "\n" +
		"  startup_wait_timeout: 30s\n  max_concurrent_requests: 10\n"
	return profile, nil
}

func yamlSingleQuoted(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("runtime profile value has invalid characters")
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'", nil
}

func readOwnedHealthURL(path string, ownedInfo os.FileInfo) (string, error) {
	currentInfo, err := os.Lstat(path)
	if err != nil {
		return "", errHealthURLInvalid
	}
	if !currentInfo.Mode().IsRegular() || currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(ownedInfo, currentInfo) {
		return "", errHealthURLInvalid
	}
	b, err := readBoundedFile(path, 2048)
	if err != nil {
		return "", errHealthURLInvalid
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return "", errHealthURLPending
	}
	if strings.ContainsAny(text, "\r\n\x00") {
		return "", errHealthURLInvalid
	}
	u, err := url.Parse(text)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return "", errHealthURLInvalid
	}
	port := u.Port()
	if port == "" {
		return "", errHealthURLInvalid
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", errHealthURLInvalid
	}
	return "http://127.0.0.1:" + strconv.Itoa(portNumber), nil
}

func removeOwnedHealthURLFile(path string, ownedInfo os.FileInfo) error {
	if path == "" || ownedInfo == nil {
		return nil
	}
	currentInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !currentInfo.Mode().IsRegular() || !os.SameFile(ownedInfo, currentInfo) {
		return errors.New("readiness file ownership changed")
	}
	return os.Remove(path)
}

func resolveDefaultTunnelClient(repoRoot, primary string) string {
	if regularFile(primary) {
		return primary
	}
	legacy := filepath.Join(filepath.Dir(repoRoot), "_tools", "tunnel-client-v0.0.14-windows-amd64", "bin", "tunnel-client.exe")
	if regularFile(legacy) {
		return legacy
	}
	return primary
}

func safeRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func openAITunnelRunArgs(profilePath, tunnelID, healthURLPath, mcpListenAddr string) []string {
	return []string{
		"run", "--config", profilePath,
		"--control-plane.tunnel-id", tunnelID,
		"--health.listen-addr", "127.0.0.1:0",
		"--health.url-file", healthURLPath,
		"--mcp.server-url", fmt.Sprintf("url=http://%s/mcp,channel=main", mcpListenAddr),
	}
}
