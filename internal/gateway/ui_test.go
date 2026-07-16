package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/buildinfo"
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
		GPUDevices: []protocol.GPUDevice{{
			Index:              0,
			Name:               "NVIDIA GeForce RTX 4090",
			UUID:               "GPU-test",
			MemoryTotalMiB:     24564,
			MemoryUsedMiB:      8192,
			MemoryFreeMiB:      16372,
			UtilizationPercent: 42,
			TemperatureCelsius: 61,
		}},
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
	req.Header.Set("Authorization", "Bearer agent-secret")
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
	if got := status.Workers[0].GPUDevices; len(got) != 1 || got[0].MemoryUsedMiB != 8192 || got[0].UtilizationPercent != 42 {
		t.Fatalf("worker gpu devices = %+v, want 4090 memory/utilization", got)
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

func TestUIRequestsEndpointPaginatesPersistedRequestLogs(t *testing.T) {
	requestLogPath := filepath.Join(t.TempDir(), "request-logs.jsonl")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id":     "chatcmpl-test",
			"object": "chat.completion",
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": "ok"},
			}},
			"usage": map[string]any{
				"prompt_tokens":     1,
				"completion_tokens": 1,
				"total_tokens":      2,
			},
		})
	}))
	defer upstream.Close()

	srv := NewServerWithGatewayPersistencePaths(testProxyConfig(), requestLogPath, "")
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	srv.ServeHTTP(httptest.NewRecorder(), proxyRequest(`{"model":"qwen","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`))
	srv.ServeHTTP(httptest.NewRecorder(), proxyRequest(`{"model":"qwen","messages":[{"role":"user","content":[{"type":"audio_url","audio_url":{"url":"https://example.com/a.mp3"}},{"type":"text","text":"hello"}]}]}`))

	first := getUIRequests(t, srv, "/ui/requests?limit=1")
	if len(first.Requests) != 1 || first.Requests[0].AudioCount != 1 {
		t.Fatalf("first requests = %+v, want newest audio request", first.Requests)
	}
	if !first.HasMore || first.NextOffset != 1 {
		t.Fatalf("first page = %+v, want has_more next_offset=1", first)
	}

	second := getUIRequests(t, srv, "/ui/requests?limit=1&offset=1")
	if len(second.Requests) != 1 || second.Requests[0].ImageCount != 1 {
		t.Fatalf("second requests = %+v, want older image request", second.Requests)
	}
	if second.HasMore || second.NextOffset != 2 {
		t.Fatalf("second page = %+v, want no more next_offset=2", second)
	}
}

func TestUIStatusIncludesReplicaCooldownDetails(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	now := time.Now()
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	})
	srv.replicaCooldowns.Mark("gpu-01", "qwen", "upstream_retry_exhausted", now)

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var status uiStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	model, ok := findUIModel(status.Models, "qwen")
	if !ok || len(model.WorkerStatuses) != 1 {
		t.Fatalf("model statuses = %+v, want qwen worker status", status.Models)
	}
	if !model.WorkerStatuses[0].CooldownActive || model.WorkerStatuses[0].CooldownReason != "upstream_retry_exhausted" {
		t.Fatalf("model worker cooldown = %+v, want active upstream_retry_exhausted", model.WorkerStatuses[0])
	}
	if len(status.Workers) != 1 || len(status.Workers[0].ReplicaCooldowns) != 1 {
		t.Fatalf("worker cooldowns = %+v, want one cooldown", status.Workers)
	}
}

