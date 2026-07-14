package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTunnelClientProxiesGatewayRequestToLocalLlamaSwap(t *testing.T) {
	var gotPath string
	var gotAuth string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		writeJSONForTunnelTest(t, w, map[string]any{"ok": true})
	}))
	defer local.Close()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/agent/tunnel" || r.URL.Query().Get("agent_id") != "worker-a" {
			t.Fatalf("tunnel URL = %s?%s, want /internal/agent/tunnel?agent_id=worker-a", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer agent-secret" {
			t.Fatalf("authorization = %q, want agent bearer", r.Header.Get("Authorization"))
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()

		if err := conn.WriteJSON(map[string]any{
			"type":        "http_request",
			"id":          "req-1",
			"method":      http.MethodPost,
			"path":        "/v1/chat/completions",
			"raw_query":   "trace=1",
			"headers":     map[string][]string{"Authorization": {"Bearer llama-secret"}},
			"body_base64": "eyJtb2RlbCI6InF3ZW4ifQ==",
		}); err != nil {
			t.Fatalf("write tunnel request: %v", err)
		}
		var start map[string]any
		if err := conn.ReadJSON(&start); err != nil {
			t.Fatalf("read tunnel response start: %v", err)
		}
		if start["type"] != "http_response_start" || start["id"] != "req-1" || int(start["status_code"].(float64)) != http.StatusOK {
			t.Fatalf("response start = %+v, want http_response_start req-1 200", start)
		}
		var chunk map[string]any
		if err := conn.ReadJSON(&chunk); err != nil {
			t.Fatalf("read tunnel response chunk: %v", err)
		}
		if chunk["type"] != "http_response_body" || chunk["body_base64"] == "" {
			t.Fatalf("response chunk = %+v, want body chunk", chunk)
		}
		var end map[string]any
		if err := conn.ReadJSON(&end); err != nil {
			t.Fatalf("read tunnel response end: %v", err)
		}
		if end["type"] != "http_response_end" || end["id"] != "req-1" {
			t.Fatalf("response end = %+v, want http_response_end req-1", end)
		}
	}))
	defer gateway.Close()

	client := TunnelClient{
		GatewayURL: gateway.URL,
		AgentID:    "worker-a",
		Token:      "agent-secret",
		LocalURL:   local.URL,
		HTTPClient: local.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.RunOnce(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if gotPath != "/v1/chat/completions?trace=1" {
		t.Fatalf("local path = %q, want proxied path and query", gotPath)
	}
	if gotAuth != "Bearer llama-secret" {
		t.Fatalf("local authorization = %q, want forwarded llama token", gotAuth)
	}
}

func TestTunnelClientHandlesConcurrentRequests(t *testing.T) {
	releaseSlow := make(chan struct{})
	var releaseOnce sync.Once
	fastHit := make(chan struct{}, 1)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			<-releaseSlow
			writeJSONForTunnelTest(t, w, map[string]any{"path": "slow"})
		case "/fast":
			fastHit <- struct{}{}
			writeJSONForTunnelTest(t, w, map[string]any{"path": "fast"})
		default:
			t.Fatalf("unexpected local path %s", r.URL.Path)
		}
	}))
	defer local.Close()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()
		for _, req := range []map[string]any{
			{"type": "http_request", "id": "slow", "method": http.MethodGet, "path": "/slow"},
			{"type": "http_request", "id": "fast", "method": http.MethodGet, "path": "/fast"},
		} {
			if err := conn.WriteJSON(req); err != nil {
				t.Fatalf("write request: %v", err)
			}
		}

		select {
		case <-fastHit:
		case <-time.After(300 * time.Millisecond):
			releaseOnce.Do(func() { close(releaseSlow) })
			t.Fatal("fast request was blocked behind slow request")
		}
		releaseOnce.Do(func() { close(releaseSlow) })
	}))
	defer gateway.Close()

	client := TunnelClient{
		GatewayURL: gateway.URL,
		AgentID:    "worker-a",
		Token:      "agent-secret",
		LocalURL:   local.URL,
		HTTPClient: local.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.RunOnce(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
}

func TestTunnelClientStreamsLocalResponseChunks(t *testing.T) {
	releaseSecond := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test response writer does not flush")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\n"))
		flusher.Flush()
		<-releaseSecond
		_, _ = w.Write([]byte("data: two\n\n"))
		flusher.Flush()
	}))
	defer local.Close()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	firstChunk := make(chan tunnelHTTPResponse, 1)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]any{
			"type":   "http_request",
			"id":     "stream-1",
			"method": http.MethodPost,
			"path":   "/v1/chat/completions",
		}); err != nil {
			t.Fatalf("write request: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		var start tunnelHTTPResponse
		if err := conn.ReadJSON(&start); err != nil {
			close(releaseSecond)
			t.Fatalf("read start: %v", err)
		}
		_ = conn.SetReadDeadline(time.Time{})
		if start.Type != "http_response_start" || start.StatusCode != http.StatusOK {
			t.Fatalf("start = %+v, want http_response_start 200", start)
		}
		var chunk tunnelHTTPResponse
		if err := conn.ReadJSON(&chunk); err != nil {
			t.Fatalf("read first chunk: %v", err)
		}
		firstChunk <- chunk
		close(releaseSecond)
	}))
	defer gateway.Close()

	client := TunnelClient{
		GatewayURL: gateway.URL,
		AgentID:    "worker-a",
		Token:      "agent-secret",
		LocalURL:   local.URL,
		HTTPClient: local.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.RunOnce(ctx) }()
	select {
	case chunk := <-firstChunk:
		if chunk.Type != "http_response_body" || chunk.BodyBase64 != "ZGF0YTogb25lCgo=" {
			t.Fatalf("first chunk = %+v, want streamed data: one", chunk)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("first local chunk was not streamed before response end")
	}
	cancel()
	<-done
}

