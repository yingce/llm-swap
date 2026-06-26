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
const defaultMetricsScrapeTimeout = 10 * time.Second

type MetricsScraper struct {
	client               *http.Client
	bearerToken          string
	mu                   sync.Mutex
	seen                 map[string]struct{}
	seenOrder            []string
	seenPerformance      map[string]struct{}
	seenPerformanceOrder []string
	maxSeenKeys          int
}

type ActivityStats struct {
	Rows     int
	Requests []ActivityRequestStats
}

type ActivityRequestStats struct {
	Model                  string
	Path                   string
	StatusCode             int
	DurationMS             float64
	Tokens                 map[string]float64
	PromptTokensPerSec     float64
	CompletionTokensPerSec float64
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
		client:               &http.Client{Timeout: defaultMetricsScrapeTimeout},
		bearerToken:          token,
		seen:                 make(map[string]struct{}),
		seenOrder:            make([]string, 0, maxSeenKeys),
		seenPerformance:      make(map[string]struct{}),
		seenPerformanceOrder: make([]string, 0, maxSeenKeys),
		maxSeenKeys:          maxSeenKeys,
	}
}

func (s *MetricsScraper) PullActivity(workerID string, baseURL string) (ActivityStats, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/metrics", nil)
	if err != nil {
		return ActivityStats{}, err
	}
	if s.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.bearerToken)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return ActivityStats{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return ActivityStats{}, fmt.Errorf("worker metrics returned %s", resp.Status)
	}

	rows, err := decodeRowPayload(resp.Body, "requests", "activity", "rows", "data")
	if err != nil {
		return ActivityStats{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var stats ActivityStats
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
		stats.Rows++
		stats.Requests = append(stats.Requests, parseActivityRequest(row))
	}
	return stats, nil
}

func (s *MetricsScraper) PullPerformance(workerID string, baseURL string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/performance", nil)
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
		return 0, fmt.Errorf("worker performance returned %s", resp.Status)
	}

	rows, err := decodeRowPayload(resp.Body, "gpu_stats", "performance", "rows", "data")
	if err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	newRows := 0
	pullSeen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		key := workerID + "\x00" + stablePerformanceKey(row)
		if _, ok := pullSeen[key]; ok {
			continue
		}
		pullSeen[key] = struct{}{}
		if _, ok := s.seenPerformance[key]; ok {
			continue
		}
		s.rememberPerformanceKey(key)
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

func (s *MetricsScraper) rememberPerformanceKey(key string) {
	s.seenPerformance[key] = struct{}{}
	s.seenPerformanceOrder = append(s.seenPerformanceOrder, key)
	for len(s.seenPerformanceOrder) > s.maxSeenKeys {
		oldest := s.seenPerformanceOrder[0]
		delete(s.seenPerformance, oldest)
		s.seenPerformanceOrder[0] = ""
		s.seenPerformanceOrder = s.seenPerformanceOrder[1:]
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

func stablePerformanceKey(row map[string]any) string {
	var parts []string
	for _, field := range []string{"timestamp", "time", "device", "gpu", "metric", "type", "name"} {
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

func parseActivityRequest(row map[string]any) ActivityRequestStats {
	tokens := make(map[string]float64)
	if rawTokens, ok := objectField(row["tokens"]); ok {
		addNonNegativeToken(tokens, "input", rawTokens["input_tokens"])
		addNonNegativeToken(tokens, "output", rawTokens["output_tokens"])
		addNonNegativeToken(tokens, "cache", rawTokens["cache_tokens"])
		addNonNegativeToken(tokens, "draft", rawTokens["draft_tokens"])
		addNonNegativeToken(tokens, "draft_accepted", rawTokens["draft_acc_tokens"])
	}
	return ActivityRequestStats{
		Model:                  firstString(row, "model"),
		Path:                   firstString(row, "req_path", "path"),
		StatusCode:             int(numberField(row["resp_status_code"])),
		DurationMS:             numberField(row["duration_ms"]),
		Tokens:                 tokens,
		PromptTokensPerSec:     nonNegativeNumber(nestedField(row, "tokens", "prompt_per_second")),
		CompletionTokensPerSec: nonNegativeNumber(nestedField(row, "tokens", "tokens_per_second")),
	}
}

func addNonNegativeToken(out map[string]float64, name string, value any) {
	number := numberField(value)
	if number < 0 {
		return
	}
	out[name] = number
}

func objectField(value any) (map[string]any, bool) {
	obj, ok := value.(map[string]any)
	return obj, ok
}

func nestedField(row map[string]any, object, field string) any {
	obj, ok := objectField(row[object])
	if !ok {
		return nil
	}
	return obj[field]
}

func firstString(row map[string]any, fields ...string) string {
	for _, field := range fields {
		if value, ok := row[field].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func numberField(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	default:
		return 0
	}
}

func nonNegativeNumber(value any) float64 {
	number := numberField(value)
	if number < 0 {
		return 0
	}
	return number
}

func decodeRowPayload(body interface{ Read([]byte) (int, error) }, objectKeys ...string) ([]map[string]any, error) {
	decoder := json.NewDecoder(body)
	decoder.UseNumber()

	var raw any
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}

	switch typed := raw.(type) {
	case []any:
		return rowsFromSlice(typed)
	case map[string]any:
		for _, key := range objectKeys {
			value, ok := typed[key]
			if !ok {
				continue
			}
			rows, err := rowsFromValue(value)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			return rows, nil
		}
		return nil, fmt.Errorf("unsupported JSON object payload")
	default:
		return nil, fmt.Errorf("unsupported JSON payload type %T", raw)
	}
}

func rowsFromValue(value any) ([]map[string]any, error) {
	slice, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array, got %T", value)
	}
	return rowsFromSlice(slice)
}

func rowsFromSlice(values []any) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, len(values))
	for _, value := range values {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected object row, got %T", value)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
