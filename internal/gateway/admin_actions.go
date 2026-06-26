package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"llm-swap/internal/protocol"
)

type uiAdminActionResponse struct {
	Action   string `json:"action"`
	Result   string `json:"result"`
	WorkerID string `json:"worker_id,omitempty"`
	Model    string `json:"model,omitempty"`
	Error    string `json:"error,omitempty"`
}

type uiAdminModelActionRequest struct {
	WorkerID string `json:"worker_id"`
	Force    bool   `json:"force"`
}

type uiAdminCooldownClearRequest struct {
	WorkerID string `json:"worker_id"`
	Model    string `json:"model"`
}

func (s *Server) handleUIWorkerDrain(w http.ResponseWriter, r *http.Request) {
	workerID := strings.TrimSpace(r.PathValue("id"))
	worker, ok := s.workers.Drain(workerID)
	if !ok {
		writeAdminActionError(w, http.StatusNotFound, "worker_drain", workerID, "", "worker not found")
		return
	}
	s.recordGatewayWorkerEvent(worker.ID, protocol.AgentEvent{Event: "gateway_worker_drain", Model: ""})
	writeJSON(w, uiAdminActionResponse{Action: "worker_drain", Result: "done", WorkerID: worker.ID})
}

func (s *Server) handleUIWorkerUndrain(w http.ResponseWriter, r *http.Request) {
	workerID := strings.TrimSpace(r.PathValue("id"))
	worker, ok := s.workers.Undrain(workerID)
	if !ok {
		writeAdminActionError(w, http.StatusNotFound, "worker_undrain", workerID, "", "worker not found")
		return
	}
	s.recordGatewayWorkerEvent(worker.ID, protocol.AgentEvent{Event: "gateway_worker_undrain", Model: ""})
	writeJSON(w, uiAdminActionResponse{Action: "worker_undrain", Result: "done", WorkerID: worker.ID})
}

func (s *Server) handleUIModelWarm(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.PathValue("model"))
	req, ok := decodeModelActionRequest(w, r)
	if !ok {
		return
	}
	cfg := s.currentConfig()
	if _, ok := cfg.Models[model]; !ok {
		writeAdminActionError(w, http.StatusNotFound, "model_warm", req.WorkerID, model, "model not found")
		return
	}
	worker, ok := s.findWorker(req.WorkerID, time.Now())
	if !ok {
		writeAdminActionError(w, http.StatusNotFound, "model_warm", req.WorkerID, model, "worker not found")
		return
	}
	if !s.workers.Healthy(worker.ID, time.Now()) {
		writeAdminActionError(w, http.StatusConflict, "model_warm", worker.ID, model, "worker is not healthy or active")
		return
	}
	if !workerAllowsModel(cfg, worker, model) {
		writeAdminActionError(w, http.StatusConflict, "model_warm", worker.ID, model, "worker is not allowed to run model")
		return
	}
	if !artifactReady(worker, model) {
		writeAdminActionError(w, http.StatusConflict, "model_warm", worker.ID, model, "artifact is not ready on worker")
		return
	}
	s.recordGatewayWorkerEvent(worker.ID, protocol.AgentEvent{Event: "gateway_model_warm_start", Model: model})
	client := LlamaSwapClient{BearerToken: cfg.Tokens.LlamaSwap}
	if err := client.Load(r.Context(), worker.LlamaSwapURL, model); err != nil {
		s.recordReverseAccessResult(worker.ID, err, time.Now())
		s.recordGatewayWorkerEvent(worker.ID, protocol.AgentEvent{Event: "gateway_model_warm_error", Model: model, Error: err.Error()})
		writeAdminActionError(w, http.StatusBadGateway, "model_warm", worker.ID, model, err.Error())
		return
	}
	s.recordReverseAccessResult(worker.ID, nil, time.Now())
	s.recordGatewayWorkerEvent(worker.ID, protocol.AgentEvent{Event: "gateway_model_warm_done", Model: model})
	writeJSON(w, uiAdminActionResponse{Action: "model_warm", Result: "done", WorkerID: worker.ID, Model: model})
}

