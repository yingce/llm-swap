package gateway

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"llm-swap/internal/config"
)

type LoadedReconciler struct {
	Config  config.GatewayConfig
	Workers *WorkerRegistry
	Client  LlamaSwapClient
	Access  *AccessTracker
}

func (r LoadedReconciler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		_ = r.Reconcile(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r LoadedReconciler) Reconcile(ctx context.Context, now time.Time) error {
	if r.Workers == nil {
		return nil
	}
	active := r.Workers.ActiveSnapshot()
	workers := r.Workers.Snapshot(now)

	var outErr error
	for modelName, model := range r.Config.Models {
		maxLoaded := model.EffectiveMaxLoaded()
		if maxLoaded <= 0 {
			continue
		}
		loaded := loadedWorkersForModel(workers, modelName, now, r.Workers)
		if len(loaded) <= maxLoaded {
			continue
		}

		sort.Slice(loaded, func(i, j int) bool {
			return r.workerModelLessRecentlyAccessed(loaded[i], loaded[j], modelName)
		})
		excess := len(loaded) - maxLoaded
		for i := 0; i < len(loaded) && excess > 0; i++ {
			worker := loaded[i]
			if active[worker.ID] > 0 {
				continue
			}
			if err := r.Client.Unload(ctx, worker.LlamaSwapURL, modelName); err != nil {
				outErr = errors.Join(outErr, err)
				continue
			}
			excess--
		}
	}
	outErr = errors.Join(outErr, r.unloadColdModelsForUnderloadedHotModels(ctx, now, workers, active))
	return outErr
}

func (r LoadedReconciler) unloadColdModelsForUnderloadedHotModels(ctx context.Context, now time.Time, workers []Worker, active map[string]int) error {
	var outErr error
	loadedCounts := runningModelCounts(workers, now, r.Workers)
	for modelName, model := range r.Config.Models {
		maxLoaded := model.EffectiveMaxLoaded()
		if maxLoaded <= 0 || loadedCounts[modelName] >= maxLoaded {
			continue
		}
		if r.Access == nil || r.Access.ModelLastAccess(modelName).IsZero() {
			continue
		}

		victim, victimModel, ok := r.pickColdVictimForModel(workers, active, loadedCounts, modelName)
		if !ok {
			continue
		}
		if err := r.Client.Unload(ctx, victim.LlamaSwapURL, victimModel); err != nil {
			outErr = errors.Join(outErr, err)
			continue
		}
		loadedCounts[victimModel]--
	}
	return outErr
}

func (r LoadedReconciler) pickColdVictimForModel(workers []Worker, active map[string]int, loadedCounts map[string]int, targetModel string) (Worker, string, bool) {
	var bestWorker Worker
	var bestModel string
	var bestLast time.Time
	found := false
	for _, worker := range workers {
		if active[worker.ID] > 0 {
			continue
		}
		if runningModelReady(worker, targetModel) {
			continue
		}
		if !workerAllowsModel(r.Config, worker, targetModel) || !artifactReady(worker, targetModel) {
			continue
		}
		for _, running := range worker.RunningModels {
			if !strings.EqualFold(running.State, "ready") || running.Model == targetModel {
				continue
			}
			if !r.canUnloadModel(running.Model, loadedCounts) {
				continue
			}
			last := r.Access.WorkerModelLastAccess(worker.ID, running.Model)
			if !found || last.Before(bestLast) || (last.Equal(bestLast) && worker.ID < bestWorker.ID) {
				bestWorker = worker
				bestModel = running.Model
				bestLast = last
				found = true
			}
		}
	}
	return bestWorker, bestModel, found
}

func (r LoadedReconciler) canUnloadModel(modelName string, loadedCounts map[string]int) bool {
	model, ok := r.Config.Models[modelName]
	if !ok {
		return true
	}
	return loadedCounts[modelName] > model.MinLoaded
}

func (r LoadedReconciler) workerModelLessRecentlyAccessed(a Worker, b Worker, model string) bool {
	aLast := r.Access.WorkerModelLastAccess(a.ID, model)
	bLast := r.Access.WorkerModelLastAccess(b.ID, model)
	if !aLast.Equal(bLast) {
		return aLast.Before(bLast)
	}
	return a.ID < b.ID
}

func runningModelCounts(workers []Worker, now time.Time, reg *WorkerRegistry) map[string]int {
	counts := make(map[string]int)
	for _, worker := range workers {
		if reg != nil && !reg.Healthy(worker.ID, now) {
			continue
		}
		for _, running := range worker.RunningModels {
			if strings.EqualFold(running.State, "ready") {
				counts[running.Model]++
			}
		}
	}
	return counts
}

func loadedWorkersForModel(workers []Worker, model string, now time.Time, reg *WorkerRegistry) []Worker {
	loaded := make([]Worker, 0, len(workers))
	for _, worker := range workers {
		if reg != nil && !reg.Healthy(worker.ID, now) {
			continue
		}
		if runningModelReady(worker, model) {
			loaded = append(loaded, worker)
		}
	}
	return loaded
}

func (s *Server) RunLoadedReconciler(ctx context.Context, interval time.Duration) {
	reconciler := LoadedReconciler{
		Config:  s.config,
		Workers: s.workers,
		Client:  LlamaSwapClient{BearerToken: s.config.Tokens.LlamaSwap},
		Access:  s.access,
	}
	reconciler.Run(ctx, interval)
}
