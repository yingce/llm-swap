package gateway

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
)

func TestLlamaSwapClientLoadRequestsUpstreamModelsEndpoint(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/upstream/qwen/v1/models" {
			t.Fatalf("unexpected load request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer llama-secret" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := LlamaSwapClient{BearerToken: "llama-secret"}
	if err := client.Load(context.Background(), server.URL, "qwen"); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !called {
		t.Fatalf("load endpoint was not called")
	}
}

func TestLlamaSwapClientLoadCanUseAgentTunnel(t *testing.T) {
	tunnel := newTestAgentTunnel(t, func(t *testing.T, conn *websocket.Conn) {
		var req tunnelHTTPRequest
		if err := conn.ReadJSON(&req); err != nil {
			t.Fatalf("read tunnel request: %v", err)
		}
		if req.Method != http.MethodGet || req.Path != "/upstream/qwen/v1/models" {
			t.Fatalf("tunnel request = %s %s, want GET /upstream/qwen/v1/models", req.Method, req.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer llama-secret" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		if err := conn.WriteJSON(tunnelHTTPResponse{
			Type:       "http_response",
			ID:         req.ID,
			StatusCode: http.StatusNoContent,
			BodyBase64: base64.StdEncoding.EncodeToString(nil),
		}); err != nil {
			t.Fatalf("write tunnel response: %v", err)
		}
	})

	client := LlamaSwapClient{BearerToken: "llama-secret", Tunnel: tunnel}
	if err := client.Load(context.Background(), "http://worker-unreachable", "qwen"); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
}
