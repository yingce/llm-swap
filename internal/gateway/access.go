package gateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const DefaultGatewayStatsPath = "/opt/llmswap/data/gateway-stats.json"

type AccessTracker struct {
	mu          sync.RWMutex
	models      map[string]AccessRecord
	workerModel map[string]map[string]AccessRecord
}

type AccessRecord struct {
	LastAccess time.Time `json:"last_access"`
	Count      uint64    `json:"count"`
}

type accessSnapshot struct {
	Models      map[string]AccessRecord            `json:"models"`
	WorkerModel map[string]map[string]AccessRecord `json:"worker_models"`
}

func NewAccessTracker() *AccessTracker {
	return &AccessTracker{
		models:      make(map[string]AccessRecord),
		workerModel: make(map[string]map[string]AccessRecord),
	}
}

func LoadAccessTracker(path string) (*AccessTracker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewAccessTracker(), nil
		}
		return nil, err
	}

	var snapshot accessSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	tracker := NewAccessTracker()
	for model, record := range snapshot.Models {
		tracker.models[model] = record
	}
	for workerID, models := range snapshot.WorkerModel {
		if tracker.workerModel[workerID] == nil {
			tracker.workerModel[workerID] = make(map[string]AccessRecord)
		}
		for model, record := range models {
			tracker.workerModel[workerID][model] = record
		}
	}
	return tracker, nil
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

func (a *AccessTracker) Save(path string) error {
	if a == nil || path == "" {
		return nil
	}
	snapshot := a.snapshot()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (a *AccessTracker) snapshot() accessSnapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()

	out := accessSnapshot{
		Models:      make(map[string]AccessRecord, len(a.models)),
		WorkerModel: make(map[string]map[string]AccessRecord, len(a.workerModel)),
	}
	for model, record := range a.models {
		out.Models[model] = record
	}
	for workerID, models := range a.workerModel {
		out.WorkerModel[workerID] = make(map[string]AccessRecord, len(models))
		for model, record := range models {
			out.WorkerModel[workerID][model] = record
		}
	}
	return out
}
