package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

const ReconcileInterval = 3 * time.Second
const agentEventBufferLimit = 256
const agentEventHeartbeatLimit = 64

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
	Health          HealthClient
	RunningModels   RunningModelsClient
	GPUDevices      GPUDevicesClient
	RunInterval     time.Duration

	needsRestart bool
	eventMu      sync.Mutex
	events       []protocol.AgentEvent
	runningMu    sync.Mutex
	lastRunning  map[string]string
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
	artifactStatus, _, err := r.installAllowedArtifactsAsync(ctx, cfg, installs, installDone)
	reconcileErr = errors.Join(reconcileErr, err)
	runningModels, err := r.fetchRunningModels(ctx)
	reconcileErr = errors.Join(reconcileErr, err)
	if err == nil {
		r.observeRunningModelChanges(runningModels)
	}
	gpuDevices, err := r.fetchGPUDevices(ctx)
	reconcileErr = errors.Join(reconcileErr, err)

	var pendingConfigContent []byte
	var restartModels []string
	if readyCfg, readyCount := configWithReadyArtifacts(cfg, artifactStatus); readyCount > 0 {
		content, err := RenderLlamaSwapConfig(readyCfg, r.ModelRoot, r.LlamaSwapToken)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		} else {
			changed, affectedModels, err := writeConfigIfChangedAndMarkPendingForRunningModels(r.LlamaSwapConfig, content, runningModels, r.markPendingRestart)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, err)
			}
			if changed {
				r.recordEvent(protocol.AgentEvent{Event: "llama_swap_config_changed"})
			}
			if len(affectedModels) > 0 {
				r.needsRestart = true
				restartModels = affectedModels
				pendingConfigContent = content
			}
		}
	}

	hb := BuildHeartbeat(r.AgentID, r.Tags, r.LlamaSwapURL, cfg, r.needsRestart, artifactStatus)
	if r.needsRestart {
		hb.RestartModels = append([]string(nil), restartModels...)
	}
	hb.RunningModels = runningModels
	hb.GPUDevices = gpuDevices
	hb.Events = r.snapshotEventsForHeartbeat(agentEventHeartbeatLimit)
	if reconcileErr != nil {
		hb.LastError = reconcileErr.Error()
	}

	resp, err := r.Gateway.HeartbeatContext(ctx, hb)
	if err != nil {
		return resp, errors.Join(reconcileErr, err)
	}
	r.dropReportedEvents(len(hb.Events))

	if resp.RestartAllowed && r.needsRestart {
		if len(pendingConfigContent) > 0 {
			changed, err := writeConfigIfChangedWithoutMarkingPending(r.LlamaSwapConfig, pendingConfigContent)
			if err != nil {
				r.recordEvent(protocol.AgentEvent{Event: "llama_swap_restart_error", Error: err.Error()})
				return resp, errors.Join(reconcileErr, err)
			}
			if changed {
				r.recordEvent(protocol.AgentEvent{Event: "llama_swap_config_changed"})
			}
		}
		r.recordEvent(protocol.AgentEvent{Event: "llama_swap_restart_start"})
		if err := r.restart(ctx); err != nil {
			r.recordEvent(protocol.AgentEvent{Event: "llama_swap_restart_error", Error: err.Error()})
			return resp, errors.Join(reconcileErr, err)
		}
		if err := r.verifyRestart(ctx); err != nil {
			r.recordEvent(protocol.AgentEvent{Event: "llama_swap_restart_error", Error: err.Error()})
			return resp, errors.Join(reconcileErr, err)
		}
		if err := r.clearPendingRestart(); err != nil {
			r.recordEvent(protocol.AgentEvent{Event: "llama_swap_restart_error", Error: err.Error()})
			return resp, errors.Join(reconcileErr, err)
		}
		r.recordEvent(protocol.AgentEvent{Event: "llama_swap_restart_done"})
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
	installRunning := false
	startedNewInstall := false
	for _, state := range installs {
		if state.running {
			installRunning = true
			break
		}
	}

	for _, modelName := range cfg.TagPolicy.AllowedModels {
		model, ok := cfg.Models[modelName]
		if !ok {
			status[modelName] = "missing"
			outErr = errors.Join(outErr, fmt.Errorf("allowed model %q missing from config models", modelName))
			continue
		}

		key := artifactKey(modelName, model.Artifact.Object, model.Artifact.Kind, model.Artifact.CRC64ECMA)
		if state, ok := installs[modelName]; ok && state.running {
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
			if state, ok := installs[modelName]; ok && !state.running {
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
			delete(installs, modelName)
			continue
		}

		if installRunning || startedNewInstall {
			status[modelName] = "pending"
			installing = true
			continue
		}

		installs[modelName] = &artifactInstallState{key: key, running: true}
		status[modelName] = "installing"
		installing = true
		startedNewInstall = true
		go func(modelName string, key artifactInstallKey) {
			started := time.Now()
			r.recordEvent(protocol.AgentEvent{
				Event:     "artifact_install_start",
				Model:     modelName,
				Object:    model.Artifact.Object,
				Kind:      model.Artifact.Kind,
				CRC64ECMA: model.Artifact.CRC64ECMA,
			})
			_, err := InstallArtifactWithProgress(ctx, r.HTTPClient, cfg.OSS.BaseURL, r.ModelRoot, modelName, model.Artifact, func(progress ArtifactProgress) {
				r.recordEvent(protocol.AgentEvent{
					Event:           "artifact_download_progress",
					Model:           modelName,
					Object:          model.Artifact.Object,
					Kind:            model.Artifact.Kind,
					DownloadedBytes: progress.DownloadedBytes,
					TotalBytes:      progress.TotalBytes,
					Percent:         progress.Percent,
				})
			})
			durationMS := time.Since(started).Milliseconds()
			if err != nil {
				r.recordEvent(protocol.AgentEvent{
					Event:      "artifact_install_error",
					Model:      modelName,
					Object:     model.Artifact.Object,
					Kind:       model.Artifact.Kind,
					DurationMS: durationMS,
					Error:      err.Error(),
				})
			} else {
				r.recordEvent(protocol.AgentEvent{
					Event:      "artifact_install_done",
					Model:      modelName,
					Object:     model.Artifact.Object,
					Kind:       model.Artifact.Kind,
					DurationMS: durationMS,
				})
			}
			result := artifactInstallResult{model: modelName, key: key, err: err}
			select {
			case installDone <- result:
			case <-ctx.Done():
			}
		}(modelName, key)
	}

	return status, installing, outErr
}

func (r *Reconciler) recordEvent(event protocol.AgentEvent) {
	if event.Event == "" {
		return
	}
	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	if data, err := json.Marshal(event); err == nil {
		log.Printf("%s", data)
	}

	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	r.events = append(r.events, event)
	if len(r.events) > agentEventBufferLimit {
		r.events = append([]protocol.AgentEvent(nil), r.events[len(r.events)-agentEventBufferLimit:]...)
	}
}

func (r *Reconciler) snapshotEventsForHeartbeat(limit int) []protocol.AgentEvent {
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	if limit <= 0 || limit > len(r.events) {
		limit = len(r.events)
	}
	if limit == 0 {
		return nil
	}
	return append([]protocol.AgentEvent(nil), r.events[:limit]...)
}

func (r *Reconciler) dropReportedEvents(count int) {
	if count <= 0 {
		return
	}
	r.eventMu.Lock()
	defer r.eventMu.Unlock()
	if count >= len(r.events) {
		r.events = nil
		return
	}
	r.events = append([]protocol.AgentEvent(nil), r.events[count:]...)
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
	runningModels, err := r.fetchRunningModels(ctx)
	reconcileErr = errors.Join(reconcileErr, err)
	if err == nil {
		r.observeRunningModelChanges(runningModels)
	}
	gpuDevices, err := r.fetchGPUDevices(ctx)
	reconcileErr = errors.Join(reconcileErr, err)

	var pendingConfigContent []byte
	var restartModels []string
	if readyCfg, readyCount := configWithReadyArtifacts(cfg, artifactStatus); readyCount > 0 {
		content, err := RenderLlamaSwapConfig(readyCfg, r.ModelRoot, r.LlamaSwapToken)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		} else {
			_, affectedModels, err := writeConfigIfChangedAndMarkPendingForRunningModels(r.LlamaSwapConfig, content, runningModels, r.markPendingRestart)
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, err)
			}
			if len(affectedModels) > 0 {
				r.needsRestart = true
				restartModels = affectedModels
				pendingConfigContent = content
			}
		}
	}

	hb := BuildHeartbeat(r.AgentID, r.Tags, r.LlamaSwapURL, cfg, r.needsRestart, artifactStatus)
	if r.needsRestart {
		hb.RestartModels = append([]string(nil), restartModels...)
	}
	hb.RunningModels = runningModels
	hb.GPUDevices = gpuDevices
	hb.Events = r.snapshotEventsForHeartbeat(agentEventHeartbeatLimit)
	if reconcileErr != nil {
		hb.LastError = reconcileErr.Error()
	}

	resp, err := r.Gateway.HeartbeatContext(ctx, hb)
	if err != nil {
		return resp, errors.Join(reconcileErr, err)
	}
	r.dropReportedEvents(len(hb.Events))

	if resp.RestartAllowed && r.needsRestart {
		if len(pendingConfigContent) > 0 {
			if _, err := writeConfigIfChangedWithoutMarkingPending(r.LlamaSwapConfig, pendingConfigContent); err != nil {
				return resp, errors.Join(reconcileErr, err)
			}
		}
		if err := r.restart(ctx); err != nil {
			return resp, errors.Join(reconcileErr, err)
		}
		if err := r.verifyRestart(ctx); err != nil {
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

func (r *Reconciler) fetchRunningModels(ctx context.Context) ([]protocol.RunningModel, error) {
	if r.RunningModels == nil {
		return nil, nil
	}
	models, err := r.RunningModels.RunningModelsContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch llama-swap running models: %w", err)
	}
	return models, nil
}

func (r *Reconciler) fetchGPUDevices(ctx context.Context) ([]protocol.GPUDevice, error) {
	if r.GPUDevices == nil {
		return nil, nil
	}
	devices, err := r.GPUDevices.GPUDevicesContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch gpu devices: %w", err)
	}
	return devices, nil
}

