package relay

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
	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const targetToken = "synthetic-target-admission-0123456789"
const agentToken = "synthetic-agent-admission-01234567890"

func TestDisconnectedWaitingTargetIsRemovedBeforeNextPairing(t *testing.T) {
	service, err := New([]Route{{"device-test", targetToken, agentToken}})
	if err != nil {
		t.Fatal(err)
	}
	outer := httptest.NewServer(service)
	defer outer.Close()
	defer service.Close()
	target, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(outer.URL, "http")+"/v1/target", http.Header{"Authorization": []string{"Bearer " + targetToken}})
	if err != nil {
		t.Fatal(err)
	}
	_ = target.Close()
	deadline := time.Now().Add(2 * time.Second)
	for {
		service.mu.Lock()
		remaining := service.total
		service.mu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected waiting target still consumes pairing capacity")
		}
		time.Sleep(time.Millisecond)
	}
	request, _ := http.NewRequest(http.MethodGet, outer.URL+"/v1/agent", nil)
	request.Header.Set("Authorization", "Bearer "+agentToken)
	response, err := outer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("offline route returned %d instead of rejecting pairing", response.StatusCode)
	}
}

func TestRelayCarriesPairedMCPWithoutTerminatingInnerTLS(t *testing.T) {
	service, err := New([]Route{{"device-test", targetToken, agentToken}})
	if err != nil {
		t.Fatal(err)
	}
	outer := httptest.NewTLSServer(service)
	defer outer.Close()
	defer service.Close()
	roots := x509.NewCertPool()
	roots.AddCert(outer.Certificate())
	baseURL := "wss" + strings.TrimPrefix(outer.URL, "https")
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
	agent, request, err := deviceidentity.NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := request.Fingerprint()
	g, err := store.IssueGrant(request, fp, "connection-relay", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := deviceidentity.NewRegistry(store)
	if err != nil {
		t.Fatal(err)
	}
	target := mcp.NewServer(&mcp.Implementation{Name: "relay-target", Version: "test"}, nil)
	target.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		grant, ok := deviceidentity.GrantFromContext(ctx)
		if !ok {
			t.Error("missing grant")
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: grant.ConnectionID}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return target }, &mcp.StreamableHTTPOptions{Stateless: true})
	inner, err := registry.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := peertransport.NewEndpoint(inner, registry.Handler(handler), func(*http.Request) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reverseDone := make(chan error, 1)
	go func() {
		reverseDone <- endpoint.ServeRelay(ctx, baseURL+"/v1/target", http.Header{"Authorization": []string{"Bearer " + targetToken}}, roots)
	}()
	// Wait for a real outgoing target registration; no arbitrary startup sleep.
	deadline := time.Now().Add(3 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		service.mu.Lock()
		for _, slot := range service.routes[0].slots {
			if slot.ws != nil {
				registered = true
			}
		}
		service.mu.Unlock()
		if registered {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !registered {
		t.Fatal("target did not register")
	}
	b := a.Bundle()
	bf, _ := b.Fingerprint()
	clientTLS, err := agent.ClientTLS(b, g, bf)
	if err != nil {
		t.Fatal(err)
	}
	hc, closeHTTP, err := peertransport.RelayHTTPClient(baseURL+"/v1/agent", clientTLS, http.Header{"Authorization": []string{"Bearer " + agentToken}}, roots)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHTTP()
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
	client := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "test"}, nil)
	session, err := client.Connect(ctx, left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Content[0].(*mcp.TextContent).Text != "connection-relay" {
		t.Fatal("inner MCP data corrupted")
	}
	cancel()
	select {
	case err := <-reverseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reverse workers did not stop")
	}
}

func TestRelayRejectsUnknownRoleOfflineRouteAndArbitraryDestination(t *testing.T) {
	service, err := New([]Route{{"device-test", targetToken, agentToken}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{"/v1/agent", targetToken, 401}, {"/v1/target", agentToken, 401}, {"/v1/agent", agentToken, 503}, {"/v1/agent?destination=http://internal", agentToken, 404},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		service.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("path=%s status=%d want=%d", tc.path, w.Code, tc.status)
		}
	}
	if _, err = New([]Route{{"device-test", agentToken, agentToken}}); err == nil {
		t.Fatal("shared role credential accepted")
	}
}

func TestRelayBoundsWaitingTargetsAndShutdown(t *testing.T) {
	service, err := New([]Route{{"device-test", targetToken, agentToken}})
	if err != nil {
		t.Fatal(err)
	}
	service.wait = 50 * time.Millisecond
	srv := httptest.NewTLSServer(service)
	defer srv.Close()
	defer service.Close()
	dialer := websocket.Dialer{TLSClientConfig: srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	conn, _, err := dialer.Dial("wss"+strings.TrimPrefix(srv.URL, "https")+"/v1/target", http.Header{"Authorization": []string{"Bearer " + targetToken}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = conn.ReadMessage(); err == nil {
		t.Fatal("unpaired target did not expire")
	}
}
