package gateway

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

type Placement struct {
	Config    config.GatewayConfig
	Workers   *WorkerRegistry
	Access    *AccessTracker
	Pressure  *PressureTracker
	Cooldowns ReplicaCooldownSnapshot
}

type PlacementDecision struct {
	Worker           Worker
	Reason           string
	ReadyReplicas    int
	OccupiedReplicas int
	MaxLoaded        int
	MaxLoadedAuto    bool
	Candidates       []PlacementCandidate
}

type PlacementCandidate struct {
	WorkerID       string `json:"worker_id"`
	Reason         string `json:"reason"`
	Score          int    `json:"score"`
	ActiveRequests int    `json:"active_requests"`
	RunningState   string `json:"running_state,omitempty"`
	RunningModels  int    `json:"running_models"`
}

type ControlActionType string

const (
	ControlActionUnload ControlActionType = "unload"
	ControlActionWarm   ControlActionType = "warm"
)

type ControlAction struct {
	Type        ControlActionType
	Worker      Worker
	Model       string
	Reason      string
	VictimModel string
	DemandScore int
	KeepScore   int
	SwitchCost  int
}

func (p Placement) PickReadyWorker(model string, now time.Time, exclude map[string]bool) (PlacementDecision, error) {
	modelCfg, ok := p.Config.Models[model]
	if !ok {
		return PlacementDecision{}, fmt.Errorf("unknown model %q", model)
	}
	if p.Workers == nil {
		return PlacementDecision{}, fmt.Errorf("no healthy worker for model %q", model)
	}

	workers := p.Workers.Snapshot(now)
	active := p.Workers.ActiveSnapshot()
	readyCount := 0
	occupiedCount := 0
	for _, worker := range workers {
		if !p.Workers.Healthy(worker.ID, now) {
			continue
		}
		if !workerAllowsModel(p.Config, worker, model) {
			continue
		}
		if !artifactReady(worker, model) {
			continue
		}
		state, running := runningModelState(worker, model)
		if running {
			occupiedCount++
		}
		if strings.EqualFold(state, "ready") {
			readyCount++
		}
	}

	targetLoaded := readyCount > 0
	maxLoaded, maxLoadedAuto := p.effectiveMaxLoaded(modelCfg, workers, model, now)
	canScaleOut := maxLoaded > 0 && occupiedCount < maxLoaded
	loadingAtCeiling := maxLoaded > 0 && readyCount == 0 && occupiedCount >= maxLoaded

	candidates := make([]scoredPlacementWorker, 0)
	for _, worker := range workers {
		if exclude != nil && exclude[worker.ID] {
			continue
		}
		if !p.Workers.Healthy(worker.ID, now) {
			continue
		}
		if !workerAllowsModel(p.Config, worker, model) {
			continue
		}
		if !artifactReady(worker, model) {
			continue
		}
		if p.Cooldowns.Active(worker.ID, model, now) {
			continue
		}
		state, running := runningModelState(worker, model)
		if running && !strings.EqualFold(state, "ready") {
			continue
		}
		if readyCount > 0 && !running {
			continue
		}
		if loadingAtCeiling && !running {
			continue
		}
		score, reason := scoreScheduleCandidate(worker, state, running, canScaleOut, targetLoaded, readyCount, active[worker.ID])
		candidates = append(candidates, scoredPlacementWorker{
			worker:         worker,
			score:          score,
			reason:         reason,
			activeRequests: active[worker.ID],
			runningState:   state,
		})
	}
	if len(candidates) == 0 {
		if occupiedCount > 0 || readyCount > 0 {
			return PlacementDecision{
				ReadyReplicas:    readyCount,
				OccupiedReplicas: occupiedCount,
				MaxLoaded:        maxLoaded,
				MaxLoadedAuto:    maxLoadedAuto,
			}, fmt.Errorf("no ready worker for model %q", model)
		}
		return PlacementDecision{}, fmt.Errorf("no healthy worker for model %q", model)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].worker.ID < candidates[j].worker.ID
	})
	picked := candidates[0]
	return PlacementDecision{
		Worker:           picked.worker,
		Reason:           picked.reason,
		ReadyReplicas:    readyCount,
		OccupiedReplicas: occupiedCount,
		MaxLoaded:        maxLoaded,
		MaxLoadedAuto:    maxLoadedAuto,
		Candidates:       placementCandidates(candidates),
	}, nil
}

