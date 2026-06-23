package gateway

import (
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestPlacementPickReadyWorkerMatchesSchedulerReadyPreference(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {MinLoaded: 1},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "loaded",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://loaded",
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "ready"},
		},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)

	placement := Placement{Config: cfg, Workers: reg}
	decision, err := placement.PickReadyWorker("qwen", now, nil)
	if err != nil {
		t.Fatalf("PickReadyWorker returned error: %v", err)
	}
	if decision.Worker.ID != "loaded" {
		t.Fatalf("picked worker = %q, want loaded", decision.Worker.ID)
	}
	if decision.ReadyReplicas != 1 || decision.OccupiedReplicas != 1 {
		t.Fatalf("replicas ready=%d occupied=%d, want 1/1", decision.ReadyReplicas, decision.OccupiedReplicas)
	}
}

func TestPlacementCountsLoadingReplicaAsOccupiedButNotRoutable(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {MinLoaded: 0, MaxLoaded: 1, MaxLoadedSet: true},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "loading",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://loading",
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "loading"},
		},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)

	decision, err := (Placement{Config: cfg, Workers: reg}).PickReadyWorker("qwen", now, nil)
	if err == nil {
		t.Fatalf("PickReadyWorker error = nil, want no ready worker")
	}
	if decision.OccupiedReplicas != 1 {
		t.Fatalf("occupied replicas = %d, want 1", decision.OccupiedReplicas)
	}
	if decision.ReadyReplicas != 0 {
		t.Fatalf("ready replicas = %d, want 0", decision.ReadyReplicas)
	}
}

func TestPlacementMissingMaxLoadedUsesEligibleWorkerCountAsAutoCeiling(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {MinLoaded: 1},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	for _, id := range []string{"a", "b", "c"} {
		reg.UpsertHeartbeat(protocol.HeartbeatRequest{
			AgentID:      id,
			Tags:         []string{"gpu"},
			LlamaSwapURL: "http://" + id,
			Artifacts:    map[string]string{"qwen": "ready"},
		}, now)
	}

	decision, err := (Placement{Config: cfg, Workers: reg}).PickReadyWorker("qwen", now, nil)
	if err != nil {
		t.Fatalf("PickReadyWorker returned error: %v", err)
	}
	if decision.MaxLoaded != 3 {
		t.Fatalf("MaxLoaded = %d, want 3 eligible workers", decision.MaxLoaded)
	}
	if !decision.MaxLoadedAuto {
		t.Fatalf("MaxLoadedAuto = false, want true")
	}
}

func TestPlacementPlanControlActionsEvictsMinLoadedZeroBeforeProtectedFloor(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 100, MinLoaded: 2},
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "hot-worker",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://hot-worker",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "hot", State: "ready"},
		},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "cold-worker",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://cold-worker",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready"},
		},
	}, now)

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker()}).PlanControlActions(now)
	if len(actions) == 0 {
		t.Fatalf("PlanControlActions returned no actions, want cold unload")
	}
	if actions[0].Type != ControlActionUnload || actions[0].Worker.ID != "cold-worker" || actions[0].Model != "cold" {
		t.Fatalf("first action = %#v, want unload cold from cold-worker", actions[0])
	}
}

func TestPlacementDoesNotUnloadMinLoadedZeroWhenNoCapacityIsNeeded(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "worker-a",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://worker-a",
		Artifacts:    map[string]string{"cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready"},
		},
	}, now)

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker()}).PlanControlActions(now)
	if len(actions) != 0 {
		t.Fatalf("PlanControlActions returned %#v, want no unload while capacity is not needed", actions)
	}
}

func TestPlacementDoesNotEvictProtectedReplica(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 100, MinLoaded: 1},
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "protected-cold",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://protected-cold",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready", ProtectedUntil: now.Add(time.Minute)},
		},
	}, now)

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker()}).PlanControlActions(now)
	if len(actions) != 0 {
		t.Fatalf("PlanControlActions returned %#v, want no eviction of protected replica", actions)
	}
}

func TestPlacementPlansWarmActionOnEmptyIdleWorkerForSustainedPressure(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {Priority: 100, MinLoaded: 1},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "ready",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://ready",
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "ready"},
		},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests; i++ {
		pressure.RecordRequest(PressureRequestObservation{
			Time:        now.Add(time.Duration(i) * time.Second),
			Model:       "qwen",
			TotalTokens: 1000,
		})
	}
	pressure.RecordQueue(PressureQueueObservation{
		Time:             now.Add(5 * time.Second),
		Model:            "qwen",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           800,
		ReadyReplicas:    1,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(10 * time.Second))
	if len(actions) != 1 {
		t.Fatalf("actions = %#v, want one warm action", actions)
	}
	if actions[0].Type != ControlActionWarm || actions[0].Worker.ID != "empty" || actions[0].Model != "qwen" {
		t.Fatalf("action = %#v, want warm qwen on empty", actions[0])
	}
	if actions[0].Reason != "empty_worker_predictive_scaleout" {
		t.Fatalf("reason = %q, want empty_worker_predictive_scaleout", actions[0].Reason)
	}
}

