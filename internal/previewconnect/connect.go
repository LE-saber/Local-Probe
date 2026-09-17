// Package previewconnect owns the deliberately narrow process lifecycle used
// by the Windows Preview application.  It is not a general command runner:
// the two child processes and every argument are fixed by the options below.
//
// The package reports safe, stable problem codes.  In particular, it never
// returns the tunnel token, puts it in an argument, or includes child output
// verbatim in an error.
package previewconnect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stage is the current bounded connection phase.
type Stage string

const (
	StageIdle           Stage = "idle"
	StageValidating     Stage = "validating"
	StageStartingMCP    Stage = "starting_mcp"
	StageWaitingMCP     Stage = "waiting_mcp"
	StageStartingTunnel Stage = "starting_tunnel"
	StageWaitingTunnel  Stage = "waiting_tunnel"
	StageReady          Stage = "ready"
	StageFailed         Stage = "failed"
	StageCancelled      Stage = "cancelled"
	StageStopping       Stage = "stopping"
)

// Code is a stable diagnosis identifier intended for a UI and support
// bundle.  Message and Remedy are safe Chinese text, not child stderr.
type Code string

const (
	CodeNone                Code = ""
	CodeAlreadyConnecting   Code = "already_connecting"
	CodeCancelled           Code = "cancelled"
	CodeMCPBinaryMissing    Code = "mcp_binary_missing"
	CodeCloudflaredMissing  Code = "cloudflared_missing"
	CodeConfigMissing       Code = "config_missing"
	CodeConfigInvalid       Code = "config_invalid"
	CodeAccessConfigInvalid Code = "access_config_invalid"
	CodeTunnelConfigInvalid Code = "tunnel_config_invalid"
	CodeTokenMissing        Code = "token_missing"
	CodeTokenEmpty          Code = "token_empty"
	CodeTokenInvalidFormat  Code = "token_invalid_format"
	CodeTokenRejected       Code = "token_rejected"
	CodeMCPPortInUse        Code = "mcp_port_in_use"
	CodeMetricsPortInUse    Code = "metrics_port_in_use"
	CodeMCPStartFailed      Code = "mcp_start_failed"
	CodeMCPNotReady         Code = "mcp_not_ready"
	CodeTunnelStartFailed   Code = "tunnel_start_failed"
	CodeTunnelNotReady      Code = "tunnel_not_ready"
	CodeNoEdgeConnections   Code = "no_edge_connections"
	CodeEdgeUnreachable     Code = "edge_unreachable"
	CodeOriginUnavailable   Code = "origin_unavailable"
	CodeInternal            Code = "internal_error"
)

