package gateway

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrQueueFull = errors.New("queue full")
var ErrQueueTimeout = errors.New("queue timeout")

const (
	QueueResultAdmitted          = "admitted"
	QueueResultAdmittedAfterWait = "admitted_after_wait"
	QueueResultFull              = "queue_full"
	QueueResultTimeout           = "queue_timeout"
)

type QueueLimiter struct {
	mu     sync.Mutex
	states map[string]*queueLimitState
}

type QueueAcquireStats struct {
	Result         string
	Waited         bool
	WaitMS         int64
	ActiveBefore   int
	QueuedBefore   int
	MaxConcurrency int
	MaxQueue       int
}

type queueLimitState struct {
	active  int
	waiters []*queueWaiter
}

type queueWaiter struct {
	ready chan struct{}
}

func NewQueueLimiter() *QueueLimiter {
	return &QueueLimiter{states: make(map[string]*queueLimitState)}
}

func (l *QueueLimiter) Acquire(ctx context.Context, key string, maxActive, maxQueue int) (func(), error) {
	release, _, err := l.AcquireWithStats(ctx, key, maxActive, maxQueue)
	return release, err
}

func (l *QueueLimiter) AcquireWithStats(ctx context.Context, key string, maxActive, maxQueue int) (func(), QueueAcquireStats, error) {
	stats := QueueAcquireStats{
		MaxConcurrency: maxActive,
		MaxQueue:       maxQueue,
	}
	if maxActive <= 0 {
		stats.Result = QueueResultAdmitted
		return func() {}, stats, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	l.mu.Lock()
	st := l.stateLocked(key)
	stats.ActiveBefore = st.active
	stats.QueuedBefore = len(st.waiters)
	if st.active < maxActive {
		st.active++
		l.mu.Unlock()
		stats.Result = QueueResultAdmitted
		return l.release(key), stats, nil
	}
	if len(st.waiters) >= maxQueue {
		l.mu.Unlock()
		stats.Result = QueueResultFull
		return nil, stats, ErrQueueFull
	}
	waiter := &queueWaiter{ready: make(chan struct{})}
	st.waiters = append(st.waiters, waiter)
	l.mu.Unlock()

	start := time.Now()
	select {
	case <-waiter.ready:
		stats.Result = QueueResultAdmittedAfterWait
		stats.Waited = true
		stats.WaitMS = time.Since(start).Milliseconds()
		return l.release(key), stats, nil
	case <-ctx.Done():
		stats.Waited = true
		stats.WaitMS = time.Since(start).Milliseconds()
		if l.cancelWaiter(key, waiter) {
			stats.Result = QueueResultTimeout
			return nil, stats, ErrQueueTimeout
		}
		<-waiter.ready
		stats.Result = QueueResultAdmittedAfterWait
		return l.release(key), stats, nil
	}
}

func (l *QueueLimiter) Active(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.states[key]; st != nil {
		return st.active
	}
	return 0
}

func (l *QueueLimiter) Queued(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.states[key]; st != nil {
		return len(st.waiters)
	}
	return 0
}

func (l *QueueLimiter) stateLocked(key string) *queueLimitState {
	st := l.states[key]
	if st == nil {
		st = &queueLimitState{}
		l.states[key] = st
	}
	return st
}

func (l *QueueLimiter) release(key string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()

			st := l.states[key]
			if st == nil {
				return
			}
			if len(st.waiters) > 0 {
				waiter := st.waiters[0]
				copy(st.waiters, st.waiters[1:])
				st.waiters[len(st.waiters)-1] = nil
				st.waiters = st.waiters[:len(st.waiters)-1]
				close(waiter.ready)
				return
			}
			if st.active > 0 {
				st.active--
			}
			if st.active == 0 && len(st.waiters) == 0 {
				delete(l.states, key)
			}
		})
	}
}

func (l *QueueLimiter) cancelWaiter(key string, waiter *queueWaiter) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	st := l.states[key]
	if st == nil {
		return false
	}
	for i, queued := range st.waiters {
		if queued != waiter {
			continue
		}
		copy(st.waiters[i:], st.waiters[i+1:])
		st.waiters[len(st.waiters)-1] = nil
		st.waiters = st.waiters[:len(st.waiters)-1]
		if st.active == 0 && len(st.waiters) == 0 {
			delete(l.states, key)
		}
		return true
	}
	return false
}
