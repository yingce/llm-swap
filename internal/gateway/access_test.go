package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAccessTrackerFromRequestLogReplaysStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-requests.jsonl")
	first := time.Unix(100, 0).UTC()
	second := time.Unix(200, 0).UTC()
	writeRequestLogEntry(t, path, RequestLogEntry{
		Time:       first,
		Model:      "qwen",
		WorkerID:   "worker-a",
		StatusCode: 429,
		DurationMS: 50,
	})
	writeRequestLogEntry(t, path, RequestLogEntry{
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

	loaded, err := LoadAccessTrackerFromRequestLog(path)
	if err != nil {
		t.Fatalf("LoadAccessTrackerFromRequestLog() error = %v", err)
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
	if got := loaded.WorkerModelStatusCount("worker-a", "qwen", 429); got != 1 {
		t.Fatalf("worker model 429 count = %d, want 1", got)
	}
}

func TestNewServerWithGatewayPersistenceReplaysRequestLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-requests.jsonl")
	now := time.Unix(300, 0).UTC()
	writeRequestLogEntry(t, path, RequestLogEntry{Time: now, Model: "qwen", WorkerID: "worker-a", StatusCode: 200, TotalTokens: 9})

	srv := NewServerWithGatewayPersistence(testProxyConfig(), path)

	if got := srv.access.ModelLastAccess("qwen"); !got.Equal(now) {
		t.Fatalf("loaded model last access = %s, want %s", got, now)
	}
	if got := srv.access.WorkerModelCount("worker-a", "qwen"); got != 1 {
		t.Fatalf("loaded worker model count = %d, want 1", got)
	}
}

func writeRequestLogEntry(t *testing.T, path string, entry RequestLogEntry) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}
