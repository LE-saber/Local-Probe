// Package relay pairs authenticated outbound WebSocket streams. It never
// terminates inner TLS, interprets MCP, or accepts arbitrary target addresses.
package relay

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"github.com/LE-saber/Local-Probe/internal/peertransport"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Route is loaded locally, with role-separated strong tokens. A route maps only
// two opaque outer identities. Target mTLS and local grants authorize operations.
type Route struct{ DeviceID, TargetToken, AgentToken string }
type route struct {
	id            string
	target, agent [32]byte
	slots         []*slot
}
type slot struct {
	ws       *websocket.Conn
	incoming chan []byte
	reserved bool
	paired   chan struct{}
	done     chan struct{}
	once     sync.Once
}
type Service struct {
	mu       sync.Mutex
	routes   []*route
	closed   bool
	total    int
	wait     time.Duration
	visitors map[string]visitor
}
type visitor struct {
	at     time.Time
	credit float64
}

const ReadyMessage = peertransport.RelayReadyMessage

func New(routes []Route) (*Service, error) {
	if len(routes) == 0 || len(routes) > 64 {
		return nil, errors.New("bounded Relay routes required")
	}
	s := &Service{wait: 30 * time.Second, visitors: make(map[string]visitor)}
	seen := map[[32]byte]bool{}
	ids := map[string]bool{}
	for _, r := range routes {
		if strings.ContainsAny(r.TargetToken+r.AgentToken, "\r\n") {
			return nil, errors.New("invalid Relay credentials")
		}
		if len(r.DeviceID) == 0 || len(r.DeviceID) > 128 || ids[r.DeviceID] || len(r.TargetToken) < 32 || len(r.AgentToken) < 32 || len(r.TargetToken) > 4096 || len(r.AgentToken) > 4096 {
			return nil, errors.New("invalid Relay route")
		}
		for _, c := range r.DeviceID {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return nil, errors.New("invalid Relay route")
			}
		}
		target, agent := sha256.Sum256([]byte("Bearer "+r.TargetToken)), sha256.Sum256([]byte("Bearer "+r.AgentToken))
		if target == agent || seen[target] || seen[agent] {
			return nil, errors.New("Relay credentials must be role separated")
		}
		seen[target] = true
		seen[agent] = true
		ids[r.DeviceID] = true
		s.routes = append(s.routes, &route{id: r.DeviceID, target: target, agent: agent})
	}
	return s, nil
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.admitRate(r.RemoteAddr) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet || (r.URL.Path != "/v1/target" && r.URL.Path != "/v1/agent") || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || len(values[0]) > 4103 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	hash := sha256.Sum256([]byte(values[0]))
	isTarget := r.URL.Path == "/v1/target"
	var selected *route
	for _, route := range s.routes {
		expected := route.agent
		if isTarget {
			expected = route.target
		}
		if subtle.ConstantTimeCompare(hash[:], expected[:]) == 1 {
			selected = route
		}
	}
	if selected == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if isTarget {
		s.serveTarget(w, r, selected)
	} else {
		s.serveAgent(w, r, selected)
	}
}

// Ignore forwarded headers: only a trusted proxy integration may replace peer
// addresses. Behind a reverse proxy this intentionally applies a shared budget.
func (s *Service) admitRate(remote string) bool {
	ip, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.visitors[ip]
	if !ok {
		if len(s.visitors) >= 1024 {
			for key, item := range s.visitors {
				if now.Sub(item.at) > time.Minute {
					delete(s.visitors, key)
				}
			}
			if len(s.visitors) >= 1024 {
				return false
			}
		}
		v = visitor{now, 20}
	}
	v.credit = min(20, v.credit+now.Sub(v.at).Seconds()*10)
	v.at = now
	allowed := v.credit >= 1
	if allowed {
		v.credit--
	}
	s.visitors[ip] = v
	return allowed
}
func upgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	u := websocket.Upgrader{HandshakeTimeout: 10 * time.Second, EnableCompression: false, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}
	c, err := u.Upgrade(w, r, nil)
	if err == nil {
		c.SetReadLimit(64 << 10)
	}
	return c, err
}

