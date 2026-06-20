package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"llm-swap/internal/protocol"
)

func WriteConfigIfChanged(path string, content []byte, service Service) (bool, error) {
	old, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(old, content) {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return false, err
	}
	cleanup = false
	return true, nil
}

func RestartAfterGatewayAllows(ctx context.Context, service Service) error {
	return service.Restart(ctx)
}

func BuildHeartbeat(agentID string, tags []string, llamaSwapURL string, cfg protocol.AgentConfigResponse, needsRestart bool) protocol.HeartbeatRequest {
	copiedTags := append([]string(nil), tags...)
	return protocol.HeartbeatRequest{
		AgentID:      agentID,
		Tags:         copiedTags,
		LlamaSwapURL: llamaSwapURL,
		Artifacts:    map[string]string{},
		Capacity:     cfg.TagPolicy.WorkerDefaults,
		NeedsRestart: needsRestart,
	}
}
