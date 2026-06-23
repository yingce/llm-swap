package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestUIStatusEndpointSummarizesWorkersModelsAndEvents(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	now := time.Unix(300, 0).UTC()
	srv.access.RecordRequest(RequestLogEntry{
		Time:             now,
		Model:            "qwen",
		WorkerID:         "gpu-01",
		StatusCode:       200,
		DurationMS:       100,
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		CacheTokens:      3,
	})
	srv.access.RecordRequest(RequestLogEntry{
		Time:        now.Add(time.Second),
		Model:       "qwen",
		WorkerID:    "gpu-01",
		StatusCode:  500,
		DurationMS:  300,
		TotalTokens: 7,
	})

	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker",
		Artifacts:     map[string]string{"qwen": "ready"},
		Capacity:      config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Events: []protocol.AgentEvent{
			{
				Time:            time.Unix(100, 0).UTC(),
				Event:           "artifact_download_progress",
				Model:           "qwen",
				Object:          "models/qwen.tar.gz",
				DownloadedBytes: 50,
				TotalBytes:      100,
				Percent:         50,
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var status uiStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Summary.TotalWorkers != 1 || status.Summary.HealthyWorkers != 1 {
		t.Fatalf("summary = %+v, want one healthy worker", status.Summary)
	}
	model, ok := findUIModel(status.Models, "qwen")
	if !ok {
		t.Fatalf("models = %+v, want qwen", status.Models)
	}
	if model.Name != "qwen" || model.ReadyWorkers != 1 || model.RunningWorkers != 1 || !model.Available {
		t.Fatalf("model status = %+v, want qwen ready/running/available", model)
	}
	if model.Traffic.Requests != 2 || model.Traffic.TotalTokens != 22 || model.Traffic.AvgDurationMS != 200 || model.Traffic.MaxDurationMS != 300 {
		t.Fatalf("model traffic = %+v, want requests/tokens/duration stats", model.Traffic)
	}
	if model.Traffic.Status2xx != 1 || model.Traffic.Status5xx != 1 || !model.Traffic.LastAccess.Equal(now.Add(time.Second)) {
		t.Fatalf("model traffic status/last = %+v, want status counts and last access", model.Traffic)
	}
	if len(status.Workers) != 1 || status.Workers[0].ID != "gpu-01" || status.Workers[0].Health != "healthy" {
		t.Fatalf("workers = %+v, want healthy gpu-01", status.Workers)
	}
	if len(status.Events) != 1 || status.Events[0].WorkerID != "gpu-01" || status.Events[0].Event != "artifact_download_progress" {
		t.Fatalf("events = %+v, want cached progress event", status.Events)
	}
}

func TestUIEventsEndpointPaginatesPersistedWorkerEvents(t *testing.T) {
	eventLogPath := filepath.Join(t.TempDir(), "worker-events.jsonl")
	srv := NewServerWithGatewayPersistencePaths(testGatewayConfig(), "", eventLogPath)
	for i, event := range []string{"artifact_download_start", "artifact_download_progress", "artifact_download_complete"} {
		postHeartbeat(t, srv, protocol.HeartbeatRequest{
			AgentID:      "gpu-01",
			Tags:         []string{"gpu-4090"},
			LlamaSwapURL: "http://worker",
			Events: []protocol.AgentEvent{{
				Time:  time.Unix(int64(100+i), 0).UTC(),
				Event: event,
				Model: "qwen",
			}},
		})
	}

	first := getUIEvents(t, srv, "/ui/events?limit=2")
	if len(first.Events) != 2 || first.Events[0].Event != "artifact_download_complete" || first.Events[1].Event != "artifact_download_progress" {
		t.Fatalf("first events = %+v, want newest two", first.Events)
	}
	if !first.HasMore || first.NextOffset != 2 {
		t.Fatalf("first page = %+v, want has_more next_offset=2", first)
	}

	second := getUIEvents(t, srv, "/ui/events?limit=2&offset=2")
	if len(second.Events) != 1 || second.Events[0].Event != "artifact_download_start" {
		t.Fatalf("second events = %+v, want oldest event", second.Events)
	}
	if second.HasMore || second.NextOffset != 3 {
		t.Fatalf("second page = %+v, want no more next_offset=3", second)
	}
}

func TestUIEndpointsRequireClientTokenWhenConfigured(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())

	for _, path := range []string{"/ui", "/ui/status", "/ui/events"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()

		srv.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want %d", path, rr.Code, http.StatusUnauthorized)
		}
	}
}

func TestUIEndpointsAcceptBearerClientToken(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	req.Header.Set("Authorization", "Bearer client-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestUIPageTokenSetsCookie(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui?token=client-secret", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusSeeOther, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "/ui" {
		t.Fatalf("location = %q, want /ui", got)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != uiAuthCookieName || cookies[0].Value != "client-secret" || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %+v, want auth cookie", cookies)
	}
}

func TestUIEndpointsAcceptAuthCookie(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/events", nil)
	req.AddCookie(&http.Cookie{Name: uiAuthCookieName, Value: "client-secret"})
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func getUIEvents(t *testing.T, srv *Server, path string) uiEventsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var resp uiEventsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	return resp
}

func testUIGatewayConfig() config.GatewayConfig {
	cfg := testGatewayConfig()
	cfg.Tokens.Client = "client-secret"
	return cfg
}

func findUIModel(models []uiModelStatus, name string) (uiModelStatus, bool) {
	for _, model := range models {
		if model.Name == name {
			return model, true
		}
	}
	return uiModelStatus{}, false
}

func TestUIPageServesDashboardHTML(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("content-type = %q, want text/html", got)
	}
	body := rr.Body.String()
	for _, want := range []string{"LLM Swap Gateway", "/ui/status", "Models", "Workers", "Recent worker events", "worker-card-grid", "model-table", "breakable"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestUIStatusEndpointUsesEmptyArraysInsteadOfNull(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	for _, forbidden := range []string{
		`"events":null`,
		`"workers":null`,
		`"models":null`,
		`"worker_statuses":null`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("body contains %s:\n%s", forbidden, body)
		}
	}
}
