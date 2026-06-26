package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestAdminDrainAndUndrainWorkerAffectsPlacement(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	})

	postAdmin(t, srv, "/ui/api/workers/gpu-01/drain", `{}`)
	if _, err := (Scheduler{Config: srv.currentConfig(), Workers: srv.workers}).Pick("qwen", time.Now(), nil); err == nil {
		t.Fatal("drained worker should not be selected")
	}

	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	})
	if _, err := (Scheduler{Config: srv.currentConfig(), Workers: srv.workers}).Pick("qwen", time.Now(), nil); err == nil {
		t.Fatal("manual drain should survive heartbeat")
	}

	postAdmin(t, srv, "/ui/api/workers/gpu-01/undrain", `{}`)
	if _, err := (Scheduler{Config: srv.currentConfig(), Workers: srv.workers}).Pick("qwen", time.Now(), nil); err != nil {
		t.Fatalf("undrained worker should be selectable: %v", err)
	}
}

func TestAdminWarmModelCallsLlamaSwapAndRecordsEvent(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: upstream.URL,
		Artifacts:    map[string]string{"qwen": "ready"},
	})

	var resp uiAdminActionResponse
	postAdminDecode(t, srv, "/ui/api/models/qwen/warm", `{"worker_id":"gpu-01"}`, &resp)
	if resp.Result != "done" {
		t.Fatalf("response = %+v, want done", resp)
	}
	if gotPath != "/upstream/qwen/v1/models" {
		t.Fatalf("llama-swap path = %q, want warm path", gotPath)
	}
	events := srv.recentAgentEvents()
	if len(events) == 0 || events[0].Event != "gateway_model_warm_done" {
		t.Fatalf("events = %+v, want warm done event", events)
	}
}

func TestAdminUnloadRejectsActiveWorkerAndUnloadsIdleReplica(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  upstream.URL,
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	})
	release, ok := srv.workers.Acquire("gpu-01", time.Now())
	if !ok {
		t.Fatal("expected test worker acquire")
	}
	activeReq := httptest.NewRequest(http.MethodPost, "/ui/api/models/qwen/unload", strings.NewReader(`{"worker_id":"gpu-01"}`))
	activeReq.Header.Set("Authorization", "Bearer agent-secret")
	activeRR := httptest.NewRecorder()
	srv.ServeHTTP(activeRR, activeReq)
	if activeRR.Code != http.StatusConflict {
		t.Fatalf("active unload status = %d, want 409: %s", activeRR.Code, activeRR.Body.String())
	}
	release()

	postAdmin(t, srv, "/ui/api/models/qwen/unload", `{"worker_id":"gpu-01"}`)
	if gotPath != "/api/models/unload/qwen" {
		t.Fatalf("llama-swap path = %q, want unload path", gotPath)
	}
}

func TestAdminClearCooldownRemovesReplicaCooldown(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	now := time.Now()
	srv.replicaCooldowns.Mark("gpu-01", "qwen", "upstream_retry_exhausted", now)

	postAdmin(t, srv, "/ui/api/cooldowns/clear", `{"worker_id":"gpu-01","model":"qwen"}`)

	if srv.replicaCooldowns.Active("gpu-01", "qwen", now.Add(time.Second)) {
		t.Fatal("cooldown should be cleared")
	}
}

func postAdmin(t *testing.T, srv *Server, path string, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200: %s", path, rr.Code, rr.Body.String())
	}
}

func postAdminDecode(t *testing.T, srv *Server, path string, body string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200: %s", path, rr.Code, rr.Body.String())
	}
	if err := json.NewDecoder(rr.Body).Decode(out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func TestAdminActionEndpointsRequireAgentToken(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodPost, "/ui/api/workers/gpu-01/drain", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestAdminWarmRejectsWorkerWithoutArtifact(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Artifacts:    map[string]string{"qwen": "missing"},
		Capacity:     config.WorkerDefaults{MaxConcurrency: 1},
	})
	req := httptest.NewRequest(http.MethodPost, "/ui/api/models/qwen/warm", strings.NewReader(`{"worker_id":"gpu-01"}`))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
}

func TestAdminWarmRejectsDrainingWorker(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Artifacts:    map[string]string{"qwen": "ready"},
	})
	postAdmin(t, srv, "/ui/api/workers/gpu-01/drain", `{}`)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/models/qwen/warm", strings.NewReader(`{"worker_id":"gpu-01"}`))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for draining worker: %s", rr.Code, rr.Body.String())
	}
}
