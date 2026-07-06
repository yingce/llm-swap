package gateway

import (
	"sort"
	"strings"
	"sync"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

type WorkerState string

const (
	WorkerActive   WorkerState = "active"
	WorkerDraining WorkerState = "draining"
)

const workerScrapeFailureBackoffThreshold = 3
const workerScrapeFailureBackoff = 30 * time.Second
const workerOfflineRetention = 10 * time.Minute
const restartGlobalScope = "*"

type Worker struct {
	ID                 string
	Tags               []string
	LlamaSwapURL       string
	RunningModels      []protocol.RunningModel
	GPUDevices         []protocol.GPUDevice
	Artifacts          map[string]string
	Capacity           config.WorkerDefaults
	NeedsRestart       bool
	LastError          string
	AgentBuild         protocol.BuildInfo
	LastHeartbeat      time.Time
	State              WorkerState
	ScrapeFailures     int
	ScrapeBackoffUntil time.Time
}

type WorkerRegistry struct {
	mu             sync.RWMutex
	staleAfter     time.Duration
	workers        map[string]*Worker
	workerOrder    []string
	active         map[string]int
	manualDrains   map[string]bool
	restartHolders map[string]string
}

func NewWorkerRegistry(staleAfter time.Duration) *WorkerRegistry {
	return &WorkerRegistry{
		staleAfter:     staleAfter,
		workers:        make(map[string]*Worker),
		active:         make(map[string]int),
		manualDrains:   make(map[string]bool),
		restartHolders: make(map[string]string),
	}
}

func (r *WorkerRegistry) UpsertHeartbeat(hb protocol.HeartbeatRequest, now time.Time) protocol.HeartbeatResponse {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.releaseExpiredRestartHolderLocked(now)
	prev := r.workers[hb.AgentID]
	if prev == nil {
		r.workerOrder = append(r.workerOrder, hb.AgentID)
	}
	w := &Worker{
		ID:            hb.AgentID,
		Tags:          append([]string(nil), hb.Tags...),
		LlamaSwapURL:  hb.LlamaSwapURL,
		RunningModels: append([]protocol.RunningModel(nil), hb.RunningModels...),
		GPUDevices:    append([]protocol.GPUDevice(nil), hb.GPUDevices...),
		Artifacts:     copyStringMap(hb.Artifacts),
		Capacity:      hb.Capacity,
		NeedsRestart:  hb.NeedsRestart,
		LastError:     hb.LastError,
		AgentBuild:    hb.AgentBuild,
		LastHeartbeat: now,
		State:         WorkerActive,
	}
	if prev != nil {
		w.ScrapeFailures = prev.ScrapeFailures
		w.ScrapeBackoffUntil = prev.ScrapeBackoffUntil
	}
	r.workers[hb.AgentID] = w

	if !hb.NeedsRestart {
		r.releaseRestartHolderForWorkerLocked(hb.AgentID)
	}
	restartDraining, restartAllowed := r.restartDecisionLocked(hb.AgentID, hb.NeedsRestart, hb.RestartModels)
	if restartDraining || r.manualDrains[hb.AgentID] {
		w.State = WorkerDraining
	}
	return protocol.HeartbeatResponse{WorkerState: string(w.State), RestartAllowed: restartAllowed}
}

func (r *WorkerRegistry) restartDecisionLocked(workerID string, needsRestart bool, restartModels []string) (bool, bool) {
	if !needsRestart {
		return false, false
	}
	scopes := restartScopes(restartModels)
	if r.restartBlockedByOtherLocked(workerID, scopes) {
		return false, false
	}
	for _, scope := range scopes {
		if r.restartHolders[scope] == "" {
			r.restartHolders[scope] = workerID
		}
	}
	return true, r.active[workerID] == 0
}

func (r *WorkerRegistry) restartBlockedByOtherLocked(workerID string, scopes []string) bool {
	if holder := r.restartHolders[restartGlobalScope]; holder != "" && holder != workerID {
		return true
	}
	if len(scopes) == 1 && scopes[0] == restartGlobalScope {
		for _, holder := range r.restartHolders {
			if holder != "" && holder != workerID {
				return true
			}
		}
		return false
	}
	for _, scope := range scopes {
		if holder := r.restartHolders[scope]; holder != "" && holder != workerID {
			return true
		}
	}
	return false
}

func restartScopes(models []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	if len(out) == 0 {
		return []string{restartGlobalScope}
	}
	sort.Strings(out)
	return out
}

func (r *WorkerRegistry) releaseExpiredRestartHolderLocked(now time.Time) {
	for scope, workerID := range r.restartHolders {
		holder := r.workers[workerID]
		if holder == nil || now.Sub(holder.LastHeartbeat) > workerOfflineRetention {
			delete(r.restartHolders, scope)
		}
	}
}

func (r *WorkerRegistry) releaseRestartHolderForWorkerLocked(workerID string) {
	for scope, holder := range r.restartHolders {
		if holder == workerID {
			delete(r.restartHolders, scope)
		}
	}
}

func (r *WorkerRegistry) Drain(workerID string) (Worker, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[workerID]
	if !ok {
		return Worker{}, false
	}
	r.manualDrains[workerID] = true
	w.State = WorkerDraining
	return cloneWorker(*w), true
}

func (r *WorkerRegistry) Undrain(workerID string) (Worker, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[workerID]
	if !ok {
		return Worker{}, false
	}
	delete(r.manualDrains, workerID)
	if !w.NeedsRestart {
		w.State = WorkerActive
	}
	return cloneWorker(*w), true
}

func (r *WorkerRegistry) Healthy(id string, now time.Time) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	w, ok := r.workers[id]
	if !ok {
		return false
	}
	if now.Sub(w.LastHeartbeat) >= r.staleAfter {
		return false
	}
	if now.Before(w.ScrapeBackoffUntil) {
		return false
	}
	return w.State == WorkerActive
}