func (s *Server) handleUIModelUnload(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.PathValue("model"))
	req, ok := decodeModelActionRequest(w, r)
	if !ok {
		return
	}
	cfg := s.currentConfig()
	if _, ok := cfg.Models[model]; !ok {
		writeAdminActionError(w, http.StatusNotFound, "model_unload", req.WorkerID, model, "model not found")
		return
	}
	worker, ok := s.findWorker(req.WorkerID, time.Now())
	if !ok {
		writeAdminActionError(w, http.StatusNotFound, "model_unload", req.WorkerID, model, "worker not found")
		return
	}
	active := s.workers.ActiveSnapshot()
	if active[worker.ID] > 0 && !req.Force {
		writeAdminActionError(w, http.StatusConflict, "model_unload", worker.ID, model, "worker has active requests")
		return
	}
	if !runningModelReady(worker, model) && !req.Force {
		writeAdminActionError(w, http.StatusConflict, "model_unload", worker.ID, model, "model is not ready on worker")
		return
	}
	client := LlamaSwapClient{BearerToken: cfg.Tokens.LlamaSwap}
	if err := client.Unload(r.Context(), worker.LlamaSwapURL, model); err != nil {
		s.recordReverseAccessResult(worker.ID, err, time.Now())
		s.recordGatewayWorkerEvent(worker.ID, protocol.AgentEvent{Event: "gateway_model_unload_error", Model: model, Error: err.Error()})
		writeAdminActionError(w, http.StatusBadGateway, "model_unload", worker.ID, model, err.Error())
		return
	}
	s.recordReverseAccessResult(worker.ID, nil, time.Now())
	s.recordGatewayWorkerEvent(worker.ID, protocol.AgentEvent{Event: "gateway_model_unload_done", Model: model})
	writeJSON(w, uiAdminActionResponse{Action: "model_unload", Result: "done", WorkerID: worker.ID, Model: model})
}

func (s *Server) handleUICooldownClear(w http.ResponseWriter, r *http.Request) {
	var req uiAdminCooldownClearRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminActionError(w, http.StatusBadRequest, "cooldown_clear", "", "", "invalid json")
		return
	}
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	req.Model = strings.TrimSpace(req.Model)
	if req.WorkerID == "" || req.Model == "" {
		writeAdminActionError(w, http.StatusBadRequest, "cooldown_clear", req.WorkerID, req.Model, "worker_id and model are required")
		return
	}
	entry, ok := s.replicaCooldowns.Clear(req.WorkerID, req.Model, time.Now())
	if ok && s.metrics != nil {
		s.metrics.ObserveReplicaCooldownClear(entry)
	}
	s.logEvent("admin_cooldown_clear", map[string]any{"worker_id": req.WorkerID, "model": req.Model, "cleared": ok})
	writeJSON(w, uiAdminActionResponse{Action: "cooldown_clear", Result: "done", WorkerID: req.WorkerID, Model: req.Model})
}

func decodeModelActionRequest(w http.ResponseWriter, r *http.Request) (uiAdminModelActionRequest, bool) {
	var req uiAdminModelActionRequest
	if r.Body == nil {
		return req, true
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAdminActionError(w, http.StatusBadRequest, "model_action", "", "", "invalid json")
		return req, false
	}
	req.WorkerID = strings.TrimSpace(req.WorkerID)
	if req.WorkerID == "" {
		writeAdminActionError(w, http.StatusBadRequest, "model_action", "", "", "worker_id is required")
		return req, false
	}
	return req, true
}

func (s *Server) findWorker(workerID string, now time.Time) (Worker, bool) {
	for _, worker := range s.workers.Snapshot(now) {
		if worker.ID == workerID {
			return worker, true
		}
	}
	return Worker{}, false
}

func writeAdminActionError(w http.ResponseWriter, status int, action, workerID, model, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, uiAdminActionResponse{Action: action, Result: "error", WorkerID: workerID, Model: model, Error: message})
}
