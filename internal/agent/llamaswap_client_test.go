package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
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

func TestLlamaSwapStateClientReadsTokenSourceForEveryRequest(t *testing.T) {
	var mu sync.RWMutex
	token := "old-token"
	var authorizations []string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		if r.URL.Path == "/running" {
			_, _ = w.Write([]byte(`{"running":[]}`))
		}
	}))
	defer worker.Close()

	client := LlamaSwapStateClient{
		BaseURL: worker.URL,
		TokenSource: func() string {
			mu.RLock()
			defer mu.RUnlock()
			return token
		},
		HTTP: worker.Client(),
	}
	if err := client.HealthContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	token = "new-token"
	mu.Unlock()
	if _, err := client.RunningModelsContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	token = ""
	mu.Unlock()
	if err := client.HealthContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{"Bearer old-token", "Bearer new-token", ""}
	if len(authorizations) != len(want) {
		t.Fatalf("authorizations = %v, want %v", authorizations, want)
	}
	for i := range want {
		if authorizations[i] != want[i] {
			t.Fatalf("authorization[%d] = %q, want %q", i, authorizations[i], want[i])
		}
	}
}
