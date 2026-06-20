package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestMetricsRouteExposesPrometheusMetrics(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "llm_swap_gateway_active_requests") {
		t.Fatalf("metrics body did not include active request metric: %s", rr.Body.String())
	}
}
