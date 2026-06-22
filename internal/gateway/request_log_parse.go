package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
)

func requestLogEntryFromBody(requestID string, model string, body []byte) RequestLogEntry {
	entry := RequestLogEntry{
		RequestID:    requestID,
		Model:        model,
		RequestBytes: int64(len(body)),
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return entry
	}
	entry.Stream, _ = payload["stream"].(bool)
	entry.MaxTokens = intFromAny(payload["max_tokens"])
	entry.Temperature = floatPtrFromAny(payload["temperature"])
	entry.TopP = floatPtrFromAny(payload["top_p"])
	entry.TopK = floatPtrFromAny(payload["top_k"])

	messages, ok := payload["messages"].([]any)
	if !ok {
		return entry
	}
	entry.MessageCount = len(messages)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		content, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "image", "image_url":
				entry.ImageCount++
			case "video", "video_url":
				entry.VideoCount++
			case "audio", "audio_url":
				entry.AudioCount++
			}
		}
	}
	return entry
}

func mergeRequestLogEntry(base RequestLogEntry, extra RequestLogEntry) RequestLogEntry {
	base.ResponseBytes = extra.ResponseBytes
	base.PromptTokens = extra.PromptTokens
	base.CompletionTokens = extra.CompletionTokens
	base.TotalTokens = extra.TotalTokens
	base.CacheTokens = extra.CacheTokens
	base.ReasoningTokens = extra.ReasoningTokens
	base.FinishReason = extra.FinishReason
	base.ErrorType = extra.ErrorType
	base.ErrorCode = extra.ErrorCode
	base.ErrorMessage = extra.ErrorMessage
	base.UpstreamURL = extra.UpstreamURL
	return base
}

func parseOpenAIResponseLog(body []byte, entry *RequestLogEntry) {
	trimmed := bytes.TrimSpace(body)
	if bytes.Contains(trimmed, []byte("data:")) {
		parseOpenAIStreamLog(string(trimmed), entry)
		return
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return
	}
	parseOpenAIResponseObject(payload, entry)
}

func parseOpenAIStreamLog(text string, entry *RequestLogEntry) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(data))
		decoder.UseNumber()
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			continue
		}
		parseOpenAIResponseObject(payload, entry)
	}
}

func parseOpenAIResponseObject(payload map[string]any, entry *RequestLogEntry) {
	if usage, ok := payload["usage"].(map[string]any); ok {
		entry.PromptTokens = firstPositiveInt(entry.PromptTokens, intFromAny(usage["prompt_tokens"]))
		entry.CompletionTokens = firstPositiveInt(entry.CompletionTokens, intFromAny(usage["completion_tokens"]))
		entry.TotalTokens = firstPositiveInt(entry.TotalTokens, intFromAny(usage["total_tokens"]))
		entry.ReasoningTokens = firstPositiveInt(entry.ReasoningTokens, intFromAny(usage["reasoning_tokens"]))
		entry.CacheTokens = firstPositiveInt(entry.CacheTokens, intFromAny(usage["cache_tokens"]))
		if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
			entry.CacheTokens = firstPositiveInt(entry.CacheTokens, intFromAny(details["cached_tokens"]))
		}
	}
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
				entry.FinishReason = finish
				break
			}
		}
	}
	if rawErr, ok := payload["error"].(map[string]any); ok {
		entry.ErrorType = stringFromAny(rawErr["type"])
		entry.ErrorCode = stringFromAny(rawErr["code"])
		entry.ErrorMessage = stringFromAny(rawErr["message"])
	}
}

func firstPositiveInt(current int, next int) int {
	if next > 0 {
		return next
	}
	return current
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Int64()
		if err == nil {
			return int(number)
		}
		floatValue, err := typed.Float64()
		if err == nil {
			return int(floatValue)
		}
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	}
	return 0
}

func floatPtrFromAny(value any) *float64 {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		if err == nil {
			return &number
		}
	case float64:
		return &typed
	case int:
		number := float64(typed)
		return &number
	}
	return nil
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		if value == nil {
			return ""
		}
		data, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(data)
	}
}