func (s *Service) serveTarget(w http.ResponseWriter, r *http.Request, route *route) {
	s.mu.Lock()
	if s.closed || s.total >= 128 || len(route.slots) >= 16 {
		s.mu.Unlock()
		http.Error(w, "capacity reached", http.StatusServiceUnavailable)
		return
	}
	// Reserve capacity before upgrade so concurrent handshakes cannot overflow.
	slot := &slot{paired: make(chan struct{}), done: make(chan struct{}), incoming: make(chan []byte, 2)}
	route.slots = append(route.slots, slot)
	s.total++
	s.mu.Unlock()
	defer s.remove(route, slot)
	conn, err := upgrade(w, r)
	if err != nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	slot.ws = conn
	s.mu.Unlock()
	defer conn.Close()
	// Keep one reader from registration through forwarding. Without a reader,
	// disconnected waiting targets remain selectable until the pairing timeout.
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer close(slot.incoming)
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(60 * time.Second)) })
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil || kind != websocket.BinaryMessage {
				return
			}
			select {
			case slot.incoming <- data:
			case <-slot.done:
				return
			}
		}
	}()
	defer func() { s.remove(route, slot); <-readDone }()
	timer := time.NewTimer(s.wait)
	defer timer.Stop()
	select {
	case <-slot.paired:
		select {
		case <-slot.done:
		case <-r.Context().Done():
		case <-readDone:
		}
	case <-slot.done:
	case <-readDone:
	case <-timer.C:
	case <-r.Context().Done():
	}
}
func (s *Service) serveAgent(w http.ResponseWriter, r *http.Request, route *route) {
	s.mu.Lock()
	var target *slot
	if !s.closed {
		for _, slot := range route.slots {
			if slot.ws != nil && !slot.reserved {
				slot.reserved = true
				target = slot
				break
			}
		}
	}
	s.mu.Unlock()
	if target == nil {
		http.Error(w, "target unavailable", http.StatusServiceUnavailable)
		return
	}
	defer s.remove(route, target)
	agent, err := upgrade(w, r)
	if err != nil {
		return
	}
	defer agent.Close()
	s.mu.Lock()
	closed := s.closed
	peer := target.ws
	s.mu.Unlock()
	if closed || peer == nil {
		return
	}
	_ = peer.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = agent.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if peer.WriteMessage(websocket.TextMessage, []byte(ReadyMessage)) != nil || agent.WriteMessage(websocket.TextMessage, []byte(ReadyMessage)) != nil {
		return
	}
	_ = peer.SetWriteDeadline(time.Time{})
	_ = agent.SetWriteDeadline(time.Time{})
	close(target.paired)
	// Each direction has one reader and one data writer; only TLS ciphertext
	// binary messages are forwarded. Reads enforce aggregate message size.
	done := make(chan struct{}, 2)
	pump := func(src, dst *websocket.Conn, incoming <-chan []byte) {
		defer func() { done <- struct{}{} }()
		if incoming == nil {
			_ = src.SetReadDeadline(time.Now().Add(60 * time.Second))
			src.SetPongHandler(func(string) error { return src.SetReadDeadline(time.Now().Add(60 * time.Second)) })
		}
		for {
			var data []byte
			if incoming != nil {
				select {
				case next, ok := <-incoming:
					if !ok {
						return
					}
					data = next
				case <-target.done:
					return
				}
			} else {
				kind, next, err := src.ReadMessage()
				if err != nil || kind != websocket.BinaryMessage {
					return
				}
				data = next
			}
			_ = dst.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if dst.WriteMessage(websocket.BinaryMessage, data) != nil {
				return
			}
		}
	}
	go pump(peer, agent, target.incoming)
	go pump(agent, peer, nil)
	heartbeatStop := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				if peer.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil || agent.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					_ = peer.Close()
					_ = agent.Close()
					return
				}
			}
		}
	}()
	completed := 0
	select {
	case <-done:
		completed++
	case <-target.done:
	case <-r.Context().Done():
	}
	_ = peer.Close()
	_ = agent.Close()
	close(heartbeatStop)
	<-heartbeatDone
	// Both pumps always complete after Close, without leaving forwarding tasks.
	// If one completed above, only one receive remains.
	for completed < 2 {
		<-done
		completed++
	}
}
func (s *Service) remove(route *route, slot *slot) {
	slot.once.Do(func() {
		s.mu.Lock()
		for i, candidate := range route.slots {
			if candidate == slot {
				route.slots = append(route.slots[:i], route.slots[i+1:]...)
				s.total--
				break
			}
		}
		conn := slot.ws
		close(slot.done)
		s.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
	})
}
func (s *Service) Close() error {
	s.mu.Lock()
	s.closed = true
	type entry struct {
		r *route
		s *slot
	}
	var all []entry
	for _, r := range s.routes {
		for _, slot := range r.slots {
			all = append(all, entry{r, slot})
		}
	}
	s.mu.Unlock()
	for _, e := range all {
		s.remove(e.r, e.s)
	}
	return nil
}
