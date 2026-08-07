package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func (s *Server) handleUIServiceNamePromote(w http.ResponseWriter, r *http.Request) {
	var req serviceNamePromotionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	resp, err := s.configManager.PromoteServiceName(r.Context(), req, func(cfg config.GatewayConfig) error {
		return s.validateServiceNamePromotionState(cfg, strings.TrimSpace(req.ServiceName), strings.TrimSpace(req.TargetModel), time.Now())
	})
	if err != nil {
		writeServiceNameTransactionError(w, err)
		return
	}
	cfg, _ := s.configManager.Snapshot()
	s.applyRuntimeConfig(cfg)
	s.recordGatewayWorkerEvent("gateway", protocol.AgentEvent{Event: "gateway_service_name_promoted", Model: resp.ServiceName, Object: resp.ArchiveID})
	s.logEvent("gateway_service_name_promoted", map[string]any{"service_name": resp.ServiceName, "target_model": resp.TargetModel, "archive_id": resp.ArchiveID, "version": resp.Version})
	writeJSON(w, resp)
}

func (s *Server) handleUIServiceNameRollback(w http.ResponseWriter, r *http.Request) {
	var req serviceNamePromotionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	resp, err := s.configManager.RollbackServiceName(r.Context(), req)
	if err != nil {
		writeServiceNameTransactionError(w, err)
		return
	}
	cfg, _ := s.configManager.Snapshot()
	s.applyRuntimeConfig(cfg)
	s.recordGatewayWorkerEvent("gateway", protocol.AgentEvent{Event: "gateway_service_name_rollback", Model: resp.ServiceName, Object: resp.ArchiveID})
	s.logEvent("gateway_service_name_rollback", map[string]any{"service_name": resp.ServiceName, "target_model": resp.TargetModel, "archive_id": resp.ArchiveID, "version": resp.Version})
	writeJSON(w, resp)
}

func writeServiceNameTransactionError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var conflict serviceNameConflict
	var invalid errInvalidConfig
	if errors.As(err, &conflict) {
		status = http.StatusConflict
	} else if errors.As(err, &invalid) {
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": err.Error()})
}

func (s *Server) validateServiceNamePromotionState(cfg config.GatewayConfig, serviceName, target string, now time.Time) error {
	if s.accounting.ModelActive(serviceName) > 0 || s.limiter.Active("model:"+serviceName) > 0 || s.limiter.Queued("model:"+serviceName) > 0 {
		return serviceNameConflict{message: "old canonical has active or pending requests"}
	}
	ready := 0
	for _, worker := range s.workers.Snapshot(now) {
		if s.limiter.Active(workerModelLimitKey(worker.ID, serviceName)) > 0 || s.limiter.Queued(workerModelLimitKey(worker.ID, serviceName)) > 0 {
			return serviceNameConflict{message: "old canonical has active or pending worker requests"}
		}
		targetReady := false
		for _, running := range worker.RunningModels {
			if running.Model == serviceName {
				return serviceNameConflict{message: "old canonical has a running replica in state " + running.State}
			}
			if running.Model == target && strings.EqualFold(running.State, "ready") && artifactReady(worker, target) && s.workers.Healthy(worker.ID, now) && workerAllowsModel(cfg, worker, target) && !s.replicaCooldowns.Active(worker.ID, target, now) {
				targetReady = true
			}
		}
		if targetReady {
			ready++
		}
		if status := strings.ToLower(strings.TrimSpace(worker.Artifacts[serviceName])); status != "" && status != "ready" && status != "missing" && status != "unavailable" && status != "error" {
			return serviceNameConflict{message: "old canonical artifact activity is " + status}
		}
		if worker.NeedsRestart && (len(worker.RunningModels) > 0 || containsStringValue(worker.Artifacts, serviceName)) {
			return serviceNameConflict{message: "old canonical has pending worker restart activity"}
		}
	}
	model := cfg.Models[target]
	floor := model.MinLoaded
	if floor < 1 {
		floor = 1
	}
	if ready < floor {
		return serviceNameConflict{message: fmt.Sprintf("target has %d ready replicas; ready floor is %d", ready, floor)}
	}
	return nil
}

func containsStringValue(values map[string]string, key string) bool { _, ok := values[key]; return ok }

