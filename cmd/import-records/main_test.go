package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"llm-swap/internal/gateway"
)

type fakeImportSink struct {
	requestHashes map[string]bool
	eventHashes   map[string]bool
	requests      []gateway.RequestLogEntry
	events        []gateway.WorkerEventRecord
}

func (s *fakeImportSink) AppendImportedRequestRecord(_ context.Context, entry gateway.RequestLogEntry, hash string) (bool, error) {
	if s.requestHashes == nil {
		s.requestHashes = map[string]bool{}
	}
	if s.requestHashes[hash] {
		return false, nil
	}
	s.requestHashes[hash] = true
	s.requests = append(s.requests, entry)
	return true, nil
}

func (s *fakeImportSink) AppendImportedWorkerEvent(_ context.Context, event gateway.WorkerEventRecord, hash string) (bool, error) {
	if s.eventHashes == nil {
		s.eventHashes = map[string]bool{}
	}
	if s.eventHashes[hash] {
		return false, nil
	}
	s.eventHashes[hash] = true
	s.events = append(s.events, event)
	return true, nil
}

func TestImportRequestLogSkipsInvalidAndDedupesByLineHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway-requests.jsonl")
	line := `{"time":"2026-07-15T01:02:03Z","request_id":"req-1","model":"qwen","status_code":200,"request_headers":{"x-app-id":["app-a"],"x-trace-id":["a","b"]}}`
	data := line + "\nnot-json\n\n" + line + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &fakeImportSink{}

	stats, err := importRequestLog(context.Background(), path, sink)

	if err != nil {
		t.Fatalf("importRequestLog returned error: %v", err)
	}
	if stats.Total != 3 || stats.Inserted != 1 || stats.Duplicates != 1 || stats.Invalid != 1 {
		t.Fatalf("stats = %+v, want total=3 inserted=1 duplicates=1 invalid=1", stats)
	}
	if len(sink.requests) != 1 {
		t.Fatalf("inserted requests = %d, want 1", len(sink.requests))
	}
	if got := sink.requests[0].RequestHeaders["x-app-id"]; got != "app-a" {
		t.Fatalf("x-app-id = %q, want app-a", got)
	}
	if got := sink.requests[0].RequestHeaders["x-trace-id"]; got != "a, b" {
		t.Fatalf("x-trace-id = %q, want joined values", got)
	}
}

func TestImportWorkerEventLogSkipsInvalidAndDedupesByLineHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway-worker-events.jsonl")
	line := `{"received_at":"2026-07-15T01:02:03Z","worker_id":"worker-a","time":"2026-07-15T01:02:02Z","event":"model_state_changed","model":"qwen","from_state":"loading","to_state":"ready"}`
	data := line + "\n{}\n" + line + "\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	sink := &fakeImportSink{}

	stats, err := importWorkerEventLog(context.Background(), path, sink)

	if err != nil {
		t.Fatalf("importWorkerEventLog returned error: %v", err)
	}
	if stats.Total != 3 || stats.Inserted != 1 || stats.Duplicates != 1 || stats.Invalid != 1 {
		t.Fatalf("stats = %+v, want total=3 inserted=1 duplicates=1 invalid=1", stats)
	}
	if len(sink.events) != 1 {
		t.Fatalf("inserted events = %d, want 1", len(sink.events))
	}
	if sink.events[0].WorkerID != "worker-a" || sink.events[0].Event != "model_state_changed" {
		t.Fatalf("event = %+v", sink.events[0])
	}
}
