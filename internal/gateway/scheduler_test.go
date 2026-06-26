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

func TestSchedulerPrefersIdleLoadedWorkerWhenLoadedReplicasBelowMaxLoaded(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {MaxLoaded: 2}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loaded",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loaded",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "idle",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://idle",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "loaded" {
		t.Fatalf("picked %s, want loaded worker before filling max_loaded", pick.ID)
	}
}

func TestSchedulerKeepsRoutingToReadyWorkerWhenScaleOutIsPossible(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {MaxLoaded: 2}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loaded",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loaded",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready"},
	}, now)
	release, ok := reg.Acquire("loaded", now)
	if !ok {
		t.Fatal("failed to mark loaded worker active")
	}
	defer release()
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "idle",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://idle",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "loaded" {
		t.Fatalf("picked %s, want ready worker even when scale-out is possible", pick.ID)
	}
}

func TestSchedulerDoesNotRouteOrDuplicateColdStartWhenSameModelIsLoadingAtMaxLoaded(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {MaxLoaded: 1}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loading",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loading",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "loading"}},
		Artifacts:     map[string]string{"qwen": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "idle",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://idle",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	_, err := s.PickDecision("qwen", now, nil)
	if err == nil || !strings.Contains(err.Error(), "no ready worker") {
		t.Fatalf("PickDecision error = %v, want no ready worker while loading occupies max_loaded", err)
	}
}

func TestSchedulerKeepsRoutingToReadyWorkerBeforeSwitchingOtherWorker(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen":  {MaxLoaded: 2},
			"other": {},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen", "other"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loaded-qwen",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loaded-qwen",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready", "other": "ready"},
	}, now)
	release, ok := reg.Acquire("loaded-qwen", now)
	if !ok {
		t.Fatal("failed to mark loaded worker active")
	}
	defer release()
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loaded-other",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loaded-other",
		RunningModels: []protocol.RunningModel{{Model: "other", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready", "other": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "loaded-qwen" {
		t.Fatalf("picked %s, want ready qwen worker before switching another worker", pick.ID)
	}
}

func TestSchedulerReturnsNoReadyWorkerInsteadOfColdWorkerWhenReadyWorkerExcluded(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {MaxLoaded: 2}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loaded",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loaded",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "idle",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://idle",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	_, err := s.Pick("qwen", now, map[string]bool{"loaded": true})
	if err == nil || !strings.Contains(err.Error(), "no healthy worker") {
		t.Fatalf("Pick error = %v, want no healthy worker instead of routing current request to cold worker", err)
	}
}

