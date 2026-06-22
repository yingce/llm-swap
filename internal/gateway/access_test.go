package gateway

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAccessTrackerPersistsLastAccessAndCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-stats.json")
	tracker := NewAccessTracker()
	first := time.Unix(100, 0).UTC()
	second := time.Unix(200, 0).UTC()
	tracker.Record("qwen", "worker-a", first)
	tracker.RecordRequest(RequestLogEntry{
		Time:             second,
		Model:            "qwen",
		WorkerID:         "worker-a",
		StatusCode:       200,
		DurationMS:       1500,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		CacheTokens:      3,
	})

	if err := tracker.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := LoadAccessTracker(path)
	if err != nil {
		t.Fatalf("LoadAccessTracker() error = %v", err)
	}

	if got := loaded.ModelLastAccess("qwen"); !got.Equal(second) {
		t.Fatalf("model last access = %s, want %s", got, second)
	}
	if got := loaded.WorkerModelLastAccess("worker-a", "qwen"); !got.Equal(second) {
		t.Fatalf("worker model last access = %s, want %s", got, second)
	}
	if got := loaded.ModelCount("qwen"); got != 2 {
		t.Fatalf("model count = %d, want 2", got)
	}
	if got := loaded.WorkerModelCount("worker-a", "qwen"); got != 2 {
		t.Fatalf("worker model count = %d, want 2", got)
	}
	if got := loaded.ModelTotalTokens("qwen"); got != 15 {
		t.Fatalf("model total tokens = %d, want 15", got)
	}
	if got := loaded.WorkerModelStatusCount("worker-a", "qwen", 200); got != 1 {
		t.Fatalf("worker model 200 count = %d, want 1", got)
	}
}

func TestNewServerWithAccessPersistenceLoadsStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-stats.json")
	tracker := NewAccessTracker()
	now := time.Unix(300, 0).UTC()
	tracker.Record("qwen", "worker-a", now)
	if err := tracker.Save(path); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	srv := NewServerWithAccessPersistence(testProxyConfig(), path)

	if got := srv.access.ModelLastAccess("qwen"); !got.Equal(now) {
		t.Fatalf("loaded model last access = %s, want %s", got, now)
	}
	if got := srv.access.WorkerModelCount("worker-a", "qwen"); got != 1 {
		t.Fatalf("loaded worker model count = %d, want 1", got)
	}
}
