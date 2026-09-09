package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
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

	if err := session.Ping(ctx, nil); err != nil {
		t.Fatalf("Ping() error = %v", err)
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

	// The SDK's session user binding must reject reusing a session ID with a
	// different connection token.
	transport.setToken(testTokenOne)
	if _, err := session.ListTools(ctx, nil); err == nil {
		t.Fatal("ListTools() with a different connection token unexpectedly succeeded")
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
	allTools := []string{ToolServerInfo, ToolPing, ToolReadFile, ToolBatchRead}
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
