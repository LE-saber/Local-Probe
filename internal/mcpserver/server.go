// Package mcpserver exposes the read-only Local-Probe core through the
// official MCP Go SDK. The package deliberately owns no listener: callers
// choose the listener and should bind it to loopback.
package mcpserver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/cfaccess"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	SchemaVersion = "local-probe.mcp.v1"
	ServerName    = "local-probe"
	ServerVersion = "0.1.0-alpha"

	ToolServerInfo   = "server_info"
	ToolPing         = "ping"
	ToolReadFile     = "read_file"
	ToolBatchRead    = "batch_read"
	LocalTokenHeader = "X-Local-Probe-Token"

	// CloudflareAccessHeader is re-exported for callers that need to construct
	// a local integration test request without depending on the cfaccess
	// package's transport details.
	CloudflareAccessHeader = cfaccess.AccessJWTHeader

	defaultMaxRequestBodyBytes = 1 << 20
	defaultMaxResponseBytes    = 512 << 10
	maxTokenBytes              = 4096
)

var (
	ErrInvalidOptions = errors.New("invalid MCP server options")
	ErrUnauthorized   = errors.New("MCP authentication failed")
	ErrToolDenied     = errors.New("MCP tool access denied")
)

// SourceBinder is implemented by rootfs.Source. Keeping this small interface
// lets the MCP layer be tested with a deterministic readcore source without
// weakening the production rootfs boundary.
type SourceBinder interface {
	Bind(policy.BoundScope) (readcore.Source, error)
}

// Credential is a local bearer token mapped to an already configured
// connection. Token material must be loaded by the caller from an environment
// variable, a protected file, or an OS secret store; it must not be persisted
// in configuration or logs.
type Credential struct {
	ConnectionID string
	Token        string
}

// Options configures one read-only MCP endpoint. Manager and Source must be
// built from the same config.Store snapshot family. Credentials are runtime
// values and are never serialized by this package.
type Options struct {
	Manager     *policy.Manager
	Source      SourceBinder
	Credentials []Credential
	// CloudflareAccess selects the explicit Cloudflare Access ingress. When it
	// is non-nil, local Credentials must be empty and every request must carry
	// a valid Cf-Access-Jwt-Assertion plus a trusted public Host.
	CloudflareAccess      *cfaccess.Verifier
	CloudflarePublicHosts []string
	Limits                readcore.Limits
	MaxBodyBytes          int64
	// MaxResponseBytes bounds the serialized MCP CallToolResult content. The
	// JSON-RPC envelope adds a small amount of transport overhead; callers that
	// impose a hard wire cap should leave room for that envelope.
	MaxResponseBytes int64
}

// Server is an authenticated, read-only MCP handler. It is safe to share
// across concurrent HTTP requests.
type Server struct {
	manager          *policy.Manager
	source           SourceBinder
	limits           readcore.Limits
	maxBodyBytes     int64
	maxResponseBytes int64
	credentials      []credential
	cloudflareAccess *cfaccess.Verifier
	cloudflareHosts  map[string]hostPattern
	requestID        atomic.Uint64
	handler          http.Handler
}

type credential struct {
	connectionID string
	token        []byte
}

type hostPattern struct {
	host string
	port string
}

