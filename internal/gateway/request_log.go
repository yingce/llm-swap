package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultGatewayRequestLogPath = "/opt/llmswap/logs/gateway-requests.jsonl"

type RequestLogEntry struct {
	Time             time.Time  `json:"time"`
	RequestID        string     `json:"request_id"`
	Model            string     `json:"model"`
	WorkerID         string     `json:"worker_id,omitempty"`
	Tag              string     `json:"tag,omitempty"`
	StatusCode       int        `json:"status_code"`
	DurationMS       int64      `json:"duration_ms"`
	Stream           bool       `json:"stream"`
	RequestBytes     int64      `json:"request_bytes"`
	ResponseBytes    int64      `json:"response_bytes"`
	MessageCount     int        `json:"message_count,omitempty"`
	ImageCount       int        `json:"image_count,omitempty"`
	VideoCount       int        `json:"video_count,omitempty"`
	AudioCount       int        `json:"audio_count,omitempty"`
	MaxTokens        int        `json:"max_tokens,omitempty"`
	Temperature      *float64   `json:"temperature,omitempty"`
	TopP             *float64   `json:"top_p,omitempty"`
	TopK             *float64   `json:"top_k,omitempty"`
	PromptTokens     int        `json:"prompt_tokens,omitempty"`
	CompletionTokens int        `json:"completion_tokens,omitempty"`
	TotalTokens      int        `json:"total_tokens,omitempty"`
	CacheTokens      int        `json:"cache_tokens,omitempty"`
	ReasoningTokens  int        `json:"reasoning_tokens,omitempty"`
	FinishReason     string     `json:"finish_reason,omitempty"`
	ErrorType        string     `json:"error_type,omitempty"`
	ErrorCode        string     `json:"error_code,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	RetryCount       int        `json:"retry_count,omitempty"`
	UpstreamURL      string     `json:"upstream_url,omitempty"`
	RequestHeaders   httpHeader `json:"request_headers,omitempty"`
	CostByTokenRMB   float64    `json:"cost_by_token_rmb,omitempty"`
	CostByRequestRMB float64    `json:"cost_by_request_rmb,omitempty"`
	ModelUsedCostUSD float64    `json:"model_used_cost_usd,omitempty"`
	CostCalculatedAt *time.Time `json:"cost_calculated_at,omitempty"`
}

type httpHeader map[string]string

func (h *httpHeader) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := httpHeader{}
	for key, value := range raw {
		switch typed := value.(type) {
		case string:
			if typed != "" {
				out[key] = typed
			}
		case []any:
			values := make([]string, 0, len(typed))
			for _, item := range typed {
				text, ok := item.(string)
				if ok && text != "" {
					values = append(values, text)
				}
			}
			if len(values) > 0 {
				out[key] = strings.Join(values, ", ")
			}
		}
	}
	if len(out) == 0 {
		*h = nil
		return nil
	}
	*h = out
	return nil
}

func appendRequestLog(path string, entry RequestLogEntry) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func loadRequestLogPage(path string, offset int, limit int) (uiRequestsResponse, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = uiEventLimit
	}
	if limit > 500 {
		limit = 500
	}
	lines, err := loadRecentLogLines(path, offset+limit+1)
	if err != nil {
		return uiRequestsResponse{}, err
	}
	requests := make([]RequestLogEntry, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		var entry RequestLogEntry
		if err := json.Unmarshal(lines[i], &entry); err != nil {
			continue
		}
		requests = append(requests, entry)
	}
	if offset > len(requests) {
		offset = len(requests)
	}
	end := offset + limit
	if end > len(requests) {
		end = len(requests)
	}
	page := append([]RequestLogEntry(nil), requests[offset:end]...)
	return uiRequestsResponse{
		Requests:   page,
		NextOffset: offset + len(page),
		HasMore:    len(requests) > end,
	}, nil
}

func loadRecentRequestLogs(path string, limit int) ([]RequestLogEntry, error) {
	lines, err := loadRecentLogLines(path, limit)
	if err != nil {
		return nil, err
	}
	requests := make([]RequestLogEntry, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		var entry RequestLogEntry
		if err := json.Unmarshal(lines[i], &entry); err != nil {
			continue
		}
		requests = append(requests, entry)
	}
	return requests, nil
}

func (s *Server) recentRequestLogs() []RequestLogEntry {
	if s == nil {
		return []RequestLogEntry{}
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if len(s.recentRequests) == 0 {
		return []RequestLogEntry{}
	}
	out := append([]RequestLogEntry(nil), s.recentRequests...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
