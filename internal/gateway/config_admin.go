package gateway

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"llm-swap/internal/config"
)

func (s *Server) handleUIConfig(w http.ResponseWriter, r *http.Request) {
	if s.configManager == nil {
		http.Error(w, "config manager is not enabled", http.StatusInternalServerError)
		return
	}
	cfg, version := s.configManager.Snapshot()
	raw, err := s.configManager.YAML()
	if err != nil {
		http.Error(w, "failed to render config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, uiConfigResponse{
		Version: version,
		Config:  cfg,
		YAML:    string(raw),
	})
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
	resp, err := s.configManager.Apply(raw)
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
			if !runtimeChanged[running.Model] || running.State == "" {
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

func (s *Server) applyRuntimeConfig(cfg config.GatewayConfig) {
	s.proxyAttempts = configuredProxyAttempts(cfg)
	s.scraper = NewMetricsScraperWithToken(cfg.Tokens.LlamaSwap)
	if cfg.MetricsStore.Enabled && strings.TrimSpace(cfg.MetricsStore.QueryURL) != "" {
		s.metricsStore = NewVictoriaMetricsClient(cfg.MetricsStore.QueryURL, time.Duration(cfg.MetricsStore.TimeoutMS)*time.Millisecond)
		return
	}
	s.metricsStore = nil
}
