package gateway

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"llm-swap/internal/config"
)

type Scheduler struct {
	Config  config.GatewayConfig
	Workers *WorkerRegistry
}

func (s Scheduler) Pick(model string, now time.Time, exclude map[string]bool) (Worker, error) {
	modelCfg, ok := s.Config.Models[model]
	if !ok {
		return Worker{}, fmt.Errorf("unknown model %q", model)
	}
	if s.Workers == nil {
		return Worker{}, fmt.Errorf("no healthy worker for model %q", model)
	}

	workers := s.Workers.Snapshot(now)
	active := s.Workers.ActiveSnapshot()
	loadedCount := 0
	for _, worker := range workers {
		if !s.Workers.Healthy(worker.ID, now) {
			continue
		}
		if !workerAllowsModel(s.Config, worker, model) {
			continue
		}
		if !artifactReady(worker, model) {
			continue
		}
		if runningModelReady(worker, model) {
			loadedCount++
		}
	}
	shouldLoadIdleReplica := modelCfg.MaxLoaded > 0 && loadedCount < modelCfg.MaxLoaded

	candidates := make([]scoredWorker, 0)
	for _, worker := range workers {
		if exclude != nil && exclude[worker.ID] {
			continue
		}
		if !s.Workers.Healthy(worker.ID, now) {
			continue
		}
		if !workerAllowsModel(s.Config, worker, model) {
			continue
		}
		if !artifactReady(worker, model) {
			continue
		}
		running := runningModelReady(worker, model)
		candidates = append(candidates, scoredWorker{
			worker: worker,
			score:  workerScore(worker, running, shouldLoadIdleReplica, active[worker.ID]),
		})
	}
	if len(candidates) == 0 {
		return Worker{}, fmt.Errorf("no healthy worker for model %q", model)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].worker.ID < candidates[j].worker.ID
	})
	return candidates[0].worker, nil
}

type scoredWorker struct {
	worker Worker
	score  int
}

func workerScore(worker Worker, running bool, shouldLoadIdleReplica bool, activeRequests int) int {
	if shouldLoadIdleReplica && !running && activeRequests == 0 && len(worker.RunningModels) == 0 {
		return 200
	}
	if running {
		return 100 - activeRequests
	}
	return 0
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
	for _, running := range worker.RunningModels {
		if running.Model == model && strings.EqualFold(running.State, "ready") {
			return true
		}
	}
	return false
}

func artifactReady(worker Worker, model string) bool {
	return strings.EqualFold(worker.Artifacts[model], "ready")
}
