package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

type Server struct {
	config     config.GatewayConfig
	workers    *WorkerRegistry
	accounting *Accounting
	metrics    *Metrics
	mux        *http.ServeMux
}

func NewServer(cfg config.GatewayConfig) *Server {
	s := &Server{
		config:     cfg,
		workers:    NewWorkerRegistry(6 * time.Second),
		accounting: NewAccounting(),
		metrics:    NewMetrics(),
		mux:        http.NewServeMux(),
	}

	s.mux.Handle("GET /metrics", s.metrics.Handler())
	s.mux.Handle("GET /internal/agent/config", bearerAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleAgentConfig)))
	s.mux.Handle("POST /internal/agent/heartbeat", bearerAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleAgentHeartbeat)))
	s.mux.Handle("POST /v1/chat/completions", bearerAuth(cfg.Tokens.Client, http.HandlerFunc(s.handleModelProxy)))

	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	tag, policy, ok := s.matchedTagPolicy(r.URL.Query().Get("tags"))
	if !ok {
		http.Error(w, "exactly one configured tag must match", http.StatusBadRequest)
		return
	}

	resp := protocol.AgentConfigResponse{
		OSS:    s.config.OSS,
		Models: make(map[string]config.Model, len(policy.AllowedModels)),
		TagPolicy: protocol.AgentTagPolicy{
			Tag:            tag,
			AllowedModels:  append([]string(nil), policy.AllowedModels...),
			WarmWhenIdle:   policy.WarmWhenIdle,
			WorkerDefaults: policy.WorkerDefaults,
		},
	}
	for _, modelName := range policy.AllowedModels {
		model, ok := s.config.Models[modelName]
		if ok {
			resp.Models[modelName] = model
		}
	}

	writeJSON(w, resp)
}

func (s *Server) matchedTagPolicy(tagsParam string) (string, config.TagPolicy, bool) {
	matches := make(map[string]config.TagPolicy)
	for _, rawTag := range strings.Split(tagsParam, ",") {
		tag := strings.TrimSpace(rawTag)
		if tag == "" {
			continue
		}
		policy, ok := s.config.TagPolicies[tag]
		if ok {
			matches[tag] = policy
		}
	}

	if len(matches) != 1 {
		return "", config.TagPolicy{}, false
	}
	for tag, policy := range matches {
		return tag, policy, true
	}
	return "", config.TagPolicy{}, false
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb protocol.HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(hb.AgentID) == "" {
		http.Error(w, "agent_id is required", http.StatusBadRequest)
		return
	}

	resp := s.workers.UpsertHeartbeat(hb, time.Now())
	writeJSON(w, resp)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}
