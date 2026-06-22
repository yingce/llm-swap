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

func TestLoadedReconcilerUnloadsLeastRecentlyAccessedExcessReplica(t *testing.T) {
	var unloadA atomic.Int32
	var unloadB atomic.Int32
	workerA := unloadServerForModel(t, "qwen", &unloadA)
	defer workerA.Close()
	workerB := unloadServerForModel(t, "qwen", &unloadB)
	defer workerB.Close()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.MaxLoaded = 1
	cfg.Models["qwen"] = model

	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(1000, 0)
	registerRunningWorker(reg, "worker-a", workerA.URL, now)
	registerRunningWorker(reg, "worker-b", workerB.URL, now)
	access := NewAccessTracker()
	access.Record("qwen", "worker-a", now.Add(-time.Hour))
	access.Record("qwen", "worker-b", now)

	reconciler := LoadedReconciler{
		Config:  cfg,
		Workers: reg,
		Client:  LlamaSwapClient{BearerToken: "llama-secret"},
		Access:  access,
	}

	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if unloadA.Load() != 1 {
		t.Fatalf("worker-a unload calls = %d, want 1 for least recently accessed replica", unloadA.Load())
	}
	if unloadB.Load() != 0 {
		t.Fatalf("worker-b unload calls = %d, want 0 for recently accessed replica", unloadB.Load())
	}
}

func TestLoadedReconcilerUnloadsColdModelFromIdleWorkerForUnderloadedHotModel(t *testing.T) {
	var unloadCold atomic.Int32
	coldWorker := unloadServerForModel(t, "cold", &unloadCold)
	defer coldWorker.Close()
	hotWorker := unloadServerForModel(t, "hot", &atomic.Int32{})
	defer hotWorker.Close()

	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {MaxLoaded: 2},
			"cold": {},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"hot", "cold"}},
		},
		Tokens: config.TokenConfig{LlamaSwap: "llama-secret"},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(1000, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "hot-worker",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  hotWorker.URL,
		Artifacts:     map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "hot", State: "ready"}},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "cold-worker",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  coldWorker.URL,
		Artifacts:     map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "cold", State: "ready"}},
	}, now)
	access := NewAccessTracker()
	access.Record("hot", "hot-worker", now)
	access.Record("cold", "cold-worker", now.Add(-time.Hour))

	reconciler := LoadedReconciler{
		Config:  cfg,
		Workers: reg,
		Client:  LlamaSwapClient{BearerToken: "llama-secret"},
		Access:  access,
	}

	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if unloadCold.Load() != 1 {
		t.Fatalf("cold model unload calls = %d, want 1 to free idle worker for hot model", unloadCold.Load())
	}
}

func unloadServer(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return unloadServerForModel(t, "qwen", calls)
}

func unloadServerForModel(t *testing.T, model string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/models/unload/"+model {
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
