package gateway

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"llm-swap/internal/protocol"
)

func TestMetricsScraperDeduplicatesRowsAcrossPulls(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/metrics" {
			t.Fatalf("path = %q, want /api/metrics", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"id":"request-1","model":"qwen","duration_ms":42},
			{"id":"request-1","model":"qwen","duration_ms":42}
		]`))
	}))
	defer worker.Close()

	scraper := NewMetricsScraper()

	first, err := scraper.PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("first PullActivity returned error: %v", err)
	}
	if first.Rows != 1 {
		t.Fatalf("first PullActivity rows = %d, want 1", first.Rows)
	}

	second, err := scraper.PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("second PullActivity returned error: %v", err)
	}
	if second.Rows != 0 {
		t.Fatalf("second PullActivity rows = %d, want 0", second.Rows)
	}
}

func TestMetricsScraperPullActivityCanUseAgentTunnel(t *testing.T) {
	tunnel := newTestAgentTunnel(t, func(t *testing.T, conn *websocket.Conn) {
		var req tunnelHTTPRequest
		if err := conn.ReadJSON(&req); err != nil {
			t.Fatalf("read tunnel request: %v", err)
		}
		if req.Method != http.MethodGet || req.Path != "/api/metrics" {
			t.Fatalf("tunnel request = %s %s, want GET /api/metrics", req.Method, req.Path)
		}
		if err := conn.WriteJSON(tunnelHTTPResponse{
			Type:       "http_response",
			ID:         req.ID,
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			BodyBase64: base64.StdEncoding.EncodeToString([]byte(`[{"id":"request-1","model":"qwen","duration_ms":42}]`)),
		}); err != nil {
			t.Fatalf("write tunnel response: %v", err)
		}
	})

	scraper := NewMetricsScraper()
	stats, err := scraper.PullActivityViaTunnel("worker-a", "http://worker-unreachable", tunnel)
	if err != nil {
		t.Fatalf("PullActivityViaTunnel returned error: %v", err)
	}
	if stats.Rows != 1 {
		t.Fatalf("rows = %d, want 1", stats.Rows)
	}
}

func TestMetricsObserveWorkersScrapesActiveWorkersConcurrently(t *testing.T) {
	metrics := NewMetrics()
	now := time.Unix(100, 0)
	workers := []Worker{
		{ID: "worker-a", LastHeartbeat: now, State: WorkerActive},
		{ID: "worker-b", LastHeartbeat: now, State: WorkerActive},
	}
	started := make(chan string, len(workers))
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		metrics.ObserveWorkers(workers, nil, now, func(worker Worker) (ActivityStats, error) {
			started <- worker.ID
			<-release
			return ActivityStats{}, nil
		}, nil)
		close(done)
	}()

	for range workers {
		select {
		case <-started:
		case <-time.After(200 * time.Millisecond):
			close(release)
			t.Fatal("worker scrapes did not start concurrently")
		}
	}
	close(release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ObserveWorkers did not finish after releasing scrapes")
	}
}

func TestMetricsScraperIsolatesWorkers(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"shared-request","model":"qwen"}]`))
	}))
	defer worker.Close()

	scraper := NewMetricsScraper()

	if got, err := scraper.PullActivity("worker-a", worker.URL); err != nil || got.Rows != 1 {
		t.Fatalf("worker-a PullActivity rows = %d, %v; want 1, nil", got.Rows, err)
	}
	if got, err := scraper.PullActivity("worker-b", worker.URL); err != nil || got.Rows != 1 {
		t.Fatalf("worker-b PullActivity rows = %d, %v; want 1, nil", got.Rows, err)
	}
}

