package relay

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/agentbridge"
	"github.com/LE-saber/Local-Probe/internal/config"
	"github.com/LE-saber/Local-Probe/internal/deviceidentity"
	"github.com/LE-saber/Local-Probe/internal/mcpserver"
	"github.com/LE-saber/Local-Probe/internal/peertransport"
	"github.com/LE-saber/Local-Probe/internal/policy"
	"github.com/LE-saber/Local-Probe/internal/rootfs"
	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This opt-in test uses an already provisioned Relay. No deployment, SSH access,
// account creation, firewall edits or user filesystem access happens here.
func TestPublicRelayReadOnlyEndToEnd(t *testing.T) {
	endpoint := os.Getenv("LOCAL_PROBE_PUBLIC_RELAY_URL")
	if endpoint == "" {
		t.Skip("opt-in: explicit deployed Relay URL and private test credentials required")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		t.Fatal("explicit HTTPS Relay origin required")
	}
	credentialsPath, caPath := os.Getenv("LOCAL_PROBE_PUBLIC_CREDENTIALS_FILE"), os.Getenv("LOCAL_PROBE_PUBLIC_CA_FILE")
	if !filepath.IsAbs(credentialsPath) || !filepath.IsAbs(caPath) {
		t.Fatal("absolute private credentials and public CA paths required")
	}
	file, err := os.Open(credentialsPath)
	if err != nil {
		t.Fatal("test credentials unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	_ = file.Close()
	if err != nil {
		t.Fatal("test credentials unavailable")
	}
	var credentials struct {
		DeviceID    string `json:"device_id"`
		TargetToken string `json:"target_token"`
		AgentToken  string `json:"agent_token"`
	}
	if deviceidentity.DecodePublic(data, &credentials) != nil || len(credentials.TargetToken) < 32 || len(credentials.AgentToken) < 32 {
		t.Fatal("invalid private test credentials")
	}
	clear(data)
	roots, err := peertransport.LoadPublicRoots(caPath)
	if err != nil {
		t.Fatal(err)
	}
	// Optional SSH diagnostic tunnel changes only TCP destination. The original
	// URL hostname still controls TLS verification, and both TLS layers remain.
	dial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	if connect := os.Getenv("LOCAL_PROBE_PUBLIC_CONNECT_ADDRESS"); connect != "" {
		host, _, err := net.SplitHostPort(connect)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() {
			t.Fatal("diagnostic connect address must be explicit loopback")
		}
		dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, connect)
		}
		t.Log("transport=SSH-forwarded TCP; hostname verification and inner mTLS remain enabled; this is not direct public WSS evidence")
	} else {
		t.Log("transport=direct public WSS")
	}
	outerTransport := &http.Transport{Proxy: nil, DialContext: dial, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer outerTransport.CloseIdleConnections()
	outerClient := &http.Client{Transport: outerTransport, Timeout: 15 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	response, err := outerClient.Get(strings.TrimSuffix(endpoint, "/") + "/healthz")
	if err != nil {
		t.Fatal("deployed HTTPS endpoint unavailable:", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 204 {
		t.Fatal("deployed HTTPS health failed")
	}
	for _, token := range []string{"invalid-public-test-credential", credentials.TargetToken} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(endpoint, "/")+"/v1/agent", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := outerClient.Do(req)
		if err != nil {
			t.Fatal("negative admission request failed")
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatal("unauthorized or wrong-role caller accepted")
		}
	}
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "evidence.txt"), []byte("public-relay-read-evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := config.NewRoot("workspace", workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := config.NewProfile("profile-a", []string{"workspace"}, []string{"server_info", "read_file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := config.NewConnection("connection-a", "public test", profile.ID(), "credential-a", true).WithPurpose(config.PurposeAgentRemote)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.New(config.SchemaVersionV2, []config.Root{root}, []config.Profile{profile}, []config.Connection{connection}, []config.CredentialRef{config.NewCredentialRef("credential-a", "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	configStore, err := config.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := policy.NewManager(configStore)
	if err != nil {
		t.Fatal(err)
	}
	source, err := rootfs.New(configStore)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	store, err := deviceidentity.CreateStore(filepath.Join(base, "identity"))
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
	agent, csr, err := deviceidentity.NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	fp, _ := csr.Fingerprint()
	grant, err := store.IssueGrant(csr, fp, connection.ID(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := deviceidentity.NewRegistry(store)
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpserver.New(mcpserver.Options{Manager: manager, Source: source, PeerRegistry: registry})
	if err != nil {
		t.Fatal(err)
	}
	inner, err := registry.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	target, err := peertransport.NewEndpoint(inner, server.Handler(), func(*http.Request) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	wss := "wss" + strings.TrimPrefix(strings.TrimSuffix(endpoint, "/"), "https")
	dialStream := func(ctx context.Context, role, token string, registered chan<- struct{}) (net.Conn, error) {
		d := websocket.Dialer{NetDialContext: dial, HandshakeTimeout: 10 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, EnableCompression: false}
		ws, resp, err := d.DialContext(ctx, wss+"/v1/"+role, http.Header{"Authorization": []string{"Bearer " + token}})
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			return nil, err
		}
		if registered != nil {
			registered <- struct{}{}
		}
		stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
		ws.SetReadLimit(16 << 10)
		_ = ws.SetReadDeadline(time.Now().Add(35 * time.Second))
		kind, b, err := ws.ReadMessage()
		stop()
		if err != nil || kind != websocket.TextMessage || string(b) != ReadyMessage {
			_ = ws.Close()
			return nil, io.ErrUnexpectedEOF
		}
		_ = ws.SetReadDeadline(time.Time{})
		return peertransport.NewStream(ws), nil
	}
	registered := make(chan struct{}, 4)
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			raw, err := dialStream(ctx, "target", credentials.TargetToken, registered)
			if err != nil {
				return
			}
			stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
			defer stop()
			_ = target.AcceptStream(ctx, raw)
			<-ctx.Done()
			_ = raw.Close()
		}()
	}
	defer func() { cancel(); workers.Wait() }()
	select {
	case <-registered:
	case <-ctx.Done():
		t.Fatal("target outbound registration failed")
	}
	bundle := a.Bundle()
	bf, _ := bundle.Fingerprint()
	clientTLS, err := agent.ClientTLS(bundle, grant, bf)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{ForceAttemptHTTP2: false, DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		raw, err := dialStream(ctx, "agent", credentials.AgentToken, nil)
		if err != nil {
			return nil, err
		}
		conn := tls.Client(raw, clientTLS)
		if err = conn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}}
	defer transport.CloseIdleConnections()
	bridge, remote, err := agentbridge.New(ctx, &http.Client{Transport: transport})
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
	client := mcp.NewClient(&mcp.Implementation{Name: "public-relay-test-agent", Version: "test"}, nil)
	session, err := client.Connect(ctx, left, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_file", Arguments: map[string]any{"root_id": "workspace", "path": "evidence.txt", "max_bytes": 64}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if result.IsError || !strings.Contains(string(encoded), "public-relay-read-evidence") {
		t.Fatal("real file bytes missing")
	}
	denied, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "read_file", Arguments: map[string]any{"root_id": "outside", "path": "evidence.txt", "max_bytes": 64}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(denied)
	if !strings.Contains(string(encoded), `"code":"denied"`) || strings.Contains(string(encoded), "public-relay-read-evidence") {
		t.Fatal("scope denial failed")
	}
	unknown, unknownCSR, err := deviceidentity.NewAgent()
	if err != nil {
		t.Fatal(err)
	}
	ufp, _ := unknownCSR.Fingerprint()
	unknownGrant, err := a.Issue(unknownCSR, ufp, connection.ID(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	unknownTLS, err := unknown.ClientTLS(bundle, unknownGrant, bf)
	if err != nil {
		t.Fatal(err)
	}
	badRaw, err := dialStream(ctx, "agent", credentials.AgentToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := tls.Client(badRaw, unknownTLS)
	defer badRaw.Close()
	_ = bad.SetDeadline(time.Now().Add(10 * time.Second))
	if err = bad.HandshakeContext(ctx); err == nil {
		_, writeErr := bad.Write([]byte("GET /mcp HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"))
		if writeErr == nil {
			buffer := make([]byte, 1)
			if _, readErr := bad.Read(buffer); readErr == nil {
				t.Fatal("unregistered Agent certificate accepted")
			}
		}
	}
	t.Log("PASS: TLS hostname validation, outer role rejection, inner mutual TLS, real scoped file read, cross-root denial, unregistered certificate rejection")
}
