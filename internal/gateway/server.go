package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
	"llm-swap/internal/transport"
)

type Server struct {
	configManager      *ConfigManager
	workers            *WorkerRegistry
	accounting         *Accounting
	limiter            *QueueLimiter
	metrics            *Metrics
	metricsStore       *VictoriaMetricsClient
	scraper            *MetricsScraper
	access             *AccessTracker
	pressure           *PressureTracker
	replicaCooldowns   *ReplicaCooldowns
	transportLeases    *TransportLeaseManager
	transportLeaseErr  error
	exchangeRates      *ExchangeRateProvider
	recordsStore       RecordsStore
	requestLogPath     string
	workerEventLogPath string
	proxyAttempts      int
	logger             *log.Logger
	eventMu            sync.Mutex
	recentEvents       []uiAgentEvent
	requestMu          sync.Mutex
	recentRequests     []RequestLogEntry
	mux                *http.ServeMux
}

func NewServer(cfg config.GatewayConfig) *Server {
	return newServerWithPaths(cfg, "", "", "", config.GatewayRuntimeOverrides{})
}

func NewServerWithGatewayPersistence(cfg config.GatewayConfig, requestLogPath string) *Server {
	return newServerWithPaths(cfg, requestLogPath, "", "", config.GatewayRuntimeOverrides{})
}

func NewServerWithGatewayPersistencePaths(cfg config.GatewayConfig, requestLogPath string, workerEventLogPath string) *Server {
	return newServerWithPaths(cfg, requestLogPath, workerEventLogPath, "", config.GatewayRuntimeOverrides{})
}

func NewServerWithGatewayConfigPath(cfg config.GatewayConfig, configPath string) *Server {
	return NewServerWithGatewayConfigPathAndOverrides(cfg, configPath, config.GatewayRuntimeOverrides{})
}

func NewServerWithGatewayConfigPathAndOverrides(cfg config.GatewayConfig, configPath string, overrides config.GatewayRuntimeOverrides) *Server {
	return newServerWithPaths(cfg, "", "", configPath, overrides)
}

func NewServerWithGatewayPersistencePathsAndConfigPath(cfg config.GatewayConfig, requestLogPath string, workerEventLogPath string, configPath string) *Server {
	return newServerWithPaths(cfg, requestLogPath, workerEventLogPath, configPath, config.GatewayRuntimeOverrides{})
}

func NewServerWithGatewayPersistencePathsAndConfigPathAndOverrides(cfg config.GatewayConfig, requestLogPath string, workerEventLogPath string, configPath string, overrides config.GatewayRuntimeOverrides) *Server {
	return newServerWithPaths(cfg, requestLogPath, workerEventLogPath, configPath, overrides)
}

func newServerWithPaths(cfg config.GatewayConfig, requestLogPath string, workerEventLogPath string, configPath string, overrides config.GatewayRuntimeOverrides) *Server {
	return newServerWithConfigManager(cfg, requestLogPath, workerEventLogPath, configPath, NewConfigManagerWithOverrides(cfg, configPath, overrides))
}

func NewServerWithConfigRevisionStore(ctx context.Context, cfg config.GatewayConfig, requestLogPath string, workerEventLogPath string, configPath string, overrides config.GatewayRuntimeOverrides, revisionStore ConfigRevisionStore) (*Server, error) {
	manager, err := NewConfigManagerWithOverridesAndRevisionStore(ctx, cfg, configPath, overrides, revisionStore)
	if err != nil {
		return nil, err
	}
	manager.promotionStore = newFileServiceNameArchiveStore(DefaultGatewayServiceNameArchivePath)
	return newServerWithConfigManager(cfg, requestLogPath, workerEventLogPath, configPath, manager), nil
}