func TestPlacementWarmEvictsOpportunityCacheOnlyWhenDemandBeatsSwitchCost(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 200, MinLoaded: 0},
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "cold-worker",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://cold-worker",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready"},
		},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests+3; i++ {
		pressure.RecordRequest(PressureRequestObservation{
			Time:        now.Add(time.Duration(i) * time.Second),
			Model:       "hot",
			TotalTokens: 1000,
		})
	}
	pressure.RecordQueue(PressureQueueObservation{
		Time:             now.Add(5 * time.Second),
		Model:            "hot",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           800,
		ReadyReplicas:    0,
		OccupiedReplicas: 0,
		ActiveBefore:     1,
	})

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(10 * time.Second))
	if len(actions) != 1 {
		t.Fatalf("actions = %#v, want one warm eviction action", actions)
	}
	action := actions[0]
	if action.Type != ControlActionWarm {
		t.Fatalf("action type = %q, want warm", action.Type)
	}
	if action.Model != "hot" || action.VictimModel != "cold" || action.Worker.ID != "cold-worker" {
		t.Fatalf("action = %#v, want warm hot by evicting cold on cold-worker", action)
	}
	if action.Reason != "evict_for_predictive_scaleout" {
		t.Fatalf("reason = %q, want evict_for_predictive_scaleout", action.Reason)
	}
	if action.DemandScore <= 0 {
		t.Fatalf("demand score = %d, want positive", action.DemandScore)
	}
	if action.SwitchCost != defaultSwitchCost {
		t.Fatalf("switch cost = %d, want %d", action.SwitchCost, defaultSwitchCost)
	}
	if action.KeepScore+action.SwitchCost >= action.DemandScore {
		t.Fatalf("keep score + switch cost = %d + %d, want less than demand score %d", action.KeepScore, action.SwitchCost, action.DemandScore)
	}
}

func TestPlacementWarmDoesNotEvictWhenDemandDoesNotBeatSwitchCost(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 70, MinLoaded: 0},
			"cold": {Priority: 200, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "cold-worker",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://cold-worker",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready"},
		},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests; i++ {
		pressure.RecordRequest(PressureRequestObservation{
			Time:  now.Add(time.Duration(i) * time.Second),
			Model: "hot",
		})
	}

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(10 * time.Second))
	if len(actions) != 0 {
		t.Fatalf("actions = %#v, want no warm eviction when demand does not beat keep score plus switch cost", actions)
	}
}

func TestPlacementWarmDoesNotEvictProtectedReplica(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 200, MinLoaded: 0},
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "cold-worker",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://cold-worker",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready", ProtectedUntil: now.Add(time.Minute)},
		},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests+3; i++ {
		pressure.RecordRequest(PressureRequestObservation{
			Time:        now.Add(time.Duration(i) * time.Second),
			Model:       "hot",
			TotalTokens: 1000,
		})
	}
	pressure.RecordQueue(PressureQueueObservation{
		Time:             now.Add(5 * time.Second),
		Model:            "hot",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           800,
		ReadyReplicas:    0,
		OccupiedReplicas: 0,
		ActiveBefore:     1,
	})

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(10 * time.Second))
	if len(actions) != 0 {
		t.Fatalf("actions = %#v, want no action against protected replica", actions)
	}
}

func TestPlacementDoesNotPlanWarmWhenTargetAlreadyLoading(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {Priority: 100, MinLoaded: 0, MaxLoaded: 2, MaxLoadedSet: true},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "loading",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://loading",
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "loading"},
		},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests+3; i++ {
		pressure.RecordRequest(PressureRequestObservation{
			Time:        now.Add(time.Duration(i) * time.Second),
			Model:       "qwen",
			TotalTokens: 1000,
		})
	}
	pressure.RecordQueue(PressureQueueObservation{
		Time:             now.Add(5 * time.Second),
		Model:            "qwen",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           800,
		ReadyReplicas:    0,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(10 * time.Second))
	if len(actions) != 0 {
		t.Fatalf("actions = %#v, want no duplicate warm while target is already loading", actions)
	}
}

func TestPlacementDoesNotPredictivelyWarmWhenAnyMinLoadedFloorIsUnderloaded(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 100, MinLoaded: 1},
			"qwen": {Priority: 200, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts: map[string]string{
			"hot":  "ready",
			"qwen": "ready",
		},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests+3; i++ {
		pressure.RecordRequest(PressureRequestObservation{
			Time:        now.Add(time.Duration(i) * time.Second),
			Model:       "qwen",
			TotalTokens: 1000,
		})
	}
	pressure.RecordQueue(PressureQueueObservation{
		Time:             now.Add(5 * time.Second),
		Model:            "qwen",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           800,
		ReadyReplicas:    0,
		OccupiedReplicas: 0,
		ActiveBefore:     1,
	})

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(10 * time.Second))
	if len(actions) != 0 {
		t.Fatalf("actions = %#v, want no predictive warm while a min_loaded floor is underloaded", actions)
	}
}
