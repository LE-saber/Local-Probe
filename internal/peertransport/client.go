package peertransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// HTTPClient creates an HTTP/1.1 client whose connections always use the fixed
// WSS destination and inner TLS. It never falls back to plaintext or follows
// redirects. Headers contain outer admission credentials and stay in-process.
func HTTPClient(endpoint string, inner *tls.Config, headers http.Header) (*http.Client, func(), error) {
	return HTTPClientWithRoots(endpoint, inner, headers, nil)
}

// HTTPClientWithRoots supports an explicitly trusted private Relay/edge CA.
// Nil uses the normal OS trust store; hostname verification remains enabled.
func HTTPClientWithRoots(endpoint string, inner *tls.Config, headers http.Header, outerRoots *x509.CertPool) (*http.Client, func(), error) {
	return newHTTPClient(endpoint, inner, headers, outerRoots, false)
}

const RelayReadyMessage = "local-probe.relay.ready.v1"

// RelayHTTPClient consumes the Relay's bounded rendezvous marker before inner
// TLS begins. The marker grants no identity or operation permission.
func RelayHTTPClient(endpoint string, inner *tls.Config, headers http.Header, outerRoots *x509.CertPool) (*http.Client, func(), error) {
	return newHTTPClient(endpoint, inner, headers, outerRoots, true)
}

func newHTTPClient(endpoint string, inner *tls.Config, headers http.Header, outerRoots *x509.CertPool, ready bool) (*http.Client, func(), error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "wss" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || inner == nil || inner.InsecureSkipVerify || inner.MinVersion < tls.VersionTLS13 {
		return nil, nil, errors.New("explicit WSS endpoint and verified inner TLS required")
	}
	inner = inner.Clone()
	headers = headers.Clone()
	transport := &http.Transport{MaxConnsPerHost: 4, MaxIdleConnsPerHost: 4, IdleConnTimeout: 60 * time.Second, ResponseHeaderTimeout: 15 * time.Second, ForceAttemptHTTP2: false}
	transport.DialTLSContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		stream, err := dialStream(ctx, endpoint, headers, outerRoots, ready)
		if err != nil {
			return nil, err
		}
		conn := tls.Client(stream, inner)
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = stream.Close()
			return nil, errors.New("remote peer authentication failed")
		}
		return conn, nil
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}, transport.CloseIdleConnections, nil
}

func dialStream(ctx context.Context, endpoint string, headers http.Header, outerRoots *x509.CertPool, ready bool) (net.Conn, error) {
	outerTLS := &tls.Config{MinVersion: tls.VersionTLS12}
	if outerRoots != nil {
		outerTLS.RootCAs = outerRoots.Clone()
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, EnableCompression: false, TLSClientConfig: outerTLS}
	ws, resp, err := dialer.DialContext(ctx, endpoint, headers)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, errors.New("remote channel unavailable")
	}
	if ready {
		stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
		ws.SetReadLimit(16 << 10)
		_ = ws.SetReadDeadline(time.Now().Add(35 * time.Second))
		kind, data, err := ws.ReadMessage()
		stop()
		if err != nil || kind != websocket.TextMessage || string(data) != RelayReadyMessage {
			_ = ws.Close()
			return nil, errors.New("Relay rendezvous unavailable")
		}
		_ = ws.SetReadDeadline(time.Time{})
	}
	return NewStream(ws), nil
}