// New creates an MCP server backed by the current policy and rootfs adapter.
// It does not open a socket. The returned Handler uses bearer authentication
// and the SDK's Streamable HTTP implementation.
func New(opts Options) (*Server, error) {
	if opts.Manager == nil || opts.Source == nil {
		return nil, fmt.Errorf("%w: manager and source are required", ErrInvalidOptions)
	}
	limits := opts.Limits
	if limits == (readcore.Limits{}) {
		limits = readcore.DefaultLimits()
	}
	// readcore validates limits when an Engine is created. Do it here so a bad
	// process configuration fails before a listener is exposed. The validation
	// source is never used for a request.
	if _, err := readcore.New(validationSource{}, limits); err != nil {
		return nil, fmt.Errorf("%w: invalid limits", ErrInvalidOptions)
	}
	maxBody := opts.MaxBodyBytes
	if maxBody == 0 {
		maxBody = defaultMaxRequestBodyBytes
	}
	if maxBody < 1 || maxBody > 16<<20 {
		return nil, fmt.Errorf("%w: request body limit out of bounds", ErrInvalidOptions)
	}
	maxResponse := opts.MaxResponseBytes
	if maxResponse == 0 {
		maxResponse = defaultMaxResponseBytes
	}
	if maxResponse < 4<<10 || maxResponse > 16<<20 {
		return nil, fmt.Errorf("%w: response size limit out of bounds", ErrInvalidOptions)
	}

	if opts.CloudflareAccess != nil && len(opts.Credentials) > 0 {
		return nil, fmt.Errorf("%w: local credentials and Cloudflare Access ingress cannot be combined", ErrInvalidOptions)
	}
	if opts.CloudflareAccess == nil && len(opts.Credentials) == 0 {
		return nil, fmt.Errorf("%w: at least one credential is required", ErrInvalidOptions)
	}
	hosts := make(map[string]hostPattern)
	if opts.CloudflareAccess != nil {
		var err error
		hosts, err = validateCloudflareHosts(opts.CloudflarePublicHosts)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid Cloudflare public host", ErrInvalidOptions)
		}
		for _, connectionID := range opts.CloudflareAccess.ConnectionIDs() {
			if _, err := opts.Manager.BindAuthenticated(connectionID); err != nil {
				return nil, fmt.Errorf("%w: Cloudflare principal mapping targets an unavailable connection", ErrInvalidOptions)
			}
		}
	}
	credentials := make([]credential, 0, len(opts.Credentials))
	seenConnections := make(map[string]struct{}, len(opts.Credentials))
	seenTokens := make([][]byte, 0, len(opts.Credentials))
	for i, c := range opts.Credentials {
		if c.ConnectionID == "" || len(c.ConnectionID) > 128 || !utf8.ValidString(c.ConnectionID) {
			return nil, fmt.Errorf("%w: credential %d has invalid connection", ErrInvalidOptions, i)
		}
		if _, exists := seenConnections[c.ConnectionID]; exists {
			return nil, fmt.Errorf("%w: duplicate credential connection", ErrInvalidOptions)
		}
		if err := validateToken(c.Token); err != nil {
			return nil, fmt.Errorf("%w: credential %d token is invalid", ErrInvalidOptions, i)
		}
		if tokenAlreadySeen(seenTokens, c.Token) {
			return nil, fmt.Errorf("%w: duplicate bearer token", ErrInvalidOptions)
		}
		seenConnections[c.ConnectionID] = struct{}{}
		token := []byte(c.Token)
		credentials = append(credentials, credential{connectionID: c.ConnectionID, token: token})
		seenTokens = append(seenTokens, token)
	}
	s := &Server{
		manager:          opts.Manager,
		source:           opts.Source,
		limits:           limits,
		maxBodyBytes:     maxBody,
		maxResponseBytes: maxResponse,
		credentials:      credentials,
		cloudflareAccess: opts.CloudflareAccess,
		cloudflareHosts:  hosts,
	}
	s.handler = s.buildHandler()
	return s, nil
}