func TestMetricsScraperEvictsOldSeenRowsWhenBoundExceeded(t *testing.T) {
	payloads := []string{
		`[{"id":"request-1"}]`,
		`[{"id":"request-2"}]`,
		`[{"id":"request-3"}]`,
		`[{"id":"request-4"}]`,
		`[{"id":"request-4"}]`,
		`[{"id":"request-1"}]`,
	}
	var pull int
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pull >= len(payloads) {
			t.Fatalf("unexpected metrics pull %d", pull+1)
		}
		_, _ = w.Write([]byte(payloads[pull]))
		pull++
	}))
	defer worker.Close()

	scraper := newMetricsScraperWithMaxSeen(3)

	for i := 0; i < 4; i++ {
		got, err := scraper.PullActivity("worker-a", worker.URL)
		if err != nil {
			t.Fatalf("PullActivity #%d returned error: %v", i+1, err)
		}
		if got.Rows != 1 {
			t.Fatalf("PullActivity #%d rows = %d, want 1", i+1, got.Rows)
		}
	}

	got, err := scraper.PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("recent duplicate PullActivity returned error: %v", err)
	}
	if got.Rows != 0 {
		t.Fatalf("recent duplicate PullActivity rows = %d, want 0", got.Rows)
	}

	got, err = scraper.PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("evicted row PullActivity returned error: %v", err)
	}
	if got.Rows != 1 {
		t.Fatalf("evicted row PullActivity rows = %d, want 1", got.Rows)
	}
}

func TestMetricsScraperReturnsErrorOnNon2xx(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer worker.Close()

	_, err := NewMetricsScraper().PullActivity("worker-a", worker.URL)
	if err == nil {
		t.Fatal("PullActivity error = nil, want error")
	}
}

func TestMetricsScraperSendsBearerToken(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bearer llama-swap-token"; got != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[{"id":"request-1"}]`))
	}))
	defer worker.Close()

	got, err := NewMetricsScraperWithToken("llama-swap-token").PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("PullActivity returned error: %v", err)
	}
	if got.Rows != 1 {
		t.Fatalf("PullActivity rows = %d, want 1", got.Rows)
	}
}

func TestMetricsScraperReturnsErrorOnInvalidJSON(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{`))
	}))
	defer worker.Close()

	_, err := NewMetricsScraper().PullActivity("worker-a", worker.URL)
	if err == nil {
		t.Fatal("PullActivity error = nil, want error")
	}
}

func TestMetricsScraperDeduplicatesPerformanceSamples(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/performance" {
			t.Fatalf("path = %q, want /api/performance", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"timestamp":"2026-06-21T00:00:00Z","device":"gpu0","metric":"util","value":50},
			{"timestamp":"2026-06-21T00:00:00Z","device":"gpu0","metric":"util","value":50}
		]`))
	}))
	defer worker.Close()

	scraper := NewMetricsScraper()
	first, err := scraper.PullPerformance("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("first PullPerformance returned error: %v", err)
	}
	if first != 1 {
		t.Fatalf("first PullPerformance = %d, want 1", first)
	}
	second, err := scraper.PullPerformance("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("second PullPerformance returned error: %v", err)
	}
	if second != 0 {
		t.Fatalf("second PullPerformance = %d, want 0", second)
	}
}

func TestMetricsScraperAcceptsPerformanceObjectWrapper(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/performance" {
			t.Fatalf("path = %q, want /api/performance", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"enabled": true,
			"gpu_stats": [
				{"timestamp":"2026-06-21T00:00:00Z","device":"gpu0","metric":"util","value":50},
				{"timestamp":"2026-06-21T00:00:00Z","device":"gpu0","metric":"util","value":50}
			]
		}`))
	}))
	defer worker.Close()

	scraper := NewMetricsScraper()
	first, err := scraper.PullPerformance("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("first PullPerformance returned error: %v", err)
	}
	if first != 1 {
		t.Fatalf("first PullPerformance = %d, want 1", first)
	}
	second, err := scraper.PullPerformance("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("second PullPerformance returned error: %v", err)
	}
	if second != 0 {
		t.Fatalf("second PullPerformance = %d, want 0", second)
	}
}

func TestMetricsScraperSummarizesNewActivityRowsIncludingCacheTokens(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{
				"id":"request-1",
				"model":"qwen",
				"req_path":"/v1/chat/completions",
				"resp_status_code":200,
				"duration_ms":552,
				"tokens":{
					"input_tokens":26,
					"output_tokens":16,
					"cache_tokens":9,
					"draft_tokens":4,
					"draft_acc_tokens":3,
					"prompt_per_second":12.5,
					"tokens_per_second":31.25
				}
			},
			{
				"id":"request-2",
				"model":"qwen",
				"resp_status_code":200,
				"duration_ms":100,
				"tokens":{
					"input_tokens":-1,
					"output_tokens":0,
					"cache_tokens":0
				}
			}
		]`))
	}))
	defer worker.Close()

	stats, err := NewMetricsScraper().PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("PullActivity returned error: %v", err)
	}
	if stats.Rows != 2 {
		t.Fatalf("Rows = %d, want 2", stats.Rows)
	}
	if len(stats.Requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(stats.Requests))
	}
	first := stats.Requests[0]
	if first.Model != "qwen" || first.Path != "/v1/chat/completions" || first.StatusCode != 200 || first.DurationMS != 552 {
		t.Fatalf("first request = %+v", first)
	}
	if got := first.Tokens["cache"]; got != 9 {
		t.Fatalf("cache tokens = %v, want 9", got)
	}
	if _, ok := stats.Requests[1].Tokens["input"]; ok {
		t.Fatalf("negative token count should be ignored: %+v", stats.Requests[1].Tokens)
	}

	again, err := NewMetricsScraper().PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("second PullActivity returned error: %v", err)
	}
	if again.Rows != 2 {
		t.Fatalf("independent scraper Rows = %d, want 2", again.Rows)
	}
}

