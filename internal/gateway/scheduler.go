package gateway

import (
	"strings"
	"time"

	"llm-swap/internal/config"
)

type Scheduler struct {
	Config    config.GatewayConfig
	Workers   *WorkerRegistry
	Access    *AccessTracker
	Cooldowns ReplicaCooldownSnapshot
}

func (s Scheduler) Pick(model string, now time.Time, exclude map[string]bool) (Worker, error) {
	decision, err := s.PickDecision(model, now, exclude)
	if err != nil {
		return Worker{}, err
	}
	return decision.Worker, nil
}

func (s Scheduler) PickDecision(model string, now time.Time, exclude map[string]bool) (ScheduleDecision, error) {
	decision, err := (Placement{Config: s.Config, Workers: s.Workers, Access: s.Access, Cooldowns: s.Cooldowns}).PickReadyWorker(model, now, exclude)
	if err != nil {
		return ScheduleDecision{
			ReadyReplicas:    decision.ReadyReplicas,
			OccupiedReplicas: decision.OccupiedReplicas,
			MaxLoaded:        decision.MaxLoaded,
			MaxLoadedAuto:    decision.MaxLoadedAuto,
			Candidates:       scheduleCandidatesFromPlacement(decision.Candidates),
		}, err
	}
	return ScheduleDecision{
		Worker:           decision.Worker,
		Reason:           decision.Reason,
		ReadyReplicas:    decision.ReadyReplicas,
		OccupiedReplicas: decision.OccupiedReplicas,
		MaxLoaded:        decision.MaxLoaded,
		MaxLoadedAuto:    decision.MaxLoadedAuto,
		Candidates:       scheduleCandidatesFromPlacement(decision.Candidates),
	}, nil
}

type ScheduleDecision struct {
	Worker           Worker
	Reason           string
	ReadyReplicas    int
	OccupiedReplicas int
	MaxLoaded        int
	MaxLoadedAuto    bool
	Candidates       []ScheduleCandidate
}

type ScheduleCandidate struct {
	WorkerID       string `json:"worker_id"`
	Reason         string `json:"reason"`
	Score          int    `json:"score"`
	ActiveRequests int    `json:"active_requests"`
	RunningState   string `json:"running_state,omitempty"`
	RunningModels  int    `json:"running_models"`
}

const (
	scheduleReasonReadyIdle          = "ready_idle"
	scheduleReasonReadyBusy          = "ready_busy"
	scheduleReasonReadyBusyScaleOut  = "ready_busy_scale_out"
	scheduleReasonSameModelLoading   = "same_model_loading"
	scheduleReasonEmptyScaleOut      = "empty_scale_out"
	scheduleReasonSwitchScaleOut     = "switch_scale_out"
	scheduleReasonEmptyColdStart     = "empty_cold_start"
	scheduleReasonSwitchColdStart    = "switch_cold_start"
	scheduleReasonMaxLoadedSatisfied = "max_loaded_satisfied"
)

func scoreScheduleCandidate(worker Worker, state string, runningSameModel bool, canScaleOut bool, targetLoaded bool, readyCount int, activeRequests int) (int, string) {
	if strings.EqualFold(state, "ready") {
		if activeRequests == 0 {
			return 600, scheduleReasonReadyIdle
		}
		if canScaleOut {
			return 100 - activeRequests, scheduleReasonReadyBusyScaleOut
		}
		return 500 - activeRequests, scheduleReasonReadyBusy
	}

	if runningSameModel {
		if readyCount == 0 {
			return 450 - activeRequests, scheduleReasonSameModelLoading
		}
		return 350 - activeRequests, scheduleReasonSameModelLoading
	}

	if canScaleOut {
		if len(worker.RunningModels) == 0 {
			return 400 - activeRequests, scheduleReasonEmptyScaleOut
		}
		return 200 - activeRequests, scheduleReasonSwitchScaleOut
	}

	if !targetLoaded && len(worker.RunningModels) == 0 {
		return 100 - activeRequests, scheduleReasonEmptyColdStart
	}
	if !targetLoaded {
		return 50 - activeRequests, scheduleReasonSwitchColdStart
	}
	return 0, scheduleReasonMaxLoadedSatisfied
}

func scheduleCandidatesFromPlacement(in []PlacementCandidate) []ScheduleCandidate {
	out := make([]ScheduleCandidate, 0, len(in))
	for _, candidate := range in {
		out = append(out, ScheduleCandidate{
			WorkerID:       candidate.WorkerID,
			Reason:         candidate.Reason,
			Score:          candidate.Score,
			ActiveRequests: candidate.ActiveRequests,
			RunningState:   candidate.RunningState,
			RunningModels:  candidate.RunningModels,
		})
	}
	return out
}

func workerAllowsModel(cfg config.GatewayConfig, worker Worker, model string) bool {
	for _, tag := range worker.Tags {
		policy, ok := tagPolicy(cfg, tag)
		if !ok {
			continue
		}
		if allowedModel(policy, model) {
			return true
		}
	}
	return false
}

func tagPolicy(cfg config.GatewayConfig, tag string) (config.TagPolicy, bool) {
	policy, ok := cfg.TagPolicies[tag]
	return policy, ok
}

func allowedModel(policy config.TagPolicy, model string) bool {
	for _, allowed := range policy.AllowedModels {
		if allowed == model {
			return true
		}
	}
	return false
}

func runningModelReady(worker Worker, model string) bool {
	state, ok := runningModelState(worker, model)
	return ok && strings.EqualFold(state, "ready")
}

func runningModelState(worker Worker, model string) (string, bool) {
	for _, running := range worker.RunningModels {
		if running.Model == model {
			return strings.ToLower(strings.TrimSpace(running.State)), true
		}
	}
	return "", false
}

func artifactReady(worker Worker, model string) bool {
	return strings.EqualFold(worker.Artifacts[model], "ready")
}
