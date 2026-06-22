package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLlamaSwapStateClientPullsRunningModelsWithBearerToken(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/running" {
			t.Fatalf("path = %q, want /running", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer worker-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		_, _ = w.Write([]byte(`{"running":[{"model":"qwen","state":"ready"},"llama"]}`))
	}))
	defer worker.Close()

	models, err := (LlamaSwapStateClient{
		BaseURL:     worker.URL,
		BearerToken: "worker-token",
		HTTP:        worker.Client(),
	}).RunningModelsContext(context.Background())
	if err != nil {
		t.Fatalf("RunningModelsContext returned error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want two entries", models)
	}
	if models[0].Model != "qwen" || models[0].State != "ready" {
		t.Fatalf("first model = %+v, want qwen ready", models[0])
	}
	if models[1].Model != "llama" || models[1].State != "ready" {
		t.Fatalf("second model = %+v, want llama ready", models[1])
	}
}
