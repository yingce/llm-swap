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

func TestHealthzEndpointReturnsNoContent(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
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

func TestWorkerRegistryPrunesOfflineWorkerAfterRetention(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Capacity:     config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
		NeedsRestart: true,
	}, now)
	reg.manualDrains["gpu-01"] = true
	reg.active["gpu-01"] = 1

	before := reg.Snapshot(now.Add(10*time.Minute - time.Second))
	if len(before) != 1 || before[0].ID != "gpu-01" {
		t.Fatalf("snapshot before retention = %+v, want gpu-01 retained", before)
	}

	after := reg.Snapshot(now.Add(10*time.Minute + time.Second))
	if len(after) != 0 {
		t.Fatalf("snapshot after retention = %+v, want offline worker pruned", after)
	}
	if _, ok := reg.workers["gpu-01"]; ok {
		t.Fatal("offline worker should be removed from registry")
	}
	if _, ok := reg.manualDrains["gpu-01"]; ok {
		t.Fatal("offline worker manual drain should be removed")
	}
	if _, ok := reg.active["gpu-01"]; ok {
		t.Fatal("offline worker active count should be removed")
	}
}

func TestWorkerRegistrySnapshotPreservesJoinOrder(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	for _, workerID := range []string{"gpu-b", "gpu-a", "gpu-c"} {
		reg.UpsertHeartbeat(protocol.HeartbeatRequest{
			AgentID:      workerID,
			Tags:         []string{"gpu-4090"},
			LlamaSwapURL: "http://worker",
		}, now)
	}

	snapshot := reg.Snapshot(now)
	if got := workerIDs(snapshot); strings.Join(got, ",") != "gpu-b,gpu-a,gpu-c" {
		t.Fatalf("snapshot worker order = %v, want join order", got)
	}

	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-a",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-updated",
	}, now.Add(time.Second))
	snapshot = reg.Snapshot(now.Add(time.Second))
	if got := workerIDs(snapshot); strings.Join(got, ",") != "gpu-b,gpu-a,gpu-c" {
		t.Fatalf("snapshot worker order after heartbeat = %v, want original join order", got)
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

func TestHeartbeatDrainResponseAllowsOnlyOneRestartAtATime(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)

	first := reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-01",
		NeedsRestart: true,
	}, now)
	second := reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-02",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-02",
		NeedsRestart: true,
	}, now.Add(time.Second))

	if !first.RestartAllowed {
		t.Fatal("first idle worker with needs_restart should be allowed to restart")
	}
	if second.RestartAllowed {
		t.Fatal("second worker should wait while another worker holds restart permission")
	}
	if first.WorkerState != "draining" || second.WorkerState != "active" {
		t.Fatalf("states = %q/%q, want draining/active", first.WorkerState, second.WorkerState)
	}
	if _, ok := reg.Acquire("gpu-02", now.Add(2*time.Second)); !ok {
		t.Fatal("waiting restart worker should remain routable until restart is allowed")
	}
}

func TestHeartbeatDrainResponseAllowsNextRestartAfterHolderCompletes(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)

	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-01",
		NeedsRestart: true,
	}, now)
	waiting := reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-02",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-02",
		NeedsRestart: true,
	}, now.Add(time.Second))
	if waiting.RestartAllowed {
		t.Fatal("second worker should not restart before holder completes")
	}

	done := reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-01",
		NeedsRestart: false,
	}, now.Add(2*time.Second))
	if done.RestartAllowed {
		t.Fatal("completed holder heartbeat should not request another restart")
	}

	next := reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-02",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-02",
		NeedsRestart: true,
	}, now.Add(3*time.Second))
	if !next.RestartAllowed {
		t.Fatal("second worker should be allowed after first restart completes")
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
	if resp.WorkerState != "draining" {
		t.Fatalf("worker_state = %q, want draining while waiting for active request to finish", resp.WorkerState)
	}
	if _, ok := reg.Acquire("gpu-01", now.Add(2500*time.Millisecond)); ok {
		t.Fatal("restart holder should not accept new requests while draining active requests")
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

func TestWorkerRegistryReverseFailureBacksOffAfterRepeatedFailuresAndSuccessClears(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
	}, now)

	if marked := reg.RecordReverseFailure("gpu-01", now.Add(time.Second)); marked {
		t.Fatal("first reverse failure should not mark worker unavailable")
	}
	if !reg.Healthy("gpu-01", now.Add(2*time.Second)) {
		t.Fatal("worker should remain healthy after one reverse-access failure")
	}
	if marked := reg.RecordReverseFailure("gpu-01", now.Add(2*time.Second)); marked {
		t.Fatal("second reverse failure should not mark worker unavailable")
	}
	if !reg.Healthy("gpu-01", now.Add(3*time.Second)) {
		t.Fatal("worker should remain healthy before reverse-access failure threshold")
	}
	if marked := reg.RecordReverseFailure("gpu-01", now.Add(3*time.Second)); !marked {
		t.Fatal("third reverse failure should mark worker unavailable")
	}
	if reg.Healthy("gpu-01", now.Add(4*time.Second)) {
		t.Fatal("worker should be unavailable during reverse-access backoff")
	}
	if marked := reg.RecordReverseFailure("gpu-01", now.Add(4*time.Second)); marked {
		t.Fatal("already backed-off worker should not emit another unavailable transition")
	}

	reg.RecordScrapeSuccess("gpu-01")
	if !reg.Healthy("gpu-01", now.Add(5*time.Second)) {
		t.Fatal("successful reverse access should clear backoff")
	}
}

func workerIDs(workers []Worker) []string {
	out := make([]string, 0, len(workers))
	for _, worker := range workers {
		out = append(out, worker.ID)
	}
	return out
}
