package mcpserver

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/agentbridge"
	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/peertransport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPairedAgentUsesExistingFileScopeAndRejectsMixedAuthentication(t *testing.T) {
	f := newFixture(t)
	defer f.close()
	_ = newTestServer(t, f, []Credential{{ConnectionID: "connection-one", Token: testTokenOne}})
	store, err := deviceidentity.CreateStore(filepath.Join(t.TempDir(), "identity"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a, err := deviceidentity.NewAuthority()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveAuthority(a); err != nil {
		t.Fatal(err)
	}
	agent, req, err := deviceidentity.NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := req.Fingerprint()
	g, err := store.IssueGrant(req, fp, "connection-one", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := deviceidentity.NewRegistry(store)
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Manager: f.manager, Source: f.source, PeerRegistry: registry}
	server, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Credentials = []Credential{{ConnectionID: "connection-one", Token: testTokenOne}}
	if _, err = New(options); err == nil {
		t.Fatal("mixed ingress accepted")
	}
	inner, err := registry.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := peertransport.NewEndpoint(inner, server.Handler(), func(*http.Request) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	outer := httptest.NewTLSServer(endpoint)
	defer outer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(outer.Certificate())
	b := a.Bundle()
	bf, _ := b.Fingerprint()
	innerClient, err := agent.ClientTLS(b, g, bf)
	if err != nil {
		t.Fatal(err)
	}
	hc, closeTransport, err := peertransport.HTTPClientWithRoots("wss"+strings.TrimPrefix(outer.URL, "https"), innerClient, nil, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bridge, remote, err := agentbridge.New(ctx, hc)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	left, right := mcp.NewInMemoryTransports()
	ss, err := bridge.Connect(ctx, right, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "paired-native-agent", Version: "test"}, nil)
	session, err := client.Connect(ctx, left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result := callTool(t, ctx, session, ToolReadFile, map[string]any{"root_id": "workspace", "path": "hello.txt", "max_bytes": 64})
	var output readFileOutput
	decodeToolJSON(t, result, &output)
	if output.Item.Content != "hello from Local-Probe\n" {
		t.Fatal("read failed")
	}
	info := callTool(t, ctx, session, ToolServerInfo, map[string]any{})
	var identity serverInfoOutput
	decodeToolJSON(t, info, &identity)
	if identity.ConnectionID != "connection-one" || identity.ProfileID != "profile-one" || identity.TargetDeviceID != b.DeviceID || identity.Purpose != "agent_remote" {
		t.Fatalf("wrong identity: %+v", identity)
	}
	denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolReadFile, Arguments: map[string]any{"root_id": "private", "path": "hello.txt", "max_bytes": 64}})
	if err != nil {
		t.Fatal(err)
	}
	var rejected readFileOutput
	decodeToolJSON(t, denied, &rejected)
	if rejected.Item.Error == nil || rejected.Item.Error.Code != "denied" || rejected.Item.Content != "" {
		t.Fatal("cross-root read did not enforce scope")
	}
	// Forged HTTP bearer identity cannot enter the exclusive peer ingress.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", strings.NewReader("{}"))
	request.Header.Set(LocalTokenHeader, testTokenTwo)
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatal("plaintext bearer entered peer ingress")
	}
}