// Handler returns the authenticated Streamable HTTP handler. It is intended
// for an http.Server bound to 127.0.0.1 (or another explicitly approved
// loopback address). This method itself cannot constrain the caller's net.Listener.
func (s *Server) Handler() http.Handler {
	if s == nil {
		return http.NotFoundHandler()
	}
	return s.handler
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

func (s *Server) buildHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: ServerVersion}, &mcp.ServerOptions{
		// No server-side features other than the explicitly registered tools.
		Capabilities: &mcp.ServerCapabilities{},
	})
	server.AddReceivingMiddleware(s.authorizationMiddleware())

	addTool(server, &mcp.Tool{
		Name:         ToolServerInfo,
		Description:  "Return the authenticated Local-Probe server identity and read-only capability summary.",
		InputSchema:  emptyObjectSchema,
		OutputSchema: serverInfoSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleServerInfo)
	addTool(server, &mcp.Tool{
		Name:         ToolPing,
		Description:  "Check that the authenticated Local-Probe MCP endpoint is ready.",
		InputSchema:  emptyObjectSchema,
		OutputSchema: pingSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handlePing)
	addTool(server, &mcp.Tool{
		Name:         ToolReadFile,
		Description:  "Read one bounded UTF-8 byte range from an authorized regular file.",
		InputSchema:  readFileInputSchema,
		OutputSchema: readFileOutputSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleReadFile)
	addTool(server, &mcp.Tool{
		Name:         ToolBatchRead,
		Description:  "Read a bounded batch of authorized UTF-8 byte ranges with deterministic partial results.",
		InputSchema:  batchReadInputSchema,
		OutputSchema: batchReadOutputSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleBatchRead)

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		CrossOriginProtection: http.NewCrossOriginProtection(),
		// The outer cloudflareHost middleware performs exact public Host
		// validation before this handler. Only that explicit CF mode may
		// disable the SDK's loopback Host check; local-token mode retains it.
		DisableLocalhostProtection:   s.cloudflareAccess != nil,
		MaxRequestBodyBytes:          s.maxBodyBytes,
		PropagateRequestCancellation: true,
	})
	// The SDK's auth middleware populates auth.TokenInfo in the context and
	// enforces session user binding. The token has no remote expiration; process
	// restart/credential rotation is the lifetime boundary for this local hop.
	verify := auth.TokenVerifier(s.verifyToken)
	authOptions := &auth.RequireBearerTokenOptions{AllowMissingExpiration: true}
	if s.cloudflareAccess != nil {
		verify = s.cloudflareAccess.Verify
		authOptions.AllowMissingExpiration = false
		authOptions.ClockSkew = s.cloudflareAccess.ClockSkew()
	}
	protected := auth.RequireBearerToken(verify, authOptions)
	protectedHandler := protected(mcpHandler)
	if s.cloudflareAccess != nil {
		return cloudflareHost(s.cloudflareHosts, cloudflareAccessHeader(protectedHandler))
	}
	return localTokenHeader(protectedHandler)
}

// localTokenHeader adapts the private X-Local-Probe-Token hop to the SDK's
// standard bearer middleware. The Authorization header is synthesized only on
// an in-memory request clone and is never required from or exposed to callers.
func localTokenHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values(LocalTokenHeader)
		if len(values) > 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "invalid authorization", http.StatusUnauthorized)
			return
		}
		clone := r.Clone(r.Context())
		clone.Header = r.Header.Clone()
		clone.Header.Del("Authorization")
		if len(values) == 1 {
			clone.Header.Set("Authorization", "Bearer "+values[0])
		}
		next.ServeHTTP(w, clone)
	})
}

// cloudflareAccessHeader adapts the assertion injected by Cloudflare Access
// to the SDK's standard bearer middleware. Any client Authorization value is
// discarded: Managed OAuth may forward its opaque bearer token, but the CF
// ingress has exactly one trusted identity source and never verifies that
// opaque value or falls back to the local hop token.
func cloudflareAccessHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values(cfaccess.AccessJWTHeader)
		if len(values) != 1 || values[0] == "" || len(values[0]) > 64<<10 || strings.TrimSpace(values[0]) != values[0] {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "invalid authorization", http.StatusUnauthorized)
			return
		}
		clone := r.Clone(r.Context())
		clone.Header = r.Header.Clone()
		clone.Header.Del("Authorization")
		clone.Header.Set("Authorization", "Bearer "+values[0])
		next.ServeHTTP(w, clone)
	})
}

func cloudflareHost(hosts map[string]hostPattern, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trustedCloudflareHost(hosts, r.Host) {
			http.Error(w, "invalid Host header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validateCloudflareHosts(values []string) (map[string]hostPattern, error) {
	if len(values) == 0 || len(values) > 16 {
		return nil, errors.New("at least one exact public host is required")
	}
	allowed := make(map[string]hostPattern, len(values))
	for _, value := range values {
		pattern, key, err := parseHostPattern(value)
		if err != nil {
			return nil, err
		}
		if _, exists := allowed[key]; exists {
			return nil, errors.New("duplicate public host")
		}
		allowed[key] = pattern
	}
	return allowed, nil
}

func trustedCloudflareHost(hosts map[string]hostPattern, raw string) bool {
	host, port, err := splitHost(raw)
	if err != nil {
		return false
	}
	for _, pattern := range hosts {
		if pattern.host != host {
			continue
		}
		if pattern.port == port {
			return true
		}
	}
	return false
}

func parseHostPattern(raw string) (hostPattern, string, error) {
	if strings.TrimSpace(raw) != raw || raw == "" || strings.ContainsAny(raw, "/?#@\\") || strings.ContainsAny(raw, "\r\n\x00") {
		return hostPattern{}, "", errors.New("host must be an exact authority")
	}
	host, port, err := splitHost(raw)
	if err != nil || host == "" || net.ParseIP(host) != nil || strings.Contains(host, "*") {
		return hostPattern{}, "", errors.New("host must be a DNS name")
	}
	if len(host) > 253 || strings.HasSuffix(host, ".") {
		return hostPattern{}, "", errors.New("invalid DNS host")
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return hostPattern{}, "", errors.New("invalid DNS host")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return hostPattern{}, "", errors.New("invalid DNS host")
			}
		}
	}
	host = strings.ToLower(host)
	pattern := hostPattern{host: host, port: port}
	return pattern, host + "\x00" + port, nil
}

