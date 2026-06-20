package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestBearerAuthRejectsWrongToken(t *testing.T) {
	h := bearerAuth("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestWorkerRegistryMarksStaleWorkerUnavailable(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Capacity:     config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
	}, now)

	if !reg.Healthy("gpu-01", now.Add(5*time.Second)) {
		t.Fatal("worker should be healthy before stale cutoff")
	}
	if reg.Healthy("gpu-01", now.Add(7*time.Second)) {
		t.Fatal("worker should be unavailable after stale cutoff")
	}
}

func TestHeartbeatDrainResponseAllowsRestartWhenIdle(t *testing.T) {
	reg := NewWorkerRegistry(6 * time.Second)
	resp := reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		NeedsRestart: true,
	}, time.Unix(100, 0))
	if resp.WorkerState != "draining" {
		t.Fatalf("state = %q, want draining", resp.WorkerState)
	}
	if !resp.RestartAllowed {
		t.Fatal("idle worker with needs_restart should be allowed to restart")
	}
}

func TestHeartbeatDrainResponseWaitsForAcquiredRequest(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
	}, now)

	release, ok := reg.Acquire("gpu-01", now.Add(time.Second))
	if !ok {
		t.Fatal("expected to acquire healthy worker")
	}

	resp := reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		NeedsRestart: true,
	}, now.Add(2*time.Second))
	if resp.RestartAllowed {
		t.Fatal("worker with acquired request should not be allowed to restart")
	}

	release()

	resp = reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		NeedsRestart: true,
	}, now.Add(3*time.Second))
	if !resp.RestartAllowed {
		t.Fatal("released worker should be allowed to restart")
	}
}

func TestWorkerRegistryAcquireRejectsUnavailableWorkers(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-02",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		NeedsRestart: true,
	}, now)

	if release, ok := reg.Acquire("missing", now); ok || release != nil {
		t.Fatal("missing worker should not be acquired")
	}
	if release, ok := reg.Acquire("gpu-01", now.Add(6*time.Second)); ok || release != nil {
		t.Fatal("stale worker should not be acquired")
	}
	if release, ok := reg.Acquire("gpu-02", now); ok || release != nil {
		t.Fatal("draining worker should not be acquired")
	}
}

func TestWorkerRegistryAcquireReleaseDecrementsOnce(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
	}, now)

	release, ok := reg.Acquire("gpu-01", now)
	if !ok {
		t.Fatal("expected to acquire healthy worker")
	}

	release()
	release()

	if got := reg.active["gpu-01"]; got != 0 {
		t.Fatalf("active count = %d, want 0", got)
	}
}
