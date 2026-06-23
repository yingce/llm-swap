package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