func TestMetricsRouteRespondsOKWithoutActiveRequests(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestMetricsRouteReportsActiveProxiedRequestByWorkerAndModel(t *testing.T) {
	started := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseUpstream)
		})
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-releaseUpstream
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()
	defer release()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "metrics-worker", upstream.URL, true)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
		done <- rr
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request did not start")
	}

	waitForActiveRequestMetric(t, srv, "metrics-worker", "qwen", 1)
	assertMetricLine(t, scrapeMetrics(t, srv), `llm_swap_gateway_model_active_requests{model="qwen"} 1`)

	release()
	select {
	case rr := <-done:
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not finish after upstream release")
	}

	waitForActiveRequestMetric(t, srv, "metrics-worker", "qwen", 0)
}

func TestProxyRecordsAppUsageMetrics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"usage": map[string]any{
				"prompt_tokens":     12,
				"completion_tokens": 8,
				"total_tokens":      20,
				"cache_tokens":      3,
			},
			"choices": []map[string]any{{"finish_reason": "stop"}},
		})
	}))
	defer upstream.Close()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.Billing.PerRequestUSD = 0.25
	cfg.Models["qwen"] = model
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	req := proxyRequest(`{"model":"qwen","messages":[]}`)
	req.Header.Set("X-App-Id", "app-a")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_app_requests_total{app_id="app-a",model="qwen",status_code="200"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_app_tokens_total{app_id="app-a",model="qwen",type="prompt"} 12`)
	assertMetricLine(t, body, `llm_swap_gateway_app_tokens_total{app_id="app-a",model="qwen",type="completion"} 8`)
	assertMetricLine(t, body, `llm_swap_gateway_app_tokens_total{app_id="app-a",model="qwen",type="total"} 20`)
	assertMetricLine(t, body, `llm_swap_gateway_app_tokens_total{app_id="app-a",model="qwen",type="cache"} 3`)
	assertMetricLine(t, body, `llm_swap_gateway_app_model_used_cost_usd_total{app_id="app-a",model="qwen"} 0.25`)
	assertMetricLine(t, body, `llm_swap_gateway_app_request_duration_seconds_count{app_id="app-a",model="qwen"} 1`)
}

func TestMetricsRouteReportsBillingAppCosts(t *testing.T) {
	metrics := NewMetrics()
	metrics.ObserveBillingSummary(BillingSummary{
		Apps: []BillingAppSummary{
			{AppID: "app-a", ModelCost: 0.7, ModelUsedCost: 0.1, ModelIdleCost: 0.6},
		},
	})

	rr := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rr.Body.String()
	assertMetricLine(t, body, `llm_swap_gateway_billing_app_cost_usd{app_id="app-a"} 0.7`)
	assertMetricLine(t, body, `llm_swap_gateway_billing_app_used_cost_usd{app_id="app-a"} 0.1`)
	assertMetricLine(t, body, `llm_swap_gateway_billing_app_idle_cost_usd{app_id="app-a"} 0.6`)
}

func TestMetricsRouteMergesWorkerStateAndLlamaSwapActivity(t *testing.T) {
	var sawAuth bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "Bearer llama-secret" {
			sawAuth = true
		}
		switch r.URL.Path {
		case "/api/metrics":
			_, _ = w.Write([]byte(`[{
				"id":"activity-1",
				"model":"qwen",
				"req_path":"/v1/chat/completions",
				"resp_status_code":200,
				"duration_ms":552,
				"tokens":{
					"input_tokens":26,
					"output_tokens":16,
					"cache_tokens":9,
					"draft_tokens":4,
					"draft_acc_tokens":3,
					"prompt_per_second":12.5,
					"tokens_per_second":31.25
				}
			}]`))
		case "/api/performance":
			_, _ = w.Write([]byte(`[{"timestamp":"t1","device":"gpu0","metric":"util"}]`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer worker.Close()

	srv := NewServer(testProxyConfig())
	srv.workers.UpsertHeartbeat(protocolHeartbeat("worker-a", worker.URL), time.Now())

	body := scrapeMetrics(t, srv)

	if !sawAuth {
		t.Fatal("worker metrics scrape did not send llama-swap bearer token")
	}
	assertMetricLine(t, body, `llm_swap_gateway_worker_up{worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_model_ready{model="qwen",worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_model_running{model="qwen",worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_model_state{model="qwen",state="ready",worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_running_models{worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_state{state="active",worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_needs_restart{worker_id="worker-a"} 0`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_activity_rows_total{worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_performance_samples_total{worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_requests_total{model="qwen",path="/v1/chat/completions",status_code="200",worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_request_tokens_total{model="qwen",type="input",worker_id="worker-a"} 26`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_request_tokens_total{model="qwen",type="output",worker_id="worker-a"} 16`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_request_tokens_total{model="qwen",type="cache",worker_id="worker-a"} 9`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_request_tokens_total{model="qwen",type="draft",worker_id="worker-a"} 4`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_request_tokens_total{model="qwen",type="draft_accepted",worker_id="worker-a"} 3`)
	assertMetricContains(t, body, `llm_swap_gateway_worker_request_duration_seconds_count{model="qwen",worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_tokens_per_second{model="qwen",type="completion",worker_id="worker-a"} 31.25`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_tokens_per_second{model="qwen",type="prompt",worker_id="worker-a"} 12.5`)

	body = scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_worker_activity_rows_total{worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_request_tokens_total{model="qwen",type="cache",worker_id="worker-a"} 9`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_performance_samples_total{worker_id="worker-a"} 1`)
}

func TestMetricsRouteReportsWorkerRestartAndErrorState(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer worker.Close()

	srv := NewServer(testProxyConfig())
	hb := protocolHeartbeat("worker-a", worker.URL)
	hb.NeedsRestart = true
	hb.LastError = "download failed"
	hb.Capacity.MaxConcurrency = 3
	hb.Capacity.MaxQueue = 7
	hb.RunningModels = []protocol.RunningModel{{Model: "qwen", State: "starting"}}
	srv.workers.UpsertHeartbeat(hb, time.Now())

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_worker_state{state="active",worker_id="worker-a"} 0`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_state{state="draining",worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_needs_restart{worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_last_error_present{worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_capacity_max_concurrency{worker_id="worker-a"} 3`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_capacity_max_queue{worker_id="worker-a"} 7`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_model_state{model="qwen",state="starting",worker_id="worker-a"} 1`)
}

func TestMetricsRouteReportsWorkerGPUDevices(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer worker.Close()

	srv := NewServer(testProxyConfig())
	hb := protocolHeartbeat("worker-a", worker.URL)
	hb.GPUDevices = []protocol.GPUDevice{{
		Index:              0,
		Name:               "NVIDIA GeForce RTX 4090",
		MemoryTotalMiB:     24564,
		MemoryUsedMiB:      8192,
		MemoryFreeMiB:      16372,
		UtilizationPercent: 42,
		TemperatureCelsius: 63,
	}}
	srv.workers.UpsertHeartbeat(hb, time.Now())

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_worker_gpu_memory_total_mib{gpu_index="0",gpu_name="NVIDIA GeForce RTX 4090",worker_id="worker-a"} 24564`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_gpu_memory_used_mib{gpu_index="0",gpu_name="NVIDIA GeForce RTX 4090",worker_id="worker-a"} 8192`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_gpu_memory_free_mib{gpu_index="0",gpu_name="NVIDIA GeForce RTX 4090",worker_id="worker-a"} 16372`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_gpu_utilization_percent{gpu_index="0",gpu_name="NVIDIA GeForce RTX 4090",worker_id="worker-a"} 42`)
	assertMetricLine(t, body, `llm_swap_gateway_worker_gpu_temperature_celsius{gpu_index="0",gpu_name="NVIDIA GeForce RTX 4090",worker_id="worker-a"} 63`)
}

func TestMetricsRouteEmitsZeroCacheTokenSeries(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/metrics":
			_, _ = w.Write([]byte(`[{
				"id":"activity-1",
				"model":"qwen",
				"tokens":{"input_tokens":0,"output_tokens":0,"cache_tokens":0}
			}]`))
		case "/api/performance":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer worker.Close()

	srv := NewServer(testProxyConfig())
	srv.workers.UpsertHeartbeat(protocolHeartbeat("worker-a", worker.URL), time.Now())

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_worker_request_tokens_total{model="qwen",type="cache",worker_id="worker-a"} 0`)
}

func TestMetricsScrapeFailuresPutWorkerInBackoffUntilSuccess(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/metrics", "/api/performance":
			if fail.Load() {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer worker.Close()

	srv := NewServer(testProxyConfig())
	srv.workers.UpsertHeartbeat(protocolHeartbeat("worker-a", worker.URL), time.Now())

	_ = scrapeMetrics(t, srv)
	_ = scrapeMetrics(t, srv)

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer client-secret")
	modelsRR := httptest.NewRecorder()
	srv.ServeHTTP(modelsRR, modelsReq)
	if modelsRR.Code != http.StatusOK {
		t.Fatalf("models status = %d, want %d: %s", modelsRR.Code, http.StatusOK, modelsRR.Body.String())
	}
	if strings.Contains(modelsRR.Body.String(), `"qwen"`) {
		t.Fatalf("models body includes qwen while worker scrape is in backoff: %s", modelsRR.Body.String())
	}

	fail.Store(false)
	_ = scrapeMetrics(t, srv)

	modelsRR = httptest.NewRecorder()
	srv.ServeHTTP(modelsRR, modelsReq)
	if !strings.Contains(modelsRR.Body.String(), `"qwen"`) {
		t.Fatalf("models body missing qwen after scrape success: %s", modelsRR.Body.String())
	}
}

func TestMetricsSchemaMismatchDoesNotImmediatelyMarkWorkerUnavailable(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/metrics":
			_, _ = w.Write([]byte(`[]`))
		case "/api/performance":
			_, _ = w.Write([]byte(`{"enabled":true}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer worker.Close()

	srv := NewServer(testProxyConfig())
	srv.workers.UpsertHeartbeat(protocolHeartbeat("worker-a", worker.URL), time.Now())

	_ = scrapeMetrics(t, srv)

	if !srv.workers.Healthy("worker-a", time.Now()) {
		t.Fatal("single scrape schema mismatch should not immediately mark worker unavailable")
	}
}

func TestMetricsReportsModelUnderprovisionedWhenMinLoadedIsNotMet(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/metrics", "/api/performance":
			_, _ = w.Write([]byte(`[]`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer worker.Close()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.MinLoaded = 2
	model.MaxLoaded = 3
	cfg.Models["qwen"] = model
	srv := NewServer(cfg)
	srv.workers.UpsertHeartbeat(protocolHeartbeat("worker-a", worker.URL), time.Now())

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_model_underprovisioned{model="qwen"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_model_loaded_replicas{model="qwen"} 1`)
}

func TestProxyRecordsRequestQueueAndDispatchMetrics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"ok": true,
			"usage": map[string]any{
				"prompt_tokens":     11,
				"completion_tokens": 7,
				"total_tokens":      18,
				"cache_tokens":      4,
				"reasoning_tokens":  2,
			},
		})
	}))
	defer upstream.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_requests_total{model="qwen",status_code="200",worker_id="worker-a"} 1`)
	assertMetricContains(t, body, `llm_swap_gateway_request_duration_seconds_count{model="qwen",worker_id="worker-a"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_model_tokens_total{model="qwen",type="cache"} 4`)
	assertMetricLine(t, body, `llm_swap_gateway_model_tokens_total{model="qwen",type="completion"} 7`)
	assertMetricLine(t, body, `llm_swap_gateway_model_tokens_total{model="qwen",type="prompt"} 11`)
	assertMetricLine(t, body, `llm_swap_gateway_model_tokens_total{model="qwen",type="reasoning"} 2`)
	assertMetricLine(t, body, `llm_swap_gateway_model_tokens_total{model="qwen",type="total"} 18`)
}

func TestProxyRecordsQueueFullMetric(t *testing.T) {
	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.MaxConcurrency = 1
	model.MaxQueue = 0
	cfg.Models["qwen"] = model
	srv := NewServer(cfg)

	release, err := srv.limiter.Acquire(context.Background(), "model:qwen", 1, 0)
	if err != nil {
		t.Fatalf("pre-acquire limiter: %v", err)
	}
	defer release()

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_queue_events_total{model="qwen",result="queue_full"} 1`)
}

func TestMetricsReportsCurrentModelQueueDepth(t *testing.T) {
	srv := NewServer(testProxyConfig())
	firstRelease, err := srv.limiter.Acquire(context.Background(), "model:qwen", 1, 1)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer firstRelease()

	acquired := make(chan func(), 1)
	go func() {
		release, err := srv.limiter.Acquire(context.Background(), "model:qwen", 1, 1)
		if err == nil {
			acquired <- release
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && srv.limiter.Queued("model:qwen") != 1 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.limiter.Queued("model:qwen"); got != 1 {
		t.Fatalf("queued = %d, want 1", got)
	}

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_model_queue_depth{model="qwen"} 1`)

	firstRelease()
	select {
	case release := <-acquired:
		release()
	case <-time.After(2 * time.Second):
		t.Fatal("queued acquire did not finish after release")
	}
}

func TestProxyRecordsDispatchFailureMetric(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	cfg := testProxyConfig()
	cfg.Gateway.ProxyAttempts = 1
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_dispatch_failures_total{model="qwen",reason="upstream_retry_exhausted",worker_id="worker-a"} 1`)
}

func TestProxyReportsCooldownAndRetryMetrics(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","choices":[]}`))
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_replica_unhealthy{model="qwen",reason="upstream_retry_exhausted",worker_id="first"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_replica_cooldown_marks_total{model="qwen",reason="upstream_retry_exhausted",worker_id="first"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_proxy_retries_total{model="qwen",reason="upstream_retry_exhausted",worker_id="first"} 1`)
}

func protocolHeartbeat(workerID, workerURL string) protocol.HeartbeatRequest {
	return protocol.HeartbeatRequest{
		AgentID:      workerID,
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: workerURL,
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "ready"},
		},
	}
}

func waitForActiveRequestMetric(t *testing.T, srv *Server, workerID, model string, want float64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body := scrapeMetrics(t, srv)
		value, ok := activeRequestMetricValue(body, workerID, model)
		if (!ok && want == 0) || (ok && value == want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	body := scrapeMetrics(t, srv)
	value, ok := activeRequestMetricValue(body, workerID, model)
	if !ok {
		t.Fatalf("active request metric for worker=%q model=%q absent, want %v; body:\n%s", workerID, model, want, body)
	}
	t.Fatalf("active request metric for worker=%q model=%q = %v, want %v; body:\n%s", workerID, model, value, want, body)
}

func scrapeMetrics(t *testing.T, srv *Server) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	return rr.Body.String()
}

func assertMetricLine(t *testing.T, body, want string) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if line == want {
			return
		}
	}
	t.Fatalf("metrics missing line %q; body:\n%s", want, body)
}

func assertMetricContains(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("metrics missing %q; body:\n%s", want, body)
	}
}

func activeRequestMetricValue(body, workerID, model string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "llm_swap_gateway_active_requests{") {
			continue
		}
		if !strings.Contains(line, `worker_id="`+workerID+`"`) || !strings.Contains(line, `model="`+model+`"`) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		value, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}
