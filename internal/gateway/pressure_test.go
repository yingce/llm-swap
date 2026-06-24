package gateway

import (
	"testing"
	"time"
)

func TestPressureTrackerSnapshotExpiresOldObservationsAndComputesP95(t *testing.T) {
	now := time.Unix(1000, 0)
	tracker := NewPressureTracker(5 * time.Minute)
	tracker.RecordQueue(PressureQueueObservation{
		Time:             now.Add(-10 * time.Minute),
		Model:            "qwen",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           9000,
		ReadyReplicas:    1,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})
	tracker.RecordQueue(PressureQueueObservation{
		Time:             now.Add(-20 * time.Second),
		Model:            "qwen",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           200,
		ReadyReplicas:    1,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})
	tracker.RecordQueue(PressureQueueObservation{
		Time:             now.Add(-10 * time.Second),
		Model:            "qwen",
		Result:           QueueResultTimeout,
		WaitMS:           500,
		ReadyReplicas:    1,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})
	tracker.RecordRequest(PressureRequestObservation{
		Time:        now.Add(-5 * time.Second),
		Model:       "qwen",
		TotalTokens: 800,
	})

	snapshot := tracker.Model("qwen", now)
	if snapshot.RecentRequests != 1 {
		t.Fatalf("RecentRequests = %d, want 1", snapshot.RecentRequests)
	}
	if snapshot.WaitedRequests != 1 {
		t.Fatalf("WaitedRequests = %d, want 1", snapshot.WaitedRequests)
	}
	if snapshot.QueueErrors != 1 {
		t.Fatalf("QueueErrors = %d, want 1", snapshot.QueueErrors)
	}
	if snapshot.P95WaitMS != 500 {
		t.Fatalf("P95WaitMS = %d, want 500", snapshot.P95WaitMS)
	}
	if snapshot.RecentTokens != 800 {
		t.Fatalf("RecentTokens = %d, want 800", snapshot.RecentTokens)
	}
}

func TestPressureDemandScoreRequiresSustainedRequests(t *testing.T) {
	now := time.Unix(1000, 0)
	tracker := NewPressureTracker(5 * time.Minute)
	for i := 0; i < minScaleOutRequests-1; i++ {
		tracker.RecordRequest(PressureRequestObservation{
			Time:  now.Add(time.Duration(i) * time.Second),
			Model: "qwen",
		})
	}
	snapshot := tracker.Model("qwen", now.Add(time.Minute))
	if score := DemandScore(snapshot, DemandScoreInput{Priority: 100, ReadyReplicas: 1, OccupiedReplicas: 1, Active: 1}); score != 0 {
		t.Fatalf("DemandScore = %d, want 0 for burst below request floor", score)
	}

	tracker.RecordRequest(PressureRequestObservation{Time: now.Add(3 * time.Second), Model: "qwen"})
	snapshot = tracker.Model("qwen", now.Add(time.Minute))
	score := DemandScore(snapshot, DemandScoreInput{Priority: 100, ReadyReplicas: 1, OccupiedReplicas: 1, Active: 1})
	if score == 0 {
		t.Fatalf("DemandScore = 0, want positive score after sustained requests")
	}
}

func TestPressureDemandScoreIgnoresTokenVolume(t *testing.T) {
	now := time.Unix(1000, 0)
	tracker := NewPressureTracker(5 * time.Minute)
	for i := 0; i < minScaleOutRequests; i++ {
		tracker.RecordRequest(PressureRequestObservation{
			Time:        now.Add(time.Duration(i) * time.Second),
			Model:       "qwen",
			TotalTokens: 100000,
		})
	}

	snapshot := tracker.Model("qwen", now.Add(time.Minute))
	score := DemandScore(snapshot, DemandScoreInput{Priority: 60, ReadyReplicas: 1, OccupiedReplicas: 1})
	if score >= minScaleOutScore {
		t.Fatalf("DemandScore = %d, want below scale-out threshold without wait or duration pressure", score)
	}
}

func TestPressureDemandScoreUsesRequestDuration(t *testing.T) {
	now := time.Unix(1000, 0)
	tracker := NewPressureTracker(5 * time.Minute)
	for i := 0; i < minScaleOutRequests; i++ {
		tracker.RecordRequest(PressureRequestObservation{
			Time:       now.Add(time.Duration(i) * time.Second),
			Model:      "qwen",
			DurationMS: 5000,
		})
	}

	snapshot := tracker.Model("qwen", now.Add(time.Minute))
	if snapshot.P95DurationMS != 5000 {
		t.Fatalf("P95DurationMS = %d, want 5000", snapshot.P95DurationMS)
	}
	score := DemandScore(snapshot, DemandScoreInput{Priority: 60, ReadyReplicas: 1, OccupiedReplicas: 1})
	if score < minScaleOutScore {
		t.Fatalf("DemandScore = %d, want at least scale-out threshold from sustained request duration", score)
	}
}

func TestPressureDemandScoreCountsQueuePressureTowardSustainedDemand(t *testing.T) {
	now := time.Unix(1000, 0)
	tracker := NewPressureTracker(5 * time.Minute)
	tracker.RecordQueue(PressureQueueObservation{
		Time:             now.Add(-3 * time.Second),
		Model:            "qwen",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           400,
		ReadyReplicas:    1,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})
	tracker.RecordQueue(PressureQueueObservation{
		Time:             now.Add(-2 * time.Second),
		Model:            "qwen",
		Result:           QueueResultTimeout,
		WaitMS:           700,
		ReadyReplicas:    1,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})
	tracker.RecordQueue(PressureQueueObservation{
		Time:             now.Add(-1 * time.Second),
		Model:            "qwen",
		Result:           QueueResultFull,
		WaitMS:           0,
		ReadyReplicas:    1,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})

	snapshot := tracker.Model("qwen", now)
	if snapshot.RecentRequests != 0 {
		t.Fatalf("RecentRequests = %d, want 0", snapshot.RecentRequests)
	}
	if snapshot.WaitedRequests+snapshot.QueueErrors < minScaleOutRequests {
		t.Fatalf("queue pressure events = %d, want at least %d", snapshot.WaitedRequests+snapshot.QueueErrors, minScaleOutRequests)
	}
	score := DemandScore(snapshot, DemandScoreInput{Priority: 100, ReadyReplicas: 1, OccupiedReplicas: 1, Active: 1})
	if score == 0 {
		t.Fatalf("DemandScore = 0, want positive score from sustained queue pressure")
	}
}
