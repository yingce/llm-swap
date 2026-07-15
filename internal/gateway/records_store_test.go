package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRecordsStore struct {
	requests      []RequestLogEntry
	workerEvents  []uiAgentEvent
	requestPage   uiRequestsResponse
	workerPage    uiEventsResponse
	requestPageOK bool
	workerPageOK  bool
}

func (f *fakeRecordsStore) AppendRequestRecord(_ context.Context, entry RequestLogEntry) error {
	f.requests = append(f.requests, entry)
	return nil
}

func (f *fakeRecordsStore) AppendWorkerEvent(_ context.Context, event uiAgentEvent) error {
	f.workerEvents = append(f.workerEvents, event)
	return nil
}

func (f *fakeRecordsStore) PageRequestRecords(_ context.Context, _ int, _ int) (uiRequestsResponse, error) {
	f.requestPageOK = true
	return f.requestPage, nil
}

func (f *fakeRecordsStore) PageWorkerEvents(_ context.Context, _ int, _ int) (uiEventsResponse, error) {
	f.workerPageOK = true
	return f.workerPage, nil
}

func (f *fakeRecordsStore) Close() error {
	return nil
}

func TestRecordsStoreReceivesRequestAndLocalLogStillWrites(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"usage": map[string]any{"total_tokens": 11}, "choices": []map[string]any{{"finish_reason": "stop"}}})
	}))
	defer upstream.Close()

	logPath := filepath.Join(t.TempDir(), "gateway-requests.jsonl")
	srv := NewServerWithGatewayPersistence(testProxyConfig(), logPath)
	store := &fakeRecordsStore{}
	srv.recordsStore = store
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	req := proxyRequest(`{"model":"qwen","messages":[]}`)
	req.Header.Set("X-App-Id", "app-a")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if len(store.requests) != 1 {
		t.Fatalf("records store requests = %d, want 1", len(store.requests))
	}
	if got := store.requests[0].RequestHeaders["x-app-id"]; got != "app-a" {
		t.Fatalf("stored x-app-id = %q, want app-a", got)
	}
	entry := readSingleRequestLogEntry(t, logPath)
	if entry.RequestID != store.requests[0].RequestID {
		t.Fatalf("local log request_id = %q, store request_id = %q", entry.RequestID, store.requests[0].RequestID)
	}
}

func TestRecordsStoreReceivesWorkerEventAndLocalLogStillWrites(t *testing.T) {
	eventLogPath := filepath.Join(t.TempDir(), "gateway-worker-events.jsonl")
	srv := NewServerWithGatewayPersistencePaths(testGatewayConfig(), "", eventLogPath)
	store := &fakeRecordsStore{}
	srv.recordsStore = store

	req := httptest.NewRequest(http.MethodPost, "/internal/agent/heartbeat", stringsReader(`{
		"agent_id":"worker-a",
		"tags":["gpu-4090"],
		"llama_swap_url":"http://worker",
		"events":[{"event":"model_state_changed","model":"qwen","from_state":"loading","to_state":"ready"}]
	}`))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if len(store.workerEvents) != 1 {
		t.Fatalf("records store worker events = %d, want 1", len(store.workerEvents))
	}
	if store.workerEvents[0].WorkerID != "worker-a" || store.workerEvents[0].Model != "qwen" {
		t.Fatalf("stored worker event = %+v", store.workerEvents[0])
	}
	events, err := loadRecentWorkerEvents(eventLogPath, 10)
	if err != nil {
		t.Fatalf("load local worker events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("local worker events = %d, want 1", len(events))
	}
}

func TestUIRequestsAndEventsPreferRecordsStore(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	store := &fakeRecordsStore{
		requestPage: uiRequestsResponse{
			Requests: []RequestLogEntry{{RequestID: "pg-request", Model: "qwen", Time: time.Now()}},
		},
		workerPage: uiEventsResponse{
			Events: []uiAgentEvent{{WorkerID: "pg-worker", Event: "model_loaded", ReceivedAt: time.Now()}},
		},
	}
	srv.recordsStore = store

	requests := getUIRequests(t, srv, "/ui/requests?limit=1")
	if !store.requestPageOK {
		t.Fatal("request records page was not queried")
	}
	if len(requests.Requests) != 1 || requests.Requests[0].RequestID != "pg-request" {
		t.Fatalf("ui requests = %+v", requests.Requests)
	}

	events := getUIEvents(t, srv, "/ui/events?limit=1")
	if !store.workerPageOK {
		t.Fatal("worker events page was not queried")
	}
	if len(events.Events) != 1 || events.Events[0].WorkerID != "pg-worker" {
		t.Fatalf("ui events = %+v", events.Events)
	}
}

func stringsReader(s string) *strings.Reader {
	return strings.NewReader(s)
}
