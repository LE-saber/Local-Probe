package agentbridge

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/peertransport"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPairedWSSBridgeForwardsOfficialMCPAndLiveRevocation(t *testing.T) {
	base := t.TempDir()
	store, err := deviceidentity.CreateStore(filepath.Join(base, "target"))
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
	agent, request, err := deviceidentity.NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := request.Fingerprint()
	grant, err := store.IssueGrant(request, fp, "connection-a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := deviceidentity.NewRegistry(store)
	if err != nil {
		t.Fatal(err)
	}
	target := mcp.NewServer(&mcp.Implementation{Name: "target-test", Version: "test"}, nil)
	target.AddTool(&mcp.Tool{Name: "check_scope", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		g, ok := deviceidentity.GrantFromContext(ctx)
		if !ok || g.ConnectionID != "connection-a" {
			t.Fatal("scope lost")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: g.ConnectionID}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return target }, &mcp.StreamableHTTPOptions{Stateless: true})
	inner, err := registry.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := peertransport.NewEndpoint(inner, registry.Handler(handler), func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer test-admission" })
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	outer := httptest.NewTLSServer(endpoint)
	defer outer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(outer.Certificate())
	bundle := a.Bundle()
	bf, _ := bundle.Fingerprint()
	clientTLS, err := agent.ClientTLS(bundle, grant, bf)
	if err != nil {
		t.Fatal(err)
	}
	httpClient, closeClient, err := peertransport.HTTPClientWithRoots("wss"+strings.TrimPrefix(outer.URL, "https"), clientTLS, http.Header{"Authorization": []string{"Bearer test-admission"}}, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bridge, remote, err := New(ctx, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	local, remoteTransport := mcp.NewInMemoryTransports()
	bridgeSession, err := bridge.Connect(ctx, remoteTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "native-agent", Version: "test"}, nil)
	session, err := client.Connect(ctx, local, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 1 || list.Tools[0].Name != "check_scope" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "check_scope", Arguments: map[string]any{}})
	if err != nil || result.IsError || result.Content[0].(*mcp.TextContent).Text != "connection-a" {
		t.Fatalf("call=%+v err=%v", result, err)
	}
	var pin peertransport.Pin
	raw, _ := hex.DecodeString(grant.KeyPin)
	copy(pin[:], raw)
	if err = registry.Revoke(pin); err != nil {
		t.Fatal(err)
	}
	if _, err = session.ListTools(ctx, nil); err == nil {
		t.Fatal("revoked tool list cached by bridge")
	}
}
