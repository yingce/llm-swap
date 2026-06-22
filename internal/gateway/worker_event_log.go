package gateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const DefaultGatewayWorkerEventLogPath = "/opt/llmswap/logs/gateway-worker-events.jsonl"

func appendWorkerEventLog(path string, entry uiAgentEvent) error {
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

func loadRecentWorkerEvents(path string, limit int) ([]uiAgentEvent, error) {
	lines, err := loadRecentLogLines(path, limit)
	if err != nil {
		return nil, err
	}
	events := make([]uiAgentEvent, 0, len(lines))
	for _, line := range lines {
		var event uiAgentEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

func loadWorkerEventPage(path string, offset int, limit int) (uiEventsResponse, error) {
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
		return uiEventsResponse{}, err
	}
	events := make([]uiAgentEvent, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		var event uiAgentEvent
		if err := json.Unmarshal(lines[i], &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	if offset > len(events) {
		offset = len(events)
	}
	end := offset + limit
	if end > len(events) {
		end = len(events)
	}
	page := append([]uiAgentEvent(nil), events[offset:end]...)
	return uiEventsResponse{
		Events:     page,
		NextOffset: offset + len(page),
		HasMore:    len(events) > end,
	}, nil
}

func loadRecentLogLines(path string, limit int) ([][]byte, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	return recentRequestLogLines(file, limit)
}
