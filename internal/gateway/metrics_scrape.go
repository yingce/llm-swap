package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// llama-swap /api/metrics is historical; keep recent rows without retaining one key per request forever.
const defaultMaxSeenActivityKeys = 10000

type MetricsScraper struct {
	client      *http.Client
	bearerToken string
	mu          sync.Mutex
	seen        map[string]struct{}
	seenOrder   []string
	maxSeenKeys int
}

func NewMetricsScraper() *MetricsScraper {
	return newMetricsScraperWithMaxSeen(defaultMaxSeenActivityKeys)
}

func NewMetricsScraperWithToken(token string) *MetricsScraper {
	return newMetricsScraperWithMaxSeenAndToken(defaultMaxSeenActivityKeys, token)
}

func newMetricsScraperWithMaxSeen(maxSeenKeys int) *MetricsScraper {
	return newMetricsScraperWithMaxSeenAndToken(maxSeenKeys, "")
}

func newMetricsScraperWithMaxSeenAndToken(maxSeenKeys int, token string) *MetricsScraper {
	if maxSeenKeys <= 0 {
		maxSeenKeys = defaultMaxSeenActivityKeys
	}
	return &MetricsScraper{
		client:      &http.Client{Timeout: 3 * time.Second},
		bearerToken: token,
		seen:        make(map[string]struct{}),
		seenOrder:   make([]string, 0, maxSeenKeys),
		maxSeenKeys: maxSeenKeys,
	}
}

func (s *MetricsScraper) PullActivity(workerID string, baseURL string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/metrics", nil)
	if err != nil {
		return 0, err
	}
	if s.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearerToken)
	}

	resp, err := s.client.Do(req)
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
	pullSeen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		key := workerID + "\x00" + stableActivityKey(row)
		if _, ok := pullSeen[key]; ok {
			continue
		}
		pullSeen[key] = struct{}{}
		if _, ok := s.seen[key]; ok {
			continue
		}
		s.rememberActivityKey(key)
		newRows++
	}
	return newRows, nil
}

func (s *MetricsScraper) rememberActivityKey(key string) {
	s.seen[key] = struct{}{}
	s.seenOrder = append(s.seenOrder, key)
	for len(s.seenOrder) > s.maxSeenKeys {
		oldest := s.seenOrder[0]
		delete(s.seen, oldest)
		s.seenOrder[0] = ""
		s.seenOrder = s.seenOrder[1:]
	}
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
