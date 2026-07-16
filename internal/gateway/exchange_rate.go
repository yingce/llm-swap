package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

const frankfurterCNYUSDURL = "https://api.frankfurter.dev/v2/rates?base=CNY&quotes=USD"
const exchangeRateCacheTTL = 10 * time.Minute

type ExchangeRateProvider struct {
	url     string
	client  *http.Client
	now     func() time.Time
	mu      sync.Mutex
	cached  BillingExchangeRate
	fetched time.Time
}

func NewExchangeRateProvider() *ExchangeRateProvider {
	return &ExchangeRateProvider{
		url: frankfurterCNYUSDURL,
		client: &http.Client{
			Timeout: 3 * time.Second,
		},
		now: time.Now,
	}
}

func (p *ExchangeRateProvider) CNYToUSD(ctx context.Context) BillingExchangeRate {
	if p == nil {
		return fallbackExchangeRate()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.cached.CNYToUSD > 0 && now.Sub(p.fetched) < exchangeRateCacheTTL {
		return p.cached
	}
	rate, err := p.fetchCNYToUSD(ctx)
	if err != nil || rate.CNYToUSD <= 0 {
		if p.cached.CNYToUSD > 0 {
			p.cached.Stale = true
			return p.cached
		}
		fallback := fallbackExchangeRate()
		p.cached = fallback
		p.fetched = now
		return fallback
	}
	p.cached = rate
	p.fetched = now
	return rate
}

func (p *ExchangeRateProvider) fetchCNYToUSD(ctx context.Context) (BillingExchangeRate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return BillingExchangeRate{}, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return BillingExchangeRate{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return BillingExchangeRate{}, HTTPStatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	}
	var rows []struct {
		Date  string  `json:"date"`
		Base  string  `json:"base"`
		Quote string  `json:"quote"`
		Rate  float64 `json:"rate"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return BillingExchangeRate{}, err
	}
	if len(rows) == 0 || rows[0].Rate <= 0 {
		return BillingExchangeRate{}, billingQueryError("exchange rate response did not include a positive CNY/USD rate")
	}
	rateTime := p.now()
	if rows[0].Date != "" {
		if parsed, err := time.Parse("2006-01-02", rows[0].Date); err == nil {
			rateTime = parsed
		}
	}
	return BillingExchangeRate{CNYToUSD: rows[0].Rate, Time: rateTime}, nil
}

func fallbackExchangeRate() BillingExchangeRate {
	return BillingExchangeRate{
		CNYToUSD: fallbackCNYToUSDRate,
		Time:     time.Now().UTC(),
		Stale:    true,
	}
}
