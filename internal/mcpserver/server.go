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
	"time"
	"unicode/utf8"

	"github.com/LE-saber/Local-Probe/internal/audit"
	"github.com/LE-saber/Local-Probe/internal/cfaccess"
	"github.com/LE-saber/Local-Probe/internal/environment"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/readcore"
	"github.com/LE-saber/Local-Probe/internal/search"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	SchemaVersion = "local-probe.mcp.v1"
	ServerName    = "local-probe"
	ServerVersion = "0.1.0-alpha"

	ToolServerInfo     = "server_info"
	ToolPing           = "ping"
	ToolReadFile       = "read_file"
	ToolBatchRead      = "batch_read"
	ToolListDirectory  = "list_directory"
	ToolFindFiles      = "find_files"
	ToolSearchText     = "search_text"
	ToolTreeDirectory  = "tree_directory"
	ToolGetEnvironment = "get_environment"
	ToolDiscoverTools  = "discover_tools"
	LocalTokenHeader   = "X-Local-Probe-Token"

	// CloudflareAccessHeader is re-exported for callers that need to construct
	// a local integration test request without depending on the cfaccess
	// package's transport details.
	CloudflareAccessHeader = cfaccess.AccessJWTHeader

	defaultMaxRequestBodyBytes   = 1 << 20
	defaultMaxResponseBytes      = 512 << 10
	maxTokenBytes                = 4096
	maxEnvironmentToolSelections = 128
	modernMCPProtocolVersion     = "2026-07-28"
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
	Audit       audit.Recorder
	// EnvironmentTools is a local-only allowlist for non-executing tool
	// discovery. Candidate paths come from trusted configuration; MCP input
	// can select only logical IDs and never supplies paths or diagnostics.
	EnvironmentTools []environment.ToolSpec
	// CloudflareAccess selects the explicit Cloudflare Access ingress. When it
	// is non-nil, local Credentials must be empty and every request must carry
	// a valid Cf-Access-Jwt-Assertion plus a trusted public Host.
	CloudflareAccess      *cfaccess.Verifier
	CloudflarePublicHosts []string
	Limits                readcore.Limits
	SearchLimits          search.Limits
	SearchCursorKey       []byte
	MaxBodyBytes          int64
	// MaxResponseBytes bounds the serialized MCP CallToolResult content. The
	// JSON-RPC envelope adds a small amount of transport overhead; callers that
	// impose a hard wire cap should leave room for that envelope.
	MaxResponseBytes int64
}

