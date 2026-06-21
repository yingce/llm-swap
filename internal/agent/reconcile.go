package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"llm-swap/internal/protocol"
)

const ReconcileInterval = 3 * time.Second

type GatewayClient interface {
	GetConfigContext(context.Context, []string) (protocol.AgentConfigResponse, error)
	HeartbeatContext(context.Context, protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error)
}

type Reconciler struct {
	AgentID         string
	Tags            []string
	ModelRoot       string
	LlamaSwapConfig string
	LlamaSwapURL    string
	LlamaSwapToken  string
	Gateway         GatewayClient
	HTTPClient      *http.Client
	Service         Service

	needsRestart bool
}

func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(ReconcileInterval)
	defer ticker.Stop()

	for {
		if _, err := r.Reconcile(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("agent reconcile error: %v", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (r *Reconciler) Reconcile(ctx context.Context) (protocol.HeartbeatResponse, error) {
	if r.Gateway == nil {
		return protocol.HeartbeatResponse{}, fmt.Errorf("agent gateway client is required")
	}

	cfg, err := r.Gateway.GetConfigContext(ctx, r.Tags)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}

	artifactStatus, reconcileErr := r.installAllowedArtifacts(ctx, cfg)

	content, err := RenderLlamaSwapConfig(cfg, r.ModelRoot, r.LlamaSwapToken)
	if err != nil {
		reconcileErr = errors.Join(reconcileErr, err)
	} else {
		changed, err := WriteConfigIfChanged(r.LlamaSwapConfig, content, r.Service)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
		if changed {
			r.needsRestart = true
		}
	}

	hb := BuildHeartbeat(r.AgentID, r.Tags, r.LlamaSwapURL, cfg, r.needsRestart, artifactStatus)
	if reconcileErr != nil {
		hb.LastError = reconcileErr.Error()
	}

	resp, err := r.Gateway.HeartbeatContext(ctx, hb)
	if err != nil {
		return resp, errors.Join(reconcileErr, err)
	}

	if resp.RestartAllowed && r.needsRestart {
		if err := r.restart(ctx); err != nil {
			return resp, errors.Join(reconcileErr, err)
		}
		r.needsRestart = false
	}

	return resp, reconcileErr
}

func (r *Reconciler) installAllowedArtifacts(ctx context.Context, cfg protocol.AgentConfigResponse) (map[string]string, error) {
	status := make(map[string]string, len(cfg.TagPolicy.AllowedModels))
	var outErr error
	for _, modelName := range cfg.TagPolicy.AllowedModels {
		model, ok := cfg.Models[modelName]
		if !ok {
			status[modelName] = "missing"
			outErr = errors.Join(outErr, fmt.Errorf("allowed model %q missing from config models", modelName))
			continue
		}
		if _, err := InstallArtifact(ctx, r.HTTPClient, cfg.OSS.BaseURL, r.ModelRoot, modelName, model.Artifact); err != nil {
			status[modelName] = "error"
			outErr = errors.Join(outErr, fmt.Errorf("install artifact for %q: %w", modelName, err))
			continue
		}
		status[modelName] = "ready"
	}
	return status, outErr
}

func (r *Reconciler) restart(ctx context.Context) error {
	if r.Service == nil {
		return LoggingService{}.Restart(ctx)
	}
	return r.Service.Restart(ctx)
}

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

func BuildHeartbeat(agentID string, tags []string, llamaSwapURL string, cfg protocol.AgentConfigResponse, needsRestart bool, artifactStatuses ...map[string]string) protocol.HeartbeatRequest {
	copiedTags := append([]string(nil), tags...)
	artifacts := make(map[string]string, len(cfg.TagPolicy.AllowedModels))
	if len(artifactStatuses) > 0 {
		for model, status := range artifactStatuses[0] {
			artifacts[model] = status
		}
	}
	for _, model := range cfg.TagPolicy.AllowedModels {
		if _, ok := artifacts[model]; !ok {
			artifacts[model] = "missing"
		}
	}
	return protocol.HeartbeatRequest{
		AgentID:      agentID,
		Tags:         copiedTags,
		LlamaSwapURL: llamaSwapURL,
		Artifacts:    artifacts,
		Capacity:     cfg.TagPolicy.WorkerDefaults,
		NeedsRestart: needsRestart,
	}
}
