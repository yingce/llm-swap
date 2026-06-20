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
	if _, ok := s.Config.Models[model]; !ok {
		return Worker{}, fmt.Errorf("unknown model %q", model)
	}
	if s.Workers == nil {
		return Worker{}, fmt.Errorf("no healthy worker for model %q", model)
	}

	candidates := make([]scoredWorker, 0)
	for _, worker := range s.Workers.Snapshot(now) {
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
			score:  workerScore(running),
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

func workerScore(running bool) int {
	if running {
		return 100
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