func (r *WorkerRegistry) Snapshot(now time.Time) []Worker {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.pruneOfflineLocked(now)
	out := make([]Worker, 0, len(r.workers))
	for _, workerID := range r.workerOrder {
		w := r.workers[workerID]
		if w == nil {
			continue
		}
		out = append(out, cloneWorker(*w))
	}
	return out
}

func (r *WorkerRegistry) pruneOfflineLocked(now time.Time) {
	pruned := false
	for workerID, worker := range r.workers {
		if now.Sub(worker.LastHeartbeat) <= workerOfflineRetention {
			continue
		}
		delete(r.workers, workerID)
		delete(r.active, workerID)
		delete(r.manualDrains, workerID)
		r.releaseRestartHolderForWorkerLocked(workerID)
		pruned = true
	}
	if !pruned {
		return
	}
	kept := r.workerOrder[:0]
	for _, workerID := range r.workerOrder {
		if _, ok := r.workers[workerID]; ok {
			kept = append(kept, workerID)
		}
	}
	r.workerOrder = kept
}

func (r *WorkerRegistry) ActiveSnapshot() map[string]int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make(map[string]int, len(r.active))
	for workerID, active := range r.active {
		out[workerID] = active
	}
	return out
}

func (r *WorkerRegistry) Acquire(workerID string, now time.Time) (func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[workerID]
	if !ok {
		return nil, false
	}
	if now.Sub(w.LastHeartbeat) >= r.staleAfter {
		return nil, false
	}
	if now.Before(w.ScrapeBackoffUntil) {
		return nil, false
	}
	if w.State != WorkerActive {
		return nil, false
	}

	r.active[workerID]++

	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()

			if r.active[workerID] <= 1 {
				delete(r.active, workerID)
				return
			}
			r.active[workerID]--
		})
	}

	return release, true
}

func (r *WorkerRegistry) RecordScrapeSuccess(workerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker := r.workers[workerID]
	if worker == nil {
		return
	}
	worker.ScrapeFailures = 0
	worker.ScrapeBackoffUntil = time.Time{}
}

func (r *WorkerRegistry) RecordScrapeFailure(workerID string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker := r.workers[workerID]
	if worker == nil {
		return
	}
	worker.ScrapeFailures++
	if worker.ScrapeFailures >= workerScrapeFailureBackoffThreshold {
		worker.ScrapeBackoffUntil = now.Add(workerScrapeFailureBackoff)
	}
}

func (r *WorkerRegistry) RecordReverseFailure(workerID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	worker := r.workers[workerID]
	if worker == nil {
		return false
	}
	alreadyBackedOff := now.Before(worker.ScrapeBackoffUntil)
	worker.ScrapeFailures++
	if worker.ScrapeFailures < workerScrapeFailureBackoffThreshold {
		return false
	}
	worker.ScrapeBackoffUntil = now.Add(workerScrapeFailureBackoff)
	return !alreadyBackedOff
}

func cloneWorker(w Worker) Worker {
	w.Tags = append([]string(nil), w.Tags...)
	w.RunningModels = append([]protocol.RunningModel(nil), w.RunningModels...)
	w.GPUDevices = append([]protocol.GPUDevice(nil), w.GPUDevices...)
	w.Artifacts = copyStringMap(w.Artifacts)
	return w
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