func TestSchedulerDoesNotRouteUnloadedModelToIdleWorker(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen":  {},
			"other": {},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen", "other"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "worker-1",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker-1",
		RunningModels: []protocol.RunningModel{{Model: "other", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready", "other": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "worker-2",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-2",
		Artifacts:    map[string]string{"qwen": "ready", "other": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	decision, err := s.PickDecision("qwen", now, nil)
	if err == nil || !strings.Contains(err.Error(), "no ready worker") {
		t.Fatalf("PickDecision error = %v, want no ready worker", err)
	}
	if decision.ReadyReplicas != 0 || decision.OccupiedReplicas != 0 {
		t.Fatalf("replicas ready=%d occupied=%d, want 0/0", decision.ReadyReplicas, decision.OccupiedReplicas)
	}
}

func TestSchedulerDoesNotRouteToSwitchWorkerWhenAllMinLoadedFloorsAreOccupied(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"a": {MinLoaded: 1},
			"b": {MinLoaded: 1},
			"c": {MinLoaded: 1},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"a", "b", "c"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "worker-a",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker-a",
		RunningModels: []protocol.RunningModel{{Model: "a", State: "ready"}},
		Artifacts:     map[string]string{"a": "ready", "b": "ready", "c": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "worker-b",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker-b",
		RunningModels: []protocol.RunningModel{{Model: "b", State: "ready"}},
		Artifacts:     map[string]string{"a": "ready", "b": "ready", "c": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	decision, err := s.PickDecision("c", now, nil)
	if err == nil || !strings.Contains(err.Error(), "no ready worker") {
		t.Fatalf("PickDecision error = %v, want no ready worker", err)
	}
	if decision.ReadyReplicas != 0 || decision.OccupiedReplicas != 0 || decision.MaxLoaded != 2 || !decision.MaxLoadedAuto {
		t.Fatalf("decision = %+v, want no c replica with auto max_loaded=2", decision)
	}
	if len(decision.Candidates) != 0 {
		t.Fatalf("candidates = %+v, want no request-routable switch worker", decision.Candidates)
	}
}

func TestSchedulerDoesNotUseColdWorkerWhenMaxLoadedAlreadySatisfied(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {MaxLoaded: 1}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loaded",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loaded",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "idle",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://idle",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "loaded" {
		t.Fatalf("picked %s, want loaded worker when max_loaded is satisfied", pick.ID)
	}
}

func TestSchedulerUsesAutoMaxLoadedWhenMaxLoadedIsMissing(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {MinLoaded: 1}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loaded",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://loaded",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Artifacts:     map[string]string{"qwen": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "idle",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://idle",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	decision, err := s.PickDecision("qwen", now, nil)
	if err != nil {
		t.Fatalf("PickDecision returned error: %v", err)
	}
	if decision.Worker.ID != "loaded" {
		t.Fatalf("picked %s, want loaded worker when max_loaded is automatic", decision.Worker.ID)
	}
	if decision.MaxLoaded != 2 {
		t.Fatalf("MaxLoaded = %d, want 2 eligible workers", decision.MaxLoaded)
	}
	if !decision.MaxLoadedAuto {
		t.Fatalf("MaxLoadedAuto = false, want true")
	}
}

func TestSchedulerRejectsUnknownModel(t *testing.T) {
	s := Scheduler{Config: config.GatewayConfig{Models: map[string]config.Model{"qwen": {}}}}

	_, err := s.Pick("missing", time.Unix(100, 0), nil)
	if err == nil || !strings.Contains(err.Error(), "unknown model") {
		t.Fatalf("error = %v, want unknown model", err)
	}
}

func TestSchedulerIgnoresStaleAndDrainingWorkers(t *testing.T) {
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
		AgentID:      "healthy",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://healthy",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	decision, err := s.PickDecision("qwen", now, nil)
	if err == nil || !strings.Contains(err.Error(), "no ready worker") {
		t.Fatalf("PickDecision error = %v, want no ready worker", err)
	}
	if decision.ReadyReplicas != 0 || decision.OccupiedReplicas != 0 {
		t.Fatalf("decision = %+v, want no routable ready replica", decision)
	}
}

func TestSchedulerAllowsWorkerWithLastErrorWhenHealthyAndArtifactReady(t *testing.T) {
	cfg := config.GatewayConfig{
		Models:      map[string]config.Model{"qwen": {}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "errored",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://errored",
		Artifacts:    map[string]string{"qwen": "ready"},
		LastError:    "previous failure",
	}, now)
	s := Scheduler{Config: cfg, Workers: reg}

	decision, err := s.PickDecision("qwen", now, nil)
	if err == nil || !strings.Contains(err.Error(), "no ready worker") {
		t.Fatalf("PickDecision error = %v, want no ready worker", err)
	}
	if decision.ReadyReplicas != 0 || decision.OccupiedReplicas != 0 {
		t.Fatalf("decision = %+v, want no routable ready replica", decision)
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
	if err == nil || !strings.Contains(err.Error(), "no ready worker") {
		t.Fatalf("error = %v, want no ready worker", err)
	}
}

func TestSchedulerSkipsReadyReplicaInCooldown(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {MaxLoaded: 2}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "bad",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://bad",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "good",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://good",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	}, now)
	cooldowns := NewReplicaCooldowns(30 * time.Second)
	cooldowns.Mark("bad", "qwen", "upstream_503", now)

	decision, err := (Scheduler{Config: cfg, Workers: reg, Cooldowns: cooldowns.Snapshot(now)}).PickDecision("qwen", now, nil)
	if err != nil {
		t.Fatalf("PickDecision returned error: %v", err)
	}
	if decision.Worker.ID != "good" {
		t.Fatalf("picked %s, want good", decision.Worker.ID)
	}
	for _, candidate := range decision.Candidates {
		if candidate.WorkerID == "bad" {
			t.Fatalf("cooled-down worker appeared in candidates: %+v", decision.Candidates)
		}
	}
}

func TestSchedulerRequiresReadyArtifactEvenWhenModelAlreadyRunning(t *testing.T) {
	cfg := config.GatewayConfig{
		Models:      map[string]config.Model{"qwen": {}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	now := time.Unix(100, 0)

	t.Run("picks ready cold worker over running worker without ready artifact", func(t *testing.T) {
		reg := NewWorkerRegistry(6 * time.Second)
		reg.UpsertHeartbeat(protocol.HeartbeatRequest{
			AgentID:       "running",
			Tags:          []string{"gpu-4090"},
			LlamaSwapURL:  "http://running",
			RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
			Artifacts:     map[string]string{"qwen": "installing"},
		}, now)
		reg.UpsertHeartbeat(protocol.HeartbeatRequest{
			AgentID:      "cold",
			Tags:         []string{"gpu-4090"},
			LlamaSwapURL: "http://cold",
			Artifacts:    map[string]string{"qwen": "ready"},
		}, now)
		s := Scheduler{Config: cfg, Workers: reg}

		decision, err := s.PickDecision("qwen", now, nil)
		if err == nil || !strings.Contains(err.Error(), "no ready worker") {
			t.Fatalf("PickDecision error = %v, want no ready worker", err)
		}
		if decision.ReadyReplicas != 0 || decision.OccupiedReplicas != 1 {
			t.Fatalf("decision = %+v, want loading replica counted as occupied only", decision)
		}
	})

	t.Run("returns no healthy worker when running worker lacks ready artifact", func(t *testing.T) {
		reg := NewWorkerRegistry(6 * time.Second)
		reg.UpsertHeartbeat(protocol.HeartbeatRequest{
			AgentID:       "running",
			Tags:          []string{"gpu-4090"},
			LlamaSwapURL:  "http://running",
			RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
			Artifacts:     map[string]string{"qwen": "installing"},
		}, now)
		s := Scheduler{Config: cfg, Workers: reg}

		_, err := s.Pick("qwen", now, nil)
		if err == nil || !strings.Contains(err.Error(), "no ready worker") {
			t.Fatalf("error = %v, want no ready worker", err)
		}
	})
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

	decision, err := s.PickDecision("qwen", now, nil)
	if err == nil || !strings.Contains(err.Error(), "no ready worker") {
		t.Fatalf("PickDecision error = %v, want no ready worker", err)
	}
	if decision.ReadyReplicas != 0 || decision.OccupiedReplicas != 0 {
		t.Fatalf("decision = %+v, want no routable ready replica", decision)
	}
}