func newServerWithConfigManager(cfg config.GatewayConfig, requestLogPath string, workerEventLogPath string, configPath string, configManager *ConfigManager) *Server {
	if cfg.Tokens.LlamaSwap == "" {
		cfg.Tokens.LlamaSwap = cfg.Tokens.Agent
	}
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
	recentRequests := []RequestLogEntry{}
	if requestLogPath != "" {
		if loaded, err := loadRecentRequestLogs(requestLogPath, uiEventLimit); err == nil {
			recentRequests = loaded
		}
	}
	var metricsStore *VictoriaMetricsClient
	if cfg.MetricsStore.Enabled && strings.TrimSpace(cfg.MetricsStore.QueryURL) != "" {
		metricsStore = NewVictoriaMetricsClient(cfg.MetricsStore.QueryURL, time.Duration(cfg.MetricsStore.TimeoutMS)*time.Millisecond)
	}
	var recordsStore RecordsStore
	if cfg.RecordsStore.Enabled {
		store, err := NewPostgresRecordsStore(
			context.Background(),
			cfg.RecordsStore.DSN,
			cfg.RecordsStore.GatewayID,
			time.Duration(cfg.RecordsStore.TimeoutMS)*time.Millisecond,
			cfg.RecordsStore.AutoMigrate,
		)
		if err != nil {
			log.Printf("records_store_init_error: %v", err)
		} else {
			recordsStore = store
		}
	}
	var transportLeaseStore TransportLeaseStore = NewMemoryTransportLeaseStore()
	leaseStorePath := strings.TrimSpace(cfg.Transport.FRP.LeaseStorePath)
	if leaseStorePath == "" && strings.TrimSpace(configPath) != "" {
		leaseStorePath = filepath.Join(filepath.Dir(configPath), "transport-leases.json")
	}
	if leaseStorePath != "" {
		transportLeaseStore = NewFileTransportLeaseStore(leaseStorePath)
	}
	transportLeases, transportLeaseErr := NewTransportLeaseManager(transportLeaseStore, nil, nil)

	s := &Server{
		configManager:      configManager,
		workers:            NewWorkerRegistry(6 * time.Second),
		accounting:         NewAccounting(),
		limiter:            NewQueueLimiter(),
		metrics:            NewMetrics(),
		metricsStore:       metricsStore,
		scraper:            NewMetricsScraperWithToken(cfg.Tokens.LlamaSwap),
		access:             access,
		pressure:           NewPressureTracker(defaultPressureWindow),
		replicaCooldowns:   NewReplicaCooldowns(defaultReplicaCooldownTTL),
		transportLeases:    transportLeases,
		transportLeaseErr:  transportLeaseErr,
		exchangeRates:      NewExchangeRateProvider(),
		recordsStore:       recordsStore,
		requestLogPath:     requestLogPath,
		workerEventLogPath: workerEventLogPath,
		proxyAttempts:      configuredProxyAttempts(cfg),
		logger:             log.New(os.Stdout, "", log.LstdFlags),
		recentEvents:       recentEvents,
		recentRequests:     recentRequests,
		mux:                http.NewServeMux(),
	}

	s.mux.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	s.mux.Handle("GET /install-worker.sh", http.HandlerFunc(s.handleInstallWorkerScript))
	s.mux.Handle("GET /metrics", http.HandlerFunc(s.handleMetrics))
	s.mux.Handle("GET /ui", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/models", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/workers", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/billing", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/event-log", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/request-log", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/config", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/advanced", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUI)))
	s.mux.Handle("GET /ui/assets/", uiAuth(cfg.Tokens.Agent, embeddedAdminAssetHandler()))
	s.mux.Handle("GET /ui/status", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIStatus)))
	s.mux.Handle("GET /ui/requests", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIRequests)))
	s.mux.Handle("GET /ui/events", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIEvents)))
	s.mux.Handle("GET /ui/traffic", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUITraffic)))
	s.mux.Handle("GET /ui/metrics/summary", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIMetricsSummary)))
	s.mux.Handle("GET /ui/metrics/model", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIMetricsModel)))
	s.mux.Handle("GET /ui/metrics/worker", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIMetricsWorker)))
	s.mux.Handle("GET /api/billing", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleBilling)))
	s.mux.Handle("GET /ui/api/billing", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleBilling)))
	s.mux.Handle("GET /ui/api/config", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIConfig)))
	s.mux.Handle("POST /ui/api/config/validate", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIConfigValidate)))
	s.mux.Handle("POST /ui/api/config/dry-run", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIConfigDryRun)))
	s.mux.Handle("POST /ui/api/config/apply", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIConfigApply)))
	s.mux.Handle("POST /ui/api/service-names/promote", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIServiceNamePromote)))
	s.mux.Handle("POST /ui/api/service-names/rollback", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIServiceNameRollback)))
	s.mux.Handle("POST /ui/api/workers/{id}/drain", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIWorkerDrain)))
	s.mux.Handle("POST /ui/api/workers/{id}/undrain", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIWorkerUndrain)))
	s.mux.Handle("POST /ui/api/models/{model}/warm", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIModelWarm)))
	s.mux.Handle("POST /ui/api/models/{model}/unload", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIModelUnload)))
	s.mux.Handle("POST /ui/api/cooldowns/clear", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUICooldownClear)))
	s.mux.Handle("GET /internal/agent/config", bearerAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleAgentConfig)))
	s.mux.Handle("POST /internal/agent/transport/lease", bearerAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleTransportLease)))
	s.mux.Handle("POST /internal/agent/heartbeat", bearerAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleAgentHeartbeat)))
	s.mux.Handle("GET /v1/models", bearerAuth(cfg.Tokens.Client, http.HandlerFunc(s.handleModels)))
	s.mux.Handle("POST /v1/chat/completions", bearerAuth(cfg.Tokens.Client, http.HandlerFunc(s.handleModelProxy)))

	return s
}