func splitHost(raw string) (string, string, error) {
	if strings.Contains(raw, ":") {
		host, port, err := net.SplitHostPort(raw)
		if err != nil || host == "" || port == "" {
			return "", "", errors.New("invalid host port")
		}
		for _, r := range port {
			if r < '0' || r > '9' {
				return "", "", errors.New("invalid host port")
			}
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", "", errors.New("invalid host port")
		}
		return strings.ToLower(host), port, nil
	}
	return strings.ToLower(raw), "", nil
}

func (s *Server) verifyToken(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	if s == nil {
		return nil, fmt.Errorf("%w", auth.ErrInvalidToken)
	}
	for _, credential := range s.credentials {
		if subtle.ConstantTimeCompare(credential.token, []byte(token)) == 1 {
			return &auth.TokenInfo{
				UserID: credential.connectionID,
				Scopes: []string{"local-probe:read"},
			}, nil
		}
	}
	return nil, fmt.Errorf("%w", auth.ErrInvalidToken)
}

func (s *Server) authorizationMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			bound, err := s.boundScope(ctx)
			if err != nil {
				return nil, err
			}
			switch method {
			case "tools/list":
				result, err := next(ctx, method, req)
				if err != nil {
					return nil, err
				}
				list, ok := result.(*mcp.ListToolsResult)
				if !ok || list == nil {
					return result, nil
				}
				filtered := list.Tools[:0]
				for _, tool := range list.Tools {
					if tool != nil && bound.AllowsTool(tool.Name) {
						filtered = append(filtered, tool)
					}
				}
				list.Tools = filtered
				// The endpoint has only four fixed tools and uses the SDK default
				// page size, so a filtered page never needs to expose a cursor.
				list.NextCursor = ""
				return list, nil
			case "tools/call":
				params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
				if !ok || params == nil || !bound.AllowsTool(params.Name) {
					return deniedToolResult(), nil
				}
			}
			return next(ctx, method, req)
		}
	}
}

func (s *Server) boundScope(ctx context.Context) (policy.BoundScope, error) {
	info := auth.TokenInfoFromContext(ctx)
	if info == nil || info.UserID == "" {
		return policy.BoundScope{}, ErrUnauthorized
	}
	bound, err := s.manager.BindAuthenticated(info.UserID)
	if err != nil {
		return policy.BoundScope{}, ErrUnauthorized
	}
	return bound, nil
}

func (s *Server) engine(ctx context.Context, tool string) (*readcore.Engine, readcore.Scope, error) {
	bound, err := s.boundScope(ctx)
	if err != nil {
		return nil, readcore.Scope{}, err
	}
	if !bound.AllowsTool(tool) {
		return nil, readcore.Scope{}, ErrToolDenied
	}
	scope, err := bound.Scope()
	if err != nil {
		return nil, readcore.Scope{}, ErrUnauthorized
	}
	source, err := s.source.Bind(bound)
	if err != nil {
		return nil, readcore.Scope{}, ErrUnauthorized
	}
	engine, err := readcore.New(source, s.limits)
	if err != nil {
		return nil, readcore.Scope{}, ErrInvalidOptions
	}
	return engine, scope, nil
}

type readFileInput struct {
	RootID          string `json:"root_id"`
	Path            string `json:"path"`
	Offset          int64  `json:"offset,omitempty"`
	MaxBytes        int    `json:"max_bytes,omitempty"`
	ExpectedVersion string `json:"expected_version,omitempty"`
}

type batchReadItem struct {
	RootID          string `json:"root_id"`
	Path            string `json:"path"`
	Offset          int64  `json:"offset,omitempty"`
	MaxBytes        int    `json:"max_bytes,omitempty"`
	ExpectedVersion string `json:"expected_version,omitempty"`
}

type batchReadInput struct {
	Requests []batchReadItem `json:"requests"`
}

type serverInfoOutput struct {
	SchemaVersion string   `json:"schema_version"`
	Server        string   `json:"server"`
	Version       string   `json:"version"`
	ReadOnly      bool     `json:"read_only"`
	Transport     string   `json:"transport"`
	ConnectionID  string   `json:"connection_id"`
	ProfileID     string   `json:"profile_id"`
	Tools         []string `json:"tools"`
}

