package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type HistoricalSeries struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Points []HistoricalPoint `json:"points"`
}

type HistoricalPoint struct {
	Timestamp float64 `json:"ts"`
	Value     float64 `json:"value"`
}

type VictoriaMetricsClient struct {
	baseURL string
	client  *http.Client
}

func NewVictoriaMetricsClient(baseURL string, timeout time.Duration) *VictoriaMetricsClient {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &VictoriaMetricsClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *VictoriaMetricsClient) QueryRange(ctx context.Context, name, query string, start, end time.Time, step time.Duration) ([]HistoricalSeries, error) {
	if c == nil || c.baseURL == "" {
		return nil, fmt.Errorf("victoriametrics query URL is not configured")
	}
	if step <= 0 {
		step = time.Minute
	}
	values := url.Values{}
	values.Set("query", query)
	values.Set("start", strconv.FormatFloat(float64(start.Unix()), 'f', -1, 64))
	values.Set("end", strconv.FormatFloat(float64(end.Unix()), 'f', -1, 64))
	values.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/prometheus/api/v1/query_range?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, HTTPStatusError{StatusCode: resp.StatusCode}
	}

	var decoded vmQueryRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if decoded.Status != "success" {
		if decoded.Error != "" {
			return nil, fmt.Errorf("victoriametrics query failed: %s", decoded.Error)
		}
		return nil, fmt.Errorf("victoriametrics query failed with status %q", decoded.Status)
	}

	series := make([]HistoricalSeries, 0, len(decoded.Data.Result))
	for _, result := range decoded.Data.Result {
		item := HistoricalSeries{
			Name:   name,
			Labels: result.Metric,
			Points: make([]HistoricalPoint, 0, len(result.Values)),
		}
		for _, raw := range result.Values {
			if len(raw) != 2 {
				continue
			}
			ts, ok := vmNumber(raw[0])
			if !ok {
				continue
			}
			value, ok := vmNumber(raw[1])
			if !ok {
				continue
			}
			item.Points = append(item.Points, HistoricalPoint{Timestamp: ts, Value: value})
		}
		series = append(series, item)
	}
	return series, nil
}

type vmQueryRangeResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Values [][]any           `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func vmNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func parseMetricsRange(rawRange, rawStep, defaultRange, maxRange string, now time.Time) (time.Time, time.Time, time.Duration, string) {
	selectedRange, selectedLabel, ok := parseMetricsDurationWithLabel(rawRange)
	if !ok || selectedRange <= 0 {
		selectedRange, selectedLabel, _ = parseMetricsDurationWithLabel(defaultRange)
	}
	if selectedRange <= 0 {
		selectedRange = time.Hour
		selectedLabel = "1h"
	}

	maxDuration, maxLabel, ok := parseMetricsDurationWithLabel(maxRange)
	if ok && maxDuration > 0 && selectedRange > maxDuration {
		selectedRange = maxDuration
		selectedLabel = maxLabel
	}

	step, _, ok := parseMetricsDurationWithLabel(rawStep)
	if !ok || step <= 0 {
		step = defaultMetricsStep(selectedRange)
	}
	if step > selectedRange {
		step = selectedRange
	}
	return now.Add(-selectedRange), now, step, selectedLabel
}

func parseMetricsDurationWithLabel(raw string) (time.Duration, string, bool) {
	label := strings.TrimSpace(raw)
	if label == "" {
		return 0, "", false
	}
	if strings.HasSuffix(label, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(label, "d"))
		if err != nil || days <= 0 {
			return 0, "", false
		}
		return time.Duration(days) * 24 * time.Hour, label, true
	}
	duration, err := time.ParseDuration(label)
	if err != nil || duration <= 0 {
		return 0, "", false
	}
	return duration, label, true
}

func defaultMetricsStep(window time.Duration) time.Duration {
	step := window / 120
	switch {
	case step < 15*time.Second:
		return 15 * time.Second
	case step > 5*time.Minute:
		return 5 * time.Minute
	default:
		return step
	}
}
