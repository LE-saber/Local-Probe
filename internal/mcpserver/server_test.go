package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
	"github.com/LE-saber/Local-Probe/internal/search"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testTokenOne = "local-probe-test-token-one-0123456789"
	testTokenTwo = "local-probe-test-token-two-0123456789"
)

func TestStreamableHTTPClientLifecycleAndReadTools(t *testing.T) {
	t.Parallel()
	fixture := newFixture(t)
	defer fixture.close()
	server := newTestServer(t, fixture, []Credential{
		{ConnectionID: "connection-one", Token: testTokenOne},
		{ConnectionID: "connection-two", Token: testTokenTwo},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &testTokenTransport{token: testTokenOne}
	client := mcp.NewClient(&mcp.Implementation{Name: "local-probe-test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: transport},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()

	// The MCP 2026-07-28 protocol removes the protocol-level ping method;
	// exercise the legacy ping only when negotiation selected that protocol.
	if session.InitializeResult().ProtocolVersion < modernMCPProtocolVersion {
		if err := session.Ping(ctx, nil); err != nil {
			t.Fatalf("Ping() error = %v", err)
		}
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	gotTools := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		gotTools = append(gotTools, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
			t.Errorf("tool %q missing read-only annotations: %#v", tool.Name, tool.Annotations)
		}
	}
	sort.Strings(gotTools)
	wantTools := []string{ToolBatchRead, ToolPing, ToolReadFile, ToolServerInfo}
	if len(gotTools) != len(wantTools) {
		t.Fatalf("tools = %v, want %v", gotTools, wantTools)
	}
	for i := range wantTools {
		if gotTools[i] != wantTools[i] {
			t.Fatalf("tools = %v, want %v", gotTools, wantTools)
		}
	}

	infoResult := callTool(t, ctx, session, ToolServerInfo, map[string]any{})
	var info serverInfoOutput
	decodeToolJSON(t, infoResult, &info)
	if info.Server != ServerName || info.ConnectionID != "connection-one" || !info.ReadOnly {
		t.Fatalf("server_info = %#v", info)
	}

	readResult := callTool(t, ctx, session, ToolReadFile, map[string]any{
		"root_id":   "workspace",
		"path":      "hello.txt",
		"offset":    0,
		"max_bytes": 64,
	})
	var read readFileOutput
	decodeToolJSON(t, readResult, &read)
	if read.Item.Content != "hello from Local-Probe\n" || read.Item.Error != nil || read.RequestID == "" {
		t.Fatalf("read_file = %#v", read)
	}

	batchResult := callTool(t, ctx, session, ToolBatchRead, map[string]any{
		"requests": []any{
			map[string]any{"root_id": "workspace", "path": "hello.txt", "max_bytes": 6},
			map[string]any{"root_id": "workspace", "path": "missing.txt", "max_bytes": 6},
		},
	})
	var batch batchReadOutput
	decodeToolJSON(t, batchResult, &batch)
	if len(batch.Items) != 2 || batch.Items[0].Content != "hello " || batch.Items[1].Error == nil || batch.Failed != 1 {
		t.Fatalf("batch_read = %#v", batch)
	}
}

func TestSearchToolsAreMCPScopedAndUseStructuredResults(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	fixture.profileOneTools = []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead, ToolListDirectory, ToolFindFiles, ToolSearchText}
	if err := os.MkdirAll(fixture.root+"/src/nested", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.root+"/src/main.go", []byte("package main\nneedle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.root+"/src/nested/util.go", []byte("package nested\nneedle in utf8\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.root+"/ignored.tmp", []byte("needle ignored by the profile test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, fixture, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "search-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: &testTokenTransport{token: testTokenOne}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 7 {
		t.Fatalf("tools = %d, want 7", len(tools.Tools))
	}
	listing := callTool(t, ctx, session, ToolListDirectory, map[string]any{"root_id": "workspace", "page_size": 32})
	var list struct {
		RequestID string          `json:"request_id"`
		Entries   []search.Entry  `json:"entries"`
		Coverage  search.Coverage `json:"coverage"`
	}
	decodeToolJSON(t, listing, &list)
	if list.RequestID == "" || len(list.Entries) < 2 {
		t.Fatalf("list_directory = %#v", list)
	}
	found := callTool(t, ctx, session, ToolFindFiles, map[string]any{"root_id": "workspace", "pattern": "**/*.go", "page_size": 16})
	var find struct {
		Entries []search.Entry `json:"entries"`
	}
	decodeToolJSON(t, found, &find)
	if len(find.Entries) != 2 {
		t.Fatalf("find_files = %#v", find)
	}
	textResult := callTool(t, ctx, session, ToolSearchText, map[string]any{"root_id": "workspace", "query": "needle", "page_size": 16})
	var text struct {
		Matches []search.Match `json:"matches"`
	}
	decodeToolJSON(t, textResult, &text)
	if len(text.Matches) != 3 {
		t.Fatalf("search_text = %#v", text)
	}
}

func TestStreamableHTTPProtocolCompatibility(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	server := newTestServer(t, fixture, []Credential{
		{ConnectionID: "connection-one", Token: testTokenOne},
		{ConnectionID: "connection-two", Token: testTokenTwo},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	type wireEnvelope struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	doPost := func(t *testing.T, token, method, version, sessionID, body string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, httpServer.URL, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set(LocalTokenHeader, token)
		if version != "" {
			req.Header.Set("Mcp-Protocol-Version", version)
			req.Header.Set("Mcp-Method", method)
		}
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp, payload
	}
	decode := func(t *testing.T, payload []byte) wireEnvelope {
		t.Helper()
		var envelope wireEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatalf("decode JSON-RPC envelope: %v; body=%q", err, payload)
		}
		if envelope.JSONRPC != "2.0" {
			t.Fatalf("jsonrpc = %q, want 2.0; body=%q", envelope.JSONRPC, payload)
		}
		return envelope
	}
	assertJSON := func(t *testing.T, response *http.Response) {
		t.Helper()
		if mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]); mediaType != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", response.Header.Get("Content-Type"))
		}
	}

	legacyInitialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"legacy-probe","version":"test"}}}`
	legacyResponse, legacyPayload := doPost(t, testTokenOne, "initialize", "", "", legacyInitialize)
	if legacyResponse.StatusCode != http.StatusOK {
		t.Fatalf("legacy initialize status = %d, want 200; body=%q", legacyResponse.StatusCode, legacyPayload)
	}
	assertJSON(t, legacyResponse)
	legacyEnvelope := decode(t, legacyPayload)
	if legacyEnvelope.Error != nil {
		t.Fatalf("legacy initialize error = %#v", legacyEnvelope.Error)
	}
	var legacyResult struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(legacyEnvelope.Result, &legacyResult); err != nil {
		t.Fatal(err)
	}
	if legacyResult.ProtocolVersion != "2025-06-18" {
		t.Fatalf("legacy protocolVersion = %q", legacyResult.ProtocolVersion)
	}
	legacySessionID := legacyResponse.Header.Get("Mcp-Session-Id")
	if legacySessionID == "" {
		t.Fatal("legacy initialize did not return Mcp-Session-Id")
	}

	initializedResponse, initializedPayload := doPost(t, testTokenOne, "notifications/initialized", "", legacySessionID, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)
	if initializedResponse.StatusCode != http.StatusAccepted || len(initializedPayload) != 0 {
		t.Fatalf("legacy initialized response = %d body=%q, want 202 with no body", initializedResponse.StatusCode, initializedPayload)
	}
	legacyToolsResponse, legacyToolsPayload := doPost(t, testTokenOne, "tools/list", "", legacySessionID, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if legacyToolsResponse.StatusCode != http.StatusOK {
		t.Fatalf("legacy tools/list status = %d; body=%q", legacyToolsResponse.StatusCode, legacyToolsPayload)
	}
	assertJSON(t, legacyToolsResponse)
	legacyToolsEnvelope := decode(t, legacyToolsPayload)
	var legacyTools struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(legacyToolsEnvelope.Result, &legacyTools); err != nil {
		t.Fatal(err)
	}
	if len(legacyTools.Tools) != 4 {
		t.Fatalf("legacy tools/list returned %d tools, want 4", len(legacyTools.Tools))
	}
	wrongIdentityResponse, _ := doPost(t, testTokenTwo, "tools/list", "", legacySessionID, `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`)
	if wrongIdentityResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy session with another token status = %d, want 403", wrongIdentityResponse.StatusCode)
	}
	deleteRequest, err := http.NewRequest(http.MethodDelete, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteRequest.Header.Set(LocalTokenHeader, testTokenOne)
	deleteRequest.Header.Set("Mcp-Session-Id", legacySessionID)
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("legacy DELETE status = %d, want 204", deleteResponse.StatusCode)
	}

	modernDiscover := `{"jsonrpc":"2.0","id":4,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-probe","version":"test"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	modernResponse, modernPayload := doPost(t, testTokenOne, "server/discover", modernMCPProtocolVersion, "", modernDiscover)
	if modernResponse.StatusCode != http.StatusOK {
		t.Fatalf("modern discover status = %d; body=%q", modernResponse.StatusCode, modernPayload)
	}
	assertJSON(t, modernResponse)
	if modernResponse.Header.Get("Mcp-Session-Id") != "" {
		t.Fatal("modern discover unexpectedly returned a session ID")
	}
	modernEnvelope := decode(t, modernPayload)
	var discovery struct {
		SupportedVersions []string `json:"supportedVersions"`
	}
	if err := json.Unmarshal(modernEnvelope.Result, &discovery); err != nil {
		t.Fatal(err)
	}
	if len(discovery.SupportedVersions) == 0 || discovery.SupportedVersions[0] != modernMCPProtocolVersion {
		t.Fatalf("modern supportedVersions = %v, want %q first", discovery.SupportedVersions, modernMCPProtocolVersion)
	}
	modernTools := `{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"modern-probe","version":"test"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	modernToolsResponse, modernToolsPayload := doPost(t, testTokenOne, "tools/list", modernMCPProtocolVersion, "", modernTools)
	if modernToolsResponse.StatusCode != http.StatusOK {
		t.Fatalf("modern tools/list status = %d; body=%q", modernToolsResponse.StatusCode, modernToolsPayload)
	}
	assertJSON(t, modernToolsResponse)
	modernToolsEnvelope := decode(t, modernToolsPayload)
	var modernToolsResult struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(modernToolsEnvelope.Result, &modernToolsResult); err != nil {
		t.Fatal(err)
	}
	if len(modernToolsResult.Tools) != 4 {
		t.Fatalf("modern tools/list returned %d tools, want 4", len(modernToolsResult.Tools))
	}
	modernGet, err := http.NewRequest(http.MethodGet, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	modernGet.Header.Set(LocalTokenHeader, testTokenOne)
	modernGet.Header.Set("Mcp-Protocol-Version", modernMCPProtocolVersion)
	modernGet.Header.Set("Accept", "text/event-stream")
	modernGetResponse, err := http.DefaultClient.Do(modernGet)
	if err != nil {
		t.Fatal(err)
	}
	modernGetResponse.Body.Close()
	if modernGetResponse.StatusCode != http.StatusMethodNotAllowed || modernGetResponse.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("modern GET response = %d Allow=%q, want 405 Allow POST", modernGetResponse.StatusCode, modernGetResponse.Header.Get("Allow"))
	}
	modernDelete, err := http.NewRequest(http.MethodDelete, httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	modernDelete.Header.Set(LocalTokenHeader, testTokenOne)
	modernDelete.Header.Set("Mcp-Protocol-Version", modernMCPProtocolVersion)
	modernDeleteResponse, err := http.DefaultClient.Do(modernDelete)
	if err != nil {
		t.Fatal(err)
	}
	modernDeleteResponse.Body.Close()
	if modernDeleteResponse.StatusCode != http.StatusMethodNotAllowed || modernDeleteResponse.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("modern DELETE response = %d Allow=%q, want 405 Allow POST", modernDeleteResponse.StatusCode, modernDeleteResponse.Header.Get("Allow"))
	}

	invalidModernResponse, invalidModernPayload := doPost(t, testTokenOne, "server/discover", modernMCPProtocolVersion, "", `{"jsonrpc":"2.0","id":6,"method":"server/discover","params":{}}`)
	if invalidModernResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid modern discover status = %d, want 400; body=%q", invalidModernResponse.StatusCode, invalidModernPayload)
	}
	invalidEnvelope := decode(t, invalidModernPayload)
	if invalidEnvelope.Error == nil {
		t.Fatalf("invalid modern discover returned no JSON-RPC error; body=%q", invalidModernPayload)
	}
}

func TestToolListAndCallsAreConnectionScoped(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	fixture.profileTwoTools = []string{ToolServerInfo, ToolPing}
	server := newTestServer(t, fixture, []Credential{
		{ConnectionID: "connection-one", Token: testTokenOne},
		{ConnectionID: "connection-two", Token: testTokenTwo},
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &testTokenTransport{token: testTokenTwo}
	client := mcp.NewClient(&mcp.Implementation{Name: "scoped-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL,
		HTTPClient:           &http.Client{Transport: transport},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	filteredNames := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		filteredNames = append(filteredNames, tool.Name)
	}
	sort.Strings(filteredNames)
	wantFilteredNames := []string{ToolPing, ToolServerInfo}
	if len(filteredNames) != len(wantFilteredNames) || filteredNames[0] != wantFilteredNames[0] || filteredNames[1] != wantFilteredNames[1] {
		t.Fatalf("filtered tools = %v", filteredNames)
	}
	disallowed := callTool(t, ctx, session, ToolReadFile, map[string]any{
		"root_id": "workspace",
		"path":    "hello.txt",
	})
	if !disallowed.IsError || toolText(t, disallowed) == "" {
		t.Fatalf("disallowed call = %#v", disallowed)
	}

	// Modern requests are sessionless, so each request is authenticated against
	// its current bearer token rather than reusing a stateful session identity.
	transport.setToken(testTokenOne)
	infoResult := callTool(t, ctx, session, ToolServerInfo, map[string]any{})
	var info serverInfoOutput
	decodeToolJSON(t, infoResult, &info)
	if info.ConnectionID != "connection-one" {
		t.Fatalf("modern request used connection %q after token switch, want connection-one", info.ConnectionID)
	}
}

func TestAuthenticationAndCrossOriginProtection(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	server := newTestServer(t, fixture, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	newRequest := func(token string) *http.Request {
		req, err := http.NewRequest(http.MethodPost, httpServer.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set(LocalTokenHeader, token)
		}
		return req
	}
	for _, test := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", token: "wrong-token-with-enough-length", status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			resp, err := http.DefaultClient.Do(newRequest(test.token))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, test.status)
			}
			body, _ := io.ReadAll(resp.Body)
			if string(body) == test.token && test.token != "" {
				t.Fatal("authentication response echoed the token")
			}
		})
	}

	valid := newRequest(testTokenOne)
	valid.Header.Set("Origin", "https://attacker.example")
	valid.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(valid)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestResolveToken(t *testing.T) {
	t.Setenv("LOCAL_PROBE_TEST_TOKEN", testTokenOne)
	got, err := ResolveToken("LOCAL_PROBE_TEST_TOKEN", "")
	if err != nil || got != testTokenOne {
		t.Fatalf("ResolveToken(env) = %q, %v", got, err)
	}
	file := t.TempDir() + "/token.txt"
	if err := os.WriteFile(file, []byte(testTokenTwo+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ResolveToken("", file)
	if err != nil || got != testTokenTwo {
		t.Fatalf("ResolveToken(file) = %q, %v", got, err)
	}
	if _, err := ResolveToken("LOCAL_PROBE_TEST_TOKEN", file); err == nil {
		t.Fatal("ResolveToken() accepted two sources")
	}
}

type testFixture struct {
	store           *config.Store
	manager         *policy.Manager
	source          *rootfs.Source
	root            string
	profileTwoTools []string
	profileOneTools []string
}

func newFixture(t *testing.T) *testFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(root+"/hello.txt", []byte("hello from Local-Probe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &testFixture{root: root}
}

func (f *testFixture) close() {
	if f.source != nil {
		_ = f.source.Close()
	}
}

func newTestServer(t *testing.T, fixture *testFixture, credentials []Credential) *Server {
	t.Helper()
	root, err := config.NewRoot("workspace", fixture.root, nil)
	if err != nil {
		t.Fatal(err)
	}
	allTools := fixture.profileOneTools
	if allTools == nil {
		allTools = []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead}
	}
	profileOne, err := config.NewProfile("profile-one", []string{"workspace"}, allTools, nil)
	if err != nil {
		t.Fatal(err)
	}
	profileTwoTools := fixture.profileTwoTools
	if profileTwoTools == nil {
		profileTwoTools = allTools
	}
	profileTwo, err := config.NewProfile("profile-two", []string{"workspace"}, profileTwoTools, nil)
	if err != nil {
		t.Fatal(err)
	}
	credentialOne := config.NewCredentialRef("credential-one", "local_token")
	credentialTwo := config.NewCredentialRef("credential-two", "local_token")
	connectionOne := config.NewConnection("connection-one", "one", profileOne.ID(), credentialOne.ID(), true)
	connectionTwo := config.NewConnection("connection-two", "two", profileTwo.ID(), credentialTwo.ID(), true)
	cfg, err := config.New(config.SchemaVersionV1, []config.Root{root}, []config.Profile{profileOne, profileTwo}, []config.Connection{connectionOne, connectionTwo}, []config.CredentialRef{credentialOne, credentialTwo})
	if err != nil {
		t.Fatal(err)
	}
	fixture.store, err = config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager, err = policy.NewManager(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	fixture.source, err = rootfs.New(fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Manager: fixture.manager, Source: fixture.source, Credentials: credentials})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%q) error = %v", name, err)
	}
	return result
}

func toolText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func decodeToolJSON(t *testing.T, result *mcp.CallToolResult, dst any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned error: %s", toolText(t, result))
	}
	data := []byte(toolText(t, result))
	if result.StructuredContent != nil {
		var err error
		data, err = json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatalf("marshal structured tool result: %v", err)
		}
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode tool result: %v; content=%q", err, toolText(t, result))
	}
}

type testTokenTransport struct {
	mu    sync.RWMutex
	token string
}

func (t *testTokenTransport) setToken(token string) {
	t.mu.Lock()
	t.token = token
	t.mu.Unlock()
}

func (t *testTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.RLock()
	token := t.token
	t.mu.RUnlock()
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set(LocalTokenHeader, token)
	return http.DefaultTransport.RoundTrip(clone)
}