func TestUIStatusIncludesAgentBuildAndVersionStatus(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "worker-current",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-current",
		Artifacts:    map[string]string{"qwen": "ready"},
		AgentBuild: protocol.BuildInfo{
			Version:         buildinfo.AgentVersion,
			Commit:          "abc123",
			ProtocolVersion: protocol.AgentProtocolVersion,
		},
	})
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "worker-outdated",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-outdated",
		Artifacts:    map[string]string{"qwen": "ready"},
		AgentBuild: protocol.BuildInfo{
			Version:         "2026.07.06.0",
			Commit:          "old123",
			ProtocolVersion: protocol.AgentProtocolVersion,
		},
	})
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "worker-legacy",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker-legacy",
		Artifacts:    map[string]string{"qwen": "ready"},
	})

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var status uiStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	workers := map[string]uiWorker{}
	for _, worker := range status.Workers {
		workers[worker.ID] = worker
	}
	if got := workers["worker-current"].AgentBuild.Commit; got != "abc123" {
		t.Fatalf("worker-current commit=%q want abc123", got)
	}
	if got := workers["worker-current"].AgentVersionStatus; got != "current" {
		t.Fatalf("worker-current version status=%q want current", got)
	}
	if got := workers["worker-outdated"].AgentVersionStatus; got != "outdated" {
		t.Fatalf("worker-outdated version status=%q want outdated", got)
	}
	if got := workers["worker-legacy"].AgentVersionStatus; got != "legacy" {
		t.Fatalf("worker-legacy version status=%q want legacy", got)
	}
}

func TestUIStatusPreservesWorkerJoinOrder(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	for _, workerID := range []string{"gpu-b", "gpu-a", "gpu-c"} {
		postHeartbeat(t, srv, protocol.HeartbeatRequest{
			AgentID:      workerID,
			Tags:         []string{"gpu-4090"},
			LlamaSwapURL: "http://worker",
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var status uiStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got := uiWorkerIDs(status.Workers); strings.Join(got, ",") != "gpu-b,gpu-a,gpu-c" {
		t.Fatalf("ui worker order = %v, want join order", got)
	}
}

func TestUIStatusShowsWorkerBackoffAfterReverseAccessFailure(t *testing.T) {
	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "broken", "", true)

	for i := 0; i < workerScrapeFailureBackoffThreshold; i++ {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("proxy attempt %d status = %d, want 503: %s", i+1, rr.Code, rr.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	statusRR := httptest.NewRecorder()
	srv.ServeHTTP(statusRR, req)
	if statusRR.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", statusRR.Code, statusRR.Body.String())
	}

	var status uiStatusResponse
	if err := json.NewDecoder(statusRR.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if len(status.Workers) != 1 {
		t.Fatalf("workers = %+v, want one worker", status.Workers)
	}
	if status.Workers[0].Health != "backoff" {
		t.Fatalf("worker health = %q, want backoff", status.Workers[0].Health)
	}
	if !strings.Contains(status.Workers[0].HealthProblem, "reverse access") {
		t.Fatalf("worker health problem = %q, want reverse access detail", status.Workers[0].HealthProblem)
	}
}

func uiWorkerIDs(workers []uiWorker) []string {
	out := make([]string, 0, len(workers))
	for _, worker := range workers {
		out = append(out, worker.ID)
	}
	return out
}

func TestUIEndpointsRequireAgentTokenWhenConfigured(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())

	for _, path := range []string{"/ui", "/ui/assets/", "/ui/status", "/ui/events", "/ui/requests", "/ui/metrics/summary", "/ui/metrics/model", "/ui/metrics/worker", "/api/billing", "/ui/api/billing", "/ui/api/config"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()

		srv.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want %d", path, rr.Code, http.StatusUnauthorized)
		}
	}
}

func TestUIMetricsSummaryReturnsServiceUnavailableWhenDisabled(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/metrics/summary", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
}

func TestUIMetricsModelQueriesVictoriaMetrics(t *testing.T) {
	var gotPath string
	var gotQuery string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		writeJSON(w, map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []any{
					map[string]any{
						"metric": map[string]string{"model": "qwen"},
						"values": [][]any{{float64(1710000000), "3"}},
					},
				},
			},
		})
	}))
	defer backend.Close()

	cfg := testUIGatewayConfig()
	cfg.MetricsStore = config.MetricsStoreConfig{
		Enabled:      true,
		Type:         "victoriametrics",
		QueryURL:     backend.URL,
		DefaultRange: "2h",
		MaxRange:     "7d",
		TimeoutMS:    1000,
	}
	srv := NewServer(cfg)
	req := httptest.NewRequest(http.MethodGet, "/ui/metrics/model?model=qwen&range=30m&step=1m", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if gotPath != "/prometheus/api/v1/query_range" {
		t.Fatalf("backend path = %q, want query_range", gotPath)
	}
	if !strings.Contains(gotQuery, `model="qwen"`) {
		t.Fatalf("backend query = %q, want model filter", gotQuery)
	}
	var resp uiMetricsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode metrics response: %v", err)
	}
	if resp.Range != "30m" || resp.Step != "1m" {
		t.Fatalf("range/step = %q/%q, want 30m/1m", resp.Range, resp.Step)
	}
	if len(resp.Series) == 0 || resp.Series[0].Name == "" || len(resp.Series[0].Points) != 1 {
		t.Fatalf("series = %+v, want parsed VM series", resp.Series)
	}
}

