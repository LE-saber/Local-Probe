package peertransport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testPKI struct {
	roots               *x509.CertPool
	target, agent       tls.Certificate
	targetPin, agentPin Pin
}

// Certificates are ephemeral test fixtures, never installed in OS trust stores.
func newPKI(t *testing.T) testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-only"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	issue := func(serial int64, usage x509.ExtKeyUsage) (tls.Certificate, Pin) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{"target.test"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
		b, err := x509.CreateCertificate(rand.Reader, leaf, ca, &k.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		priv, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}))
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(b)
		if err != nil {
			t.Fatal(err)
		}
		return pair, PublicKeyPin(cert)
	}
	target, targetPin := issue(2, x509.ExtKeyUsageServerAuth)
	agent, agentPin := issue(3, x509.ExtKeyUsageClientAuth)
	return testPKI{roots, target, agent, targetPin, agentPin}
}

func TestMutualTLSRejectsUnpairedAndRevokedPeers(t *testing.T) {
	p := newPKI(t)
	for _, tc := range []struct {
		name    string
		pin     Pin
		allowed bool
	}{
		{"paired", p.targetPin, true}, {"wrong-target", Pin{1}, true}, {"revoked-agent", p.targetPin, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scfg, err := ServerTLS(p.target, p.roots, func(pin Pin) bool { return tc.allowed && pin == p.agentPin })
			if err != nil {
				t.Fatal(err)
			}
			ccfg, err := ClientTLS(p.agent, p.roots, "target.test", tc.pin)
			if err != nil {
				t.Fatal(err)
			}
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- tls.Server(a, scfg).HandshakeContext(ctx) }()
			clientErr := tls.Client(b, ccfg).HandshakeContext(ctx)
			serverErr := <-done
			if tc.name == "paired" {
				if clientErr != nil || serverErr != nil {
					t.Fatalf("handshake: %v / %v", clientErr, serverErr)
				}
			} else if clientErr == nil && serverErr == nil {
				t.Fatal("unpaired handshake accepted")
			}
		})
	}
}

func TestHTTPAuthorizationRejectsPlaintextAndHeaderIdentity(t *testing.T) {
	called := false
	handler := AuthorizeHTTP(func(Pin) bool { return true }, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req := httptest.NewRequest(http.MethodPost, "https://127.0.0.1/mcp", nil)
	req.Header.Set("X-Peer-Identity", "paired-agent")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized || called {
		t.Fatal("unauthenticated identity reached handler")
	}
}

func TestStreamRejectsTextAndOversizedMessages(t *testing.T) {
	for _, kind := range []int{websocket.TextMessage, websocket.BinaryMessage} {
		t.Run(map[int]string{websocket.TextMessage: "text", websocket.BinaryMessage: "oversized"}[kind], func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.WriteMessage(kind, []byte(strings.Repeat("x", MaxMessageBytes+1)))
			}))
			defer srv.Close()
			ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			stream := NewStream(ws)
			defer stream.Close()
			_ = stream.SetReadDeadline(time.Now().Add(time.Second))
			if _, err = io.ReadAll(stream); err == nil {
				t.Fatal("invalid message accepted")
			}
			// A failed stream must return its cached error rather than repeatedly
			// calling Gorilla's NextReader until it panics.
			for range 1100 {
				if _, err = stream.Read(make([]byte, 1)); err == nil {
					t.Fatal("failed stream recovered")
				}
			}
		})
	}
}

func TestReadDeadlineCanBeResetWithoutPoisoningWebSocket(t *testing.T) {
	send := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		<-send
		_ = ws.WriteMessage(websocket.BinaryMessage, []byte("recovered"))
	}))
	defer srv.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	stream := NewStream(ws)
	defer stream.Close()
	_ = stream.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	if _, err = stream.Read(make([]byte, 1)); err == nil {
		t.Fatal("read did not time out")
	} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
		t.Fatalf("wrong timeout: %v", err)
	}
	_ = stream.SetReadDeadline(time.Time{})
	close(send)
	buffer := make([]byte, len("recovered"))
	if _, err = io.ReadFull(stream, buffer); err != nil || string(buffer) != "recovered" {
		t.Fatalf("deadline poisoned reader: %q / %v", buffer, err)
	}
}

// This exercises the official SDK through real outer WSS and inner mTLS, with
// no public listener and no changes to the user's running MCP service.
func TestOfficialMCPOverWSSWithInnerTLS(t *testing.T) {
	p := newPKI(t)
	var allowed atomic.Bool
	allowed.Store(true)
	scfg, err := ServerTLS(p.target, p.roots, func(pin Pin) bool { return allowed.Load() && pin == p.agentPin })
	if err != nil {
		t.Fatal(err)
	}
	ccfg, err := ClientTLS(p.agent, p.roots, "target.test", p.targetPin)
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "encrypted-test", Version: "test"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", MaxMessageBytes+7)}}}, nil
	})
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	// Revocation must be enforced per request on an already established channel.
	handler := AuthorizeHTTP(func(pin Pin) bool { return allowed.Load() && pin == p.agentPin }, mcpHandler)
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second, MaxHeaderBytes: 16 << 10}
	defer httpServer.Close()
	upgrader := websocket.Upgrader{EnableCompression: false}
	outer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		stream := NewStream(ws)
		conn := tls.Server(stream, scfg)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = stream.Close()
			return
		}
		// http.Server owns the accepted encrypted connection from here.
		go func() { _ = httpServer.Serve(&oneConnListener{conn: conn, done: make(chan struct{})}) }()
	}))
	defer outer.Close()
	transport := &http.Transport{ForceAttemptHTTP2: false, DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialer := websocket.Dialer{TLSClientConfig: outer.Client().Transport.(*http.Transport).TLSClientConfig.Clone(), HandshakeTimeout: 2 * time.Second}
		ws, _, err := dialer.DialContext(ctx, "wss"+strings.TrimPrefix(outer.URL, "https"), nil)
		if err != nil {
			return nil, err
		}
		stream := NewStream(ws)
		conn := tls.Client(stream, ccfg)
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = stream.Close()
			return nil, err
		}
		return conn, nil
	}}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "https://127.0.0.1/mcp", HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || len(result.Content[0].(*mcp.TextContent).Text) != MaxMessageBytes+7 {
		t.Fatal("large result corrupted")
	}
	allowed.Store(false)
	if _, err := session.ListTools(ctx, nil); err == nil {
		t.Fatal("revoked peer retained access")
	}
}

// Test-only listener hands an established connection to net/http once.
type oneConnListener struct {
	conn   net.Conn
	done   chan struct{}
	handed bool
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	if !l.handed {
		l.handed = true
		return l.conn, nil
	}
	<-l.done
	return nil, io.EOF
}
func (l *oneConnListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return l.conn.Close()
}
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