func TestTunnelClientCancelsLocalRequest(t *testing.T) {
	localStarted := make(chan struct{})
	localCanceled := make(chan struct{})
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(localStarted)
		<-r.Context().Done()
		close(localCanceled)
	}))
	defer local.Close()

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]any{
			"type":   "http_request",
			"id":     "cancel-1",
			"method": http.MethodPost,
			"path":   "/v1/chat/completions",
		}); err != nil {
			t.Fatalf("write request: %v", err)
		}
		select {
		case <-localStarted:
		case <-time.After(time.Second):
			t.Fatal("local request did not start")
		}
		if err := conn.WriteJSON(map[string]any{"type": "http_cancel", "id": "cancel-1"}); err != nil {
			t.Fatalf("write cancel: %v", err)
		}
		select {
		case <-localCanceled:
		case <-time.After(time.Second):
			t.Fatal("local request context was not canceled")
		}
	}))
	defer gateway.Close()

	client := TunnelClient{
		GatewayURL: gateway.URL,
		AgentID:    "worker-a",
		Token:      "agent-secret",
		LocalURL:   local.URL,
		HTTPClient: local.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.RunOnce(ctx); err != nil && ctx.Err() == nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
}

func TestTunnelKeepAliveSendsPing(t *testing.T) {
	pingReceived := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade: %v", err)
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
	defer gateway.Close()

	wsURL := "ws" + strings.TrimPrefix(gateway.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()

	var writeMu sync.Mutex
	done := make(chan struct{})
	defer close(done)
	go tunnelKeepAlive(conn, &writeMu, done, 10*time.Millisecond)

	select {
	case <-pingReceived:
	case <-time.After(time.Second):
		t.Fatal("agent tunnel did not send websocket ping")
	}
}

func writeJSONForTunnelTest(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write json: %v", err)
	}
}

func TestTunnelClientURLUsesWebSocketScheme(t *testing.T) {
	if got := tunnelWebSocketURL("http://gateway.example", "worker-a"); got != "ws://gateway.example/internal/agent/tunnel?agent_id=worker-a" {
		t.Fatalf("ws url = %q", got)
	}
	if got := tunnelWebSocketURL("https://gateway.example/base/", "worker-a"); !strings.HasPrefix(got, "wss://gateway.example/base/internal/agent/tunnel?") {
		t.Fatalf("wss url = %q, want gateway base path preserved", got)
	}
}