func (p Placement) PlanControlActions(now time.Time) []ControlAction {
	if p.Workers == nil {
		return nil
	}
	workers := p.Workers.Snapshot(now)
	active := p.Workers.ActiveSnapshot()
	loadedCounts := runningModelCounts(workers, now, p.Workers)

	models := placementModelNamesByPriority(p.Config)
	underloadedFloor := false
	for _, modelName := range models {
		model := p.Config.Models[modelName]
		if model.MinLoaded <= 0 {
			continue
		}
		occupiedCount := occupiedModelCount(workers, now, p.Workers, modelName)
		if occupiedCount >= model.MinLoaded {
			continue
		}
		maxLoaded, _ := p.effectiveMaxLoaded(model, workers, modelName, now)
		if maxLoaded <= 0 || occupiedCount >= maxLoaded {
			continue
		}
		underloadedFloor = true
		if worker, ok := p.pickEmptyWarmWorker(now, workers, active, modelName); ok {
			return []ControlAction{{
				Type:   ControlActionWarm,
				Worker: worker,
				Model:  modelName,
				Reason: "warm_for_min_loaded_empty_worker",
			}}
		}
		victim, victimModel, ok := p.pickEvictionVictimForModel(now, workers, active, loadedCounts, modelName)
		if !ok {
			continue
		}
		return []ControlAction{{
			Type:   ControlActionUnload,
			Worker: victim,
			Model:  victimModel,
			Reason: "free_capacity_for_min_loaded",
		}}
	}
	if underloadedFloor {
		return nil
	}
	if action, ok := p.planWarmAction(now, workers, active, loadedCounts); ok {
		return []ControlAction{action}
	}
	return nil
}

func (p Placement) pickEmptyWarmWorker(now time.Time, workers []Worker, active map[string]int, modelName string) (Worker, bool) {
	for _, worker := range sortedWorkersByID(workers) {
		if !p.warmEligibleWorker(worker, now, active, modelName) {
			continue
		}
		if len(worker.RunningModels) == 0 {
			return worker, true
		}
	}
	return Worker{}, false
}

func (p Placement) planWarmAction(now time.Time, workers []Worker, active map[string]int, loadedCounts map[string]int) (ControlAction, bool) {
	if p.Pressure == nil {
		return ControlAction{}, false
	}
	workers = sortedWorkersByID(workers)
	for _, modelName := range placementModelNamesByPriority(p.Config) {
		model := p.Config.Models[modelName]
		readyCount := loadedCounts[modelName]
		occupiedCount := occupiedModelCount(workers, now, p.Workers, modelName)
		maxLoaded, _ := p.effectiveMaxLoaded(model, workers, modelName, now)
		if maxLoaded <= 0 || occupiedCount >= maxLoaded {
			continue
		}
		if occupiedCount > readyCount {
			continue
		}

		demandScore := DemandScore(p.Pressure.Model(modelName, now), DemandScoreInput{
			Priority:         model.Priority,
			ReadyReplicas:    readyCount,
			OccupiedReplicas: occupiedCount,
			Active:           activeCountForReadyModel(workers, active, modelName),
		})
		if demandScore < minScaleOutScore {
			continue
		}

		if worker, ok := p.pickEmptyWarmWorker(now, workers, active, modelName); ok {
			return ControlAction{
				Type:        ControlActionWarm,
				Worker:      worker,
				Model:       modelName,
				Reason:      "empty_worker_predictive_scaleout",
				DemandScore: demandScore,
			}, true
		}

		if action, ok := p.planWarmEviction(now, workers, active, loadedCounts, modelName, demandScore); ok {
			return action, true
		}
	}
	return ControlAction{}, false
}

func (p Placement) planWarmEviction(now time.Time, workers []Worker, active map[string]int, loadedCounts map[string]int, targetModel string, demandScore int) (ControlAction, bool) {
	var best ControlAction
	found := false
	for _, worker := range workers {
		if !p.warmEligibleWorker(worker, now, active, targetModel) {
			continue
		}
		for _, running := range worker.RunningModels {
			if running.Model == targetModel || !strings.EqualFold(running.State, "ready") {
				continue
			}
			if !running.ProtectedUntil.IsZero() && running.ProtectedUntil.After(now) {
				continue
			}
			if !p.canUnloadModelForPlacement(running.Model, loadedCounts) {
				continue
			}
			keep := p.keepScore(now, worker.ID, running, active[worker.ID], loadedCounts)
			if demandScore <= keep+defaultSwitchCost {
				continue
			}
			action := ControlAction{
				Type:        ControlActionWarm,
				Worker:      worker,
				Model:       targetModel,
				Reason:      "evict_for_predictive_scaleout",
				VictimModel: running.Model,
				DemandScore: demandScore,
				KeepScore:   keep,
				SwitchCost:  defaultSwitchCost,
			}
			if !found || action.KeepScore < best.KeepScore || (action.KeepScore == best.KeepScore && (action.VictimModel < best.VictimModel || (action.VictimModel == best.VictimModel && action.Worker.ID < best.Worker.ID))) {
				best = action
				found = true
			}
		}
	}
	return best, found
}

func (p Placement) warmEligibleWorker(worker Worker, now time.Time, active map[string]int, modelName string) bool {
	if p.Workers != nil && !p.Workers.Healthy(worker.ID, now) {
		return false
	}
	if active[worker.ID] > 0 {
		return false
	}
	if !workerAllowsModel(p.Config, worker, modelName) || !artifactReady(worker, modelName) {
		return false
	}
	_, running := runningModelState(worker, modelName)
	return !running
}

