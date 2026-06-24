package gateway

import (
	"sync"
	"time"
)

const defaultReplicaCooldownTTL = 30 * time.Second

type ReplicaCooldowns struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[replicaCooldownKey]ReplicaCooldown
}

type replicaCooldownKey struct {
	WorkerID string
	Model    string
}

type ReplicaCooldown struct {
	WorkerID         string    `json:"worker_id"`
	Model            string    `json:"model"`
	Reason           string    `json:"reason"`
	FirstFailure     time.Time `json:"first_failure"`
	LastFailure      time.Time `json:"last_failure"`
	FailureCount     int       `json:"failure_count"`
	CooldownUntil    time.Time `json:"cooldown_until"`
	RemainingSeconds int64     `json:"remaining_seconds"`
}

type ReplicaCooldownSnapshot map[string]map[string]ReplicaCooldown

func NewReplicaCooldowns(ttl time.Duration) *ReplicaCooldowns {
	if ttl <= 0 {
		ttl = defaultReplicaCooldownTTL
	}
	return &ReplicaCooldowns{ttl: ttl, entries: make(map[replicaCooldownKey]ReplicaCooldown)}
}

func (c *ReplicaCooldowns) Mark(workerID, model, reason string, now time.Time) (ReplicaCooldown, bool) {
	if c == nil || workerID == "" || model == "" {
		return ReplicaCooldown{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	key := replicaCooldownKey{WorkerID: workerID, Model: model}
	entry := c.entries[key]
	if entry.FirstFailure.IsZero() {
		entry.FirstFailure = now
	}
	entry.WorkerID = workerID
	entry.Model = model
	entry.Reason = reason
	entry.LastFailure = now
	entry.FailureCount++
	entry.CooldownUntil = now.Add(c.ttl)
	entry.RemainingSeconds = int64(c.ttl.Seconds())
	c.entries[key] = entry
	return entry, true
}

func (c *ReplicaCooldowns) Clear(workerID, model string, now time.Time) (ReplicaCooldown, bool) {
	if c == nil || workerID == "" || model == "" {
		return ReplicaCooldown{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)

	key := replicaCooldownKey{WorkerID: workerID, Model: model}
	entry, ok := c.entries[key]
	if !ok {
		return ReplicaCooldown{}, false
	}
	delete(c.entries, key)
	entry.RemainingSeconds = 0
	return entry, true
}

func (c *ReplicaCooldowns) Active(workerID, model string, now time.Time) bool {
	_, ok := c.Get(workerID, model, now)
	return ok
}

func (c *ReplicaCooldowns) Get(workerID, model string, now time.Time) (ReplicaCooldown, bool) {
	if c == nil || workerID == "" || model == "" {
		return ReplicaCooldown{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)

	entry, ok := c.entries[replicaCooldownKey{WorkerID: workerID, Model: model}]
	if !ok {
		return ReplicaCooldown{}, false
	}
	entry.RemainingSeconds = remainingSeconds(entry.CooldownUntil, now)
	return entry, true
}

func (c *ReplicaCooldowns) Snapshot(now time.Time) ReplicaCooldownSnapshot {
	out := ReplicaCooldownSnapshot{}
	if c == nil {
		return out
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)

	for _, entry := range c.entries {
		entry.RemainingSeconds = remainingSeconds(entry.CooldownUntil, now)
		if out[entry.WorkerID] == nil {
			out[entry.WorkerID] = map[string]ReplicaCooldown{}
		}
		out[entry.WorkerID][entry.Model] = entry
	}
	return out
}

func (s ReplicaCooldownSnapshot) Active(workerID, model string, now time.Time) bool {
	_, ok := s.Get(workerID, model, now)
	return ok
}

func (s ReplicaCooldownSnapshot) Get(workerID, model string, now time.Time) (ReplicaCooldown, bool) {
	if s == nil {
		return ReplicaCooldown{}, false
	}
	byModel := s[workerID]
	if byModel == nil {
		return ReplicaCooldown{}, false
	}
	entry, ok := byModel[model]
	if !ok || !entry.CooldownUntil.After(now) {
		return ReplicaCooldown{}, false
	}
	entry.RemainingSeconds = remainingSeconds(entry.CooldownUntil, now)
	return entry, true
}

func (c *ReplicaCooldowns) pruneLocked(now time.Time) {
	for key, entry := range c.entries {
		if !entry.CooldownUntil.After(now) {
			delete(c.entries, key)
		}
	}
}

func remainingSeconds(until time.Time, now time.Time) int64 {
	if !until.After(now) {
		return 0
	}
	remaining := until.Sub(now)
	seconds := int64(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	return seconds
}
