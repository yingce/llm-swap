package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type MetricsScraper struct {
	client *http.Client
	mu     sync.Mutex
	seen   map[string]struct{}
}

func NewMetricsScraper() *MetricsScraper {
	return &MetricsScraper{
		client: &http.Client{Timeout: 3 * time.Second},
		seen:   make(map[string]struct{}),
	}
}

func (s *MetricsScraper) PullActivity(workerID string, baseURL string) (int, error) {
	resp, err := s.client.Get(strings.TrimRight(baseURL, "/") + "/api/metrics")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf("worker metrics returned %s", resp.Status)
	}

	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	newRows := 0
	for _, row := range rows {
		key := workerID + "\x00" + stableActivityKey(row)
		if _, ok := s.seen[key]; ok {
			continue
		}
		s.seen[key] = struct{}{}
		newRows++
	}
	return newRows, nil
}

func stableActivityKey(row map[string]any) string {
	for _, field := range []string{"id", "request_id", "timestamp", "created_at"} {
		if value, ok := stableScalar(row[field]); ok {
			return field + "=" + value
		}
	}

	var parts []string
	for _, field := range []string{"model", "path", "duration_ms"} {
		if value, ok := stableScalar(row[field]); ok {
			parts = append(parts, field+"="+value)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "|")
	}

	encoded, err := json.Marshal(row)
	if err != nil {
		return fmt.Sprintf("%#v", row)
	}
	return string(encoded)
}

func stableScalar(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
		return "", false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}