// Status is safe to display directly in a desktop UI.  TokenConfigured only
// says that a local token file passed shape checks; it is never the token.
type Status struct {
	Stage           Stage     `json:"stage"`
	Code            Code      `json:"code,omitempty"`
	Message         string    `json:"message,omitempty"`
	Remedy          string    `json:"remedy,omitempty"`
	PublicHost      string    `json:"public_host,omitempty"`
	CredentialHint  string    `json:"credential_hint,omitempty"`
	TokenConfigured bool      `json:"token_configured"`
	MCPReady        bool      `json:"mcp_ready"`
	TunnelReady     bool      `json:"tunnel_ready"`
	EdgeConnections int       `json:"edge_connections"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

// Problem is returned for a failed connection.  Its Error method is safe for
// logs and UI, and intentionally does not include paths, token text, or raw
// process output.
type Problem struct {
	Code    Code
	Message string
	Remedy  string
}

func (p *Problem) Error() string {
	if p == nil {
		return ""
	}
	if p.Message == "" {
		return string(p.Code)
	}
	return string(p.Code) + ": " + p.Message
}

// Options describes only the supported Preview layout.  Empty path fields
// are filled from RepoRoot by DefaultOptions/New.  Callers must not use this
// type to turn the package into a generic process launcher.
type Options struct {
	RepoRoot          string
	RuntimeRoot       string
	SecretRoot        string
	MCPBinary         string
	CloudflaredBinary string
	MCPConfig         string
	AccessConfig      string
	TunnelConfig      string
	TokenFile         string
	MCPListenAddr     string
	Timeout           time.Duration
	PollInterval      time.Duration
}

// DefaultOptions returns the repository layout used by the Preview build.
// The secret root deliberately lives beside the repository, not in it.
func DefaultOptions(repoRoot string) Options {
	repoRoot = filepath.Clean(repoRoot)
	runtimeRoot := filepath.Join(repoRoot, ".runtime")
	secretRoot := filepath.Join(filepath.Dir(repoRoot), ".secrets")
	return Options{
		RepoRoot:          repoRoot,
		RuntimeRoot:       runtimeRoot,
		SecretRoot:        secretRoot,
		MCPBinary:         filepath.Join(repoRoot, "bin", "local-probe-mcp.exe"),
		CloudflaredBinary: filepath.Join(filepath.Dir(repoRoot), "_tools", "tunnel-client-v0.0.14-windows-amd64", "bin", "cloudflared.exe"),
		MCPConfig:         filepath.Join(runtimeRoot, "local-probe.json"),
		AccessConfig:      filepath.Join(runtimeRoot, "cloudflare-access.json"),
		TunnelConfig:      filepath.Join(runtimeRoot, "cloudflare-tunnel.json"),
		TokenFile:         filepath.Join(secretRoot, "cloudflared-tunnel-token.txt"),
		MCPListenAddr:     "127.0.0.1:8788",
		Timeout:           30 * time.Second,
		PollInterval:      150 * time.Millisecond,
	}
}

// ProgressFunc receives immutable status snapshots.  It is called without
// the controller lock and may update a UI asynchronously.
type ProgressFunc func(Status)

type child interface {
	Wait() error
	Kill() error
}

type childHandle struct {
	process child
	done    chan struct{}
	mu      sync.Mutex
	err     error
	output  *safeOutput
}

type childStarter func(path string, args []string, dir string, env []string, stdout, stderr io.Writer) (child, error)

// Controller starts only the configured MCP binary and cloudflared binary.
// It does not inspect or control unrelated processes.
type Controller struct {
	mu       sync.Mutex
	opts     Options
	status   Status
	starting bool
	mcp      *childHandle
	tunnel   *childHandle
	start    childStarter
	dial     func(context.Context, string, string) (net.Conn, error)
	http     *http.Client
	now      func() time.Time
}

// New validates the static layout and returns a controller.  It does not
// start processes or contact the network.
func New(opts Options) (*Controller, error) {
	opts, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	return &Controller{
		opts:   opts,
		status: Status{Stage: StageIdle, CredentialHint: credentialHint(opts)},
		start:  startCommand,
		dial:   (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext,
		http:   &http.Client{Timeout: 900 * time.Millisecond},
		now:    time.Now,
	}, nil
}

// Options returns a copy with normalized default paths.  It is useful to a
// host that wants to display a setup hint, but never contains token content.
func (c *Controller) Options() Options {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opts
}

// Status returns a safe snapshot.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Connect validates, starts and waits for the local MCP and Cloudflare
// metrics/HA evidence.  It is synchronous so callers can put it in a UI
// goroutine, while ctx and progress provide cancellation and live updates.
func (c *Controller) Connect(ctx context.Context, progress ProgressFunc) (Status, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.starting {
		status := c.status
		c.mu.Unlock()
		return status, problem(CodeAlreadyConnecting)
	}
	c.starting = true
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		c.starting = false
		c.mu.Unlock()
	}()

	begin := c.now()
	c.publish(progress, Status{Stage: StageValidating, StartedAt: begin, UpdatedAt: begin, CredentialHint: credentialHint(c.opts)})
	validated, err := c.validate()
	if err != nil {
		return c.fail(progress, begin, err)
	}
	base := Status{Stage: StageStartingMCP, StartedAt: begin, UpdatedAt: c.now(), PublicHost: validated.tunnel.PublicHost, CredentialHint: credentialHint(c.opts), TokenConfigured: validated.tokenConfigured}
	c.publish(progress, base)

	if c.tcpReachable(ctx, c.opts.MCPListenAddr) {
		return c.fail(progress, begin, problem(CodeMCPPortInUse))
	}
	mcpArgs := []string{
		"-config", c.opts.MCPConfig,
		"-ingress", "cloudflare-access",
		"-cloudflare-access-config", c.opts.AccessConfig,
		"-listen-addr", c.opts.MCPListenAddr,
	}
	mcp, startErr := c.startChild(c.opts.MCPBinary, mcpArgs, validated.tokenDigest)
	if startErr != nil {
		return c.fail(progress, begin, problem(CodeMCPStartFailed))
	}
	c.setChild(true, mcp)
	base.Stage = StageWaitingMCP
	c.publish(progress, base)
	if waitErr := c.waitMCP(ctx, mcp, base, progress); waitErr != nil {
		c.stopChildren()
		return c.fail(progress, begin, waitErr)
	}
	base.MCPReady = true
	base.Stage = StageStartingTunnel
	base.UpdatedAt = c.now()
	c.publish(progress, base)

	if c.tcpReachable(ctx, validated.tunnel.MetricsAddr) {
		c.stopChildren()
		return c.fail(progress, begin, problem(CodeMetricsPortInUse))
	}
	tunnelArgs := []string{"tunnel", "--no-autoupdate", "--metrics", validated.tunnel.MetricsAddr, "run", "--token-file", c.opts.TokenFile}
	env := []string(nil)
	if validated.tunnel.TransportProtocol != "" && validated.tunnel.TransportProtocol != "auto" {
		env = []string{"TUNNEL_TRANSPORT_PROTOCOL=" + validated.tunnel.TransportProtocol}
	}
	tunnel, tunnelErr := c.startChildWithEnv(c.opts.CloudflaredBinary, tunnelArgs, env, validated.tokenDigest)
	if tunnelErr != nil {
		c.stopChildren()
		return c.fail(progress, begin, problem(CodeTunnelStartFailed))
	}
	c.setChild(false, tunnel)
	base.Stage = StageWaitingTunnel
	base.UpdatedAt = c.now()
	c.publish(progress, base)

	status, waitErr := c.waitTunnel(ctx, tunnel, validated.tunnel, base, progress)
	if waitErr != nil {
		c.stopChildren()
		return c.fail(progress, begin, waitErr)
	}
	status.Stage = StageReady
	status.Code = CodeNone
	status.Message = "本地 MCP 与 Cloudflare Tunnel 已连接"
	status.Remedy = "网页端 GPT 现在可以尝试连接；如果网页端仍失败，请检查 ChatGPT 端的 MCP/Access 授权。"
	status.UpdatedAt = c.now()
	c.publish(progress, status)
	return status, nil
}

// Reconnect stops only child processes owned by this controller, then starts
// a fresh connection.  Unrelated listeners are never killed.
func (c *Controller) Reconnect(ctx context.Context, progress ProgressFunc) (Status, error) {
	_ = c.Stop()
	return c.Connect(ctx, progress)
}

// Stop terminates only the two processes started by this controller.  It is
// safe to call repeatedly and does not touch an unrelated process on either
// configured port.
func (c *Controller) Stop() error {
	c.mu.Lock()
	c.status.Stage = StageStopping
	c.status.UpdatedAt = c.now()
	mcp, tunnel := c.mcp, c.tunnel
	c.mcp, c.tunnel = nil, nil
	c.mu.Unlock()

	var first error
	for _, item := range []*childHandle{tunnel, mcp} {
		if item == nil || item.process == nil {
			continue
		}
		if err := item.process.Kill(); err != nil && !isAlreadyExited(err) && first == nil {
			first = errors.New("owned process stop failed")
		}
		select {
		case <-item.done:
		case <-time.After(2 * time.Second):
			if first == nil {
				first = errors.New("owned process did not exit")
			}
		}
	}
	c.mu.Lock()
	if first == nil {
		c.status.Stage = StageIdle
		c.status.Code = CodeNone
		c.status.Message = "未连接"
		c.status.Remedy = "点击“一键连接”启动本地 MCP 与 Tunnel。"
	}
	c.status.UpdatedAt = c.now()
	c.mu.Unlock()
	return first
}

type validatedConfig struct {
	tunnel          tunnelConfig
	tokenConfigured bool
	tokenDigest     [sha256.Size]byte
}

type tunnelConfig struct {
	PublicHost        string
	OriginURL         string
	MetricsAddr       string
	TransportProtocol string
}

func normalizeOptions(in Options) (Options, error) {
	if strings.TrimSpace(in.RepoRoot) == "" {
		return Options{}, errors.New("previewconnect: repo root is required")
	}
	defaults := DefaultOptions(in.RepoRoot)
	if in.RuntimeRoot == "" {
		in.RuntimeRoot = defaults.RuntimeRoot
	}
	if in.SecretRoot == "" {
		in.SecretRoot = defaults.SecretRoot
	}
	if in.MCPBinary == "" {
		in.MCPBinary = defaults.MCPBinary
	}
	if in.CloudflaredBinary == "" {
		in.CloudflaredBinary = defaults.CloudflaredBinary
	}
	if in.MCPConfig == "" {
		in.MCPConfig = defaults.MCPConfig
	}
	if in.AccessConfig == "" {
		in.AccessConfig = defaults.AccessConfig
	}
	if in.TunnelConfig == "" {
		in.TunnelConfig = defaults.TunnelConfig
	}
	if in.TokenFile == "" {
		in.TokenFile = defaults.TokenFile
	}
	if in.MCPListenAddr == "" {
		in.MCPListenAddr = defaults.MCPListenAddr
	}
	if in.Timeout <= 0 {
		in.Timeout = defaults.Timeout
	}
	if in.Timeout > 2*time.Minute {
		return Options{}, errors.New("previewconnect: timeout is too large")
	}
	if in.PollInterval <= 0 {
		in.PollInterval = defaults.PollInterval
	}
	if in.PollInterval > time.Second {
		return Options{}, errors.New("previewconnect: poll interval is too large")
	}
	if !isLoopbackAddr(in.MCPListenAddr) {
		return Options{}, errors.New("previewconnect: MCP must bind loopback")
	}
	return in, nil
}

func (c *Controller) validate() (validatedConfig, error) {
	if !regularFile(c.opts.MCPBinary) {
		return validatedConfig{}, problem(CodeMCPBinaryMissing)
	}
	if !regularFile(c.opts.CloudflaredBinary) {
		return validatedConfig{}, problem(CodeCloudflaredMissing)
	}
	if !regularFile(c.opts.MCPConfig) {
		return validatedConfig{}, problem(CodeConfigMissing)
	}
	if !regularFile(c.opts.AccessConfig) {
		return validatedConfig{}, problem(CodeAccessConfigInvalid)
	}
	if !regularFile(c.opts.TunnelConfig) {
		return validatedConfig{}, problem(CodeTunnelConfigInvalid)
	}
	tunnel, err := loadTunnelConfig(c.opts.TunnelConfig, c.opts.MCPListenAddr)
	if err != nil {
		return validatedConfig{}, err
	}
	if err := validateAccessConfig(c.opts.AccessConfig, tunnel.PublicHost); err != nil {
		return validatedConfig{}, err
	}
	if !validJSONFile(c.opts.MCPConfig) {
		return validatedConfig{}, problem(CodeConfigInvalid)
	}
	tokenDigest, tokenErr := validateToken(c.opts.TokenFile)
	if tokenErr != nil {
		return validatedConfig{}, tokenErr
	}
	return validatedConfig{tunnel: tunnel, tokenConfigured: true, tokenDigest: tokenDigest}, nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func validJSONFile(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && json.Valid(b)
}

func loadTunnelConfig(path, mcpListenAddr string) (tunnelConfig, error) {
	var raw struct {
		PublicHost        string `json:"public_host"`
		OriginURL         string `json:"origin_url"`
		MetricsAddr       string `json:"metrics_addr"`
		TransportProtocol string `json:"transport_protocol"`
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &raw) != nil {
		return tunnelConfig{}, problem(CodeTunnelConfigInvalid)
	}
	raw.PublicHost, raw.OriginURL, raw.MetricsAddr, raw.TransportProtocol = strings.TrimSpace(raw.PublicHost), strings.TrimSpace(raw.OriginURL), strings.TrimSpace(raw.MetricsAddr), strings.ToLower(strings.TrimSpace(raw.TransportProtocol))
	if raw.PublicHost == "" || strings.ContainsAny(raw.PublicHost, "/\\ ") || !isLoopbackAddr(raw.MetricsAddr) || raw.OriginURL == "" {
		return tunnelConfig{}, problem(CodeTunnelConfigInvalid)
	}
	u, err := url.Parse(raw.OriginURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || !sameLoopbackEndpoint(u.Host, mcpListenAddr) {
		return tunnelConfig{}, problem(CodeTunnelConfigInvalid)
	}
	if raw.TransportProtocol == "" {
		raw.TransportProtocol = "auto"
	}
	if raw.TransportProtocol != "auto" && raw.TransportProtocol != "quic" && raw.TransportProtocol != "http2" {
		return tunnelConfig{}, problem(CodeTunnelConfigInvalid)
	}
	return tunnelConfig{PublicHost: raw.PublicHost, OriginURL: raw.OriginURL, MetricsAddr: raw.MetricsAddr, TransportProtocol: raw.TransportProtocol}, nil
}

func validateAccessConfig(path, publicHost string) error {
	var raw struct {
		Issuer      string            `json:"issuer"`
		JWKSURL     string            `json:"jwks_url"`
		Audience    string            `json:"audience"`
		Principal   map[string]string `json:"principal_to_connection"`
		PublicHosts []string          `json:"public_hosts"`
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &raw) != nil || strings.TrimSpace(raw.Issuer) == "" || strings.TrimSpace(raw.JWKSURL) == "" || strings.TrimSpace(raw.Audience) == "" || len(raw.Principal) == 0 {
		return problem(CodeAccessConfigInvalid)
	}
	issuer, issuerErr := url.Parse(raw.Issuer)
	jwks, jwksErr := url.Parse(raw.JWKSURL)
	if issuerErr != nil || jwksErr != nil || issuer.Scheme != "https" || jwks.Scheme != "https" {
		return problem(CodeAccessConfigInvalid)
	}
	for _, host := range raw.PublicHosts {
		if strings.EqualFold(strings.TrimSpace(host), publicHost) {
			return nil
		}
	}
	return problem(CodeAccessConfigInvalid)
}

func validateToken(path string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return digest, problem(CodeTokenMissing)
		}
		return digest, problem(CodeTokenInvalidFormat)
	}
	defer f.Close()
	const maxTokenBytes = 4096
	b, err := io.ReadAll(io.LimitReader(f, maxTokenBytes+1))
	if err != nil {
		return digest, problem(CodeTokenInvalidFormat)
	}
	trimmed := bytes.TrimSuffix(bytes.TrimSuffix(b, []byte("\n")), []byte("\r"))
	if len(trimmed) == 0 {
		return digest, problem(CodeTokenEmpty)
	}
	if len(b) > maxTokenBytes || bytes.ContainsAny(trimmed, "\r\n") {
		return digest, problem(CodeTokenInvalidFormat)
	}
	if len(trimmed) < 16 {
		return digest, problem(CodeTokenInvalidFormat)
	}
	return sha256.Sum256(trimmed), nil
}

func (c *Controller) startChild(path string, args []string, tokenDigest [sha256.Size]byte) (*childHandle, error) {
	return c.startChildWithEnv(path, args, nil, tokenDigest)
}

func (c *Controller) startChildWithEnv(path string, args []string, extraEnv []string, tokenDigest [sha256.Size]byte) (*childHandle, error) {
	stdout := newSafeOutput(16*1024, tokenDigest)
	stderr := newSafeOutput(32*1024, tokenDigest)
	env := filteredEnvironment(extraEnv)
	p, err := c.start(path, args, c.opts.RepoRoot, env, stdout, stderr)
	if err != nil {
		return nil, err
	}
	h := &childHandle{process: p, done: make(chan struct{}), output: stderr}
	go func() {
		err := p.Wait()
		h.mu.Lock()
		h.err = err
		h.mu.Unlock()
		close(h.done)
	}()
	return h, nil
}

func (h *childHandle) exitError() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

func (c *Controller) setChild(mcp bool, h *childHandle) {
	c.mu.Lock()
	if mcp {
		c.mcp = h
	} else {
		c.tunnel = h
	}
	c.mu.Unlock()
}

func (c *Controller) waitMCP(ctx context.Context, h *childHandle, base Status, progress ProgressFunc) error {
	deadline := time.NewTimer(c.opts.Timeout)
	defer deadline.Stop()
	for {
		if c.tcpReachable(ctx, c.opts.MCPListenAddr) {
			return nil
		}
		select {
		case <-h.done:
			if err := h.exitError(); err != nil {
				return classifyChildFailure(CodeMCPStartFailed, h.output.String())
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

func (c *Controller) waitTunnel(ctx context.Context, h *childHandle, config tunnelConfig, base Status, progress ProgressFunc) (Status, error) {
	status := base
	deadline := time.NewTimer(c.opts.Timeout)
	defer deadline.Stop()
	for {
		ready, edge := c.metrics(config.MetricsAddr)
		status.TunnelReady, status.EdgeConnections = ready, edge
		if ready && edge > 0 {
			return status, nil
		}
		select {
		case <-h.done:
			if err := h.exitError(); err != nil {
				return status, classifyTunnelFailure(h.output.String())
			}
			return status, classifyTunnelFailure(h.output.String())
		case <-ctx.Done():
			return status, problem(CodeCancelled)
		case <-deadline.C:
			if outputHasOriginFailure(h.output.String()) {
				return status, problem(CodeOriginUnavailable)
			}
			if outputHasNetworkFailure(h.output.String()) {
				return status, problem(CodeEdgeUnreachable)
			}
			if status.TunnelReady {
				return status, problem(CodeNoEdgeConnections)
			}
			return status, problem(CodeTunnelNotReady)
		case <-time.After(c.opts.PollInterval):
			status.UpdatedAt = c.now()
			c.publish(progress, status)
		}
	}
}

func (c *Controller) metrics(addr string) (bool, int) {
	ready := false
	resp, err := c.http.Get("http://" + addr + "/ready")
	if err == nil {
		ready = resp.StatusCode == http.StatusOK
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if !ready {
		return false, 0
	}
	resp, err = c.http.Get("http://" + addr + "/metrics")
	if err != nil {
		return true, 0
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return true, 0
	}
	return true, parseHAConnections(b)
}

var haMetric = regexp.MustCompile(`(?m)^cloudflared_tunnel_ha_connections(?:\{[^}\r\n]*\})?\s+([0-9]+(?:\.[0-9]+)?)\s*$`)

func parseHAConnections(body []byte) int {
	max := 0
	for _, match := range haMetric.FindAllSubmatch(body, -1) {
		if len(match) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(string(match[1]), 64)
		if err == nil && int(v) > max {
			max = int(v)
		}
	}
	return max
}

func (c *Controller) tcpReachable(ctx context.Context, addr string) bool {
	conn, err := c.dial(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (c *Controller) stopChildren() { _ = c.Stop() }

func (c *Controller) publish(progress ProgressFunc, status Status) {
	status.CredentialHint = credentialHint(c.opts)
	status.UpdatedAt = c.now()
	c.mu.Lock()
	c.status = status
	c.mu.Unlock()
	if progress != nil {
		progress(status)
	}
}

func (c *Controller) fail(progress ProgressFunc, begin time.Time, p error) (Status, error) {
	problemValue := asProblem(p)
	status := c.Status()
	status.Stage = StageFailed
	if problemValue.Code == CodeCancelled {
		status.Stage = StageCancelled
	}
	status.Code, status.Message, status.Remedy = problemValue.Code, problemValue.Message, problemValue.Remedy
	status.StartedAt, status.UpdatedAt = begin, c.now()
	c.publish(progress, status)
	return status, problemValue
}

func asProblem(err error) *Problem {
	var p *Problem
	if errors.As(err, &p) {
		return p
	}
	return problem(CodeInternal)
}

func problem(code Code) *Problem {
	messages := map[Code][2]string{
		CodeAlreadyConnecting:   {"正在连接，请等待当前操作完成。", "不要重复点击连接按钮。"},
		CodeCancelled:           {"连接已取消。", "再次点击“一键连接”重试。"},
		CodeMCPBinaryMissing:    {"找不到本地 MCP 程序。", "重新运行 Preview 构建/安装步骤，确保 bin\\local-probe-mcp.exe 存在。"},
		CodeCloudflaredMissing:  {"找不到 cloudflared 程序。", "安装或恢复 Cloudflare Tunnel 客户端后再重试。"},
		CodeConfigMissing:       {"找不到 Local-Probe 配置。", "先完成初始化配置，再点击连接。"},
		CodeConfigInvalid:       {"Local-Probe 配置无效。", "重新生成 .runtime/local-probe.json，并确认 JSON 未被手动破坏。"},
		CodeAccessConfigInvalid: {"Cloudflare Access 配置无效。", "检查 issuer、JWKS、audience、principal 映射和 public host。"},
		CodeTunnelConfigInvalid: {"Tunnel 配置无效。", "检查 public host、loopback origin、metrics 地址和传输协议。"},
		CodeTokenMissing:        {"没有填写 Tunnel token。", "在 .secrets/cloudflared-tunnel-token.txt 写入 Cloudflare Tunnel token，每行一个。"},
		CodeTokenEmpty:          {"Tunnel token 文件为空。", "在 .secrets/cloudflared-tunnel-token.txt 写入有效 token 后重试。"},
		CodeTokenInvalidFormat:  {"Tunnel token 格式不正确。", "只保留一行有效 token，删除多余文本或换行。"},
		CodeTokenRejected:       {"Cloudflare 拒绝了 Tunnel token。", "重新从 Cloudflare 复制当前 Tunnel token；旧 token 可能已撤销或已过期。"},
		CodeMCPPortInUse:        {"本地 MCP 端口已被占用。", "关闭占用 127.0.0.1:8788 的旧实例，或清理旧连接后重试。"},
		CodeMetricsPortInUse:    {"Tunnel metrics 端口已被占用。", "关闭占用 metrics 端口的旧 cloudflared 实例后重试。"},
		CodeMCPStartFailed:      {"本地 MCP 启动失败。", "检查配置、权限和本地审计目录；查看诊断日志中的启动阶段。"},
		CodeMCPNotReady:         {"本地 MCP 未在规定时间内就绪。", "确认 127.0.0.1:8788 未被防火墙或其他程序占用，然后重试。"},
		CodeTunnelStartFailed:   {"Tunnel 客户端启动失败。", "确认 cloudflared 文件可执行且 token/config 已准备好。"},
		CodeTunnelNotReady:      {"Tunnel 尚未连接到 Cloudflare。", "检查网络、防火墙和 Cloudflare Tunnel 状态；允许出站 TCP/UDP 7844 或使用 http2。"},
		CodeNoEdgeConnections:   {"Tunnel metrics 已启动但没有活动边缘连接。", "检查网络、防火墙和 Tunnel token，然后重试。"},
		CodeEdgeUnreachable:     {"无法连接 Cloudflare 边缘。", "允许出站 TCP/UDP 7844；若 QUIC 被阻断，将传输协议改为 http2。"},
		CodeOriginUnavailable:   {"Tunnel 已运行但无法访问本地 MCP。", "确认 MCP 仍监听 127.0.0.1:8788，并检查 origin 地址配置。"},
		CodeInternal:            {"连接过程中发生内部错误。", "刷新应用后重试；若仍失败，请导出脱敏诊断。"},
	}
	entry, ok := messages[code]
	if !ok {
		entry = messages[CodeInternal]
	}
	return &Problem{Code: code, Message: entry[0], Remedy: entry[1]}
}

func classifyChildFailure(defaultCode Code, output string) error {
	if outputHasOriginFailure(output) {
		return problem(CodeOriginUnavailable)
	}
	if outputHasNetworkFailure(output) {
		return problem(CodeEdgeUnreachable)
	}
	return problem(defaultCode)
}

func classifyTunnelFailure(output string) error {
	if outputHasTokenFailure(output) {
		return problem(CodeTokenRejected)
	}
	if outputHasOriginFailure(output) {
		return problem(CodeOriginUnavailable)
	}
	if outputHasNetworkFailure(output) {
		return problem(CodeEdgeUnreachable)
	}
	return problem(CodeTunnelStartFailed)
}

func outputHasTokenFailure(output string) bool {
	s := strings.ToLower(output)
	for _, phrase := range []string{"invalid tunnel credentials", "invalid tunnel token", "token is invalid", "token rejected", "unauthorized tunnel", "authentication failed", "failed to unmarshal token", "credential rejected"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func outputHasNetworkFailure(output string) bool {
	s := strings.ToLower(output)
	for _, phrase := range []string{"unable to establish connection with cloudflare edge", "failed to dial", "tls handshake with edge", "connection is blocked or unreachable", "no recent network activity", "connection refused", "network is unreachable", "i/o timeout"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func outputHasOriginFailure(output string) bool {
	s := strings.ToLower(output)
	for _, phrase := range []string{"unable to reach the origin", "failed to proxy", "connect tcp 127.0.0.1:8788: connectex", "dial tcp 127.0.0.1:8788: connect: connection refused", "dial tcp 127.0.0.1:8788: connectex"} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func credentialHint(opts Options) string {
	if rel, err := filepath.Rel(opts.RepoRoot, opts.TokenFile); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return ".secrets/cloudflared-tunnel-token.txt"
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	return err == nil && isLoopbackHost(strings.Trim(host, "[]"))
}
func isLoopbackHost(host string) bool { ip := net.ParseIP(host); return ip != nil && ip.IsLoopback() }

func sameLoopbackEndpoint(originHostPort, listenHostPort string) bool {
	originHost, originPort, originErr := net.SplitHostPort(originHostPort)
	listenHost, listenPort, listenErr := net.SplitHostPort(listenHostPort)
	if originErr != nil || listenErr != nil || originPort != listenPort {
		return false
	}
	originIP, listenIP := net.ParseIP(strings.Trim(originHost, "[]")), net.ParseIP(strings.Trim(listenHost, "[]"))
	return originIP != nil && listenIP != nil && originIP.IsLoopback() && listenIP.IsLoopback() && originIP.Equal(listenIP)
}
func isAlreadyExited(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "process already finished")
}

// safeOutput is a bounded, non-persistent diagnostic tail.  It also strips
// obvious token-shaped material and never returns more than its configured
// capacity.
type safeOutput struct {
	mu          sync.Mutex
	max         int
	b           bytes.Buffer
	tokenDigest [sha256.Size]byte
}

func newSafeOutput(max int, tokenDigest [sha256.Size]byte) *safeOutput {
	return &safeOutput{max: max, tokenDigest: tokenDigest}
}
func (s *safeOutput) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cleaned := sanitizeOutputWithDigest(string(p), s.tokenDigest)
	if len(cleaned) > s.max {
		cleaned = cleaned[len(cleaned)-s.max:]
	}
	s.b.WriteString(cleaned)
	if s.b.Len() > s.max {
		data := s.b.Bytes()
		s.b.Reset()
		s.b.Write(data[len(data)-s.max:])
	}
	return len(p), nil
}
func (s *safeOutput) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func sanitizeOutput(s string) string { return sanitizeOutputWithDigest(s, [sha256.Size]byte{}) }

var secretCandidate = regexp.MustCompile(`\b[A-Za-z0-9_.-]{16,}\b`)

func sanitizeOutputWithDigest(s string, digest [sha256.Size]byte) string {
	s = secretCandidate.ReplaceAllStringFunc(s, func(candidate string) string {
		if digest != ([sha256.Size]byte{}) && sha256.Sum256([]byte(candidate)) == digest {
			return "<redacted-token>"
		}
		return candidate
	})
	s = strings.ReplaceAll(s, "cloudflared-tunnel-token.txt", "<token-file>")
	s = regexp.MustCompile(`(?i)(token|secret|authorization|bearer)[=: ]+[^\s,]+`).ReplaceAllString(s, "$1=<redacted>")
	s = regexp.MustCompile(`\b[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`).ReplaceAllString(s, "<redacted-token>")
	return s
}

func startCommand(path string, args []string, dir string, env []string, stdout, stderr io.Writer) (child, error) {
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	prepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return commandChild{cmd: cmd}, nil
}

func filteredEnvironment(extra []string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "TUNNEL_TRANSPORT_PROTOCOL", "TUNNEL_TOKEN", "TUNNEL_TOKEN_FILE":
			continue
		}
		env = append(env, entry)
	}
	return append(env, extra...)
}

type commandChild struct{ cmd *exec.Cmd }

func (c commandChild) Wait() error { return c.cmd.Wait() }
func (c commandChild) Kill() error {
	if c.cmd.Process == nil {
		return errors.New("process already finished")
	}
	return c.cmd.Process.Kill()
}
