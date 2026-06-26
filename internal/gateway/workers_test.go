package gateway

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestAgentConfigEndpointReturnsTagScopedModels(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	body := rr.Body.Bytes()
	var resp protocol.AgentConfigResponse
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.Models["qwen"]; !ok {
		t.Fatalf("models = %#v, want qwen", resp.Models)
	}
	if _, ok := resp.Models["other"]; ok {
		t.Fatalf("models = %#v, did not want other", resp.Models)
	}
	if bytes.Contains(body, []byte("other.tar.gz")) {
		t.Fatalf("response exposed unrelated artifact: %s", string(body))
	}
	if resp.TagPolicy.Tag != "gpu-4090" {
		t.Fatalf("tag = %q, want gpu-4090", resp.TagPolicy.Tag)
	}
	if resp.TagPolicy.WarmWhenIdle != "qwen" {
		t.Fatalf("warm_when_idle = %q, want qwen", resp.TagPolicy.WarmWhenIdle)
	}
	if resp.TagPolicy.WorkerDefaults.MaxConcurrency != 2 {
		t.Fatalf("worker default concurrency = %d, want 2", resp.TagPolicy.WorkerDefaults.MaxConcurrency)
	}
}

func TestAgentConfigEndpointRejectsWrongOrMissingToken(t *testing.T) {
	srv := NewServer(testGatewayConfig())

	for _, tt := range []struct {
		name  string
		token string
	}{
		{name: "missing"},
		{name: "wrong", token: "Bearer wrong"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090", nil)
			if tt.token != "" {
				req.Header.Set("Authorization", tt.token)
			}
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestAgentConfigEndpointRejectsUnknownTag(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=unknown", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestAgentConfigEndpointRejectsMultipleConfiguredTags(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090,gpu-a100", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHeartbeatEndpointRegistersWorkerAndReturnsActiveState(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	body := protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Capacity:     config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
	}
	resp := postHeartbeat(t, srv, body)

	if resp.WorkerState != "active" {
		t.Fatalf("worker_state = %q, want active", resp.WorkerState)
	}
	if resp.RestartAllowed {
		t.Fatal("healthy heartbeat should not be restart_allowed")
	}
	if !srv.workers.Healthy("gpu-01", time.Now()) {
		t.Fatal("worker should be registered as healthy")
	}
}

func TestHeartbeatEndpointLogsAgentEvents(t *testing.T) {
	var logs bytes.Buffer
	srv := NewServer(testGatewayConfig())
	srv.logger = log.New(&logs, "", 0)

	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Events: []protocol.AgentEvent{
			{
				Event:           "artifact_download_progress",
				Model:           "qwen",
				Object:          "models/qwen.tar.gz",
				DownloadedBytes: 50,
				TotalBytes:      100,
				Percent:         50,
			},
		},
	})

	got := logs.String()
	for _, want := range []string{
		`"event":"agent_event"`,
		`"worker_id":"gpu-01"`,
		`"agent_event":"artifact_download_progress"`,
		`"model":"qwen"`,
		`"percent":50`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log = %s, want substring %s", got, want)
		}
	}
}

func TestHeartbeatEndpointPersistsAgentEvents(t *testing.T) {
	eventLogPath := filepath.Join(t.TempDir(), "worker-events.jsonl")
	srv := NewServerWithGatewayPersistencePaths(testGatewayConfig(), "", eventLogPath)

	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Events: []protocol.AgentEvent{
			{
				Time:            time.Unix(100, 0).UTC(),
				Event:           "artifact_download_progress",
				Model:           "qwen",
				Object:          "models/qwen.tar.gz",
				DownloadedBytes: 50,
				TotalBytes:      100,
				Percent:         50,
			},
		},
	})

	data, err := os.ReadFile(eventLogPath)
	if err != nil {
		t.Fatalf("read event log: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("event log lines = %d, want 1:\n%s", len(lines), string(data))
	}
	var entry uiAgentEvent
	if err := json.Unmarshal(lines[0], &entry); err != nil {
		t.Fatalf("decode event log: %v", err)
	}
	if entry.WorkerID != "gpu-01" || entry.Event != "artifact_download_progress" || entry.Model != "qwen" || entry.Percent != 50 {
		t.Fatalf("event log entry = %+v, want persisted worker event", entry)
	}
}

func TestHeartbeatEndpointReturnsDrainingAndRestartAllowedForIdleNeedsRestart(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	resp := postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		NeedsRestart: true,
	})

	if resp.WorkerState != "draining" {
		t.Fatalf("worker_state = %q, want draining", resp.WorkerState)
	}
	if !resp.RestartAllowed {
		t.Fatal("idle needs_restart heartbeat should be restart_allowed")
	}
}

func TestHeartbeatEndpointRejectsInvalidJSON(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/heartbeat", bytes.NewBufferString("{"))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestHeartbeatEndpointRejectsBlankAgentID(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	data, err := json.Marshal(protocol.HeartbeatRequest{
		AgentID:      " ",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/heartbeat", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if srv.workers.Healthy("", time.Now()) {
		t.Fatal("blank agent_id should not register an empty worker")
	}
}

func postHeartbeat(t *testing.T, srv *Server, body protocol.HeartbeatRequest) protocol.HeartbeatResponse {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/heartbeat", bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var resp protocol.HeartbeatResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func testGatewayConfig() config.GatewayConfig {
	return config.GatewayConfig{
		OSS: config.OSSConfig{BaseURL: "https://oss.example.com"},
		Tokens: config.TokenConfig{
			Agent: "agent-secret",
		},
		Models: map[string]config.Model{
			"qwen": {
				Artifact: config.Artifact{Object: "qwen.tar.gz", Kind: "tar_gz", CRC64ECMA: "123"},
				Run:      "llama-swap run qwen",
			},
			"other": {
				Artifact: config.Artifact{Object: "other.tar.gz", Kind: "tar_gz", CRC64ECMA: "456"},
				Run:      "llama-swap run other",
			},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {
				AllowedModels:  []string{"qwen"},
				WarmWhenIdle:   "qwen",
				WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
			},
			"gpu-a100": {
				AllowedModels:  []string{"other"},
				WarmWhenIdle:   "other",
				WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 4, MaxQueue: 8},
			},
		},
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

func TestWorkerRegistryReverseFailureBacksOffImmediatelyAndSuccessClears(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
	}, now)

	if marked := reg.RecordReverseFailure("gpu-01", now.Add(time.Second)); !marked {
		t.Fatal("first reverse failure should mark worker unavailable")
	}
	if reg.Healthy("gpu-01", now.Add(2*time.Second)) {
		t.Fatal("worker should be unavailable during reverse-access backoff")
	}

	reg.RecordScrapeSuccess("gpu-01")
	if !reg.Healthy("gpu-01", now.Add(2*time.Second)) {
		t.Fatal("successful reverse access should clear backoff")
	}
}
