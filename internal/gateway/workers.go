package gateway

import (
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

type Worker struct {
	ID                 string
	Tags               []string
	LlamaSwapURL       string
	RunningModels      []protocol.RunningModel
	Artifacts          map[string]string
	Capacity           config.WorkerDefaults
	NeedsRestart       bool
	LastError          string
	LastHeartbeat      time.Time
	State              WorkerState
	ScrapeFailures     int
	ScrapeBackoffUntil time.Time
}

type WorkerRegistry struct {
	mu         sync.RWMutex
	staleAfter time.Duration
	workers    map[string]*Worker
	active     map[string]int
}

func NewWorkerRegistry(staleAfter time.Duration) *WorkerRegistry {
	return &WorkerRegistry{
		staleAfter: staleAfter,
		workers:    make(map[string]*Worker),
		active:     make(map[string]int),
	}
}

func (r *WorkerRegistry) UpsertHeartbeat(hb protocol.HeartbeatRequest, now time.Time) protocol.HeartbeatResponse {
	r.mu.Lock()
	defer r.mu.Unlock()

	prev := r.workers[hb.AgentID]
	w := &Worker{
		ID:            hb.AgentID,
		Tags:          append([]string(nil), hb.Tags...),
		LlamaSwapURL:  hb.LlamaSwapURL,
		RunningModels: append([]protocol.RunningModel(nil), hb.RunningModels...),
		Artifacts:     copyStringMap(hb.Artifacts),
		Capacity:      hb.Capacity,
		NeedsRestart:  hb.NeedsRestart,
		LastError:     hb.LastError,
		LastHeartbeat: now,
		State:         WorkerActive,
	}
	if prev != nil {
		w.ScrapeFailures = prev.ScrapeFailures
		w.ScrapeBackoffUntil = prev.ScrapeBackoffUntil
	}
	if hb.NeedsRestart {
		w.State = WorkerDraining
	}
	r.workers[hb.AgentID] = w

	restartAllowed := hb.NeedsRestart && r.active[hb.AgentID] == 0
	return protocol.HeartbeatResponse{WorkerState: string(w.State), RestartAllowed: restartAllowed}
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
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Worker, 0, len(r.workers))
	for _, w := range r.workers {
		cp := *w
		cp.Tags = append([]string(nil), w.Tags...)
		cp.RunningModels = append([]protocol.RunningModel(nil), w.RunningModels...)
		cp.Artifacts = copyStringMap(w.Artifacts)
		out = append(out, cp)
		_ = now
	}
	return out
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