func (s *Server) currentConfig() config.GatewayConfig {
	if s.configManager == nil {
		return config.GatewayConfig{}
	}
	cfg, _ := s.configManager.Snapshot()
	return cfg
}

func configuredProxyAttempts(cfg config.GatewayConfig) int {
	if cfg.Gateway.ProxyAttempts > 0 {
		return cfg.Gateway.ProxyAttempts
	}
	return config.DefaultProxyAttempts
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	cfg := activeGatewayConfig(s.currentConfig())
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
	s.metrics.ObserveModelProvisioning(cfg, workers, now)
	s.metrics.ObserveModelQueues(cfg, s.limiter)
	s.metrics.ObserveReplicaCooldowns(s.replicaCooldowns.Snapshot(now), now)
	s.observeBillingMetrics(r.Context(), now)
	s.metrics.Handler().ServeHTTP(w, r)
}

func (s *Server) observeBillingMetrics(ctx context.Context, now time.Time) {
	if s == nil || s.metrics == nil || s.recordsStore == nil {
		return
	}
	store, ok := s.recordsStore.(interface {
		BillingSummary(context.Context, BillingQuery) (BillingSummary, error)
	})
	if !ok {
		return
	}
	summary, err := store.BillingSummary(ctx, BillingQuery{
		Start:            now.Add(-time.Hour),
		End:              now,
		WorkerDayCostRMB: defaultWorkerDayCostRMB,
		ExchangeRate:     s.exchangeRates.CNYToUSD(ctx),
		ModelPricing:     modelBillingPricing(s.currentConfig()),
	})
	if err != nil {
		s.logEvent("billing_metrics_error", map[string]any{"error": err.Error()})
		return
	}
	s.metrics.ObserveBillingSummary(summary)
}

func (s *Server) recordScrapeResult(workerID string, err error, now time.Time) {
	if s.workers == nil {
		return
	}
	if err != nil {
		if isReverseAccessFailure(err) {
			s.recordReverseAccessFailure(workerID, err, now)
			return
		}
		s.workers.RecordScrapeFailure(workerID, now)
		return
	}
	s.workers.RecordScrapeSuccess(workerID)
}

