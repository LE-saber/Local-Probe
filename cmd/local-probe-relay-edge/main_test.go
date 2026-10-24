package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LE-saber/Local-Probe/internal/relay"
	"github.com/gorilla/websocket"
)

func TestEdgePreservesRelayWebSocketsAndRoleAuthentication(t *testing.T) {
	service, err := relay.New([]relay.Route{{DeviceID: "edge-test", TargetToken: strings.Repeat("t", 40), AgentToken: strings.Repeat("a", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	backend := httptest.NewServer(service)
	defer backend.Close()
	handler, closeTransport, err := edgeHandler(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	front := httptest.NewTLSServer(handler)
	defer front.Close()
	dialer := websocket.Dialer{TLSClientConfig: front.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	base := "wss" + strings.TrimPrefix(front.URL, "https")
	target, _, err := dialer.Dial(base+"/v1/target", http.Header{"Authorization": []string{"Bearer " + strings.Repeat("t", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	agent, _, err := dialer.Dial(base+"/v1/agent", http.Header{"Authorization": []string{"Bearer " + strings.Repeat("a", 40)}})
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	for _, conn := range []*websocket.Conn{target, agent} {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		kind, b, err := conn.ReadMessage()
		if err != nil || kind != websocket.TextMessage || string(b) != relay.ReadyMessage {
			t.Fatal("rendezvous failed")
		}
	}
	if err = agent.WriteMessage(websocket.BinaryMessage, []byte("ciphertext-fixture")); err != nil {
		t.Fatal(err)
	}
	kind, b, err := target.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || string(b) != "ciphertext-fixture" {
		t.Fatal("WebSocket forwarding corrupted data")
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/agent", nil)
	request.Header.Set("Authorization", "Bearer wrong")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("role authentication bypassed")
	}
}

func TestEdgeCannotProxyArbitraryDestinations(t *testing.T) {
	for _, upstream := range []string{"https://127.0.0.1:8792", "http://example.com", "http://127.0.0.1:8792/private", "http://user@127.0.0.1:8792"} {
		if _, _, err := edgeHandler(upstream); err == nil {
			t.Fatal("unsafe upstream accepted")
		}
	}
	handler, closeTransport, err := edgeHandler("http://127.0.0.1:8792")
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	for _, path := range []string{"/admin", "/v1/agent?url=http://internal"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 404 {
			t.Fatal("unexpected route exposed")
		}
	}
}
