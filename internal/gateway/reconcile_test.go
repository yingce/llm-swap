package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestLoadedReconcilerUnloadsExcessIdleReplica(t *testing.T) {
	var unloadA atomic.Int32
	var unloadB atomic.Int32
	workerA := unloadServer(t, &unloadA)
	defer workerA.Close()
	workerB := unloadServer(t, &unloadB)
	defer workerB.Close()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.MaxLoaded = 1
	cfg.Models["qwen"] = model

	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Now()
	registerRunningWorker(reg, "worker-a", workerA.URL, now)
	registerRunningWorker(reg, "worker-b", workerB.URL, now)

	reconciler := LoadedReconciler{
		Config:  cfg,
		Workers: reg,
		Client:  LlamaSwapClient{BearerToken: "llama-secret"},
	}

	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if unloadA.Load()+unloadB.Load() != 1 {
		t.Fatalf("unload calls = worker-a:%d worker-b:%d, want exactly 1", unloadA.Load(), unloadB.Load())
	}
}

func TestLoadedReconcilerDoesNotUnloadActiveWorker(t *testing.T) {
	var unloadA atomic.Int32
	var unloadB atomic.Int32
	workerA := unloadServer(t, &unloadA)
	defer workerA.Close()
	workerB := unloadServer(t, &unloadB)
	defer workerB.Close()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.MaxLoaded = 1
	cfg.Models["qwen"] = model

	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Now()
	registerRunningWorker(reg, "worker-a", workerA.URL, now)
	registerRunningWorker(reg, "worker-b", workerB.URL, now)
	release, ok := reg.Acquire("worker-a", now)
	if !ok {
		t.Fatal("expected to acquire worker-a")
	}
	defer release()

	reconciler := LoadedReconciler{
		Config:  cfg,
		Workers: reg,
		Client:  LlamaSwapClient{BearerToken: "llama-secret"},
	}

	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if unloadA.Load() != 0 {
		t.Fatalf("unload calls = %d, want 0 for active worker", unloadA.Load())
	}
	if unloadB.Load() != 1 {
		t.Fatalf("worker-b unload calls = %d, want 1 for idle excess worker", unloadB.Load())
	}
}

func unloadServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/models/unload/qwen" {
			t.Fatalf("unexpected unload request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer llama-secret" {
			t.Fatalf("authorization = %q, want llama-secret bearer", got)
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
}

func registerRunningWorker(reg *WorkerRegistry, id, url string, now time.Time) {
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      id,
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: url,
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "ready"},
		},
		Capacity: config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
	}, now)
}