func (s *Server) recordReverseAccessResult(workerID string, err error, now time.Time) bool {
	if err == nil {
		if s.workers != nil {
			s.workers.RecordScrapeSuccess(workerID)
		}
		return false
	}
	if !isReverseAccessFailure(err) {
		return false
	}
	return s.recordReverseAccessFailure(workerID, err, now)
}

func (s *Server) recordReverseAccessFailure(workerID string, err error, now time.Time) bool {
	if s == nil || s.workers == nil || workerID == "" || err == nil {
		return false
	}
	if marked := s.workers.RecordReverseFailure(workerID, now); marked {
		s.logEvent("worker_reverse_access_unavailable", map[string]any{
			"worker_id": workerID,
			"error":     err.Error(),
		})
		s.recordGatewayWorkerEvent(workerID, protocol.AgentEvent{
			Event: "gateway_worker_reverse_access_unavailable",
			Error: err.Error(),
		})
		return true
	}
	return false
}

func isReverseAccessFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var statusErr HTTPStatusError
	if errors.As(err, &statusErr) {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
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
	cfg := activeGatewayConfig(s.currentConfig())
	models := make([]modelEntry, 0, len(cfg.Models)+len(cfg.ModelAliases))
	scheduler := Scheduler{Config: cfg, Workers: s.workers}
	for name := range cfg.Models {
		if _, err := scheduler.Pick(name, now, nil); err != nil {
			continue
		}
		models = append(models, modelEntry{
			ID:      name,
			Object:  "model",
			OwnedBy: "self_host",
		})
	}
	for alias, target := range cfg.ModelAliases {
		if _, err := scheduler.Pick(target, now, nil); err != nil {
			continue
		}
		models = append(models, modelEntry{
			ID:      alias,
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
	cfg, configRevision := s.configManager.Snapshot()
	cfg = activeGatewayConfig(cfg)
	tag, policy, ok := s.matchedTagPolicy(cfg, r.URL.Query().Get("tags"))
	if !ok {
		http.Error(w, "exactly one configured tag must match", http.StatusBadRequest)
		return
	}

	resp := protocol.AgentConfigResponse{
		ConfigRevision: configRevision,
		OSS:            cfg.OSS,
		Models:         make(map[string]config.Model, len(policy.AllowedModels)),
		TagPolicy: protocol.AgentTagPolicy{
			Tag:            tag,
			AllowedModels:  append([]string(nil), policy.AllowedModels...),
			WarmWhenIdle:   policy.WarmWhenIdle,
			WorkerDefaults: policy.WorkerDefaults,
		},
	}
	for _, modelName := range policy.AllowedModels {
		model, ok := cfg.Models[modelName]
		if ok {
			resp.Models[modelName] = model
		}
	}
	if cfg.Transport.Type == "frp_tcp" {
		if s.transportLeases == nil || s.transportLeaseErr != nil {
			http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
			return
		}
		agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
		if agentID == "" {
			http.Error(w, "agent_id is required", http.StatusBadRequest)
			return
		}
		generation, err := transportConfigGeneration(cfg)
		if err != nil {
			http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
			return
		}
		bootstrap, err := transport.SealBootstrap(cfg.Tokens.Agent, agentID, generation, transport.Bootstrap{
			Type:            cfg.Transport.Type,
			ServerAddr:      cfg.Transport.FRP.ServerAddr,
			ServerPort:      cfg.Transport.FRP.ServerPort,
			AuthToken:       cfg.Transport.FRP.AuthToken,
			PortStart:       cfg.Transport.FRP.PortStart,
			PortEnd:         cfg.Transport.FRP.PortEnd,
			LeaseTTLSeconds: cfg.Transport.FRP.LeaseTTLSeconds,
			LlamaSwapToken:  cfg.Tokens.LlamaSwap,
		})
		if err != nil {
			http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
			return
		}
		resp.Transport = &bootstrap
	}

	writeJSON(w, resp)
}

func (s *Server) handleTransportLease(w http.ResponseWriter, r *http.Request) {
	if s.transportLeases == nil || s.transportLeaseErr != nil {
		http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
		return
	}
	var request protocol.TransportLeaseRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid transport lease request", http.StatusBadRequest)
		return
	}
	request.AgentID = strings.TrimSpace(request.AgentID)
	if request.AgentID == "" || request.Generation == 0 {
		http.Error(w, "invalid transport lease request", http.StatusBadRequest)
		return
	}
	if request.Release {
		if strings.TrimSpace(request.LeaseID) == "" {
			http.Error(w, "invalid transport lease request", http.StatusBadRequest)
			return
		}
		released, err := s.transportLeases.Release(request.AgentID, request.LeaseID, request.Generation)
		if err != nil {
			http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
			return
		}
		if !released {
			http.Error(w, "transport lease conflict", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	cfg, _ := s.configManager.Snapshot()
	if cfg.Transport.Type != "frp_tcp" {
		http.Error(w, "transport lease unavailable", http.StatusBadRequest)
		return
	}
	if _, _, ok := s.matchedTagPolicy(activeGatewayConfig(cfg), strings.Join(request.Tags, ",")); !ok {
		http.Error(w, "invalid transport lease request", http.StatusBadRequest)
		return
	}
	generation, err := transportConfigGeneration(cfg)
	if err != nil {
		http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
		return
	}
	if request.Generation != generation {
		http.Error(w, "transport lease generation conflict", http.StatusConflict)
		return
	}
	policy := transportLeasePolicy(cfg)
	lease, err := s.transportLeases.Acquire(policy, TransportLeaseAcquireRequest{
		AgentID:        request.AgentID,
		Generation:     request.Generation,
		CurrentLeaseID: request.LeaseID,
		ExcludedSlots:  append([]int(nil), request.ExcludeSlots...),
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrTransportLeaseConflict):
			http.Error(w, "transport lease conflict", http.StatusConflict)
		case errors.Is(err, ErrTransportLeaseCapacity):
			http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
		default:
			if strings.HasPrefix(err.Error(), "invalid ") {
				http.Error(w, "invalid transport lease request", http.StatusBadRequest)
			} else {
				http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
			}
		}
		return
	}
	writeJSON(w, protocol.TransportLeaseResponse{
		LeaseID:    lease.LeaseID,
		Slot:       lease.Slot,
		RemotePort: lease.RemotePort,
		Generation: lease.Generation,
		ExpiresAt:  lease.ExpiresAt,
	})
}

func transportLeasePolicy(cfg config.GatewayConfig) TransportLeasePolicy {
	return TransportLeasePolicy{
		PortStart: cfg.Transport.FRP.PortStart,
		PortEnd:   cfg.Transport.FRP.PortEnd,
		TTL:       time.Duration(cfg.Transport.FRP.LeaseTTLSeconds) * time.Second,
	}
}

func (s *Server) matchedTagPolicy(cfg config.GatewayConfig, tagsParam string) (string, config.TagPolicy, bool) {
	matches := make(map[string]config.TagPolicy)
	for _, rawTag := range strings.Split(tagsParam, ",") {
		tag := strings.TrimSpace(rawTag)
		if tag == "" {
			continue
		}
		policy, ok := cfg.TagPolicies[tag]
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
	cfg, _ := s.configManager.Snapshot()
	if cfg.Transport.Type == "frp_tcp" {
		if s.transportLeases == nil || s.transportLeaseErr != nil {
			http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, _, ok := s.matchedTagPolicy(activeGatewayConfig(cfg), strings.Join(hb.Tags, ",")); !ok {
			http.Error(w, "invalid agent heartbeat", http.StatusBadRequest)
			return
		}
		generation, err := transportConfigGeneration(cfg)
		if err != nil {
			http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
			return
		}
		notReady := hb.LlamaSwapURL == "" && hb.TransportLeaseID == "" && hb.TransportGeneration == 0
		if !notReady && (hb.LlamaSwapURL == "" || hb.TransportLeaseID == "" || hb.TransportGeneration != generation) {
			http.Error(w, "transport lease conflict", http.StatusConflict)
			return
		}
		if !notReady {
			lease, err := s.transportLeases.LookupCurrent(hb.AgentID, hb.TransportLeaseID, hb.TransportGeneration)
			if err != nil {
				if errors.Is(err, ErrTransportLeaseNotFound) {
					http.Error(w, "transport lease conflict", http.StatusConflict)
				} else {
					http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
				}
				return
			}
			if hb.LlamaSwapURL != expectedFRPLlamaSwapURL(cfg.Transport.FRP.ServerAddr, lease.RemotePort) {
				http.Error(w, "transport lease conflict", http.StatusConflict)
				return
			}
			if _, err := s.transportLeases.Renew(transportLeasePolicy(cfg), hb.AgentID, hb.TransportLeaseID, hb.TransportGeneration); err != nil {
				if errors.Is(err, ErrTransportLeaseNotFound) {
					http.Error(w, "transport lease conflict", http.StatusConflict)
				} else {
					http.Error(w, "transport unavailable", http.StatusServiceUnavailable)
				}
				return
			}
			hb.LlamaSwapURL = expectedFRPLlamaSwapURL(gatewayFRPDialAddr(cfg.Transport.FRP), lease.RemotePort)
		}
	}

	now := time.Now()
	resp := s.workers.UpsertHeartbeat(hb, now)
	for _, event := range hb.Events {
		cached := s.recordAgentEvent(hb.AgentID, event, now)
		if cached.Event == "" {
			continue
		}
		if s.recordsStore != nil {
			if err := s.recordsStore.AppendWorkerEvent(r.Context(), cached); err != nil {
				s.logEvent("worker_event_record_store_error", map[string]any{
					"worker_id": hb.AgentID,
					"event":     cached.Event,
					"error":     err.Error(),
				})
			}
		}
		if err := appendWorkerEventLog(s.workerEventLogPath, cached); err != nil {
			s.logEvent("worker_event_log_error", map[string]any{
				"worker_id": hb.AgentID,
				"error":     err.Error(),
			})
		}
		s.logAgentEvent(hb.AgentID, event)
	}
	if s.recordsStore != nil {
		if store, ok := s.recordsStore.(interface {
			RecordWorkerModelSnapshot(context.Context, string, []protocol.RunningModel, time.Time) error
		}); ok {
			if err := store.RecordWorkerModelSnapshot(r.Context(), hb.AgentID, hb.RunningModels, now); err != nil {
				s.logEvent("worker_model_snapshot_store_error", map[string]any{
					"worker_id": hb.AgentID,
					"error":     err.Error(),
				})
			}
		}
	}
	writeJSON(w, resp)
}

func expectedFRPLlamaSwapURL(serverAddr string, remotePort int) string {
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(strings.TrimSpace(serverAddr), strconv.Itoa(remotePort)),
	}).String()
}

func gatewayFRPDialAddr(cfg config.FRPTCPConfig) string {
	if dialAddr := strings.TrimSpace(cfg.DialAddr); dialAddr != "" {
		return dialAddr
	}
	return strings.TrimSpace(cfg.ServerAddr)
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
	if s.recordsStore != nil {
		if err := s.recordsStore.AppendWorkerEvent(context.Background(), cached); err != nil {
			s.logEvent("worker_event_record_store_error", map[string]any{
				"worker_id": workerID,
				"event":     cached.Event,
				"error":     err.Error(),
			})
		}
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
