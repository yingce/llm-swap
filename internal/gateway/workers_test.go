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
