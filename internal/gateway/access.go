package gateway

import (
	"sync"
	"time"
)

type AccessTracker struct {
	mu          sync.RWMutex
	models      map[string]time.Time
	workerModel map[string]map[string]time.Time
}

func NewAccessTracker() *AccessTracker {
	return &AccessTracker{
		models:      make(map[string]time.Time),
		workerModel: make(map[string]map[string]time.Time),
	}
}

func (a *AccessTracker) Record(model string, workerID string, now time.Time) {
	if a == nil || model == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	a.models[model] = now
	if workerID == "" {
		return
	}
	if a.workerModel[workerID] == nil {
		a.workerModel[workerID] = make(map[string]time.Time)
	}
	a.workerModel[workerID][model] = now
}

func (a *AccessTracker) ModelLastAccess(model string) time.Time {
	if a == nil {
		return time.Time{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.models[model]
}

func (a *AccessTracker) WorkerModelLastAccess(workerID string, model string) time.Time {
	if a == nil {
		return time.Time{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workerModel[workerID][model]
}
