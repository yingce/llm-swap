package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestUITrafficReturnsServiceUnavailableWhenRecordsStoreIsDisabled(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/traffic?range=24h", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
}

func TestSummarizeTrafficRecordsAggregatesRequestedWindow(t *testing.T) {
	start := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	got := summarizeTrafficRecords([]billingRequestRecord{
		{Time: start.Add(time.Hour), StatusCode: 200, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CacheTokens: 3, DurationMS: 100},
		{Time: start.Add(2 * time.Hour), StatusCode: 404, PromptTokens: 20, CompletionTokens: 6, TotalTokens: 26, CacheTokens: 4, DurationMS: 200},
		{Time: start.Add(3 * time.Hour), StatusCode: 503, PromptTokens: 30, CompletionTokens: 7, TotalTokens: 37, CacheTokens: 5, DurationMS: 300},
	}, start, end)

	if got.Requests != 3 || got.Status2xx != 1 || got.Status4xx != 1 || got.Status5xx != 1 || got.Non200 != 2 {
		t.Fatalf("status totals = %+v", got)
	}
	if got.PromptTokens != 60 || got.CompletionTokens != 18 || got.TotalTokens != 78 || got.CacheTokens != 12 {
		t.Fatalf("token totals = %+v", got)
	}
	if got.AvgDurationMS != 200 || got.MaxDurationMS != 300 {
		t.Fatalf("duration totals = %+v", got)
	}
	if !got.Start.Equal(start) || !got.End.Equal(end) {
		t.Fatalf("range = %s..%s, want %s..%s", got.Start, got.End, start, end)
	}
}
