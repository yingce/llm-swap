package gateway

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

type LoadedReconciler struct {
	Config             config.GatewayConfig
	Workers            *WorkerRegistry
	Client             LlamaSwapClient
	Access             *AccessTracker
	Pressure           *PressureTracker
	ReplicaCooldowns   *ReplicaCooldowns
	Cooldowns          ReplicaCooldownSnapshot
	Metrics            *Metrics
	RecordEvent        func(workerID string, event protocol.AgentEvent)
	RecordReachability func(workerID string, err error, now time.Time)
	LogEvent           func(event string, fields map[string]any)
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
			r.observeControlAction("unload", modelName, worker.ID, "max_loaded", "planned")
			if err := r.Client.Unload(ctx, worker.LlamaSwapURL, modelName); err != nil {
				if r.RecordReachability != nil {
					r.RecordReachability(worker.ID, err, time.Now())
				}
				r.recordUnloadEvent(worker.ID, modelName, "gateway_model_unload_error", err)
				r.observeControlAction("unload", modelName, worker.ID, "max_loaded", "error")
				outErr = errors.Join(outErr, err)
				continue
			}
			if r.RecordReachability != nil {
				r.RecordReachability(worker.ID, nil, time.Now())
			}
			r.recordUnloadEvent(worker.ID, modelName, "gateway_model_unload_done", nil)
			r.observeControlAction("unload", modelName, worker.ID, "max_loaded", "done")
			excess--
		}
	}
	placement := Placement{Config: r.Config, Workers: r.Workers, Access: r.Access, Pressure: r.Pressure, Cooldowns: r.Cooldowns}
	for _, action := range placement.PlanControlActions(now) {
		actionActive := r.Workers.ActiveSnapshot()
		switch action.Type {
		case ControlActionUnload:
			if actionActive[action.Worker.ID] > 0 {
				continue
			}
			r.logControlAction("control_action_planned", action, nil)
			r.observeControlAction(string(action.Type), action.Model, action.Worker.ID, action.Reason, "planned")
			if err := r.Client.Unload(ctx, action.Worker.LlamaSwapURL, action.Model); err != nil {
				if r.RecordReachability != nil {
					r.RecordReachability(action.Worker.ID, err, time.Now())
				}
				r.recordUnloadEvent(action.Worker.ID, action.Model, "gateway_model_unload_error", err)
				r.logControlAction("control_action_error", action, err)
				r.observeControlAction(string(action.Type), action.Model, action.Worker.ID, action.Reason, "error")
				outErr = errors.Join(outErr, err)
				continue
			}
			if r.RecordReachability != nil {
				r.RecordReachability(action.Worker.ID, nil, time.Now())
			}
			r.recordUnloadEvent(action.Worker.ID, action.Model, "gateway_model_unload_done", nil)
			r.logControlAction("control_action_done", action, nil)
			r.observeControlAction(string(action.Type), action.Model, action.Worker.ID, action.Reason, "done")
		case ControlActionWarm:
			if actionActive[action.Worker.ID] > 0 {
				continue
			}
			r.logControlAction("control_action_planned", action, nil)
			r.observeControlAction(string(action.Type), action.Model, action.Worker.ID, action.Reason, "planned")
			r.recordWarmEvent(action.Worker.ID, action.Model, "gateway_model_warm_start", nil)
			if err := r.Client.Load(ctx, action.Worker.LlamaSwapURL, action.Model); err != nil {
				if r.RecordReachability != nil {
					r.RecordReachability(action.Worker.ID, err, time.Now())
				}
				r.markWarmCooldown(action.Worker.ID, action.Model, "control_warm_failed")
				r.recordWarmEvent(action.Worker.ID, action.Model, "gateway_model_warm_error", err)
				r.logControlAction("control_action_error", action, err)
				r.observeControlAction(string(action.Type), action.Model, action.Worker.ID, action.Reason, "error")
				outErr = errors.Join(outErr, err)
				continue
			}
			if r.RecordReachability != nil {
				r.RecordReachability(action.Worker.ID, nil, time.Now())
			}
			r.recordWarmEvent(action.Worker.ID, action.Model, "gateway_model_warm_done", nil)
			r.logControlAction("control_action_done", action, nil)
			r.observeControlAction(string(action.Type), action.Model, action.Worker.ID, action.Reason, "done")
		default:
			continue
		}
	}
	return outErr
}

func (r LoadedReconciler) markWarmCooldown(workerID, modelName, reason string) {
	if r.ReplicaCooldowns == nil {
		return
	}
	entry, marked := r.ReplicaCooldowns.Mark(workerID, modelName, reason, time.Now())
	if marked && r.Metrics != nil {
		r.Metrics.ObserveReplicaCooldownMark(entry)
	}
}

func (r LoadedReconciler) observeControlAction(action, model, workerID, reason, result string) {
	if r.Metrics == nil {
		return
	}
	r.Metrics.ObserveControlAction(action, model, workerID, reason, result)
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
			r.recordUnloadEvent(victim.ID, victimModel, "gateway_model_unload_error", err)
			outErr = errors.Join(outErr, err)
			continue
		}
		r.recordUnloadEvent(victim.ID, victimModel, "gateway_model_unload_done", nil)
		loadedCounts[victimModel]--
	}
	return outErr
}

func (r LoadedReconciler) recordUnloadEvent(workerID string, modelName string, eventName string, err error) {
	if r.RecordEvent == nil {
		return
	}
	event := protocol.AgentEvent{
		Event: eventName,
		Model: modelName,
	}
	if err != nil {
		event.Error = err.Error()
	}
	r.RecordEvent(workerID, event)
}

func (r LoadedReconciler) recordWarmEvent(workerID string, modelName string, eventName string, err error) {
	if r.RecordEvent == nil {
		return
	}
	event := protocol.AgentEvent{
		Event: eventName,
		Model: modelName,
	}
	if err != nil {
		event.Error = err.Error()
	}
	r.RecordEvent(workerID, event)
}

func (r LoadedReconciler) logControlAction(event string, action ControlAction, err error) {
	if r.LogEvent == nil {
		return
	}
	fields := map[string]any{
		"action":       string(action.Type),
		"model":        action.Model,
		"worker_id":    action.Worker.ID,
		"reason":       action.Reason,
		"demand_score": action.DemandScore,
		"keep_score":   action.KeepScore,
		"switch_cost":  action.SwitchCost,
		"victim_model": action.VictimModel,
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	r.LogEvent(event, fields)
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
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		cfg := s.currentConfig()
		reconciler := LoadedReconciler{
			Config:           cfg,
			Workers:          s.workers,
			Client:           LlamaSwapClient{BearerToken: cfg.Tokens.LlamaSwap},
			Access:           s.access,
			Pressure:         s.pressure,
			ReplicaCooldowns: s.replicaCooldowns,
			Cooldowns:        s.replicaCooldowns.Snapshot(time.Now()),
			Metrics:          s.metrics,
			RecordEvent:      s.recordGatewayWorkerEvent,
			RecordReachability: func(workerID string, err error, now time.Time) {
				s.recordReverseAccessResult(workerID, err, now)
			},
			LogEvent: s.logEvent,
		}
		_ = reconciler.Reconcile(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