func (r *Reconciler) observeRunningModelChanges(models []protocol.RunningModel) {
	current := make(map[string]string, len(models))
	for _, model := range models {
		if model.Model == "" {
			continue
		}
		current[model.Model] = model.State
	}

	r.runningMu.Lock()
	previous := r.lastRunning
	r.lastRunning = current
	r.runningMu.Unlock()

	for model, state := range current {
		oldState, existed := previous[model]
		switch {
		case !existed:
			r.recordEvent(protocol.AgentEvent{Event: "model_loaded", Model: model, ToState: state})
		case oldState != state:
			r.recordEvent(protocol.AgentEvent{Event: "model_state_changed", Model: model, FromState: oldState, ToState: state})
		}
	}
	for model, oldState := range previous {
		if _, ok := current[model]; !ok {
			r.recordEvent(protocol.AgentEvent{Event: "model_unloaded", Model: model, FromState: oldState})
		}
	}
}

func configWithReadyArtifacts(cfg protocol.AgentConfigResponse, artifactStatus map[string]string) (protocol.AgentConfigResponse, int) {
	out := cfg
	out.TagPolicy = cfg.TagPolicy
	out.TagPolicy.AllowedModels = make([]string, 0, len(cfg.TagPolicy.AllowedModels))
	out.Models = make(map[string]config.Model, len(cfg.TagPolicy.AllowedModels))
	for _, modelName := range cfg.TagPolicy.AllowedModels {
		if artifactStatus[modelName] != "ready" {
			continue
		}
		model, ok := cfg.Models[modelName]
		if !ok {
			continue
		}
		out.TagPolicy.AllowedModels = append(out.TagPolicy.AllowedModels, modelName)
		out.Models[modelName] = model
	}
	return out, len(out.TagPolicy.AllowedModels)
}

