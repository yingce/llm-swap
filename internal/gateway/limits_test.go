package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQueueLimiterAllowsImmediateAcquireAndRelease(t *testing.T) {
	limiter := NewQueueLimiter()
	release, err := limiter.Acquire(context.Background(), "model:qwen", 1, 0)
	if err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	if got := limiter.Active("model:qwen"); got != 1 {
		t.Fatalf("active = %d, want 1", got)
	}

	release()
	release()

	if got := limiter.Active("model:qwen"); got != 0 {
		t.Fatalf("active after release = %d, want 0", got)
	}
}

func TestQueueLimiterRejectsWhenQueueFull(t *testing.T) {
	limiter := NewQueueLimiter()
	release, err := limiter.Acquire(context.Background(), "model:qwen", 1, 0)
	if err != nil {
		t.Fatalf("first Acquire returned error: %v", err)
	}
	defer release()

	_, err = limiter.Acquire(context.Background(), "model:qwen", 1, 0)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second Acquire error = %v, want ErrQueueFull", err)
	}
}

func TestQueueLimiterTimesOutQueuedAcquire(t *testing.T) {
	limiter := NewQueueLimiter()
	release, err := limiter.Acquire(context.Background(), "model:qwen", 1, 1)
	if err != nil {
		t.Fatalf("first Acquire returned error: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = limiter.Acquire(ctx, "model:qwen", 1, 1)
	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("queued Acquire error = %v, want ErrQueueTimeout", err)
	}
	if got := limiter.Queued("model:qwen"); got != 0 {
		t.Fatalf("queued after timeout = %d, want 0", got)
	}
}

func TestQueueLimiterQueuedAcquireRunsAfterRelease(t *testing.T) {
	limiter := NewQueueLimiter()
	releaseFirst, err := limiter.Acquire(context.Background(), "model:qwen", 1, 1)
	if err != nil {
		t.Fatalf("first Acquire returned error: %v", err)
	}

	acquired := make(chan func(), 1)
	go func() {
		release, err := limiter.Acquire(context.Background(), "model:qwen", 1, 1)
		if err != nil {
			t.Errorf("queued Acquire returned error: %v", err)
			return
		}
		acquired <- release
	}()

	deadline := time.After(50 * time.Millisecond)
	select {
	case <-acquired:
		t.Fatal("queued acquire completed before release")
	case <-deadline:
	}

	releaseFirst()

	select {
	case releaseQueued := <-acquired:
		releaseQueued()
	case <-time.After(time.Second):
		t.Fatal("queued acquire did not complete after release")
	}
}
