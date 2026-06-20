package gateway

import "sync"

type Accounting struct {
	mu       sync.RWMutex
	requests map[string]RequestAccounting
	models   map[string]int
	tags     map[string]int
	workers  map[string]int
}

type RequestAccounting struct {
	RequestID string
	Model     string
	Tag       string
	WorkerID  string
}

func NewAccounting() *Accounting {
	return &Accounting{
		requests: make(map[string]RequestAccounting),
		models:   make(map[string]int),
		tags:     make(map[string]int),
		workers:  make(map[string]int),
	}
}

func (a *Accounting) Acquire(requestID, model, tag, workerID string) func() {
	record := RequestAccounting{
		RequestID: requestID,
		Model:     model,
		Tag:       tag,
		WorkerID:  workerID,
	}

	a.mu.Lock()
	if existing, ok := a.requests[requestID]; ok {
		a.decrementLocked(existing)
	}
	a.requests[requestID] = record
	a.models[model]++
	a.tags[tag]++
	a.workers[workerID]++
	a.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.release(record)
		})
	}
}

func (a *Accounting) ModelActive(model string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.models[model]
}

func (a *Accounting) TagActive(tag string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tags[tag]
}

func (a *Accounting) WorkerActive(workerID string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workers[workerID]
}

func (a *Accounting) RequestSnapshot() map[string]RequestAccounting {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := make(map[string]RequestAccounting, len(a.requests))
	for requestID, record := range a.requests {
		out[requestID] = record
	}
	return out
}

func (a *Accounting) release(record RequestAccounting) {
	a.mu.Lock()
	defer a.mu.Unlock()

	current, ok := a.requests[record.RequestID]
	if !ok || current != record {
		return
	}
	delete(a.requests, record.RequestID)
	a.decrementLocked(record)
}

func (a *Accounting) decrementLocked(record RequestAccounting) {
	decrementCount(a.models, record.Model)
	decrementCount(a.tags, record.Tag)
	decrementCount(a.workers, record.WorkerID)
}

func decrementCount(counts map[string]int, key string) {
	if counts[key] <= 1 {
		delete(counts, key)
		return
	}
	counts[key]--
}
