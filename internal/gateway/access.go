package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const defaultReplayRequestLogLines = 1000

type AccessTracker struct {
	mu          sync.RWMutex
	models      map[string]AccessRecord
	workerModel map[string]map[string]AccessRecord
}

type AccessRecord struct {
	LastAccess       time.Time         `json:"last_access"`
	Count            uint64            `json:"count"`
	StatusCounts     map[string]uint64 `json:"status_counts,omitempty"`
	PromptTokens     uint64            `json:"prompt_tokens,omitempty"`
	CompletionTokens uint64            `json:"completion_tokens,omitempty"`
	TotalTokens      uint64            `json:"total_tokens,omitempty"`
	CacheTokens      uint64            `json:"cache_tokens,omitempty"`
	ReasoningTokens  uint64            `json:"reasoning_tokens,omitempty"`
	DurationMS       uint64            `json:"duration_ms,omitempty"`
	MaxDurationMS    uint64            `json:"max_duration_ms,omitempty"`
}

func NewAccessTracker() *AccessTracker {
	return &AccessTracker{
		models:      make(map[string]AccessRecord),
		workerModel: make(map[string]map[string]AccessRecord),
	}
}

func LoadAccessTrackerFromRequestLog(path string) (*AccessTracker, error) {
	tracker := NewAccessTracker()
	if path == "" {
		return tracker, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tracker, nil
		}
		return nil, err
	}
	defer file.Close()

	lines, err := recentRequestLogLines(file, defaultReplayRequestLogLines)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		var entry RequestLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		tracker.RecordRequest(entry)
	}
	return tracker, nil
}

func recentRequestLogLines(file *os.File, limit int) ([][]byte, error) {
	if limit <= 0 {
		limit = defaultReplayRequestLogLines
	}
	ring := make([][]byte, limit)
	count := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		ring[count%limit] = append([]byte(nil), line...)
		count++
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	total := count
	if total > limit {
		total = limit
	}
	start := 0
	if count > limit {
		start = count % limit
	}
	lines := make([][]byte, 0, total)
	for i := 0; i < total; i++ {
		lines = append(lines, ring[(start+i)%limit])
	}
	return lines, nil
}

func (a *AccessTracker) Record(model string, workerID string, now time.Time) {
	if a == nil || model == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	modelRecord := a.models[model]
	modelRecord.LastAccess = now
	modelRecord.Count++
	a.models[model] = modelRecord
	if workerID == "" {
		return
	}
	if a.workerModel[workerID] == nil {
		a.workerModel[workerID] = make(map[string]AccessRecord)
	}
	workerRecord := a.workerModel[workerID][model]
	workerRecord.LastAccess = now
	workerRecord.Count++
	a.workerModel[workerID][model] = workerRecord
}

func (a *AccessTracker) RecordRequest(entry RequestLogEntry) {
	if a == nil || entry.Model == "" {
		return
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now()
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	modelRecord := applyRequestStats(a.models[entry.Model], entry)
	a.models[entry.Model] = modelRecord
	if entry.WorkerID == "" {
		return
	}
	if a.workerModel[entry.WorkerID] == nil {
		a.workerModel[entry.WorkerID] = make(map[string]AccessRecord)
	}
	workerRecord := applyRequestStats(a.workerModel[entry.WorkerID][entry.Model], entry)
	a.workerModel[entry.WorkerID][entry.Model] = workerRecord
}

func applyRequestStats(record AccessRecord, entry RequestLogEntry) AccessRecord {
	record.LastAccess = entry.Time
	record.Count++
	if entry.StatusCode > 0 {
		if record.StatusCounts == nil {
			record.StatusCounts = make(map[string]uint64)
		}
		record.StatusCounts[fmt.Sprintf("%d", entry.StatusCode)]++
	}
	record.PromptTokens += uint64(maxInt(entry.PromptTokens, 0))
	record.CompletionTokens += uint64(maxInt(entry.CompletionTokens, 0))
	record.TotalTokens += uint64(maxInt(entry.TotalTokens, 0))
	record.CacheTokens += uint64(maxInt(entry.CacheTokens, 0))
	record.ReasoningTokens += uint64(maxInt(entry.ReasoningTokens, 0))
	duration := uint64(maxInt64(entry.DurationMS, 0))
	record.DurationMS += duration
	if duration > record.MaxDurationMS {
		record.MaxDurationMS = duration
	}
	return record
}

func (a *AccessTracker) ModelLastAccess(model string) time.Time {
	if a == nil {
		return time.Time{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.models[model].LastAccess
}

func (a *AccessTracker) WorkerModelLastAccess(workerID string, model string) time.Time {
	if a == nil {
		return time.Time{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workerModel[workerID][model].LastAccess
}

func (a *AccessTracker) ModelCount(model string) uint64 {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.models[model].Count
}

func (a *AccessTracker) WorkerModelCount(workerID string, model string) uint64 {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workerModel[workerID][model].Count
}

func (a *AccessTracker) ModelTotalTokens(model string) uint64 {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.models[model].TotalTokens
}

func (a *AccessTracker) ModelRecord(model string) AccessRecord {
	if a == nil {
		return AccessRecord{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneAccessRecord(a.models[model])
}

func (a *AccessTracker) WorkerModelStatusCount(workerID string, model string, statusCode int) uint64 {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.workerModel[workerID][model].StatusCounts[fmt.Sprintf("%d", statusCode)]
}

func cloneAccessRecord(record AccessRecord) AccessRecord {
	if record.StatusCounts == nil {
		return record
	}
	statusCounts := make(map[string]uint64, len(record.StatusCounts))
	for status, count := range record.StatusCounts {
		statusCounts[status] = count
	}
	record.StatusCounts = statusCounts
	return record
}

func maxInt(value int, floor int) int {
	if value < floor {
		return floor
	}
	return value
}

func maxInt64(value int64, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}
