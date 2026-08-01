package gateway

import "time"

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
		policy, ok := tagPolicy(cfg, selectedWorkerTag(cfg, worker, model))
		if !ok {
			continue
		}
		capacity.MaxConcurrency += policy.WorkerDefaults.MaxConcurrency
		capacity.MaxQueue += policy.WorkerDefaults.MaxQueue
	}
	return capacity
}
