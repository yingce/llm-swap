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

func TestQueueLimiterAcquireWithStatsReportsWaitOutcomes(t *testing.T) {
	limiter := NewQueueLimiter()
	releaseFirst, stats, err := limiter.AcquireWithStats(context.Background(), "model:qwen", 1, 1)
	if err != nil {
		t.Fatalf("first AcquireWithStats returned error: %v", err)
	}
	if stats.Result != QueueResultAdmitted || stats.Waited || stats.ActiveBefore != 0 || stats.QueuedBefore != 0 {
		t.Fatalf("first stats = %+v, want immediate admitted", stats)
	}

	acquired := make(chan QueueAcquireStats, 1)
	go func() {
		release, stats, err := limiter.AcquireWithStats(context.Background(), "model:qwen", 1, 1)
		if err != nil {
			t.Errorf("queued AcquireWithStats returned error: %v", err)
			return
		}
		release()
		acquired <- stats
	}()

	time.Sleep(20 * time.Millisecond)
	releaseFirst()

	select {
	case stats := <-acquired:
		if stats.Result != QueueResultAdmittedAfterWait || !stats.Waited || stats.WaitMS <= 0 {
			t.Fatalf("queued stats = %+v, want admitted after wait", stats)
		}
		if stats.ActiveBefore != 1 || stats.QueuedBefore != 0 || stats.MaxConcurrency != 1 || stats.MaxQueue != 1 {
			t.Fatalf("queued capacity stats = %+v", stats)
		}
	case <-time.After(time.Second):
		t.Fatal("queued acquire did not finish")
	}
}

func TestQueueLimiterAcquireWithStatsReportsFullAndTimeout(t *testing.T) {
	limiter := NewQueueLimiter()
	release, _, err := limiter.AcquireWithStats(context.Background(), "model:qwen", 1, 1)
	if err != nil {
		t.Fatalf("first AcquireWithStats returned error: %v", err)
	}
	defer release()

	waiting := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _, err := limiter.AcquireWithStats(ctx, "model:qwen", 1, 1)
		waiting <- err
	}()
	time.Sleep(20 * time.Millisecond)

	_, fullStats, err := limiter.AcquireWithStats(context.Background(), "model:qwen", 1, 1)
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full error = %v, want ErrQueueFull", err)
	}
	if fullStats.Result != QueueResultFull || fullStats.ActiveBefore != 1 || fullStats.QueuedBefore != 1 {
		t.Fatalf("full stats = %+v", fullStats)
	}

	cancel()
	if err := <-waiting; !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("waiting error = %v, want ErrQueueTimeout after cancel", err)
	}
}
