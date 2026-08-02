package gateway

import (
	"context"
	"net/http"
	"time"
)

type uiTrafficSummary struct {
	GeneratedAt      time.Time `json:"generated_at"`
	Range            string    `json:"range"`
	Start            time.Time `json:"start"`
	End              time.Time `json:"end"`
	Requests         int       `json:"requests"`
	Status2xx        int       `json:"status_2xx"`
	Status4xx        int       `json:"status_4xx"`
	Status5xx        int       `json:"status_5xx"`
	Non200           int       `json:"non_200"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	CacheTokens      int64     `json:"cache_tokens"`
	ReasoningTokens  int64     `json:"reasoning_tokens"`
	AvgDurationMS    int64     `json:"avg_duration_ms"`
	MaxDurationMS    int64     `json:"max_duration_ms"`
}

type trafficSummaryStore interface {
	TrafficSummary(context.Context, time.Time, time.Time) (uiTrafficSummary, error)
}

func (s *Server) handleUITraffic(w http.ResponseWriter, r *http.Request) {
	if s.recordsStore == nil {
		http.Error(w, "records store is not enabled", http.StatusServiceUnavailable)
		return
	}
	store, ok := s.recordsStore.(trafficSummaryStore)
	if !ok {
		http.Error(w, "records store does not support traffic summaries", http.StatusServiceUnavailable)
		return
	}

	now := time.Now().UTC()
	start, end, _, selectedRange := parseMetricsRange(r.URL.Query().Get("range"), "", "24h", "7d", now)
	summary, err := store.TrafficSummary(r.Context(), start, end)
	if err != nil {
		http.Error(w, "failed to query traffic summary", http.StatusInternalServerError)
		return
	}
	summary.GeneratedAt = now
	summary.Range = selectedRange
	summary.Start = start
	summary.End = end
	writeJSON(w, summary)
}

func (s *PostgresRecordsStore) TrafficSummary(ctx context.Context, start, end time.Time) (uiTrafficSummary, error) {
	records, err := s.billingRequests(ctx, start, end)
	if err != nil {
		return uiTrafficSummary{}, err
	}
	return summarizeTrafficRecords(records, start, end), nil
}

func summarizeTrafficRecords(records []billingRequestRecord, start, end time.Time) uiTrafficSummary {
	summary := uiTrafficSummary{Start: start, End: end}
	var durationTotal int64
	for _, record := range records {
		if record.Time.Before(start) || !record.Time.Before(end) {
			continue
		}
		summary.Requests++
		switch {
		case record.StatusCode >= 200 && record.StatusCode < 300:
			summary.Status2xx++
		case record.StatusCode >= 400 && record.StatusCode < 500:
			summary.Status4xx++
		case record.StatusCode >= 500 && record.StatusCode < 600:
			summary.Status5xx++
		}
		if record.StatusCode != http.StatusOK {
			summary.Non200++
		}
		summary.PromptTokens += int64(record.PromptTokens)
		summary.CompletionTokens += int64(record.CompletionTokens)
		summary.TotalTokens += int64(record.TotalTokens)
		summary.CacheTokens += int64(record.CacheTokens)
		summary.ReasoningTokens += int64(record.ReasoningTokens)
		durationTotal += record.DurationMS
		if record.DurationMS > summary.MaxDurationMS {
			summary.MaxDurationMS = record.DurationMS
		}
	}
	if summary.Requests > 0 {
		summary.AvgDurationMS = (durationTotal + int64(summary.Requests)/2) / int64(summary.Requests)
	}
	return summary
}
