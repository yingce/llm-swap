package gateway

import (
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestSchedulerPrefersLoadedHealthyWorker(t *testing.T) {
	cfg := config.GatewayConfig{
		Models:      map[string]config.Model{"qwen": {Priority: 100}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "cold",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://cold",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loaded",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loaded",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "loaded" {
		t.Fatalf("picked %s, want loaded", pick.ID)
	}
}

func TestSchedulerRejectsUnknownModel(t *testing.T) {
	s := Scheduler{Config: config.GatewayConfig{Models: map[string]config.Model{"qwen": {}}}}

	_, err := s.Pick("missing", time.Unix(100, 0), nil)
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("error = %v, want unknown model", err)
	}
}

func TestSchedulerIgnoresStaleDrainingAndErroredWorkers(t *testing.T) {
	cfg := config.GatewayConfig{
		Models:      map[string]config.Model{"qwen": {}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "stale",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://stale",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now.Add(-10*time.Second))
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "draining",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://draining",
		Artifacts:    map[string]string{"qwen": "ready"},
		NeedsRestart: true,
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "errored",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://errored",
		Artifacts:    map[string]string{"qwen": "ready"},
		LastError:    "disk unavailable",
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "healthy",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://healthy",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "healthy" {
		t.Fatalf("picked %s, want healthy", pick.ID)
	}
}

func TestSchedulerRespectsExcludeMap(t *testing.T) {
	cfg := config.GatewayConfig{
		Models:      map[string]config.Model{"qwen": {}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://gpu-01",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	_, err := s.Pick("qwen", now, map[string]bool{"gpu-01": true})
	if err == nil || !strings.Contains(err.Error(), "no healthy worker") {
		t.Fatalf("error = %v, want no healthy worker", err)
	}
}

func TestSchedulerRequiresReadyArtifactUnlessModelAlreadyRunning(t *testing.T) {
	cfg := config.GatewayConfig{
		Models:      map[string]config.Model{"qwen": {}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "missing-artifact",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://missing-artifact",
		Artifacts:    map[string]string{"qwen": "installing"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "running",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://running",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "running" {
		t.Fatalf("picked %s, want running", pick.ID)
	}
}

func TestSchedulerTieBreaksDeterministicallyByWorkerID(t *testing.T) {
	cfg := config.GatewayConfig{
		Models:      map[string]config.Model{"qwen": {}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-b",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://gpu-b",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-a",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://gpu-a",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "gpu-a" {
		t.Fatalf("picked %s, want gpu-a", pick.ID)
	}
}