func (s *Server) handleUIConfig(w http.ResponseWriter, r *http.Request) {
	if s.configManager == nil {
		http.Error(w, "config manager is not enabled", http.StatusInternalServerError)
		return
	}
	resp, err := s.configManager.UIConfig()
	if err != nil {
		http.Error(w, "failed to render config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleUIConfigValidate(w http.ResponseWriter, r *http.Request) {
	s.handleUIConfigDryRun(w, r)
}

func (s *Server) handleUIConfigDryRun(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read config", http.StatusBadRequest)
		return
	}
	resp, _ := s.configManager.DryRun(raw)
	s.decorateConfigImpact(&resp)
	if !resp.Valid {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, resp)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleUIConfigApply(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read config", http.StatusBadRequest)
		return
	}
	resp, err := s.configManager.ApplyContext(r.Context(), raw)
	if err != nil {
		status := http.StatusInternalServerError
		var invalid errInvalidConfig
		if errors.As(err, &invalid) {
			status = http.StatusBadRequest
		}
		w.WriteHeader(status)
		writeJSON(w, uiConfigDryRunResponse{Valid: false, Error: err.Error()})
		return
	}
	s.decorateApplyImpact(&resp)
	cfg, _ := s.configManager.Snapshot()
	s.applyRuntimeConfig(cfg)
	s.logEvent("gateway_config_apply", map[string]any{
		"version":                  resp.Version,
		"changes":                  len(resp.Changes),
		"requires_gateway_restart": resp.RequiresGatewayRestart,
	})
	writeJSON(w, resp)
}

func (s *Server) decorateApplyImpact(resp *uiConfigApplyResponse) {
	if resp == nil {
		return
	}
	dryRun := uiConfigDryRunResponse{
		Valid:                  true,
		Version:                resp.Version,
		Changes:                append([]uiConfigChange(nil), resp.Changes...),
		ApplyMode:              resp.ApplyMode,
		RequiresGatewayRestart: resp.RequiresGatewayRestart,
	}
	s.decorateConfigImpact(&dryRun)
	resp.Changes = dryRun.Changes
	resp.Impacts = dryRun.Impacts
	if resp.ApplyMode == "" {
		resp.ApplyMode = dryRun.ApplyMode
	}
}

func (s *Server) decorateConfigImpact(resp *uiConfigDryRunResponse) {
	if resp == nil || !resp.Valid {
		return
	}
	if resp.ApplyMode == "" {
		resp.ApplyMode = applyModeForChanges(resp.Changes)
	}
	impacts := s.configImpacts(resp.Changes, time.Now())
	resp.Impacts = impacts
	loadedByModel := map[string]bool{}
	for _, impact := range impacts {
		if impact.RequiresWorkerRestart {
			loadedByModel[impact.Model] = true
		}
	}
	for i := range resp.Changes {
		if resp.Changes[i].Model == "" {
			continue
		}
		if loadedByModel[resp.Changes[i].Model] {
			resp.Changes[i].RequiresWorkerRestart = true
			continue
		}
		if resp.Changes[i].Detail == "runtime command or artifact changed" {
			resp.Changes[i].RequiresWorkerRestart = false
		}
	}
}

func (s *Server) configImpacts(changes []uiConfigChange, now time.Time) []uiConfigImpact {
	if s == nil || s.workers == nil {
		return nil
	}
	runtimeChanged := map[string]bool{}
	for _, change := range changes {
		if change.Model == "" {
			continue
		}
		if change.Detail == "runtime command or artifact changed" || change.Type == "removed" {
			runtimeChanged[change.Model] = true
		}
	}
	if len(runtimeChanged) == 0 {
		return nil
	}
	workers := s.workers.Snapshot(now)
	out := []uiConfigImpact{}
	for _, worker := range workers {
		for _, running := range worker.RunningModels {
			if !runtimeChanged[running.Model] || !runningModelStateRequiresConfigRestart(running.State) {
				continue
			}
			out = append(out, uiConfigImpact{
				Model:                 running.Model,
				WorkerID:              worker.ID,
				RunningState:          running.State,
				Loaded:                true,
				RequiresWorkerRestart: true,
				Reason:                "loaded model runtime or artifact config changed",
			})
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

func (s *Server) applyRuntimeConfig(cfg config.GatewayConfig) {
	s.proxyAttempts = configuredProxyAttempts(cfg)
	s.scraper = NewMetricsScraperWithToken(cfg.Tokens.LlamaSwap)
	if cfg.MetricsStore.Enabled && strings.TrimSpace(cfg.MetricsStore.QueryURL) != "" {
		s.metricsStore = NewVictoriaMetricsClient(cfg.MetricsStore.QueryURL, time.Duration(cfg.MetricsStore.TimeoutMS)*time.Millisecond)
		return
	}
	s.metricsStore = nil
}