type pingOutput struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
}

type readFileOutput struct {
	SchemaVersion string          `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	Item          readcore.Result `json:"item"`
}

type batchReadOutput struct {
	SchemaVersion string            `json:"schema_version"`
	RequestID     string            `json:"request_id"`
	Items         []readcore.Result `json:"items"`
	ReturnedBytes int               `json:"returned_bytes"`
	BytesRead     int               `json:"bytes_read"`
	Failed        int               `json:"failed"`
}

func (s *Server) handleServerInfo(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bound, err := s.boundScope(ctx)
	if err != nil {
		return errorResult("unauthorized", "authentication is required"), nil
	}
	tools := make([]string, 0, 4)
	for _, name := range []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead} {
		if bound.AllowsTool(name) {
			tools = append(tools, name)
		}
	}
	out := serverInfoOutput{
		SchemaVersion: SchemaVersion,
		Server:        ServerName,
		Version:       ServerVersion,
		ReadOnly:      true,
		Transport:     "mcp-streamable-http",
		ConnectionID:  bound.ConnectionID(),
		ProfileID:     bound.ProfileID(),
		Tools:         tools,
	}
	return s.jsonResult(out), nil
}

func (s *Server) handlePing(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if _, _, err := s.engine(ctx, ToolPing); err != nil {
		return errorResult("unauthorized", "authentication or tool authorization failed"), nil
	}
	return s.jsonResult(pingOutput{SchemaVersion: SchemaVersion, Status: "ok"}), nil
}

func (s *Server) handleReadFile(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input readFileInput
	if err := decodeArguments(req, &input); err != nil {
		return errorResult("invalid_request", "arguments must be a JSON object with supported fields"), nil
	}
	engine, scope, err := s.engine(ctx, ToolReadFile)
	if err != nil {
		return errorResult(classifyServerError(err), safeServerMessage(err)), nil
	}
	result, err := engine.ReadBatch(ctx, scope, []readcore.Request{{
		File:            readcore.FileRef{RootID: input.RootID, Path: input.Path},
		Offset:          input.Offset,
		MaxBytes:        input.MaxBytes,
		ExpectedVersion: input.ExpectedVersion,
	}})
	if err != nil {
		return errorResult("invalid_request", "read request was rejected"), nil
	}
	item := readcore.Result{}
	if len(result.Items) == 1 {
		item = result.Items[0]
	}
	return s.jsonResult(readFileOutput{
		SchemaVersion: SchemaVersion,
		RequestID:     s.nextRequestID(),
		Item:          item,
	}), nil
}

func (s *Server) handleBatchRead(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input batchReadInput
	if err := decodeArguments(req, &input); err != nil {
		return errorResult("invalid_request", "arguments must be a JSON object with supported fields"), nil
	}
	requests := make([]readcore.Request, len(input.Requests))
	for i, item := range input.Requests {
		requests[i] = readcore.Request{
			File:            readcore.FileRef{RootID: item.RootID, Path: item.Path},
			Offset:          item.Offset,
			MaxBytes:        item.MaxBytes,
			ExpectedVersion: item.ExpectedVersion,
		}
	}
	engine, scope, err := s.engine(ctx, ToolBatchRead)
	if err != nil {
		return errorResult(classifyServerError(err), safeServerMessage(err)), nil
	}
	result, err := engine.ReadBatch(ctx, scope, requests)
	if err != nil {
		return errorResult("invalid_request", "batch read request was rejected"), nil
	}
	return s.jsonResult(batchReadOutput{
		SchemaVersion: SchemaVersion,
		RequestID:     s.nextRequestID(),
		Items:         result.Items,
		ReturnedBytes: result.ReturnedBytes,
		BytesRead:     result.BytesRead,
		Failed:        result.Failed,
	}), nil
}

func (s *Server) nextRequestID() string {
	return fmt.Sprintf("req-%d", s.requestID.Add(1))
}

func addTool(server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandler) {
	server.AddTool(tool, handler)
}

func decodeArguments(req *mcp.CallToolRequest, dst any) error {
	if req == nil || req.Params == nil || len(req.Params.Arguments) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (s *Server) jsonResult(value any) *mcp.CallToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return errorResult("unavailable", "result serialization failed")
	}
	// Keep the full payload in structuredContent, as required when an output
	// schema is advertised. Content is intentionally only a short compatibility
	// hint; copying a potentially large file body into both fields would double
	// the MCP wire payload.
	result := &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: "Local-Probe structured result; see structuredContent."}},
		StructuredContent: json.RawMessage(data),
	}
	if s != nil && s.maxResponseBytes > 0 {
		wire, err := json.Marshal(result)
		if err != nil || int64(len(wire)) > s.maxResponseBytes {
			return errorResult("budget_exhausted", "serialized result exceeds the MCP response budget")
		}
	}
	return result
}

func errorResult(code, message string) *mcp.CallToolResult {
	payload := struct {
		SchemaVersion string `json:"schema_version"`
		Error         struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{SchemaVersion: SchemaVersion}
	payload.Error.Code, payload.Error.Message = code, message
	data, _ := json.Marshal(payload)
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}

func deniedToolResult() *mcp.CallToolResult {
	return errorResult("denied", "tool is not enabled for this connection")
}

func classifyServerError(err error) string {
	switch {
	case errors.Is(err, ErrToolDenied), errors.Is(err, ErrUnauthorized):
		return "denied"
	default:
		return "unavailable"
	}
}

func safeServerMessage(err error) string {
	if errors.Is(err, ErrToolDenied) {
		return "tool is not enabled for this connection"
	}
	if errors.Is(err, ErrUnauthorized) {
		return "authentication or authorization is no longer valid"
	}
	return "read service is unavailable"
}

func validateToken(token string) error {
	if len(token) < 16 || len(token) > maxTokenBytes || !utf8.ValidString(token) || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid token")
	}
	return nil
}

func tokenAlreadySeen(seen [][]byte, candidate string) bool {
	for _, token := range seen {
		if subtle.ConstantTimeCompare(token, []byte(candidate)) == 1 {
			return true
		}
	}
	return false
}

type validationSource struct{}

func (validationSource) Open(context.Context, readcore.Scope, readcore.FileRef) (readcore.Handle, error) {
	return nil, errors.New("validation source is not available")
}

// ResolveToken reads exactly one runtime token from an environment variable
// or a file. A file may end in one line ending, which is removed; other
// surrounding whitespace is rejected. Callers should protect the file with
// OS ACLs and keep it outside the repository.
func ResolveToken(envName, fileName string) (string, error) {
	if (envName == "") == (fileName == "") {
		return "", errors.New("choose exactly one token environment variable or file")
	}
	var token string
	if envName != "" {
		token = os.Getenv(envName)
	} else {
		data, err := os.ReadFile(fileName)
		if err != nil || len(data) > maxTokenBytes {
			return "", errors.New("cannot read token file")
		}
		token = string(data)
		token = strings.TrimSuffix(token, "\n")
		token = strings.TrimSuffix(token, "\r")
	}
	if err := validateToken(token); err != nil {
		return "", errors.New("token source is invalid")
	}
	return token, nil
}

func readOnlyAnnotations() *mcp.ToolAnnotations {
	falseValue := false
	return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &falseValue}
}

var emptyObjectSchema = map[string]any{"type": "object", "additionalProperties": false}

var readFileInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"root_id", "path"},
	"properties": map[string]any{
		"root_id":          map[string]any{"type": "string", "minLength": 1},
		"path":             map[string]any{"type": "string", "minLength": 1},
		"offset":           map[string]any{"type": "integer", "minimum": 0},
		"max_bytes":        map[string]any{"type": "integer", "minimum": 0},
		"expected_version": map[string]any{"type": "string"},
	},
}

var batchReadInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"requests"},
	"properties": map[string]any{
		"requests": map[string]any{
			"type":     "array",
			"minItems": 1,
			"maxItems": 32,
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"root_id", "path"},
				"properties": map[string]any{
					"root_id":          map[string]any{"type": "string", "minLength": 1},
					"path":             map[string]any{"type": "string", "minLength": 1},
					"offset":           map[string]any{"type": "integer", "minimum": 0},
					"max_bytes":        map[string]any{"type": "integer", "minimum": 0},
					"expected_version": map[string]any{"type": "string"},
				},
			},
		},
	},
}

var serverInfoSchema = map[string]any{"type": "object"}
var pingSchema = map[string]any{"type": "object"}
var readFileOutputSchema = map[string]any{"type": "object"}
var batchReadOutputSchema = map[string]any{"type": "object"}
