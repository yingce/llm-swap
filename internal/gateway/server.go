package gateway

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

type Server struct {
	config             config.GatewayConfig
	workers            *WorkerRegistry
	accounting         *Accounting
	limiter            *QueueLimiter
	metrics            *Metrics
	scraper            *MetricsScraper
	access             *AccessTracker
	pressure           *PressureTracker
	replicaCooldowns   *ReplicaCooldowns
	requestLogPath     string
	workerEventLogPath string
	proxyAttempts      int
	logger             *log.Logger
	eventMu            sync.Mutex
	recentEvents       []uiAgentEvent
	mux                *http.ServeMux
}

func NewServer(cfg config.GatewayConfig) *Server {
	return newServer(cfg, "", "")
}

func NewServerWithGatewayPersistence(cfg config.GatewayConfig, requestLogPath string) *Server {
	return newServer(cfg, requestLogPath, "")
}

func NewServerWithGatewayPersistencePaths(cfg config.GatewayConfig, requestLogPath string, workerEventLogPath string) *Server {
	return newServer(cfg, requestLogPath, workerEventLogPath)
}

func newServer(cfg config.GatewayConfig, requestLogPath string, workerEventLogPath string) *Server {
	access := NewAccessTracker()
	if requestLogPath != "" {
		if loaded, err := LoadAccessTrackerFromRequestLog(requestLogPath); err == nil {
			access = loaded
		}
	}
	recentEvents := []uiAgentEvent{}
	if workerEventLogPath != "" {
		if loaded, err := loadRecentWorkerEvents(workerEventLogPath, uiEventLimit); err == nil {
			recentEvents = loaded
		}
	}
	s := &Server{
		config:             cfg,
		workers:            NewWorkerRegistry(6 * time.Second),
		accounting:         NewAccounting(),
		limiter:            NewQueueLimiter(),
		metrics:            NewMetrics(),
		scraper:            NewMetricsScraperWithToken(cfg.Tokens.LlamaSwap),
		access:             access,
		pressure:           NewPressureTracker(defaultPressureWindow),
		replicaCooldowns:   NewReplicaCooldowns(defaultReplicaCooldownTTL),
		requestLogPath:     requestLogPath,
		workerEventLogPath: workerEventLogPath,
		proxyAttempts:      configuredProxyAttempts(cfg),
		logger:             log.New(os.Stdout, "", log.LstdFlags),
		recentEvents:       recentEvents,
		mux:                http.NewServeMux(),
	}

	s.mux.Handle("GET /metrics", http.HandlerFunc(s.handleMetrics))
	s.mux.Handle("GET /ui", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/status", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIStatus)))
	s.mux.Handle("GET /ui/events", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIEvents)))
	s.mux.Handle("GET /internal/agent/config", bearerAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleAgentConfig)))
	s.mux.Handle("POST /internal/agent/heartbeat", bearerAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleAgentHeartbeat)))
	s.mux.Handle("GET /v1/models", bearerAuth(cfg.Tokens.Client, http.HandlerFunc(s.handleModels)))
	s.mux.Handle("POST /v1/chat/completions", bearerAuth(cfg.Tokens.Client, http.HandlerFunc(s.handleModelProxy)))

	return s
}

func configuredProxyAttempts(cfg config.GatewayConfig) int {
	if cfg.Gateway.ProxyAttempts > 0 {
		return cfg.Gateway.ProxyAttempts
	}
	return config.DefaultProxyAttempts
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	workers := s.workers.Snapshot(now)
	active := s.workers.ActiveSnapshot()
	s.metrics.ObserveWorkers(workers, active, now, func(worker Worker) (ActivityStats, error) {
		if s.scraper == nil || worker.LlamaSwapURL == "" {
			return ActivityStats{}, nil
		}
		activity, err := s.scraper.PullActivity(worker.ID, worker.LlamaSwapURL)
		s.recordScrapeResult(worker.ID, err, time.Now())
		return activity, err
	}, func(worker Worker) (int, error) {
		if s.scraper == nil || worker.LlamaSwapURL == "" {
			return 0, nil
		}
		samples, err := s.scraper.PullPerformance(worker.ID, worker.LlamaSwapURL)
		s.recordScrapeResult(worker.ID, err, time.Now())
		return samples, err
	})
	s.metrics.ObserveModelProvisioning(s.config, workers, now)
	s.metrics.Handler().ServeHTTP(w, r)
}

func (s *Server) recordScrapeResult(workerID string, err error, now time.Time) {
	if s.workers == nil {
		return
	}
	if err != nil {
		s.workers.RecordScrapeFailure(workerID, now)
		return
	}
	s.workers.RecordScrapeSuccess(workerID)
}

type modelsResponse struct {
	Object string       `json:"object"`
	Data   []modelEntry `json:"data"`
}

type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	models := make([]modelEntry, 0, len(s.config.Models))
	scheduler := Scheduler{Config: s.config, Workers: s.workers}
	for name := range s.config.Models {
		if _, err := scheduler.Pick(name, now, nil); err != nil {
			continue
		}
		models = append(models, modelEntry{
			ID:      name,
			Object:  "model",
			OwnedBy: "self_host",
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	writeJSON(w, modelsResponse{Object: "list", Data: models})
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
	for _, event := range hb.Events {
		cached := s.recordAgentEvent(hb.AgentID, event, time.Now())
		if cached.Event == "" {
			continue
		}
		if err := appendWorkerEventLog(s.workerEventLogPath, cached); err != nil {
			s.logEvent("worker_event_log_error", map[string]any{
				"worker_id": hb.AgentID,
				"error":     err.Error(),
			})
		}
		s.logAgentEvent(hb.AgentID, event)
	}
	writeJSON(w, resp)
}

func (s *Server) logAgentEvent(workerID string, event protocol.AgentEvent) {
	fields := map[string]any{
		"worker_id":   workerID,
		"agent_event": event.Event,
	}
	if !event.Time.IsZero() {
		fields["time"] = event.Time.Format(time.RFC3339Nano)
	}
	if event.Model != "" {
		fields["model"] = event.Model
	}
	if event.FromState != "" {
		fields["from_state"] = event.FromState
	}
	if event.ToState != "" {
		fields["to_state"] = event.ToState
	}
	if event.Object != "" {
		fields["object"] = event.Object
	}
	if event.Kind != "" {
		fields["kind"] = event.Kind
	}
	if event.CRC64ECMA != "" {
		fields["crc64ecma"] = event.CRC64ECMA
	}
	if event.DownloadedBytes > 0 {
		fields["downloaded_bytes"] = event.DownloadedBytes
	}
	if event.TotalBytes > 0 {
		fields["total_bytes"] = event.TotalBytes
	}
	if event.Percent > 0 {
		fields["percent"] = event.Percent
	}
	if event.DurationMS > 0 {
		fields["duration_ms"] = event.DurationMS
	}
	if event.Error != "" {
		fields["error"] = event.Error
	}
	s.logEvent("agent_event", fields)
}

func (s *Server) recordGatewayWorkerEvent(workerID string, event protocol.AgentEvent) {
	cached := s.recordAgentEvent(workerID, event, time.Now())
	if cached.Event == "" {
		return
	}
	if err := appendWorkerEventLog(s.workerEventLogPath, cached); err != nil {
		s.logEvent("worker_event_log_error", map[string]any{
			"worker_id": workerID,
			"error":     err.Error(),
		})
	}
	s.logAgentEvent(workerID, event)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}
