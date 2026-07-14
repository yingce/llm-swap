package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newTestAgentTunnel(t *testing.T, handler func(*testing.T, *websocket.Conn)) *AgentTunnel {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade tunnel: %v", err)
			return
		}
		defer conn.Close()
		handler(t, conn)
	}))
	t.Cleanup(server.Close)

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test tunnel: %v", err)
	}
	tunnel := newAgentTunnel("worker-a", conn)
	t.Cleanup(tunnel.close)
	go tunnel.Serve()
	return tunnel
}

func TestAgentTunnelSendsCancelWhenRequestContextCancels(t *testing.T) {
	requestReceived := make(chan struct{})
	cancelReceived := make(chan tunnelHTTPRequest, 1)
	tunnel := newTestAgentTunnel(t, func(t *testing.T, conn *websocket.Conn) {
		var req tunnelHTTPRequest
		if err := conn.ReadJSON(&req); err != nil {
			t.Fatalf("read request: %v", err)
		}
		if req.Type != "http_request" || req.ID != "req-cancel" {
			t.Fatalf("request = %+v, want http_request req-cancel", req)
		}
		close(requestReceived)
		var cancel tunnelHTTPRequest
		if err := conn.ReadJSON(&cancel); err != nil {
			t.Fatalf("read cancel: %v", err)
		}
		cancelReceived <- cancel
	})

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := tunnel.RoundTripHTTP(ctx, "req-cancel", req, nil)
		done <- err
	}()
	select {
	case <-requestReceived:
	case <-time.After(time.Second):
		t.Fatal("gateway did not send tunnel request")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("RoundTripHTTP error = nil, want context cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("RoundTripHTTP did not return after context cancellation")
	}
	select {
	case got := <-cancelReceived:
		if got.Type != "http_cancel" || got.ID != "req-cancel" {
			t.Fatalf("cancel = %+v, want http_cancel req-cancel", got)
		}
	case <-time.After(time.Second):
		t.Fatal("gateway did not send http_cancel")
	}
}

func TestAgentTunnelKeepAliveSendsPing(t *testing.T) {
	pingReceived := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade tunnel: %v", err)
			return
		}
		defer conn.Close()
		conn.SetPingHandler(func(string) error {
			select {
			case pingReceived <- struct{}{}:
			default:
			}
			return conn.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial test tunnel: %v", err)
	}
	defer conn.Close()

	tunnel := newAgentTunnel("worker-a", conn)
	done := make(chan struct{})
	defer close(done)
	go tunnel.keepAlive(done, 10*time.Millisecond)

	select {
	case <-pingReceived:
	case <-time.After(time.Second):
		t.Fatal("gateway tunnel did not send websocket ping")
	}
}
