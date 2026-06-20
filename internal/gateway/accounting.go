package gateway

import (
	"fmt"
	"sync"
)

type Accounting struct {
	mu          sync.RWMutex
	nextRequest uint64
	requests    map[uint64]RequestAccounting
	models      map[string]int
	tags        map[string]int
	workers     map[string]int
}

type RequestAccounting struct {
	RequestID string
	Model     string
	Tag       string
	WorkerID  string
}

func NewAccounting() *Accounting {
	return &Accounting{
		requests: make(map[uint64]RequestAccounting),
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
	a.nextRequest++
	ownershipKey := a.nextRequest
	a.requests[ownershipKey] = record
	a.models[model]++
	a.tags[tag]++
	a.workers[workerID]++
	a.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.release(ownershipKey)
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

	requestIDCounts := make(map[string]int, len(a.requests))
	for _, record := range a.requests {
		requestIDCounts[record.RequestID]++
	}

	out := make(map[string]RequestAccounting, len(a.requests))
	for ownershipKey, record := range a.requests {
		snapshotKey := record.RequestID
		if snapshotKey == "" || requestIDCounts[snapshotKey] > 1 {
			snapshotKey = syntheticRequestKey(ownershipKey)
		}
		baseSnapshotKey := snapshotKey
		for suffix := 2; ; suffix++ {
			if _, exists := out[snapshotKey]; !exists {
				break
			}
			snapshotKey = fmt.Sprintf("%s:%d", baseSnapshotKey, suffix)
		}
		out[snapshotKey] = record
	}
	return out
}

func (a *Accounting) release(ownershipKey uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	record, ok := a.requests[ownershipKey]
	if !ok {
		return
	}
	delete(a.requests, ownershipKey)
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

func syntheticRequestKey(ownershipKey uint64) string {
	return fmt.Sprintf("__acquire:%d", ownershipKey)
}
