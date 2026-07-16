package gateway

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
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
		writeJSON(w, map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 20,
				},
			},
			"choices": []map[string]any{{"finish_reason": "stop"}},
		})
	}))
	defer upstream.Close()

	logPath := filepath.Join(t.TempDir(), "gateway-requests.jsonl")
	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.Billing = config.ModelBilling{
		InputPerMillionUSD:       1,
		OutputPerMillionUSD:      2,
		CachedInputPerMillionUSD: 0.25,
	}
	cfg.Models["qwen"] = model
	srv := NewServerWithGatewayPersistence(cfg, logPath)
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
	if got := store.requests[0].ModelUsedCostUSD; math.Abs(got-0.000185) > 1e-9 {
		t.Fatalf("stored model_used_cost_usd = %v, want cost snapshot 0.000185", got)
	}
	if store.requests[0].CostCalculatedAt == nil {
		t.Fatalf("stored cost_calculated_at is nil, want snapshot timestamp")
	}
	if got := store.requests[0].BillingInputPerMillionUSD; got != 1 {
		t.Fatalf("stored billing_input_per_million_usd = %v, want 1", got)
	}
	if got := store.requests[0].BillingOutputPerMillionUSD; got != 2 {
		t.Fatalf("stored billing_output_per_million_usd = %v, want 2", got)
	}
	if got := store.requests[0].BillingCachedInputPerMillionUSD; got != 0.25 {
		t.Fatalf("stored billing_cached_input_per_million_usd = %v, want 0.25", got)
	}
	entry := readSingleRequestLogEntry(t, logPath)
	if entry.RequestID != store.requests[0].RequestID {
		t.Fatalf("local log request_id = %q, store request_id = %q", entry.RequestID, store.requests[0].RequestID)
	}
	if entry.ModelUsedCostUSD != store.requests[0].ModelUsedCostUSD || entry.CostCalculatedAt == nil {
		t.Fatalf("local log billing snapshot = cost %v calculated_at %v, want store snapshot", entry.ModelUsedCostUSD, entry.CostCalculatedAt)
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
