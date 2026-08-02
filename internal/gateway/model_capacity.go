package gateway

import (
	"time"

	"llm-swap/internal/config"
)

type modelCapacity struct {
	MaxConcurrency int
	MaxQueue       int
}

func effectiveCapacity(derived, legacyCeiling int) int {
	if derived <= 0 {
		return legacyCeiling
	}
	if legacyCeiling <= 0 || derived < legacyCeiling {
		return derived
	}
	return legacyCeiling
}

func modelTagCapacity(cfg config.GatewayConfig, modelName, tag string) config.WorkerDefaults {
	if model, ok := cfg.Models[modelName]; ok {
		if capacity, configured := model.TagCapacity[tag]; configured {
			return capacity
		}
	}
	if policy, ok := cfg.TagPolicies[tag]; ok {
		if policy.WorkerDefaults.MaxConcurrency > 0 || policy.WorkerDefaults.MaxQueue > 0 {
			return policy.WorkerDefaults
		}
	}
	return config.WorkerDefaults{MaxConcurrency: 1, MaxQueue: 1}
}

func workerModelLimitKey(workerID, model string) string {
	return "worker:" + workerID + ":model:" + model
}

func workerModelLimitPrefix(workerID string) string {
	return "worker:" + workerID + ":model:"
}

func (s *Server) modelCapacity(model string, now time.Time) modelCapacity {
	if s == nil || s.workers == nil {
		return modelCapacity{}
	}
	cfg := activeGatewayConfig(s.currentConfig())
	if _, ok := cfg.Models[model]; !ok {
		return modelCapacity{}
	}

	var capacity modelCapacity
	for _, worker := range s.workers.Snapshot(now) {
		if !s.workers.Healthy(worker.ID, now) ||
			!workerAllowsModel(cfg, worker, model) ||
			!artifactReady(worker, model) ||
			!runningModelReady(worker, model) {
			continue
		}
		tag := selectedWorkerTag(cfg, worker, model)
		if _, ok := tagPolicy(cfg, tag); !ok {
			continue
		}
		workerCapacity := modelTagCapacity(cfg, model, tag)
		capacity.MaxConcurrency += workerCapacity.MaxConcurrency
		capacity.MaxQueue += workerCapacity.MaxQueue
	}
	return capacity
}
