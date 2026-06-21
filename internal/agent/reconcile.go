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
	RunInterval     time.Duration

	needsRestart bool
}

func (r *Reconciler) Run(ctx context.Context) error {
	interval := r.RunInterval
	if interval <= 0 {
		interval = ReconcileInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	installs := make(map[string]*artifactInstallState)
	installDone := make(chan artifactInstallResult, 64)

	for {
		if _, err := r.reconcileRunOnce(ctx, installs, installDone); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("agent reconcile error: %v", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-installDone:
			r.applyInstallResult(installs, result)
			r.drainInstallResults(installs, installDone)
		case <-ticker.C:
		}
	}
}

type artifactInstallKey struct {
	Model     string
	Object    string
	Kind      string
	CRC64ECMA string
}

type artifactInstallState struct {
	key     artifactInstallKey
	running bool
	err     error
}

type artifactInstallResult struct {
	model string
	key   artifactInstallKey
	err   error
}

func artifactKey(modelName string, artifactObject string, artifactKind string, artifactCRC string) artifactInstallKey {
	return artifactInstallKey{
		Model:     modelName,
		Object:    artifactObject,
		Kind:      artifactKind,
		CRC64ECMA: artifactCRC,
	}
}

func (r *Reconciler) reconcileRunOnce(ctx context.Context, installs map[string]*artifactInstallState, installDone chan artifactInstallResult) (protocol.HeartbeatResponse, error) {
	if r.Gateway == nil {
		return protocol.HeartbeatResponse{}, fmt.Errorf("agent gateway client is required")
	}

	var reconcileErr error
	if pending, err := r.pendingRestart(); err != nil {
		reconcileErr = errors.Join(reconcileErr, err)
	} else if pending {
		r.needsRestart = true
	}

	cfg, err := r.Gateway.GetConfigContext(ctx, r.Tags)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}

	r.drainInstallResults(installs, installDone)
	artifactStatus, installing, err := r.installAllowedArtifactsAsync(ctx, cfg, installs, installDone)
	reconcileErr = errors.Join(reconcileErr, err)

	if !installing {
		content, err := RenderLlamaSwapConfig(cfg, r.ModelRoot, r.LlamaSwapToken)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		} else {
			changed, err := WriteConfigIfChanged(r.LlamaSwapConfig, content, r.Service)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, err)
			}
			if changed {
				if err := r.markPendingRestart(); err != nil {
					reconcileErr = errors.Join(reconcileErr, err)
				} else {
					r.needsRestart = true
				}
			}
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
		if err := r.clearPendingRestart(); err != nil {
			return resp, errors.Join(reconcileErr, err)
		}
		r.needsRestart = false
	}

	return resp, reconcileErr
}

func (r *Reconciler) drainInstallResults(installs map[string]*artifactInstallState, installDone <-chan artifactInstallResult) {
	for {
		select {
		case result := <-installDone:
			r.applyInstallResult(installs, result)
		default:
			return
		}
	}
}

func (r *Reconciler) applyInstallResult(installs map[string]*artifactInstallState, result artifactInstallResult) {
	state, ok := installs[result.model]
	if !ok || state.key != result.key {
		return
	}
	state.running = false
	state.err = result.err
}

func (r *Reconciler) installAllowedArtifactsAsync(ctx context.Context, cfg protocol.AgentConfigResponse, installs map[string]*artifactInstallState, installDone chan artifactInstallResult) (map[string]string, bool, error) {
	status := make(map[string]string, len(cfg.TagPolicy.AllowedModels))
	var outErr error
	var installing bool

	for _, modelName := range cfg.TagPolicy.AllowedModels {
		model, ok := cfg.Models[modelName]
		if !ok {
			status[modelName] = "missing"
			outErr = errors.Join(outErr, fmt.Errorf("allowed model %q missing from config models", modelName))
			continue
		}

		key := artifactKey(modelName, model.Artifact.Object, model.Artifact.Kind, model.Artifact.CRC64ECMA)
		if state, ok := installs[modelName]; ok && state.key == key && state.running {
			status[modelName] = "installing"
			installing = true
			continue
		}

		modelDir := filepath.Join(r.ModelRoot, modelName)
		matches, err := MarkerMatches(modelDir, modelName, model.Artifact)
		if err != nil {
			status[modelName] = "error"
			outErr = errors.Join(outErr, fmt.Errorf("read artifact marker for %q: %w", modelName, err))
			continue
		}
		if matches {
			status[modelName] = "ready"
			if state, ok := installs[modelName]; ok && state.key == key && !state.running {
				delete(installs, modelName)
			}
			continue
		}

		if state, ok := installs[modelName]; ok && state.key == key && !state.running {
			status[modelName] = "error"
			if state.err != nil {
				outErr = errors.Join(outErr, fmt.Errorf("install artifact for %q: %w", modelName, state.err))
			} else {
				outErr = errors.Join(outErr, fmt.Errorf("install artifact for %q completed without matching marker", modelName))
			}
			continue
		}

		installs[modelName] = &artifactInstallState{key: key, running: true}
		status[modelName] = "installing"
		installing = true
		go func(modelName string, key artifactInstallKey) {
			_, err := InstallArtifact(ctx, r.HTTPClient, cfg.OSS.BaseURL, r.ModelRoot, modelName, model.Artifact)
			result := artifactInstallResult{model: modelName, key: key, err: err}
			select {
			case installDone <- result:
			case <-ctx.Done():
			}
		}(modelName, key)
	}

	return status, installing, outErr
}

func (r *Reconciler) Reconcile(ctx context.Context) (protocol.HeartbeatResponse, error) {
	if r.Gateway == nil {
		return protocol.HeartbeatResponse{}, fmt.Errorf("agent gateway client is required")
	}

	var reconcileErr error
	if pending, err := r.pendingRestart(); err != nil {
		reconcileErr = errors.Join(reconcileErr, err)
	} else if pending {
		r.needsRestart = true
	}

	cfg, err := r.Gateway.GetConfigContext(ctx, r.Tags)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}

	artifactStatus, err := r.installAllowedArtifacts(ctx, cfg)
	reconcileErr = errors.Join(reconcileErr, err)

	content, err := RenderLlamaSwapConfig(cfg, r.ModelRoot, r.LlamaSwapToken)
	if err != nil {
		reconcileErr = errors.Join(reconcileErr, err)
	} else {
		changed, err := WriteConfigIfChanged(r.LlamaSwapConfig, content, r.Service)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
		if changed {
			if err := r.markPendingRestart(); err != nil {
				reconcileErr = errors.Join(reconcileErr, err)
			} else {
				r.needsRestart = true
			}
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
		if err := r.clearPendingRestart(); err != nil {
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

func (r *Reconciler) pendingRestart() (bool, error) {
	_, err := os.Stat(restartPendingMarkerPath(r.LlamaSwapConfig))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func (r *Reconciler) markPendingRestart() error {
	path := restartPendingMarkerPath(r.LlamaSwapConfig)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("pending\n"), 0o644)
}

func (r *Reconciler) clearPendingRestart() error {
	err := os.Remove(restartPendingMarkerPath(r.LlamaSwapConfig))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func restartPendingMarkerPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), filepath.Base(configPath)+".restart-pending")
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