func (r *Reconciler) restart(ctx context.Context) error {
	if r.Service == nil {
		return LoggingService{}.Restart(ctx)
	}
	return r.Service.Restart(ctx)
}

func (r *Reconciler) verifyRestart(ctx context.Context) error {
	if r.Health != nil {
		if err := r.Health.HealthContext(ctx); err != nil {
			return fmt.Errorf("verify llama-swap health: %w", err)
		}
	}
	if r.RunningModels != nil {
		if _, err := r.RunningModels.RunningModelsContext(ctx); err != nil {
			return fmt.Errorf("verify llama-swap running models: %w", err)
		}
	}
	return nil
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

func writeConfigIfChangedAndMarkPending(path string, content []byte, markPendingRestart func() error) (bool, error) {
	old, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(old, content) {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if markPendingRestart == nil {
		return false, fmt.Errorf("mark pending restart function is required")
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
	if err := markPendingRestart(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return false, err
	}
	cleanup = false
	return true, nil
}

func writeConfigIfChangedAndMarkPendingForRunningModels(path string, content []byte, runningModels []protocol.RunningModel, markPendingRestart func() error) (bool, []string, error) {
	old, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(old, content) {
			return false, nil, nil
		}
	} else if errors.Is(err, os.ErrNotExist) {
		changed, err := writeConfigIfChangedAndMarkPending(path, content, markPendingRestart)
		if !changed {
			return false, nil, err
		}
		return true, []string{""}, err
	} else {
		return false, nil, err
	}

	if hasRestartRelevantRunningModel(runningModels) {
		affectedModels := loadedModelConfigChangedModels(old, content, runningModels)
		if len(affectedModels) > 0 {
			if markPendingRestart == nil {
				return false, nil, fmt.Errorf("mark pending restart function is required")
			}
			if err := markPendingRestart(); err != nil {
				return false, nil, err
			}
			return false, affectedModels, nil
		}
		return false, nil, nil
	}

	affectedModels := loadedModelConfigChangedModels(old, content, runningModels)
	if len(affectedModels) > 0 {
		changed, err := writeConfigIfChangedAndMarkPending(path, content, markPendingRestart)
		return changed, affectedModels, err
	}
	changed, err := writeConfigIfChangedWithoutMarkingPending(path, content)
	return changed, nil, err
}

func hasRestartRelevantRunningModel(runningModels []protocol.RunningModel) bool {
	return len(restartRelevantRunningModelNames(runningModels)) > 0
}

func restartRelevantRunningModelNames(runningModels []protocol.RunningModel) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, running := range runningModels {
		if running.Model == "" {
			continue
		}
		if !runningModelStateRequiresConfigRestart(running.State) || seen[running.Model] {
			continue
		}
		seen[running.Model] = true
		out = append(out, running.Model)
	}
	return out
}

func loadedModelConfigChanged(oldContent []byte, newContent []byte, runningModels []protocol.RunningModel) bool {
	return len(loadedModelConfigChangedModels(oldContent, newContent, runningModels)) > 0
}

func loadedModelConfigChangedModels(oldContent []byte, newContent []byte, runningModels []protocol.RunningModel) []string {
	if len(runningModels) == 0 {
		return nil
	}
	var oldConfig llamaSwapConfig
	var newConfig llamaSwapConfig
	if err := yaml.Unmarshal(oldContent, &oldConfig); err != nil {
		return restartRelevantRunningModelNames(runningModels)
	}
	if err := yaml.Unmarshal(newContent, &newConfig); err != nil {
		return restartRelevantRunningModelNames(runningModels)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, running := range runningModels {
		if running.Model == "" {
			continue
		}
		if !runningModelStateRequiresConfigRestart(running.State) || seen[running.Model] {
			continue
		}
		oldModel, oldOK := oldConfig.Models[running.Model]
		newModel, newOK := newConfig.Models[running.Model]
		if oldOK != newOK || oldModel != newModel {
			seen[running.Model] = true
			out = append(out, running.Model)
		}
	}
	return out
}

func runningModelStateRequiresConfigRestart(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "active", "loaded", "loading", "ready", "running", "starting":
		return true
	default:
		return false
	}
}

func writeConfigIfChangedWithoutMarkingPending(path string, content []byte) (bool, error) {
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
