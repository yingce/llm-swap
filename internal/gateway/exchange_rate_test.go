package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExchangeRateProviderFetchesCachesAndFallsBack(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, []map[string]any{{"date": "2035-01-01", "base": "CNY", "quote": "USD", "rate": 0.15}})
	}))
	defer server.Close()

	now := time.Date(2035, 1, 1, 12, 0, 0, 0, time.UTC)
	provider := &ExchangeRateProvider{
		url:    server.URL,
		client: server.Client(),
		now:    func() time.Time { return now },
	}

	first := provider.CNYToUSD(context.Background())
	second := provider.CNYToUSD(context.Background())

	if first.CNYToUSD != 0.15 || first.Stale {
		t.Fatalf("first rate = %+v, want fresh 0.15", first)
	}
	if !first.Time.Equal(time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("first rate time = %s, want API date", first.Time)
	}
	if second.CNYToUSD != 0.15 || second.Stale || calls != 1 {
		t.Fatalf("cached rate = %+v calls=%d, want cached fresh rate and one call", second, calls)
	}

	provider.url = "http://127.0.0.1:1/unreachable"
	now = now.Add(11 * time.Minute)
	stale := provider.CNYToUSD(context.Background())
	if stale.CNYToUSD != 0.15 || !stale.Stale {
		t.Fatalf("stale cached rate = %+v, want stale cached 0.15", stale)
	}

	fallback := (&ExchangeRateProvider{
		url:    "http://127.0.0.1:1/unreachable",
		client: server.Client(),
		now:    func() time.Time { return now },
	}).CNYToUSD(context.Background())
	if fallback.CNYToUSD != fallbackCNYToUSDRate || !fallback.Stale {
		t.Fatalf("fallback rate = %+v, want stale fallback", fallback)
	}
}
