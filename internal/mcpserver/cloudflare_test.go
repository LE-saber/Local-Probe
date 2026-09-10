package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/cfaccess"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCloudflareAccessMCPInitializeToolsListAndCall(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	// Reuse the existing test setup helper to build the policy manager and
	// rootfs source. The returned local-token server never opens a listener.
	_ = newTestServer(t, fixture, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &oidctest.Server{PublicKeys: []oidctest.PublicKey{{
		PublicKey: privateKey.Public(),
		KeyID:     "test-key",
		Algorithm: oidc.RS256,
	}}}
	issuerServer := httptest.NewServer(provider)
	defer issuerServer.Close()
	provider.SetIssuer(issuerServer.URL)

	accessVerifier, err := cfaccess.New(cfaccess.Config{
		Issuer:                issuerServer.URL,
		JWKSURL:               issuerServer.URL + "/keys",
		Audience:              "audience-a",
		ClockSkew:             30 * time.Second,
		PrincipalToConnection: map[string]string{"subject-a": "connection-one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC()
	claims, err := json.Marshal(map[string]any{
		"iss": issuerServer.URL,
		"aud": "audience-a",
		"sub": "subject-a",
		"iat": base.Unix(),
		"exp": base.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	assertion := oidctest.SignIDToken(privateKey, "test-key", oidc.RS256, string(claims))

	server, err := New(Options{
		Manager:               fixture.manager,
		Source:                fixture.source,
		CloudflareAccess:      accessVerifier,
		CloudflarePublicHosts: []string{"mcp.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &cloudflareTestTransport{token: assertion, host: "mcp.example.test"}
	client := mcp.NewClient(&mcp.Implementation{Name: "cloudflare-test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL + "/mcp",
		HTTPClient:           &http.Client{Transport: transport},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{ToolBatchRead, ToolPing, ToolReadFile, ToolServerInfo}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want %v", names, want)
		}
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolPing, Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("CallTool(ping) = %#v, %v", result, err)
	}

	// Exercise the stateful transport's standalone GET and session DELETE
	// paths through the same trusted Host/assertion adapter.
	sessionID := transportSessionID(transport)
	if sessionID == "" {
		t.Fatal("initialize did not return an MCP session ID")
	}
	getContext, getCancel := context.WithTimeout(ctx, 2*time.Second)
	getRequest, err := http.NewRequestWithContext(getContext, http.MethodGet, httpServer.URL+"/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	getRequest.Header.Set("Accept", "text/event-stream")
	getRequest.Header.Set("Mcp-Session-Id", sessionID)
	getResponse, err := (&http.Client{Transport: transport}).Do(getRequest)
	getCancel()
	if err != nil {
		t.Fatalf("GET SSE error = %v", err)
	}
	if getResponse.StatusCode != http.StatusOK || getResponse.Header.Get("Content-Type") != "text/event-stream" {
		getResponse.Body.Close()
		t.Fatalf("GET SSE response = %d %q", getResponse.StatusCode, getResponse.Header.Get("Content-Type"))
	}
	_ = getResponse.Body.Close()
	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}
	if !transportSawMethod(transport, http.MethodGet) || !transportSawMethod(transport, http.MethodDelete) {
		t.Fatalf("transport methods = %v, want GET and DELETE", transportMethods(transport))
	}
}

func TestCloudflareAccessHostAndIdentitySourceAreExplicit(t *testing.T) {
	fixture := newFixture(t)
	defer fixture.close()
	_ = newTestServer(t, fixture, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})

	issuerServer := newTestOIDCServer(t)
	accessVerifier, err := cfaccess.New(cfaccess.Config{
		Issuer:                issuerServer.URL,
		JWKSURL:               issuerServer.URL + "/keys",
		Audience:              "audience-a",
		PrincipalToConnection: map[string]string{"subject-a": "connection-one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Manager:               fixture.manager,
		Source:                fixture.source,
		CloudflareAccess:      accessVerifier,
		CloudflarePublicHosts: []string{"mcp.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}

	validRequest := httptest.NewRequest(http.MethodPost, "http://mcp.example.test/mcp", nil)
	validRequest.Host = "mcp.example.test"
	validRequest.Header.Set("Authorization", "Bearer local-token-must-not-work")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, validRequest)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("Authorization fallback status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	wrongHost := httptest.NewRequest(http.MethodPost, "http://attacker.example/mcp", nil)
	wrongHost.Host = "attacker.example"
	wrongHost.Header.Set(CloudflareAccessHeader, "not-a-token")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, wrongHost)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong Host status = %d, want %d", response.Code, http.StatusForbidden)
	}

	missingAssertion := httptest.NewRequest(http.MethodPost, "http://mcp.example.test/mcp", nil)
	missingAssertion.Host = "mcp.example.test"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, missingAssertion)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing CF assertion status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	duplicateAssertion := httptest.NewRequest(http.MethodPost, "http://mcp.example.test/mcp", nil)
	duplicateAssertion.Host = "mcp.example.test"
	duplicateAssertion.Header.Add(CloudflareAccessHeader, "first")
	duplicateAssertion.Header.Add(CloudflareAccessHeader, "second")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, duplicateAssertion)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate CF assertion status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	for _, host := range []string{"mcp.example.test:443", "mcp.example.test."} {
		request := httptest.NewRequest(http.MethodPost, "http://mcp.example.test/mcp", nil)
		request.Host = host
		request.Header.Set(CloudflareAccessHeader, "not-a-token")
		response = httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("non-exact Host %q status = %d, want %d", host, response.Code, http.StatusForbidden)
		}
	}
}

func TestCloudflareAccessDiscardsManagedOAuthAuthorization(t *testing.T) {
	var gotAuthorization string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "http://mcp.example.test/mcp", nil)
	request.Header.Set(CloudflareAccessHeader, "signed-assertion")
	request.Header.Set("Authorization", "Bearer opaque-managed-oauth-token")
	response := httptest.NewRecorder()

	cloudflareAccessHeader(next).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if gotAuthorization != "Bearer signed-assertion" {
		t.Fatalf("adapted Authorization = %q", gotAuthorization)
	}
}

type cloudflareTestTransport struct {
	token string
	host  string
	mu    sync.Mutex
	seen  []string
	state string
}

func (t *cloudflareTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.seen = append(t.seen, req.Method)
	t.mu.Unlock()
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set(CloudflareAccessHeader, t.token)
	// Managed OAuth can forward the client's opaque access token. The origin
	// must ignore it and authenticate only the Access-signed assertion.
	clone.Header.Set("Authorization", "Bearer opaque-managed-oauth-token")
	clone.Host = t.host
	response, err := http.DefaultTransport.RoundTrip(clone)
	if response != nil {
		if sessionID := response.Header.Get("Mcp-Session-Id"); sessionID != "" {
			t.mu.Lock()
			t.state = sessionID
			t.mu.Unlock()
		}
	}
	return response, err
}

func transportSessionID(transport *cloudflareTestTransport) string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.state
}

func transportSawMethod(transport *cloudflareTestTransport, method string) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, seen := range transport.seen {
		if seen == method {
			return true
		}
	}
	return false
}

func transportMethods(transport *cloudflareTestTransport) []string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]string(nil), transport.seen...)
}

type testOIDCServer struct {
	*httptest.Server
	privateKey *rsa.PrivateKey
}

func newTestOIDCServer(t *testing.T) *testOIDCServer {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	provider := &oidctest.Server{PublicKeys: []oidctest.PublicKey{{
		PublicKey: privateKey.Public(),
		KeyID:     "test-key",
		Algorithm: oidc.RS256,
	}}}
	server := httptest.NewServer(provider)
	provider.SetIssuer(server.URL)
	t.Cleanup(server.Close)
	return &testOIDCServer{Server: server, privateKey: privateKey}
}
