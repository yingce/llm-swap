package gateway

import (
	"sort"
	"sync"
	"time"
)

const (
	defaultPressureWindow = 5 * time.Minute
	minScaleOutRequests   = 3
	minScaleOutScore      = 120
	defaultSwitchCost     = 80
)

type PressureTracker struct {
	mu       sync.Mutex
	window   time.Duration
	queues   []PressureQueueObservation
	requests []PressureRequestObservation
}

type PressureQueueObservation struct {
	Time             time.Time
	Model            string
	Result           string
	WaitMS           int64
	ReadyReplicas    int
	OccupiedReplicas int
	ActiveBefore     int
	QueuedBefore     int
}

type PressureRequestObservation struct {
	Time        time.Time
	Model       string
	WorkerID    string
	TotalTokens int
	DurationMS  int64
	StatusCode  int
}

type PressureSnapshot struct {
	Model            string
	RecentRequests   int
	RecentTokens     int
	WaitedRequests   int
	QueueErrors      int
	P95WaitMS        int64
	P95DurationMS    int64
	ReadyReplicas    int
	OccupiedReplicas int
	MaxActive        int
	LastAccess       time.Time
}

type DemandScoreInput struct {
	Priority         int
	ReadyReplicas    int
	OccupiedReplicas int
	Active           int
}

func NewPressureTracker(window time.Duration) *PressureTracker {
	if window <= 0 {
		window = defaultPressureWindow
	}
	return &PressureTracker{window: window}
}

func (p *PressureTracker) RecordQueue(obs PressureQueueObservation) {
	if p == nil || obs.Model == "" || obs.Result == "" {
		return
	}
	if obs.Time.IsZero() {
		obs.Time = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queues = append(p.queues, obs)
	p.pruneLocked(obs.Time)
}

func (p *PressureTracker) RecordRequest(obs PressureRequestObservation) {
	if p == nil || obs.Model == "" {
		return
	}
	if obs.Time.IsZero() {
		obs.Time = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, obs)
	p.pruneLocked(obs.Time)
}

func (p *PressureTracker) Model(model string, now time.Time) PressureSnapshot {
	if p == nil || model == "" {
		return PressureSnapshot{Model: model}
	}
	if now.IsZero() {
		now = time.Now()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now)

	snapshot := PressureSnapshot{Model: model}
	var waits []int64
	var durations []int64
	for _, obs := range p.requests {
		if obs.Model != model {
			continue
		}
		snapshot.RecentRequests++
		snapshot.RecentTokens += maxInt(obs.TotalTokens, 0)
		if obs.DurationMS > 0 {
			durations = append(durations, obs.DurationMS)
		}
		if obs.Time.After(snapshot.LastAccess) {
			snapshot.LastAccess = obs.Time
		}
	}
	for _, obs := range p.queues {
		if obs.Model != model {
			continue
		}
		switch obs.Result {
		case QueueResultAdmittedAfterWait:
			snapshot.WaitedRequests++
		case QueueResultFull, QueueResultTimeout:
			snapshot.QueueErrors++
		}
		if obs.WaitMS > 0 {
			waits = append(waits, obs.WaitMS)
		}
		if obs.ReadyReplicas > snapshot.ReadyReplicas {
			snapshot.ReadyReplicas = obs.ReadyReplicas
		}
		if obs.OccupiedReplicas > snapshot.OccupiedReplicas {
			snapshot.OccupiedReplicas = obs.OccupiedReplicas
		}
		if obs.ActiveBefore > snapshot.MaxActive {
			snapshot.MaxActive = obs.ActiveBefore
		}
		if obs.Time.After(snapshot.LastAccess) {
			snapshot.LastAccess = obs.Time
		}
	}
	snapshot.P95WaitMS = percentile95(waits)
	snapshot.P95DurationMS = percentile95(durations)
	return snapshot
}

func DemandScore(snapshot PressureSnapshot, input DemandScoreInput) int {
	demandEvents := snapshot.RecentRequests + snapshot.WaitedRequests + snapshot.QueueErrors
	if demandEvents < minScaleOutRequests {
		return 0
	}

	score := input.Priority
	score += minInt(snapshot.RecentRequests*10, 60)
	score += minInt(int(snapshot.P95DurationMS/100), 50)
	score += minInt(snapshot.WaitedRequests*25, 75)
	score += minInt(snapshot.QueueErrors*40, 80)
	score += minInt(int(snapshot.P95WaitMS/100), 40)
	if input.ReadyReplicas > 0 && input.Active >= input.ReadyReplicas {
		score += 35
	}
	if input.ReadyReplicas == 0 && snapshot.RecentRequests > 0 {
		score += 20
	}
	if starting := input.OccupiedReplicas - input.ReadyReplicas; starting > 0 {
		score -= starting * 60
	}
	if score < 0 {
		return 0
	}
	return score
}

func (p *PressureTracker) pruneLocked(now time.Time) {
	cutoff := now.Add(-p.window)
	p.queues = pruneQueueObservations(p.queues, cutoff)
	p.requests = pruneRequestObservations(p.requests, cutoff)
}

func pruneQueueObservations(observations []PressureQueueObservation, cutoff time.Time) []PressureQueueObservation {
	kept := observations[:0]
	for _, obs := range observations {
		if !obs.Time.Before(cutoff) {
			kept = append(kept, obs)
		}
	}
	return kept
}

func pruneRequestObservations(observations []PressureRequestObservation, cutoff time.Time) []PressureRequestObservation {
	kept := observations[:0]
	for _, obs := range observations {
		if !obs.Time.Before(cutoff) {
			kept = append(kept, obs)
		}
	}
	return kept
}

func percentile95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := (95*len(values) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(values) {
		index = len(values)
	}
	return values[index-1]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