// Server is an authenticated, read-only MCP handler. It is safe to share
// across concurrent HTTP requests.
type Server struct {
	manager             *policy.Manager
	source              SourceBinder
	limits              readcore.Limits
	maxBodyBytes        int64
	maxResponseBytes    int64
	credentials         []credential
	cloudflareAccess    *cfaccess.Verifier
	cloudflareHosts     map[string]hostPattern
	environmentTools    []environment.ToolSpec
	discoverEnvironment func([]environment.ToolSpec, environment.DiscoveryOptions) ([]environment.ToolDiscoveryResult, error)
	audit               audit.Recorder
	requestID           atomic.Uint64
	auditID             atomic.Uint64
	handler             http.Handler
	search              *search.Service
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
		manager:             opts.Manager,
		source:              opts.Source,
		limits:              limits,
		maxBodyBytes:        maxBody,
		maxResponseBytes:    maxResponse,
		credentials:         credentials,
		cloudflareAccess:    opts.CloudflareAccess,
		cloudflareHosts:     hosts,
		environmentTools:    cloneEnvironmentTools(opts.EnvironmentTools),
		discoverEnvironment: environment.DiscoverTools,
		audit:               opts.Audit,
	}
	if binder, ok := opts.Source.(search.Binder); ok {
		searchService, searchErr := search.New(binder, opts.SearchLimits, opts.SearchCursorKey)
		if searchErr != nil {
			return nil, fmt.Errorf("%w: invalid search options", ErrInvalidOptions)
		}
		s.search = searchService
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

func usesModernMCPProtocol(r *http.Request) bool {
	if r == nil {
		return false
	}
	// Protocol versions are ISO dates, so lexical comparison preserves their
	// ordering. Future versions stay on the sessionless path and are then
	// validated by the SDK instead of accidentally entering a legacy session.
	return strings.TrimSpace(r.Header.Get("Mcp-Protocol-Version")) >= modernMCPProtocolVersion
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
	addTool(server, &mcp.Tool{
		Name:         ToolListDirectory,
		Description:  "List one bounded page of entries in an authorized directory without crossing symlink or reparse boundaries.",
		InputSchema:  listDirectoryInputSchema,
		OutputSchema: listDirectoryOutputSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleListDirectory)
	addTool(server, &mcp.Tool{
		Name:         ToolFindFiles,
		Description:  "Find authorized regular files by a slash-separated glob using a bounded, cancellable traversal.",
		InputSchema:  findFilesInputSchema,
		OutputSchema: findFilesOutputSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleFindFiles)
	addTool(server, &mcp.Tool{
		Name:         ToolSearchText,
		Description:  "Search authorized UTF-8 files for a literal query with bounded I/O, byte locations and context.",
		InputSchema:  searchTextInputSchema,
		OutputSchema: searchTextOutputSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleSearchText)
	addTool(server, &mcp.Tool{
		Name:         ToolTreeDirectory,
		Description:  "Return a bounded flat tree of an authorized directory without changing process state or crossing links.",
		InputSchema:  treeDirectoryInputSchema,
		OutputSchema: treeDirectoryOutputSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleTreeDirectory)
	addTool(server, &mcp.Tool{
		Name:         ToolGetEnvironment,
		Description:  "Return coarse local operating-system facts without reading the environment or starting a process.",
		InputSchema:  getEnvironmentInputSchema,
		OutputSchema: getEnvironmentOutputSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleGetEnvironment)
	addTool(server, &mcp.Tool{
		Name:         ToolDiscoverTools,
		Description:  "Check locally configured tool candidates without searching PATH or exposing candidate paths.",
		InputSchema:  discoverToolsInputSchema,
		OutputSchema: discoverToolsOutputSchema,
		Annotations:  readOnlyAnnotations(),
	}, s.handleDiscoverTools)

	streamableOptions := func(stateless bool) *mcp.StreamableHTTPOptions {
		return &mcp.StreamableHTTPOptions{
			// A single JSON response is more robust through HTTP proxies than an
			// SSE response for ordinary request/response calls. The protocol still
			// uses SSE for standalone GET and long-lived subscription streams.
			JSONResponse: true,
			// The outer cloudflareHost middleware performs exact public Host
			// validation before this handler. Only that explicit CF mode may
			// disable the SDK's loopback Host check; local-token mode retains it.
			CrossOriginProtection:        http.NewCrossOriginProtection(),
			DisableLocalhostProtection:   s.cloudflareAccess != nil,
			MaxRequestBodyBytes:          s.maxBodyBytes,
			PropagateRequestCancellation: true,
			Stateless:                    stateless,
		}
	}
	// MCP 2026-07-28 is sessionless and is only supported by the SDK's
	// stateless transport. Keep a stateful handler for older protocol versions
	// so legacy clients retain session IDs, GET/SSE, DELETE, and session-user
	// binding. The protocol header is mandatory for modern requests, so it is a
	// safe and unambiguous dispatch key here.
	statefulHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, streamableOptions(false))
	statelessHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, streamableOptions(true))
	mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if usesModernMCPProtocol(r) {
			statelessHandler.ServeHTTP(w, r)
			return
		}
		statefulHandler.ServeHTTP(w, r)
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
		return s.auditHTTP(cloudflareHost(s.cloudflareHosts, cloudflareAccessHeader(protectedHandler)))
	}
	return s.auditHTTP(localTokenHeader(protectedHandler))
}

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *auditResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *auditResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) auditHTTP(next http.Handler) http.Handler {
	if s == nil || s.audit == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &auditResponseWriter{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		if recorder.status == http.StatusUnauthorized || recorder.status == http.StatusForbidden {
			_ = s.audit.EmitSecurity(audit.Event{
				Component: audit.ComponentAuth, EventType: audit.EventAuthReject,
				Severity: audit.SeverityWarn, Outcome: audit.OutcomeRejected,
				ErrorCode: "auth_failed",
			})
		}
	})
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
			s.recordAudit(audit.Event{CorrelationID: s.nextAuditCorrelation(), Component: audit.ComponentAuth, EventType: audit.EventAuthAccept, Severity: audit.SeverityInfo, Outcome: audit.OutcomeSucceeded, ConnectionID: bound.ConnectionID(), ProfileID: bound.ProfileID(), ProfileRevision: bound.Revision()})
			switch method {
			case "tools/list":
				result, err := s.auditMCP(audit.EventMCPList, "tools/list", bound, false, 0, func() (mcp.Result, error) { return next(ctx, method, req) })
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
				// The endpoint has a small fixed tool set and uses the SDK default
				// page size, so a filtered page never needs to expose a cursor.
				list.NextCursor = ""
				return list, nil
			case "tools/call":
				params, ok := req.GetParams().(*mcp.CallToolParamsRaw)
				action := "unknown"
				if ok && params != nil && isRegisteredAuditAction(params.Name) {
					action = params.Name
				}
				denied := !ok || params == nil || !bound.AllowsTool(params.Name)
				inputBytes := 0
				if params != nil {
					inputBytes = len(params.Arguments)
				}
				if denied {
					result, err := s.auditMCP(audit.EventMCPCall, action, bound, true, inputBytes, func() (mcp.Result, error) { return deniedToolResult(), nil })
					return result, err
				}
				return s.auditMCP(audit.EventMCPCall, action, bound, false, inputBytes, func() (mcp.Result, error) { return next(ctx, method, req) })
			}
			return next(ctx, method, req)
		}
	}
}