func TestUIMetricsSummaryQueriesModelRequestLatencyAndQueueSeries(t *testing.T) {
	var queries []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query"))
		writeJSON(w, map[string]any{
			"status": "success",
			"data": map[string]any{
				"result": []any{},
			},
		})
	}))
	defer backend.Close()

	cfg := testUIGatewayConfig()
	cfg.MetricsStore = config.MetricsStoreConfig{
		Enabled:      true,
		Type:         "victoriametrics",
		QueryURL:     backend.URL,
		DefaultRange: "1h",
		MaxRange:     "7d",
		TimeoutMS:    1000,
	}
	srv := NewServer(cfg)
	req := httptest.NewRequest(http.MethodGet, "/ui/metrics/summary?range=15m&step=15s", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	joined := strings.Join(queries, "\n")
	for _, want := range []string{
		"llm_swap_gateway_requests_total",
		"llm_swap_gateway_request_duration_seconds_sum",
		"llm_swap_gateway_model_queue_depth",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("summary queries missing %q:\n%s", want, joined)
		}
	}
}

func TestUIEndpointsAcceptBearerAgentToken(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestUIPageTokenSetsCookie(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui?token=agent-secret", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusSeeOther, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "/ui" {
		t.Fatalf("location = %q, want /ui", got)
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != uiAuthCookieName || cookies[0].Value != "agent-secret" || !cookies[0].HttpOnly {
		t.Fatalf("cookies = %+v, want auth cookie", cookies)
	}
	if cookies[0].Path != "/" {
		t.Fatalf("cookie path = %q, want / so /api billing requests include UI auth", cookies[0].Path)
	}
}

func TestUIEndpointsAcceptAuthCookie(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/events", nil)
	req.AddCookie(&http.Cookie{Name: uiAuthCookieName, Value: "agent-secret"})
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func getUIEvents(t *testing.T, srv *Server, path string) uiEventsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
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

func getUIRequests(t *testing.T, srv *Server, path string) uiRequestsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var resp uiRequestsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode requests: %v", err)
	}
	return resp
}

func testUIGatewayConfig() config.GatewayConfig {
	cfg := testGatewayConfig()
	cfg.Tokens.Client = "client-secret"
	cfg.Tokens.Agent = "agent-secret"
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
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("content-type = %q, want text/html", got)
	}
	body := rr.Body.String()
	for _, want := range []string{"LLM Swap Admin", "llmswap-admin-root", "/ui/assets/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestUIStatusEndpointUsesEmptyArraysInsteadOfNull(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-empty",
		LlamaSwapURL: "http://worker-empty",
	})
	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
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
		`"tags":null`,
		`"running_models":null`,
		`"allowed_models":null`,
		`"replica_cooldowns":null`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("body contains %s:\n%s", forbidden, body)
		}
	}
}
