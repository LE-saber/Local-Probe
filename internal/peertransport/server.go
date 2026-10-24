package peertransport

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Endpoint upgrades an admitted outer connection and feeds inner-mTLS HTTP to
// the target's handler. It owns no public listener. The caller supplies bounded
// outer admission; inner TLS and per-request scope remain independently required.
type Endpoint struct {
	mu          sync.Mutex
	closed      bool
	connections map[net.Conn]struct{}
	slots       chan struct{}
	inner       *tls.Config
	admit       func(*http.Request) bool
	server      *http.Server
	listener    *virtualListener
	done        chan struct{}
}

func NewEndpoint(inner *tls.Config, handler http.Handler, admit func(*http.Request) bool) (*Endpoint, error) {
	if inner == nil || inner.MinVersion < tls.VersionTLS13 || inner.ClientAuth != tls.RequireAndVerifyClientCert || inner.VerifyConnection == nil || handler == nil || admit == nil {
		return nil, errors.New("authenticated peer endpoint required")
	}
	l := &virtualListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
	e := &Endpoint{connections: make(map[net.Conn]struct{}), slots: make(chan struct{}, 16), inner: inner.Clone(), admit: admit, listener: l, done: make(chan struct{})}
	e.server = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { defer close(e.done); _ = e.server.Serve(l) }()
	return e, nil
}

func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !e.admit(r) {
		http.Error(w, "channel admission denied", http.StatusUnauthorized)
		return
	}
	select {
	case e.slots <- struct{}{}:
	default:
		http.Error(w, "channel capacity reached", http.StatusServiceUnavailable)
		return
	}
	owned := false
	defer func() {
		if !owned {
			<-e.slots
		}
	}()
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		http.Error(w, "endpoint closed", http.StatusServiceUnavailable)
		return
	}
	upgrader := websocket.Upgrader{HandshakeTimeout: 10 * time.Second, EnableCompression: false, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	stream := NewStream(ws)
	owned = true // acceptStream now owns this reserved slot, including failures.
	_ = e.acceptStream(r.Context(), stream)
}

// AcceptStream accepts an externally admitted Relay stream. It takes ownership
// even on failure; all application requests still pass through inner mTLS.
func (e *Endpoint) AcceptStream(ctx context.Context, stream net.Conn) error {
	select {
	case e.slots <- struct{}{}:
	default:
		_ = stream.Close()
		return errors.New("endpoint capacity reached")
	}
	return e.acceptStream(ctx, stream)
}

// acceptStream is called with one reserved capacity slot.
func (e *Endpoint) acceptStream(parent context.Context, stream net.Conn) error {
	owned := false
	defer func() {
		if !owned {
			_ = stream.Close()
			<-e.slots
		}
	}()
	tracked := &trackedConn{Conn: stream}
	tracked.onClose = func() { e.mu.Lock(); delete(e.connections, tracked); e.mu.Unlock(); <-e.slots }
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		_ = stream.Close()
		return net.ErrClosed
	}
	e.connections[tracked] = struct{}{}
	e.mu.Unlock()
	owned = true
	conn := tls.Server(tracked, e.inner)
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = tracked.Close()
		return err
	}
	select {
	case e.listener.incoming <- conn:
	case <-e.listener.closed:
		_ = tracked.Close()
	case <-ctx.Done():
		_ = tracked.Close()
	}
	return nil
}
func (e *Endpoint) Close() error {
	e.mu.Lock()
	e.closed = true
	conns := make([]net.Conn, 0, len(e.connections))
	for c := range e.connections {
		conns = append(conns, c)
	}
	e.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	err := e.server.Close()
	<-e.done
	return err
}

type trackedConn struct {
	net.Conn
	once    sync.Once
	onClose func()
}

func (c *trackedConn) Close() error {
	var err error
	c.once.Do(func() { err = c.Conn.Close(); c.onClose() })
	return err
}

type virtualListener struct {
	incoming chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func (l *virtualListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case c := <-l.incoming:
		return c, nil
	}
}
func (l *virtualListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *virtualListener) Addr() net.Addr { return virtualAddr("paired-peer-stream") }

type virtualAddr string

func (a virtualAddr) Network() string { return "peer" }
func (a virtualAddr) String() string  { return string(a) }