func (s *Server) recordAudit(event audit.Event) {
	if s != nil && s.audit != nil {
		_ = s.audit.Emit(event)
	}
}

func (s *Server) nextAuditCorrelation() string {
	return fmt.Sprintf("mcp-%d", s.auditID.Add(1))
}

func (s *Server) auditMCP(eventType audit.EventType, action string, bound policy.BoundScope, denied bool, inputBytes int, call func() (mcp.Result, error)) (mcp.Result, error) {
	correlation := s.nextAuditCorrelation()
	s.recordAudit(audit.Event{CorrelationID: correlation, Component: audit.ComponentMCP, EventType: eventType, Action: action, Severity: audit.SeverityInfo, Outcome: audit.OutcomeStarted, ConnectionID: bound.ConnectionID(), ProfileID: bound.ProfileID(), ProfileRevision: bound.Revision(), Budget: audit.Budget{WireInBytes: int64(inputBytes)}})
	started := time.Now()
	result, err := call()
	outcome, errorCode := audit.OutcomeSucceeded, ""
	if denied {
		outcome, errorCode = audit.OutcomeRejected, "denied"
	} else if err != nil {
		outcome, errorCode = audit.OutcomeFailed, "unavailable"
		if errors.Is(err, context.Canceled) {
			errorCode = "cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			errorCode = "deadline_exceeded"
		}
	} else if callResult, ok := result.(*mcp.CallToolResult); ok && callResult.IsError {
		outcome, errorCode = auditToolError(callResult)
	}
	outputBytes := auditResultBytes(result)
	s.recordAudit(audit.Event{CorrelationID: correlation, Component: audit.ComponentMCP, EventType: audit.EventMCPResult, Action: action, Severity: audit.SeverityInfo, Outcome: outcome, ErrorCode: errorCode, DurationMS: time.Since(started).Milliseconds(), ConnectionID: bound.ConnectionID(), ProfileID: bound.ProfileID(), ProfileRevision: bound.Revision(), Budget: audit.Budget{WireInBytes: int64(inputBytes), WireOutBytes: outputBytes, ReturnedBytes: outputBytes}})
	return result, err
}

// auditToolError extracts only the stable error code from this server's typed
// error envelope. It never copies the message or any other response content
// into the audit event. Unknown/malformed envelopes are deliberately reduced
// to unavailable so an arbitrary tool response cannot inject audit fields.
func auditToolError(result *mcp.CallToolResult) (audit.Outcome, string) {
	if result == nil || !result.IsError {
		return audit.OutcomeSucceeded, ""
	}
	type errorEnvelope struct {
		SchemaVersion string `json:"schema_version"`
		Error         struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok || text == nil || len(text.Text) > 4096 {
			continue
		}
		var envelope errorEnvelope
		if err := json.Unmarshal([]byte(text.Text), &envelope); err != nil || envelope.SchemaVersion != SchemaVersion {
			continue
		}
		switch envelope.Error.Code {
		case "denied":
			return audit.OutcomeRejected, envelope.Error.Code
		case "invalid_request", "not_found", "unsupported_type", "unsupported_encoding", "stale_version", "budget_exhausted", "deadline_exceeded", "cancelled", "unavailable":
			return audit.OutcomeFailed, envelope.Error.Code
		}
	}
	return audit.OutcomeFailed, "unavailable"
}

