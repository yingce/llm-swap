package gateway

import (
	"context"
	"errors"
	"sort"
	"time"

	"llm-swap/internal/config"
)

type LoadedReconciler struct {
	Config  config.GatewayConfig
	Workers *WorkerRegistry
	Client  LlamaSwapClient
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
		if model.MaxLoaded <= 0 {
			continue
		}
		loaded := loadedWorkersForModel(workers, modelName, now, r.Workers)
		if len(loaded) <= model.MaxLoaded {
			continue
		}

		sort.Slice(loaded, func(i, j int) bool {
			return loaded[i].ID < loaded[j].ID
		})
		excess := len(loaded) - model.MaxLoaded
		for i := len(loaded) - 1; i >= 0 && excess > 0; i-- {
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
	return outErr
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
	}
	reconciler.Run(ctx, interval)
}
