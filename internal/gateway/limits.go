package gateway

import (
	"context"
	"errors"
	"sync"
)

var ErrQueueFull = errors.New("queue full")
var ErrQueueTimeout = errors.New("queue timeout")

type QueueLimiter struct {
	mu     sync.Mutex
	states map[string]*queueLimitState
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
	if maxActive <= 0 {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	l.mu.Lock()
	st := l.stateLocked(key)
	if st.active < maxActive {
		st.active++
		l.mu.Unlock()
		return l.release(key), nil
	}
	if len(st.waiters) >= maxQueue {
		l.mu.Unlock()
		return nil, ErrQueueFull
	}
	waiter := &queueWaiter{ready: make(chan struct{})}
	st.waiters = append(st.waiters, waiter)
	l.mu.Unlock()

	select {
	case <-waiter.ready:
		return l.release(key), nil
	case <-ctx.Done():
		if l.cancelWaiter(key, waiter) {
			return nil, ErrQueueTimeout
		}
		<-waiter.ready
		return l.release(key), nil
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
