package gateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const DefaultGatewayRequestLogPath = "/opt/llmswap/logs/gateway-requests.jsonl"

type RequestLogEntry struct {
	Time             time.Time `json:"time"`
	RequestID        string    `json:"request_id"`
	Model            string    `json:"model"`
	WorkerID         string    `json:"worker_id,omitempty"`
	Tag              string    `json:"tag,omitempty"`
	StatusCode       int       `json:"status_code"`
	DurationMS       int64     `json:"duration_ms"`
	Stream           bool      `json:"stream"`
	RequestBytes     int64     `json:"request_bytes"`
	ResponseBytes    int64     `json:"response_bytes"`
	MessageCount     int       `json:"message_count,omitempty"`
	ImageCount       int       `json:"image_count,omitempty"`
	VideoCount       int       `json:"video_count,omitempty"`
	AudioCount       int       `json:"audio_count,omitempty"`
	MaxTokens        int       `json:"max_tokens,omitempty"`
	Temperature      *float64  `json:"temperature,omitempty"`
	TopP             *float64  `json:"top_p,omitempty"`
	TopK             *float64  `json:"top_k,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty"`
	TotalTokens      int       `json:"total_tokens,omitempty"`
	CacheTokens      int       `json:"cache_tokens,omitempty"`
	ReasoningTokens  int       `json:"reasoning_tokens,omitempty"`
	FinishReason     string    `json:"finish_reason,omitempty"`
	ErrorType        string    `json:"error_type,omitempty"`
	ErrorCode        string    `json:"error_code,omitempty"`
	ErrorMessage     string    `json:"error_message,omitempty"`
	RetryCount       int       `json:"retry_count,omitempty"`
	UpstreamURL      string    `json:"upstream_url,omitempty"`
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
