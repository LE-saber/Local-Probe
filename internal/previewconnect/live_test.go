package previewconnect

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestControllerLiveTunnelOptIn exercises the exact controller used by the
// Preview button. It is skipped in normal test runs because it needs the
// operator's external Tunnel credential and makes a real outbound connection.
func TestControllerLiveTunnelOptIn(t *testing.T) {
	if os.Getenv("LOCAL_PROBE_LIVE_TUNNEL_TEST") != "1" {
		t.Skip("set LOCAL_PROBE_LIVE_TUNNEL_TEST=1 for an authorized live check")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("cannot resolve repository root")
	}
	opts := DefaultOptions(repoRoot)
	opts.Timeout = 45 * time.Second
	controller, err := New(opts)
	if err != nil {
		t.Fatalf("create controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Stop() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	status, err := controller.Connect(ctx, nil)
	if err != nil {
		t.Fatalf("connect failed safely: code=%s stage=%s", status.Code, status.Stage)
	}
	if status.Stage != StageReady || !status.MCPReady || !status.TunnelReady || status.EdgeConnections < 1 {
		t.Fatalf("connection not ready: code=%s stage=%s mcp=%t tunnel=%t edges=%d", status.Code, status.Stage, status.MCPReady, status.TunnelReady, status.EdgeConnections)
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+status.PublicHost+"/mcp", nil)
	if err != nil {
		t.Fatal("cannot create public route probe")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal("public route request failed")
	}
	_ = response.Body.Close()
	if response.StatusCode >= http.StatusInternalServerError {
		t.Fatalf("public route returned HTTP %d", response.StatusCode)
	}
	status, err = controller.Reconnect(ctx, nil)
	if err != nil || status.Stage != StageReady || status.EdgeConnections < 1 {
		t.Fatalf("reconnect failed safely: code=%s stage=%s edges=%d", status.Code, status.Stage, status.EdgeConnections)
	}
}
