package gateway

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
	if first != 1 {
		t.Fatalf("first PullActivity = %d, want 1", first)
	}

	second, err := scraper.PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("second PullActivity returned error: %v", err)
	}
	if second != 0 {
		t.Fatalf("second PullActivity = %d, want 0", second)
	}
}

func TestMetricsScraperIsolatesWorkers(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"shared-request","model":"qwen"}]`))
	}))
	defer worker.Close()

	scraper := NewMetricsScraper()

	if got, err := scraper.PullActivity("worker-a", worker.URL); err != nil || got != 1 {
		t.Fatalf("worker-a PullActivity = %d, %v; want 1, nil", got, err)
	}
	if got, err := scraper.PullActivity("worker-b", worker.URL); err != nil || got != 1 {
		t.Fatalf("worker-b PullActivity = %d, %v; want 1, nil", got, err)
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
		if got != 1 {
			t.Fatalf("PullActivity #%d = %d, want 1", i+1, got)
		}
	}

	got, err := scraper.PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("recent duplicate PullActivity returned error: %v", err)
	}
	if got != 0 {
		t.Fatalf("recent duplicate PullActivity = %d, want 0", got)
	}

	got, err = scraper.PullActivity("worker-a", worker.URL)
	if err != nil {
		t.Fatalf("evicted row PullActivity returned error: %v", err)
	}
	if got != 1 {
		t.Fatalf("evicted row PullActivity = %d, want 1", got)
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
	if got != 1 {
		t.Fatalf("PullActivity = %d, want 1", got)
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

func TestMetricsRouteMergesWorkerStateAndLlamaSwapActivity(t *testing.T) {
	var sawAuth bool
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/metrics" {
			t.Fatalf("path = %q, want /api/metrics", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "Bearer llama-secret" {
			sawAuth = true
		}
		_, _ = w.Write([]byte(`[{"id":"activity-1","model":"qwen"}]`))
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
	assertMetricLine(t, body, `llm_swap_gateway_worker_activity_rows_total{worker_id="worker-a"} 1`)

	body = scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_worker_activity_rows_total{worker_id="worker-a"} 1`)
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