func (p Placement) keepScore(now time.Time, workerID string, running protocol.RunningModel, active int, loadedCounts map[string]int) int {
	model := p.Config.Models[running.Model]
	score := model.Priority
	if loadedCounts[running.Model] <= model.MinLoaded {
		score += 120
	}
	if !running.ProtectedUntil.IsZero() && running.ProtectedUntil.After(now) {
		score += 80
	}
	if active > 0 {
		score += 50
	}
	if p.Access != nil {
		last := p.Access.WorkerModelLastAccess(workerID, running.Model)
		if !last.IsZero() && now.Sub(last) <= defaultPressureWindow {
			score += 30
		}
	}
	if model.MinLoaded == 0 {
		score -= 40
	}
	return score
}

func sortedWorkersByID(workers []Worker) []Worker {
	out := append([]Worker(nil), workers...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func occupiedModelCount(workers []Worker, now time.Time, reg *WorkerRegistry, model string) int {
	count := 0
	for _, worker := range workers {
		if reg != nil && !reg.Healthy(worker.ID, now) {
			continue
		}
		if _, running := runningModelState(worker, model); running {
			count++
		}
	}
	return count
}

func activeCountForReadyModel(workers []Worker, active map[string]int, model string) int {
	total := 0
	for _, worker := range workers {
		if runningModelReady(worker, model) {
			total += active[worker.ID]
		}
	}
	return total
}

func placementModelNamesByPriority(cfg config.GatewayConfig) []string {
	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		left := cfg.Models[names[i]]
		right := cfg.Models[names[j]]
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		return names[i] < names[j]
	})
	return names
}

func (p Placement) pickEvictionVictimForModel(now time.Time, workers []Worker, active map[string]int, loadedCounts map[string]int, targetModel string) (Worker, string, bool) {
	var bestWorker Worker
	var bestModel string
	var bestRank evictionRank
	found := false
	for _, worker := range workers {
		if active[worker.ID] > 0 {
			continue
		}
		if runningModelReady(worker, targetModel) {
			continue
		}
		if !workerAllowsModel(p.Config, worker, targetModel) || !artifactReady(worker, targetModel) {
			continue
		}
		for _, running := range worker.RunningModels {
			if !strings.EqualFold(running.State, "ready") || running.Model == targetModel {
				continue
			}
			if !running.ProtectedUntil.IsZero() && running.ProtectedUntil.After(now) {
				continue
			}
			if !p.canUnloadModelForPlacement(running.Model, loadedCounts) {
				continue
			}
			rank := p.evictionRank(worker.ID, running.Model)
			if !found || rank.less(bestRank) {
				bestWorker = worker
				bestModel = running.Model
				bestRank = rank
				found = true
			}
		}
	}
	return bestWorker, bestModel, found
}

type evictionRank struct {
	minLoadedZero bool
	priority      int
	lastAccess    time.Time
	workerID      string
}

func (r evictionRank) less(other evictionRank) bool {
	if r.minLoadedZero != other.minLoadedZero {
		return r.minLoadedZero
	}
	if r.priority != other.priority {
		return r.priority < other.priority
	}
	if !r.lastAccess.Equal(other.lastAccess) {
		return r.lastAccess.Before(other.lastAccess)
	}
	return r.workerID < other.workerID
}

func (p Placement) evictionRank(workerID string, modelName string) evictionRank {
	model := p.Config.Models[modelName]
	last := time.Time{}
	if p.Access != nil {
		last = p.Access.WorkerModelLastAccess(workerID, modelName)
	}
	return evictionRank{
		minLoadedZero: model.MinLoaded == 0,
		priority:      model.Priority,
		lastAccess:    last,
		workerID:      workerID,
	}
}

func (p Placement) canUnloadModelForPlacement(modelName string, loadedCounts map[string]int) bool {
	model, ok := p.Config.Models[modelName]
	if !ok {
		return true
	}
	return loadedCounts[modelName] > model.MinLoaded || model.MinLoaded == 0
}

func (p Placement) effectiveMaxLoaded(model config.Model, workers []Worker, modelName string, now time.Time) (int, bool) {
	if !model.MaxLoadedIsAuto() {
		return model.HardMaxLoaded(), false
	}
	count := 0
	for _, worker := range workers {
		if p.Workers != nil && !p.Workers.Healthy(worker.ID, now) {
			continue
		}
		if workerAllowsModel(p.Config, worker, modelName) && artifactReady(worker, modelName) {
			count++
		}
	}
	return count, true
}

type scoredPlacementWorker struct {
	worker         Worker
	score          int
	reason         string
	activeRequests int
	runningState   string
}

func placementCandidates(scored []scoredPlacementWorker) []PlacementCandidate {
	out := make([]PlacementCandidate, 0, len(scored))
	for _, candidate := range scored {
		out = append(out, PlacementCandidate{
			WorkerID:       candidate.worker.ID,
			Reason:         candidate.reason,
			Score:          candidate.score,
			ActiveRequests: candidate.activeRequests,
			RunningState:   candidate.runningState,
			RunningModels:  len(candidate.worker.RunningModels),
		})
	}
	return out
}
