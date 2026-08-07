package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func (s *Server) handleUIServiceNamePromote(w http.ResponseWriter, r *http.Request) {
	var req serviceNamePromotionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditServiceNameRejection("promotion", "", "invalid_request")
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	resp, err := s.configManager.PromoteServiceName(r.Context(), req, func(cfg config.GatewayConfig) error {
		return s.validateServiceNamePromotionState(cfg, strings.TrimSpace(req.ServiceName), strings.TrimSpace(req.TargetModel), time.Now())
	}, s.applyRuntimeConfig)
	if err != nil {
		s.auditServiceNameRejection("promotion", req.ServiceName, serviceNameReasonCode(err))
		writeServiceNameTransactionError(w, err)
		return
	}
	s.recordGatewayWorkerEvent("gateway", protocol.AgentEvent{Event: "gateway_service_name_promoted", Model: resp.ServiceName, Object: resp.ArchiveID})
	s.logEvent("gateway_service_name_promoted", map[string]any{"service_name": resp.ServiceName, "target_model": resp.TargetModel, "archive_id": resp.ArchiveID, "version": resp.Version})
	writeJSON(w, resp)
}

func (s *Server) handleUIServiceNameRollback(w http.ResponseWriter, r *http.Request) {
	var req serviceNamePromotionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.auditServiceNameRejection("rollback", "", "invalid_request")
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	resp, err := s.configManager.RollbackServiceName(r.Context(), req, s.applyRuntimeConfig)
	if err != nil {
		s.auditServiceNameRejection("rollback", req.ServiceName, serviceNameReasonCode(err))
		writeServiceNameTransactionError(w, err)
		return
	}
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
	message := err.Error()
	if status == http.StatusInternalServerError {
		message = "service-name transaction failed"
	}
	w.WriteHeader(status)
	writeJSON(w, map[string]string{"error": message, "reason_code": serviceNameReasonCode(err)})
}

func serviceNameReasonCode(err error) string {
	var conflict serviceNameConflict
	if errors.As(err, &conflict) && conflict.code != "" {
		return conflict.code
	}
	var invalid errInvalidConfig
	if errors.As(err, &invalid) {
		return "invalid_config"
	}
	return "storage_failure"
}

func (s *Server) auditServiceNameRejection(action, serviceName, reasonCode string) {
	eventName := "gateway_service_name_" + action + "_rejected"
	s.recordGatewayWorkerEvent("gateway", protocol.AgentEvent{Event: eventName, Model: strings.TrimSpace(serviceName), Object: reasonCode})
	s.logEvent(eventName, map[string]any{"service_name": strings.TrimSpace(serviceName), "reason_code": reasonCode})
}

func (s *Server) validateServiceNamePromotionState(cfg config.GatewayConfig, serviceName, target string, now time.Time) error {
	if s.accounting.ModelActive(serviceName) > 0 || s.limiter.Active("model:"+serviceName) > 0 || s.limiter.Queued("model:"+serviceName) > 0 {
		return serviceNameConflict{code: "old_requests_active", message: "old canonical has active or pending requests"}
	}
	for _, worker := range s.workers.Snapshot(now) {
		if s.limiter.Active(workerModelLimitKey(worker.ID, serviceName)) > 0 || s.limiter.Queued(workerModelLimitKey(worker.ID, serviceName)) > 0 {
			return serviceNameConflict{code: "old_requests_active", message: "old canonical has active or pending worker requests"}
		}
		for _, running := range worker.RunningModels {
			if running.Model == serviceName {
				return serviceNameConflict{code: "old_replica_active", message: "old canonical has a running replica in state " + running.State}
			}
		}
		if status := strings.ToLower(strings.TrimSpace(worker.Artifacts[serviceName])); status != "" && status != "ready" && status != "missing" && status != "unavailable" && status != "error" {
			return serviceNameConflict{code: "old_artifact_busy", message: "old canonical artifact activity is " + status}
		}
	}
	ready, floor := s.serviceNameTargetReadiness(cfg, target, now)
	if ready < floor {
		return serviceNameConflict{code: "target_not_ready", message: fmt.Sprintf("target has %d ready replicas; ready floor is %d", ready, floor)}
	}
	return nil
}

type uiServiceNameTargetEligibility struct {
	Model         string `json:"model"`
	RoutableReady int    `json:"routable_ready"`
	ReadyFloor    int    `json:"ready_floor"`
	Eligible      bool   `json:"eligible"`
}

type uiServiceNameEligibilityResponse struct {
	ServiceName string                           `json:"service_name"`
	Targets     []uiServiceNameTargetEligibility `json:"targets"`
}

func (s *Server) serviceNameTargetReadiness(cfg config.GatewayConfig, target string, now time.Time) (int, int) {
	floor := cfg.Models[target].MinLoaded
	if floor < 1 {
		floor = 1
	}
	ready := 0
	for _, worker := range s.workers.Snapshot(now) {
		if runningModelReady(worker, target) && artifactReady(worker, target) && s.workers.Healthy(worker.ID, now) && workerAllowsModel(cfg, worker, target) && !s.replicaCooldowns.Active(worker.ID, target, now) {
			ready++
		}
	}
	return ready, floor
}

func (s *Server) handleUIServiceNameEligibility(w http.ResponseWriter, r *http.Request) {
	serviceName := strings.TrimSpace(r.URL.Query().Get("service_name"))
	cfg := s.currentConfig()
	targets := make([]uiServiceNameTargetEligibility, 0, len(cfg.Models))
	for name, model := range cfg.Models {
		if name == serviceName || model.Disabled {
			continue
		}
		ready, floor := s.serviceNameTargetReadiness(cfg, name, time.Now())
		targets = append(targets, uiServiceNameTargetEligibility{Model: name, RoutableReady: ready, ReadyFloor: floor, Eligible: ready >= floor})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Model < targets[j].Model })
	writeJSON(w, uiServiceNameEligibilityResponse{ServiceName: serviceName, Targets: targets})
}

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
	resp, err := s.configManager.ApplyContextWithPublish(r.Context(), raw, s.applyRuntimeConfig)
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