func auditResultBytes(result mcp.Result) int64 {
	call, ok := result.(*mcp.CallToolResult)
	if !ok || call == nil {
		return 0
	}
	var total int
	if raw, ok := call.StructuredContent.(json.RawMessage); ok {
		total += len(raw)
	} else if call.StructuredContent != nil {
		if data, err := json.Marshal(call.StructuredContent); err == nil {
			total += len(data)
		}
	}
	for _, content := range call.Content {
		if text, ok := content.(*mcp.TextContent); ok && text != nil {
			total += len(text.Text)
		}
	}
	return int64(total)
}

func isRegisteredAuditAction(value string) bool {
	switch value {
	case ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead, ToolListDirectory,
		ToolFindFiles, ToolSearchText, ToolTreeDirectory, ToolGetEnvironment, ToolDiscoverTools:
		return true
	default:
		// Never copy an arbitrary client-supplied tool name into an audit record.
		return false
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

type discoverToolsInput struct {
	LogicalIDs []string `json:"logical_ids,omitempty"`
}

type getEnvironmentOutput struct {
	SchemaVersion string   `json:"schema_version"`
	RequestID     string   `json:"request_id"`
	OS            string   `json:"os"`
	Arch          string   `json:"arch"`
	Capabilities  []string `json:"capabilities"`
}

type discoverToolsOutput struct {
	SchemaVersion string                            `json:"schema_version"`
	RequestID     string                            `json:"request_id"`
	Tools         []environment.ToolDiscoveryResult `json:"tools"`
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

func (s *Server) searchErrorResult(err error) *mcp.CallToolResult {
	code := "unavailable"
	message := "search service is unavailable"
	switch {
	case errors.Is(err, search.ErrInvalidRequest):
		code, message = "invalid_request", "arguments are invalid or exceed the search budget"
	case errors.Is(err, search.ErrDenied):
		code, message = "denied", "search access is not authorized"
	case errors.Is(err, search.ErrInvalidCursor):
		code, message = "invalid_cursor", "continuation is invalid or expired; restart the search"
	case errors.Is(err, search.ErrGenerationChanged):
		code, message = "stale_cursor", "the directory changed; restart the search"
	case errors.Is(err, context.Canceled):
		code, message = "cancelled", "search was cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		code, message = "deadline_exceeded", "search deadline exceeded"
	}
	return errorResult(code, message)
}

func (s *Server) handleListDirectory(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input search.ListDirectoryRequest
	if err := decodeArguments(req, &input); err != nil {
		return errorResult("invalid_request", "arguments must be a JSON object with supported fields"), nil
	}
	bound, err := s.boundScope(ctx)
	if err != nil {
		return errorResult("unauthorized", "authentication is required"), nil
	}
	if s.search == nil {
		return s.searchErrorResult(search.ErrUnavailable), nil
	}
	out, err := s.search.ListDirectory(ctx, bound, input)
	if err != nil {
		return s.searchErrorResult(err), nil
	}
	// Search result IDs are assigned only at the authenticated MCP boundary.
	result := struct {
		SchemaVersion string          `json:"schema_version"`
		RequestID     string          `json:"request_id"`
		RootID        string          `json:"root_id"`
		Path          string          `json:"path"`
		Entries       []search.Entry  `json:"entries"`
		Coverage      search.Coverage `json:"coverage"`
		Warnings      []string        `json:"warnings,omitempty"`
		Budget        search.Budget   `json:"budget"`
		Continuation  string          `json:"continuation,omitempty"`
	}{out.SchemaVersion, s.nextRequestID(), out.RootID, out.Path, out.Entries, out.Coverage, out.Warnings, out.Budget, out.Continuation}
	return s.jsonResult(result), nil
}

func (s *Server) handleFindFiles(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input search.FindFilesRequest
	if err := decodeArguments(req, &input); err != nil {
		return errorResult("invalid_request", "arguments must be a JSON object with supported fields"), nil
	}
	bound, err := s.boundScope(ctx)
	if err != nil {
		return errorResult("unauthorized", "authentication is required"), nil
	}
	if s.search == nil {
		return s.searchErrorResult(search.ErrUnavailable), nil
	}
	out, err := s.search.FindFiles(ctx, bound, input)
	if err != nil {
		return s.searchErrorResult(err), nil
	}
	result := struct {
		SchemaVersion string          `json:"schema_version"`
		RequestID     string          `json:"request_id"`
		RootID        string          `json:"root_id"`
		Path          string          `json:"path"`
		Pattern       string          `json:"pattern"`
		Entries       []search.Entry  `json:"entries"`
		Coverage      search.Coverage `json:"coverage"`
		Warnings      []string        `json:"warnings,omitempty"`
		Budget        search.Budget   `json:"budget"`
		Continuation  string          `json:"continuation,omitempty"`
	}{out.SchemaVersion, s.nextRequestID(), out.RootID, out.Path, out.Pattern, out.Entries, out.Coverage, out.Warnings, out.Budget, out.Continuation}
	return s.jsonResult(result), nil
}

func (s *Server) handleSearchText(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input search.SearchTextRequest
	if err := decodeArguments(req, &input); err != nil {
		return errorResult("invalid_request", "arguments must be a JSON object with supported fields"), nil
	}
	bound, err := s.boundScope(ctx)
	if err != nil {
		return errorResult("unauthorized", "authentication is required"), nil
	}
	if s.search == nil {
		return s.searchErrorResult(search.ErrUnavailable), nil
	}
	out, err := s.search.SearchText(ctx, bound, input)
	if err != nil {
		return s.searchErrorResult(err), nil
	}
	result := struct {
		SchemaVersion string          `json:"schema_version"`
		RequestID     string          `json:"request_id"`
		RootID        string          `json:"root_id"`
		Path          string          `json:"path"`
		Query         string          `json:"query"`
		Matches       []search.Match  `json:"matches"`
		Coverage      search.Coverage `json:"coverage"`
		Warnings      []string        `json:"warnings,omitempty"`
		Budget        search.Budget   `json:"budget"`
		Continuation  string          `json:"continuation,omitempty"`
	}{out.SchemaVersion, s.nextRequestID(), out.RootID, out.Path, out.Query, out.Matches, out.Coverage, out.Warnings, out.Budget, out.Continuation}
	return s.jsonResult(result), nil
}

func (s *Server) handleTreeDirectory(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input search.TreeDirectoryRequest
	if err := decodeArguments(req, &input); err != nil {
		return errorResult("invalid_request", "arguments must be a JSON object with supported fields"), nil
	}
	bound, err := s.boundScope(ctx)
	if err != nil {
		return errorResult("unauthorized", "authentication is required"), nil
	}
	if s.search == nil {
		return s.searchErrorResult(search.ErrUnavailable), nil
	}
	if !bound.AllowsTool(ToolTreeDirectory) {
		return deniedToolResult(), nil
	}
	out, err := s.search.TreeDirectory(ctx, bound, input)
	if err != nil {
		return s.searchErrorResult(err), nil
	}
	result := struct {
		SchemaVersion string             `json:"schema_version"`
		RequestID     string             `json:"request_id"`
		RootID        string             `json:"root_id"`
		Path          string             `json:"path"`
		Entries       []search.TreeEntry `json:"entries"`
		Coverage      search.Coverage    `json:"coverage"`
		Warnings      []string           `json:"warnings,omitempty"`
		Budget        search.Budget      `json:"budget"`
		Continuation  string             `json:"continuation,omitempty"`
	}{out.SchemaVersion, s.nextRequestID(), out.RootID, out.Path, out.Entries, out.Coverage, out.Warnings, out.Budget, out.Continuation}
	return s.jsonResult(result), nil
}

func (s *Server) handleGetEnvironment(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input struct{}
	if err := decodeArguments(req, &input); err != nil {
		return errorResult("invalid_request", "arguments must be an empty JSON object"), nil
	}
	bound, err := s.boundScope(ctx)
	if err != nil {
		return errorResult("unauthorized", "authentication is required"), nil
	}
	if !bound.AllowsTool(ToolGetEnvironment) {
		return deniedToolResult(), nil
	}
	env := environment.GetEnvironment()
	return s.jsonResult(getEnvironmentOutput{
		SchemaVersion: SchemaVersion,
		RequestID:     s.nextRequestID(),
		OS:            env.OS,
		Arch:          env.Arch,
		Capabilities:  append([]string(nil), env.Capabilities...),
	}), nil
}

func (s *Server) handleDiscoverTools(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input discoverToolsInput
	if err := decodeArguments(req, &input); err != nil {
		return errorResult("invalid_request", "arguments must be a JSON object with supported fields"), nil
	}
	bound, err := s.boundScope(ctx)
	if err != nil {
		return errorResult("unauthorized", "authentication is required"), nil
	}
	if !bound.AllowsTool(ToolDiscoverTools) {
		return deniedToolResult(), nil
	}
	specs, err := s.selectEnvironmentTools(input.LogicalIDs)
	if err != nil {
		return errorResult("invalid_request", "logical tool selection is invalid"), nil
	}
	discover := s.discoverEnvironment
	if discover == nil {
		discover = environment.DiscoverTools
	}
	results, err := discover(specs, environment.DiscoveryOptions{})
	if err != nil {
		return environmentErrorResult(err), nil
	}
	// The remote MCP contract never enables local diagnostics. Keep this
	// defensive redaction at the boundary so a future discovery implementation
	// cannot accidentally put candidate paths on the wire.
	for i := range results {
		results[i].CandidatePaths = nil
		results[i].DiagnosticsNotice = ""
	}
	return s.jsonResult(discoverToolsOutput{
		SchemaVersion: SchemaVersion,
		RequestID:     s.nextRequestID(),
		Tools:         results,
	}), nil
}

func (s *Server) selectEnvironmentTools(logicalIDs []string) ([]environment.ToolSpec, error) {
	if len(logicalIDs) > maxEnvironmentToolSelections {
		return nil, environment.ErrCandidateLimit
	}
	if len(logicalIDs) == 0 {
		return cloneEnvironmentTools(s.environmentTools), nil
	}
	byID := make(map[string]environment.ToolSpec, len(s.environmentTools))
	for _, spec := range s.environmentTools {
		if _, exists := byID[spec.ID]; exists {
			return nil, environment.ErrDuplicateID
		}
		byID[spec.ID] = spec
	}
	selected := make([]environment.ToolSpec, 0, len(logicalIDs))
	seen := make(map[string]struct{}, len(logicalIDs))
	for _, id := range logicalIDs {
		if _, exists := seen[id]; exists {
			return nil, environment.ErrDuplicateID
		}
		spec, exists := byID[id]
		if !exists {
			return nil, environment.ErrInvalidInput
		}
		seen[id] = struct{}{}
		selected = append(selected, cloneEnvironmentTool(spec))
	}
	return selected, nil
}

func environmentErrorResult(err error) *mcp.CallToolResult {
	code, message := "unavailable", "configured environment discovery is unavailable"
	switch {
	case errors.Is(err, environment.ErrInvalidInput), errors.Is(err, environment.ErrDuplicateID), errors.Is(err, environment.ErrCandidateLimit):
		code, message = "invalid_request", "configured environment discovery request is invalid"
	case errors.Is(err, environment.ErrRejectedCandidate):
		code, message = "unavailable", "configured environment candidate was rejected"
	}
	return errorResult(code, message)
}

func cloneEnvironmentTools(values []environment.ToolSpec) []environment.ToolSpec {
	if values == nil {
		return nil
	}
	out := make([]environment.ToolSpec, len(values))
	for i, value := range values {
		out[i] = cloneEnvironmentTool(value)
	}
	return out
}

func cloneEnvironmentTool(value environment.ToolSpec) environment.ToolSpec {
	value.CandidateFiles = append([]string(nil), value.CandidateFiles...)
	value.CandidateDirs = append([]string(nil), value.CandidateDirs...)
	return value
}

func (s *Server) handleServerInfo(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	bound, err := s.boundScope(ctx)
	if err != nil {
		return errorResult("unauthorized", "authentication is required"), nil
	}
	tools := make([]string, 0, 10)
	for _, name := range []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead, ToolListDirectory, ToolFindFiles, ToolSearchText, ToolTreeDirectory, ToolGetEnvironment, ToolDiscoverTools} {
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
var getEnvironmentInputSchema = map[string]any{"type": "object", "additionalProperties": false}

var discoverToolsInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"logical_ids": map[string]any{
			"type":     "array",
			"maxItems": maxEnvironmentToolSelections,
			"items": map[string]any{
				"type":      "string",
				"minLength": 1,
				"maxLength": 128,
				"pattern":   "^[A-Za-z0-9_-]+$",
			},
		},
	},
}

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
var getEnvironmentOutputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"schema_version", "request_id", "os", "arch", "capabilities"},
	"properties": map[string]any{
		"schema_version": map[string]any{"type": "string"},
		"request_id":     map[string]any{"type": "string"},
		"os":             map[string]any{"type": "string"},
		"arch":           map[string]any{"type": "string"},
		"capabilities":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
}
var discoverToolsOutputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"schema_version", "request_id", "tools"},
	"properties": map[string]any{
		"schema_version": map[string]any{"type": "string"},
		"request_id":     map[string]any{"type": "string"},
		"tools": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"approved_logical_id", "exists", "candidate_count"},
				"properties": map[string]any{
					"approved_logical_id": map[string]any{"type": "string"},
					"exists":              map[string]any{"type": "boolean"},
					"candidate_count":     map[string]any{"type": "integer", "minimum": 0},
				},
			},
		},
	},
}
var readFileOutputSchema = map[string]any{"type": "object"}
var batchReadOutputSchema = map[string]any{"type": "object"}

var listDirectoryInputSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"root_id"},
	"properties": map[string]any{
		"root_id":     map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"path":        map[string]any{"type": "string", "maxLength": 4096},
		"page_size":   map[string]any{"type": "integer", "minimum": 1, "maximum": 1024},
		"max_entries": map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576},
		"cursor":      map[string]any{"type": "string", "maxLength": 65536},
	},
}

var findFilesInputSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"root_id", "pattern"},
	"properties": map[string]any{
		"root_id":        map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"path":           map[string]any{"type": "string", "maxLength": 4096},
		"pattern":        map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		"page_size":      map[string]any{"type": "integer", "minimum": 1, "maximum": 1024},
		"max_depth":      map[string]any{"type": "integer", "minimum": 0, "maximum": 256},
		"max_entries":    map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576},
		"case_sensitive": map[string]any{"type": "boolean"},
		"cursor":         map[string]any{"type": "string", "maxLength": 65536},
	},
}

var searchTextInputSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"root_id", "query"},
	"properties": map[string]any{
		"root_id":        map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"path":           map[string]any{"type": "string", "maxLength": 4096},
		"query":          map[string]any{"type": "string", "minLength": 1, "maxLength": 4096},
		"globs":          map[string]any{"type": "array", "maxItems": 32, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}},
		"page_size":      map[string]any{"type": "integer", "minimum": 1, "maximum": 1024},
		"max_depth":      map[string]any{"type": "integer", "minimum": 0, "maximum": 256},
		"max_entries":    map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576},
		"max_read_bytes": map[string]any{"type": "integer", "minimum": 1024, "maximum": 67108864},
		"context_bytes":  map[string]any{"type": "integer", "minimum": 0, "maximum": 65536},
		"case_sensitive": map[string]any{"type": "boolean"},
		"cursor":         map[string]any{"type": "string", "maxLength": 65536},
	},
}

var treeDirectoryInputSchema = map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"root_id"},
	"properties": map[string]any{
		"root_id":     map[string]any{"type": "string", "minLength": 1, "maxLength": 128},
		"path":        map[string]any{"type": "string", "maxLength": 4096},
		"max_depth":   map[string]any{"type": "integer", "minimum": 0, "maximum": 256},
		"page_size":   map[string]any{"type": "integer", "minimum": 1, "maximum": 1024},
		"max_entries": map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576},
		"cursor":      map[string]any{"type": "string", "maxLength": 65536},
	},
}

var listDirectoryOutputSchema = map[string]any{"type": "object"}
var findFilesOutputSchema = map[string]any{"type": "object"}
var searchTextOutputSchema = map[string]any{"type": "object"}
var treeDirectoryOutputSchema = map[string]any{"type": "object"}
