package peertransport

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const MaxMessageBytes = 64 << 10

// Stream converts an already established, externally authenticated WebSocket
// into an ordered stream for inner TLS. Never serve plaintext MCP over Stream.
// Read deadlines are implemented above Gorilla: net/http deliberately uses a
// temporary read deadline to abort its background read between requests. Giving
// that deadline to Gorilla would permanently poison its reader. Write failures
// remain terminal for TLS and require reconnecting.
type Stream struct {
	ws              *websocket.Conn
	readMu          sync.Mutex
	writeMu         sync.Mutex
	pending         []byte
	readErr         error
	reads           chan streamRead
	closed          chan struct{}
	closeOnce       sync.Once
	deadlineMu      sync.Mutex
	readDeadline    time.Time
	writeDeadline   time.Time
	deadlineChanged chan struct{}
}
type streamRead struct {
	data []byte
	err  error
}
type deadlineError struct{}

func (deadlineError) Error() string   { return "peer stream deadline exceeded" }
func (deadlineError) Timeout() bool   { return true }
func (deadlineError) Temporary() bool { return true }

var _ net.Conn = (*Stream)(nil)

// NewStream must be called before any concurrent WebSocket reads or writes.
// Compression must be disabled at both the dialer and upgrader.
func NewStream(ws *websocket.Conn) *Stream {
	ws.SetReadLimit(MaxMessageBytes)
	ws.EnableWriteCompression(false)
	s := &Stream{ws: ws, reads: make(chan streamRead, 1), closed: make(chan struct{}), deadlineChanged: make(chan struct{})}
	go s.readPump()
	return s
}

func (s *Stream) readPump() {
	for {
		kind, data, err := s.ws.ReadMessage()
		if err == nil && kind != websocket.BinaryMessage {
			err = errors.New("non-binary peer stream message")
		}
		select {
		case s.reads <- streamRead{data, err}:
		case <-s.closed:
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *Stream) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	if s.readErr != nil {
		return 0, s.readErr
	}
	for {
		s.deadlineMu.Lock()
		deadline, changed := s.readDeadline, s.deadlineChanged
		s.deadlineMu.Unlock()
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return 0, deadlineError{}
		}
		if len(s.pending) > 0 {
			n := copy(p, s.pending)
			s.pending = s.pending[n:]
			return n, nil
		}
		var timer *time.Timer
		var timeout <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case <-s.closed:
			if timer != nil {
				timer.Stop()
			}
			return 0, net.ErrClosed
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-timeout:
			return 0, deadlineError{}
		case value := <-s.reads:
			if timer != nil {
				timer.Stop()
			}
			if value.err != nil {
				s.readErr = value.err
				return 0, value.err
			}
			s.pending = value.data
		}
	}
}

func (s *Stream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written := 0
	for len(p) > 0 {
		n := min(len(p), MaxMessageBytes)
		s.deadlineMu.Lock()
		deadline := s.writeDeadline
		s.deadlineMu.Unlock()
		if err := s.ws.SetWriteDeadline(deadline); err != nil {
			return written, err
		}
		if err := s.ws.WriteMessage(websocket.BinaryMessage, p[:n]); err != nil {
			return written, err
		}
		written += n
		p = p[n:]
	}
	return written, nil
}

func (s *Stream) Close() error {
	var err error
	s.closeOnce.Do(func() { close(s.closed); err = s.ws.Close() })
	return err
}
func (s *Stream) LocalAddr() net.Addr  { return s.ws.LocalAddr() }
func (s *Stream) RemoteAddr() net.Addr { return s.ws.RemoteAddr() }

func (s *Stream) SetDeadline(t time.Time) error {
	if err := s.SetReadDeadline(t); err != nil {
		return err
	}
	return s.SetWriteDeadline(t)
}
func (s *Stream) SetReadDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.readDeadline = t
	close(s.deadlineChanged)
	s.deadlineChanged = make(chan struct{})
	s.deadlineMu.Unlock()
	return nil
}
func (s *Stream) SetWriteDeadline(t time.Time) error {
	s.deadlineMu.Lock()
	s.writeDeadline = t
	s.deadlineMu.Unlock()
	return s.ws.UnderlyingConn().SetWriteDeadline(t)
}
