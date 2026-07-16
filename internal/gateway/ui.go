package gateway

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"llm-swap/internal/buildinfo"
	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

const uiEventLimit = 100

type uiStatusResponse struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Summary     uiSummary       `json:"summary"`
	Models      []uiModelStatus `json:"models"`
	Workers     []uiWorker      `json:"workers"`
	Events      []uiAgentEvent  `json:"events"`
}

type uiSummary struct {
	TotalWorkers      int `json:"total_workers"`
	HealthyWorkers    int `json:"healthy_workers"`
	DrainingWorkers   int `json:"draining_workers"`
	StaleWorkers      int `json:"stale_workers"`
	WorkersWithErrors int `json:"workers_with_errors"`
	ConfiguredModels  int `json:"configured_models"`
	AvailableModels   int `json:"available_models"`
	Underprovisioned  int `json:"underprovisioned_models"`
	ActiveRequests    int `json:"active_requests"`
	RecentErrorEvents int `json:"recent_error_events"`
}

type uiModelStatus struct {
	Name             string          `json:"name"`
	Priority         int             `json:"priority"`
	MinLoaded        int             `json:"min_loaded"`
	MaxLoaded        int             `json:"max_loaded"`
	MaxConcurrency   int             `json:"max_concurrency"`
	MaxQueue         int             `json:"max_queue"`
	QueueTimeoutMS   int             `json:"queue_timeout_ms"`
	TTL              int             `json:"ttl"`
	Artifact         config.Artifact `json:"artifact"`
	Available        bool            `json:"available"`
	ReadyWorkers     int             `json:"ready_workers"`
	RunningWorkers   int             `json:"running_workers"`
	Installing       int             `json:"installing_workers"`
	Missing          int             `json:"missing_workers"`
	ErrorWorkers     int             `json:"error_workers"`
	WorkerStatuses   []uiModelWorker `json:"worker_statuses"`
	AvailabilityNote string          `json:"availability_note"`
	Traffic          uiModelTraffic  `json:"traffic"`
}

type uiModelTraffic struct {
	LastAccess       time.Time `json:"last_access,omitempty"`
	Requests         uint64    `json:"requests"`
	Status2xx        uint64    `json:"status_2xx"`
	Status4xx        uint64    `json:"status_4xx"`
	Status5xx        uint64    `json:"status_5xx"`
	PromptTokens     uint64    `json:"prompt_tokens"`
	CompletionTokens uint64    `json:"completion_tokens"`
	TotalTokens      uint64    `json:"total_tokens"`
	CacheTokens      uint64    `json:"cache_tokens"`
	ReasoningTokens  uint64    `json:"reasoning_tokens"`
	AvgDurationMS    uint64    `json:"avg_duration_ms"`
	MaxDurationMS    uint64    `json:"max_duration_ms"`
}

type uiModelWorker struct {
	WorkerID                 string    `json:"worker_id"`
	ArtifactStatus           string    `json:"artifact_status"`
	RunningState             string    `json:"running_state,omitempty"`
	Health                   string    `json:"health"`
	CooldownActive           bool      `json:"cooldown_active"`
	CooldownReason           string    `json:"cooldown_reason,omitempty"`
	CooldownRemainingSeconds int64     `json:"cooldown_remaining_seconds,omitempty"`
	CooldownUntil            time.Time `json:"cooldown_until,omitempty"`
}

type uiWorker struct {
	ID                   string                  `json:"id"`
	Tags                 []string                `json:"tags"`
	Health               string                  `json:"health"`
	State                string                  `json:"state"`
	LlamaSwapURL         string                  `json:"llama_swap_url"`
	LastHeartbeat        time.Time               `json:"last_heartbeat"`
	LastHeartbeatAgeMS   int64                   `json:"last_heartbeat_age_ms"`
	ActiveRequests       int                     `json:"active_requests"`
	RunningModels        []protocol.RunningModel `json:"running_models"`
	GPUDevices           []protocol.GPUDevice    `json:"gpu_devices"`
	Artifacts            map[string]string       `json:"artifacts"`
	Capacity             config.WorkerDefaults   `json:"capacity"`
	NeedsRestart         bool                    `json:"needs_restart"`
	LastError            string                  `json:"last_error,omitempty"`
	ScrapeFailures       int                     `json:"scrape_failures"`
	ScrapeBackoffUntil   time.Time               `json:"scrape_backoff_until,omitempty"`
	ScrapeBackoffSeconds int64                   `json:"scrape_backoff_seconds,omitempty"`
	AllowedModels        []string                `json:"allowed_models"`
	HealthProblem        string                  `json:"health_problem,omitempty"`
	ReplicaCooldowns     []ReplicaCooldown       `json:"replica_cooldowns"`
	AgentBuild           protocol.BuildInfo      `json:"agent_build"`
	AgentVersionStatus   string                  `json:"agent_version_status"`
}

type uiAgentEvent struct {
	ReceivedAt      time.Time `json:"received_at"`
	WorkerID        string    `json:"worker_id"`
	Time            time.Time `json:"time"`
	Event           string    `json:"event"`
	Model           string    `json:"model,omitempty"`
	FromState       string    `json:"from_state,omitempty"`
	ToState         string    `json:"to_state,omitempty"`
	Object          string    `json:"object,omitempty"`
	Kind            string    `json:"kind,omitempty"`
	DownloadedBytes int64     `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64     `json:"total_bytes,omitempty"`
	Percent         float64   `json:"percent,omitempty"`
	DurationMS      int64     `json:"duration_ms,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type WorkerEventRecord = uiAgentEvent

type uiEventsResponse struct {
	Events     []uiAgentEvent `json:"events"`
	NextOffset int            `json:"next_offset"`
	HasMore    bool           `json:"has_more"`
}

type uiRequestsResponse struct {
	Requests   []RequestLogEntry `json:"requests"`
	NextOffset int               `json:"next_offset"`
	HasMore    bool              `json:"has_more"`
}

type uiMetricsResponse struct {
	Range  string             `json:"range"`
	Step   string             `json:"step"`
	Series []HistoricalSeries `json:"series"`
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if html, ok := embeddedAdminIndex(); ok {
		_, _ = w.Write(html)
		return
	}
	_, _ = w.Write([]byte(gatewayUIHTML))
}

func (s *Server) handleUIStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildUIStatus(time.Now()))
}

func (s *Server) handleUIEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if s.recordsStore != nil {
		resp, err := s.recordsStore.PageWorkerEvents(r.Context(), offset, limit)
		if err != nil {
			http.Error(w, "failed to load worker events", http.StatusInternalServerError)
			return
		}
		if resp.Events == nil {
			resp.Events = []uiAgentEvent{}
		}
		writeJSON(w, resp)
		return
	}
	if s.workerEventLogPath == "" {
		events := s.recentAgentEvents()
		if offset < 0 {
			offset = 0
		}
		if limit <= 0 {
			limit = uiEventLimit
		}
		if offset > len(events) {
			offset = len(events)
		}
		end := offset + limit
		if end > len(events) {
			end = len(events)
		}
		writeJSON(w, uiEventsResponse{
			Events:     append([]uiAgentEvent(nil), events[offset:end]...),
			NextOffset: end,
			HasMore:    len(events) > end,
		})
		return
	}
	resp, err := loadWorkerEventPage(s.workerEventLogPath, offset, limit)
	if err != nil {
		http.Error(w, "failed to load worker events", http.StatusInternalServerError)
		return
	}
	if resp.Events == nil {
		resp.Events = []uiAgentEvent{}
	}
	writeJSON(w, resp)
}

func (s *Server) handleUIRequests(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if s.recordsStore != nil {
		resp, err := s.recordsStore.PageRequestRecords(r.Context(), offset, limit)
		if err != nil {
			http.Error(w, "failed to load request records", http.StatusInternalServerError)
			return
		}
		if resp.Requests == nil {
			resp.Requests = []RequestLogEntry{}
		}
		writeJSON(w, resp)
		return
	}
	if s.requestLogPath == "" {
		requests := s.recentRequestLogs()
		if offset < 0 {
			offset = 0
		}
		if limit <= 0 {
			limit = uiEventLimit
		}
		if offset > len(requests) {
			offset = len(requests)
		}
		end := offset + limit
		if end > len(requests) {
			end = len(requests)
		}
		writeJSON(w, uiRequestsResponse{
			Requests:   append([]RequestLogEntry(nil), requests[offset:end]...),
			NextOffset: end,
			HasMore:    len(requests) > end,
		})
		return
	}
	resp, err := loadRequestLogPage(s.requestLogPath, offset, limit)
	if err != nil {
		http.Error(w, "failed to load request logs", http.StatusInternalServerError)
		return
	}
	if resp.Requests == nil {
		resp.Requests = []RequestLogEntry{}
	}
	writeJSON(w, resp)
}

func (s *Server) handleUIMetricsSummary(w http.ResponseWriter, r *http.Request) {
	queries := []historicalQuery{
		{Name: "requests_rate", Query: `sum(rate(llm_swap_gateway_requests_total[5m]))`},
		{Name: "errors_rate", Query: `sum(rate(llm_swap_gateway_requests_total{status_code=~"5.."}[5m]))`},
		{Name: "model_requests", Query: `sum(increase(llm_swap_gateway_requests_total[5m])) by (model)`},
		{Name: "model_avg_duration_ms", Query: `1000 * sum(rate(llm_swap_gateway_request_duration_seconds_sum[5m])) by (model) / clamp_min(sum(rate(llm_swap_gateway_request_duration_seconds_count[5m])) by (model), 1)`},
		{Name: "model_queue_depth", Query: `llm_swap_gateway_model_queue_depth`},
		{Name: "active_requests", Query: `sum(llm_swap_gateway_active_requests)`},
		{Name: "healthy_workers", Query: `sum(llm_swap_gateway_worker_up)`},
		{Name: "loaded_replicas", Query: `sum(llm_swap_gateway_model_loaded_replicas)`},
		{Name: "queue_wait_p95", Query: `histogram_quantile(0.95, sum(rate(llm_swap_gateway_queue_wait_seconds_bucket[5m])) by (le))`},
	}
	s.writeHistoricalMetrics(w, r, queries)
}

func (s *Server) handleUIMetricsModel(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		http.Error(w, "model is required", http.StatusBadRequest)
		return
	}
	label := promLabelValue(model)
	queries := []historicalQuery{
		{Name: "model_requests_rate", Query: `sum(rate(llm_swap_gateway_requests_total{model="` + label + `"}[5m])) by (model)`},
		{Name: "model_errors_rate", Query: `sum(rate(llm_swap_gateway_requests_total{model="` + label + `",status_code=~"5.."}[5m])) by (model)`},
		{Name: "model_duration_p95", Query: `histogram_quantile(0.95, sum(rate(llm_swap_gateway_request_duration_seconds_bucket{model="` + label + `"}[5m])) by (model, le))`},
		{Name: "model_active_requests", Query: `sum(llm_swap_gateway_active_requests{model="` + label + `"}) by (model)`},
		{Name: "model_tokens_rate", Query: `sum(rate(llm_swap_gateway_model_tokens_total{model="` + label + `"}[5m])) by (model, type)`},
	}
	s.writeHistoricalMetrics(w, r, queries)
}

func (s *Server) handleUIMetricsWorker(w http.ResponseWriter, r *http.Request) {
	workerID := strings.TrimSpace(r.URL.Query().Get("worker_id"))
	if workerID == "" {
		http.Error(w, "worker_id is required", http.StatusBadRequest)
		return
	}
	label := promLabelValue(workerID)
	queries := []historicalQuery{
		{Name: "worker_up", Query: `llm_swap_gateway_worker_up{worker_id="` + label + `"}`},
		{Name: "worker_active_requests", Query: `llm_swap_gateway_worker_active_requests{worker_id="` + label + `"}`},
		{Name: "worker_running_models", Query: `llm_swap_gateway_worker_running_models{worker_id="` + label + `"}`},
		{Name: "worker_scrape_errors_rate", Query: `sum(rate(llm_swap_gateway_worker_metrics_scrape_errors_total{worker_id="` + label + `"}[5m])) by (worker_id)`},
	}
	s.writeHistoricalMetrics(w, r, queries)
}

type historicalQuery struct {
	Name  string
	Query string
}

func (s *Server) writeHistoricalMetrics(w http.ResponseWriter, r *http.Request, queries []historicalQuery) {
	if s.metricsStore == nil {
		http.Error(w, "metrics store is not enabled", http.StatusServiceUnavailable)
		return
	}
	cfg := s.currentConfig()
	now := time.Now()
	start, end, step, rangeLabel := parseMetricsRange(
		r.URL.Query().Get("range"),
		r.URL.Query().Get("step"),
		cfg.MetricsStore.DefaultRange,
		cfg.MetricsStore.MaxRange,
		now,
	)
	series := make([]HistoricalSeries, 0, len(queries))
	for _, query := range queries {
		items, err := s.metricsStore.QueryRange(r.Context(), query.Name, query.Query, start, end, step)
		if err != nil {
			http.Error(w, "failed to query metrics store", http.StatusBadGateway)
			return
		}
		series = append(series, items...)
	}
	if series == nil {
		series = []HistoricalSeries{}
	}
	writeJSON(w, uiMetricsResponse{
		Range:  rangeLabel,
		Step:   metricsStepLabel(r.URL.Query().Get("step"), step),
		Series: series,
	})
}

func promLabelValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return replacer.Replace(value)
}

func metricsStepLabel(raw string, step time.Duration) string {
	_, label, ok := parseMetricsDurationWithLabel(raw)
	if ok {
		return label
	}
	return formatMetricsDuration(step)
}

func formatMetricsDuration(duration time.Duration) string {
	if duration%(24*time.Hour) == 0 {
		return strconv.Itoa(int(duration/(24*time.Hour))) + "d"
	}
	if duration%time.Hour == 0 {
		return strconv.Itoa(int(duration/time.Hour)) + "h"
	}
	if duration%time.Minute == 0 {
		return strconv.Itoa(int(duration/time.Minute)) + "m"
	}
	return strconv.Itoa(int(duration/time.Second)) + "s"
}

func (s *Server) buildUIStatus(now time.Time) uiStatusResponse {
	workers := s.workers.Snapshot(now)
	active := s.workers.ActiveSnapshot()
	events := s.recentAgentEvents()
	cooldowns := ReplicaCooldownSnapshot{}
	if s.replicaCooldowns != nil {
		cooldowns = s.replicaCooldowns.Snapshot(now)
	}
	resp := uiStatusResponse{
		GeneratedAt: now.UTC(),
		Models:      s.buildUIModels(workers, cooldowns, now),
		Workers:     s.buildUIWorkers(workers, active, cooldowns, now),
		Events:      events,
	}
	if resp.Models == nil {
		resp.Models = []uiModelStatus{}
	}
	if resp.Workers == nil {
		resp.Workers = []uiWorker{}
	}
	if resp.Events == nil {
		resp.Events = []uiAgentEvent{}
	}
	resp.Summary = buildUISummary(resp.Models, resp.Workers, events)
	return resp
}

func (s *Server) buildUIModels(workers []Worker, cooldowns ReplicaCooldownSnapshot, now time.Time) []uiModelStatus {
	cfg := activeGatewayConfig(s.currentConfig())
	models := make([]uiModelStatus, 0, len(cfg.Models))
	for name, model := range cfg.Models {
		item := uiModelStatus{
			Name:           name,
			Priority:       model.Priority,
			MinLoaded:      model.MinLoaded,
			MaxLoaded:      model.EffectiveMaxLoaded(),
			MaxConcurrency: model.MaxConcurrency,
			MaxQueue:       model.MaxQueue,
			QueueTimeoutMS: model.QueueTimeoutMS,
			TTL:            model.TTL,
			Artifact:       model.Artifact,
			WorkerStatuses: []uiModelWorker{},
			Traffic:        modelTrafficFromAccess(s.access.ModelRecord(name)),
		}
		for _, worker := range workers {
			if !workerAllowsModel(cfg, worker, name) {
				continue
			}
			artifactStatus := worker.Artifacts[name]
			if artifactStatus == "" {
				artifactStatus = "missing"
			}
			runningState := runningStateForModel(worker, name)
			health := workerHealth(worker, now)
			status := uiModelWorker{
				WorkerID:       worker.ID,
				ArtifactStatus: artifactStatus,
				RunningState:   runningState,
				Health:         health,
			}
			if cooldown, ok := cooldowns.Get(worker.ID, name, now); ok {
				status.CooldownActive = true
				status.CooldownReason = cooldown.Reason
				status.CooldownRemainingSeconds = cooldown.RemainingSeconds
				status.CooldownUntil = cooldown.CooldownUntil.UTC()
			}
			item.WorkerStatuses = append(item.WorkerStatuses, status)
			switch artifactStatus {
			case "ready":
				item.ReadyWorkers++
			case "installing", "pending":
				item.Installing++
			case "error":
				item.ErrorWorkers++
			default:
				item.Missing++
			}
			if runningState != "" {
				item.RunningWorkers++
			}
		}
		item.Available = modelAvailable(item)
		item.AvailabilityNote = modelAvailabilityNote(item)
		models = append(models, item)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Available != models[j].Available {
			return models[i].Available
		}
		if models[i].Priority != models[j].Priority {
			return models[i].Priority > models[j].Priority
		}
		return models[i].Name < models[j].Name
	})
	return models
}

func (s *Server) buildUIWorkers(workers []Worker, active map[string]int, cooldowns ReplicaCooldownSnapshot, now time.Time) []uiWorker {
	out := make([]uiWorker, 0, len(workers))
	cfg := activeGatewayConfig(s.currentConfig())
	for _, worker := range workers {
		backoffSeconds := int64(0)
		if now.Before(worker.ScrapeBackoffUntil) {
			backoffSeconds = int64(time.Until(worker.ScrapeBackoffUntil).Seconds())
		}
		out = append(out, uiWorker{
			ID:                   worker.ID,
			Tags:                 stringsOrEmpty(worker.Tags),
			Health:               workerHealth(worker, now),
			State:                string(worker.State),
			LlamaSwapURL:         worker.LlamaSwapURL,
			LastHeartbeat:        worker.LastHeartbeat.UTC(),
			LastHeartbeatAgeMS:   now.Sub(worker.LastHeartbeat).Milliseconds(),
			ActiveRequests:       active[worker.ID],
			RunningModels:        runningModelsOrEmpty(worker.RunningModels),
			GPUDevices:           gpuDevicesOrEmpty(worker.GPUDevices),
			Artifacts:            copyStringMap(worker.Artifacts),
			Capacity:             worker.Capacity,
			NeedsRestart:         worker.NeedsRestart,
			LastError:            worker.LastError,
			ScrapeFailures:       worker.ScrapeFailures,
			ScrapeBackoffUntil:   worker.ScrapeBackoffUntil.UTC(),
			ScrapeBackoffSeconds: backoffSeconds,
			AllowedModels:        stringsOrEmpty(allowedModelsForWorker(cfg, worker)),
			HealthProblem:        workerHealthProblem(worker, now),
			ReplicaCooldowns:     cooldownsForWorker(cooldowns, worker.ID, now),
			AgentBuild:           worker.AgentBuild,
			AgentVersionStatus:   agentVersionStatus(worker.AgentBuild),
		})
	}
	return out
}

func agentVersionStatus(build protocol.BuildInfo) string {
	if build.ProtocolVersion == 0 && build.Commit == "" && build.Version == "" {
		return "legacy"
	}
	if build.ProtocolVersion < protocol.AgentProtocolVersion {
		return "outdated"
	}
	if build.Version != buildinfo.AgentVersion {
		return "outdated"
	}
	return "current"
}

func cooldownsForWorker(cooldowns ReplicaCooldownSnapshot, workerID string, now time.Time) []ReplicaCooldown {
	byModel := cooldowns[workerID]
	if len(byModel) == 0 {
		return []ReplicaCooldown{}
	}
	out := make([]ReplicaCooldown, 0, len(byModel))
	for model := range byModel {
		cooldown, ok := cooldowns.Get(workerID, model, now)
		if !ok {
			continue
		}
		cooldown.CooldownUntil = cooldown.CooldownUntil.UTC()
		out = append(out, cooldown)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

func stringsOrEmpty(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func runningModelsOrEmpty(values []protocol.RunningModel) []protocol.RunningModel {
	if len(values) == 0 {
		return []protocol.RunningModel{}
	}
	return append([]protocol.RunningModel(nil), values...)
}

func gpuDevicesOrEmpty(values []protocol.GPUDevice) []protocol.GPUDevice {
	if len(values) == 0 {
		return []protocol.GPUDevice{}
	}
	return append([]protocol.GPUDevice(nil), values...)
}

func buildUISummary(models []uiModelStatus, workers []uiWorker, events []uiAgentEvent) uiSummary {
	summary := uiSummary{TotalWorkers: len(workers), ConfiguredModels: len(models)}
	for _, worker := range workers {
		summary.ActiveRequests += worker.ActiveRequests
		switch worker.Health {
		case "healthy":
			summary.HealthyWorkers++
		case "draining":
			summary.DrainingWorkers++
		case "stale":
			summary.StaleWorkers++
		}
		if worker.LastError != "" {
			summary.WorkersWithErrors++
		}
	}
	for _, model := range models {
		if model.Available {
			summary.AvailableModels++
		}
		if model.MinLoaded > 0 && model.RunningWorkers < model.MinLoaded {
			summary.Underprovisioned++
		}
	}
	for _, event := range events {
		if event.Error != "" || strings.HasSuffix(event.Event, "_error") {
			summary.RecentErrorEvents++
		}
	}
	return summary
}

func (s *Server) recordAgentEvent(workerID string, event protocol.AgentEvent, receivedAt time.Time) uiAgentEvent {
	if event.Event == "" {
		return uiAgentEvent{}
	}
	cached := uiAgentEvent{
		ReceivedAt:      receivedAt.UTC(),
		WorkerID:        workerID,
		Time:            event.Time.UTC(),
		Event:           event.Event,
		Model:           event.Model,
		FromState:       event.FromState,
		ToState:         event.ToState,
		Object:          event.Object,
		Kind:            event.Kind,
		DownloadedBytes: event.DownloadedBytes,
		TotalBytes:      event.TotalBytes,
		Percent:         event.Percent,
		DurationMS:      event.DurationMS,
		Error:           event.Error,
	}
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	s.recentEvents = append(s.recentEvents, cached)
	if len(s.recentEvents) > uiEventLimit {
		s.recentEvents = append([]uiAgentEvent(nil), s.recentEvents[len(s.recentEvents)-uiEventLimit:]...)
	}
	return cached
}

func (s *Server) recentAgentEvents() []uiAgentEvent {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	if len(s.recentEvents) == 0 {
		return []uiAgentEvent{}
	}
	out := append([]uiAgentEvent(nil), s.recentEvents...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func workerHealth(worker Worker, now time.Time) string {
	if now.Sub(worker.LastHeartbeat) >= 6*time.Second {
		return "stale"
	}
	if worker.State == WorkerDraining {
		return "draining"
	}
	if now.Before(worker.ScrapeBackoffUntil) {
		return "backoff"
	}
	if worker.LastError != "" {
		return "error"
	}
	return "healthy"
}

func workerHealthProblem(worker Worker, now time.Time) string {
	switch workerHealth(worker, now) {
	case "stale":
		return "heartbeat is stale"
	case "draining":
		return "waiting for safe restart"
	case "backoff":
		return "gateway temporarily marked worker unavailable after reverse access failures"
	case "error":
		return worker.LastError
	default:
		return ""
	}
}

func healthRank(health string) int {
	switch health {
	case "error":
		return 0
	case "stale":
		return 1
	case "backoff":
		return 2
	case "draining":
		return 3
	case "healthy":
		return 4
	default:
		return 5
	}
}

func modelAvailable(model uiModelStatus) bool {
	return model.ReadyWorkers > 0
}

func modelAvailabilityNote(model uiModelStatus) string {
	if model.Available {
		return "ready"
	}
	if model.ErrorWorkers > 0 {
		return "artifact error"
	}
	if model.Installing > 0 {
		return "installing"
	}
	return "not ready"
}

func allowedModelsForWorker(cfg config.GatewayConfig, worker Worker) []string {
	seen := map[string]struct{}{}
	for _, tag := range worker.Tags {
		policy, ok := cfg.TagPolicies[tag]
		if !ok {
			continue
		}
		for _, model := range policy.AllowedModels {
			seen[model] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for model := range seen {
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func runningStateForModel(worker Worker, modelName string) string {
	for _, running := range worker.RunningModels {
		if running.Model == modelName {
			return running.State
		}
	}
	return ""
}

func modelTrafficFromAccess(record AccessRecord) uiModelTraffic {
	traffic := uiModelTraffic{
		LastAccess:       record.LastAccess.UTC(),
		Requests:         record.Count,
		PromptTokens:     record.PromptTokens,
		CompletionTokens: record.CompletionTokens,
		TotalTokens:      record.TotalTokens,
		CacheTokens:      record.CacheTokens,
		ReasoningTokens:  record.ReasoningTokens,
		MaxDurationMS:    record.MaxDurationMS,
	}
	if record.Count > 0 {
		traffic.AvgDurationMS = record.DurationMS / record.Count
	}
	for status, count := range record.StatusCounts {
		switch {
		case strings.HasPrefix(status, "2"):
			traffic.Status2xx += count
		case strings.HasPrefix(status, "4"):
			traffic.Status4xx += count
		case strings.HasPrefix(status, "5"):
			traffic.Status5xx += count
		}
	}
	return traffic
}

const gatewayUIHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>LLM Swap Gateway</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f5f7fa;
      --panel: #ffffff;
      --line: #d9e0e7;
      --text: #17202a;
      --muted: #657080;
      --good: #0f7b4f;
      --warn: #a86600;
      --bad: #b42318;
      --info: #1d5d9b;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: var(--bg);
      color: var(--text);
      letter-spacing: 0;
    }
    header {
      position: sticky;
      top: 0;
      z-index: 1;
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 16px;
      padding: 14px 24px;
      background: rgba(255,255,255,.94);
      border-bottom: 1px solid var(--line);
      backdrop-filter: blur(8px);
    }
    h1 { margin: 0; font-size: 18px; font-weight: 700; }
    main { width: min(1840px, calc(100vw - 48px)); margin: 0 auto; padding: 18px 0 28px; }
    .toolbar { display: flex; align-items: center; gap: 10px; color: var(--muted); font-size: 13px; }
    .summary { display: grid; grid-template-columns: repeat(auto-fit, minmax(210px, 1fr)); gap: 10px; margin-bottom: 16px; }
    .metric, section { background: var(--panel); border: 1px solid var(--line); border-radius: 8px; }
    .metric { padding: 12px; min-height: 72px; }
    .metric .value { font-size: 24px; font-weight: 760; line-height: 1.1; }
    .metric .label { margin-top: 6px; color: var(--muted); font-size: 12px; }
    section { margin-bottom: 16px; overflow: hidden; }
    section h2 { margin: 0; padding: 12px 14px; border-bottom: 1px solid var(--line); font-size: 14px; }
    table { width: 100%; border-collapse: collapse; font-size: 13px; }
    th, td { padding: 10px 12px; border-bottom: 1px solid #edf1f5; text-align: left; vertical-align: top; }
    th { color: var(--muted); font-size: 12px; font-weight: 650; background: #fafbfd; }
    tr:last-child td { border-bottom: 0; }
    .dashboard-stack { display: grid; grid-template-columns: 1fr; gap: 16px; }
    .table-wrap { overflow-x: auto; }
    .model-table { min-width: 1220px; table-layout: fixed; }
    .model-table th:nth-child(1), .model-table td:nth-child(1) { width: 16%; }
    .model-table th:nth-child(2), .model-table td:nth-child(2) { width: 12%; }
    .model-table th:nth-child(3), .model-table td:nth-child(3) { width: 20%; }
    .model-table th:nth-child(4), .model-table td:nth-child(4) { width: 15%; }
    .model-table th:nth-child(5), .model-table td:nth-child(5) { width: 17%; }
    .model-table th:nth-child(6), .model-table td:nth-child(6) { width: 20%; }
    .worker-card-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(420px, 1fr)); gap: 12px; padding: 12px; }
    .worker-card { border: 1px solid #edf1f5; border-radius: 8px; padding: 12px; min-width: 0; }
    .worker-card-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 12px; margin-bottom: 8px; }
    .worker-title { min-width: 0; }
    .worker-title strong { display: block; margin-bottom: 4px; }
    .worker-url { display: block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .worker-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; margin: 10px 0; }
    .stat { border: 1px solid #edf1f5; border-radius: 6px; padding: 8px; min-width: 0; }
    .stat-label { color: var(--muted); font-size: 11px; margin-bottom: 3px; }
    .stat-value { font-weight: 700; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .worker-detail-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 10px; margin-top: 8px; }
    .detail-block { min-width: 0; }
    .detail-title { color: var(--muted); font-size: 11px; margin-bottom: 5px; }
    .problem { margin-top: 8px; padding: 8px; border-radius: 6px; background: #fff0ef; color: var(--bad); overflow-wrap: anywhere; }
    .section-head { display: flex; justify-content: space-between; align-items: center; gap: 12px; border-bottom: 1px solid var(--line); }
    .section-head h2 { border-bottom: 0; }
    .range-buttons { display: flex; gap: 6px; padding-right: 12px; }
    .range-buttons button { padding: 5px 8px; font-size: 12px; }
    .range-buttons button.active { color: var(--info); border-color: #8cb7dc; background: #edf6ff; }
    .chart-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 12px; padding: 12px; }
    .chart-card { border: 1px solid #edf1f5; border-radius: 8px; padding: 10px; min-width: 0; }
    .chart-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 10px; margin-bottom: 8px; }
    .chart-name { color: var(--muted); font-size: 11px; margin-bottom: 3px; }
    .chart-value { font-size: 18px; font-weight: 760; }
    .chart-meta { color: var(--muted); font-size: 11px; white-space: nowrap; }
    .chart-svg { display: block; width: 100%; height: 130px; background: #fbfdff; border: 1px solid #edf1f5; border-radius: 6px; }
    .chart-gridline { stroke: #e7edf3; stroke-width: 1; }
    .chart-line { fill: none; stroke: var(--info); stroke-width: 2.2; vector-effect: non-scaling-stroke; }
    .chart-point { fill: var(--info); }
    .traffic-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 4px 10px; }
    .traffic-grid span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .pill { display: inline-flex; align-items: center; min-height: 22px; padding: 2px 8px; border-radius: 999px; font-size: 12px; font-weight: 650; border: 1px solid transparent; white-space: nowrap; }
    .healthy, .available, .ready { color: var(--good); background: #eaf7f1; border-color: #bfe4d2; }
    .installing, .pending, .draining, .backoff { color: var(--warn); background: #fff4df; border-color: #f0d29a; }
    .error, .stale, .unavailable { color: var(--bad); background: #fff0ef; border-color: #f2c0bc; }
    .muted { color: var(--muted); }
    .mono { font-family: ui-monospace, SFMono-Regular, Consolas, "Liberation Mono", monospace; font-size: 12px; }
    .stack { display: flex; flex-direction: column; gap: 4px; }
    .workers { display: flex; flex-wrap: wrap; gap: 4px; }
    .breakable { overflow-wrap: anywhere; word-break: break-word; }
    .bar { width: 150px; height: 8px; border-radius: 999px; background: #e5ebf1; overflow: hidden; }
    .bar span { display: block; height: 100%; background: var(--info); }
    .event-actions { display: flex; justify-content: center; padding: 12px; border-top: 1px solid #edf1f5; }
    button { appearance: none; border: 1px solid var(--line); border-radius: 6px; background: #fff; color: var(--text); padding: 8px 12px; font: inherit; cursor: pointer; }
    button:hover { border-color: #b6c2d0; background: #fafbfd; }
    button:disabled { color: var(--muted); cursor: default; }
    .empty, .errorbox { padding: 20px; color: var(--muted); }
    .errorbox { color: var(--bad); }
    @media (max-width: 980px) {
      header { align-items: flex-start; flex-direction: column; }
      main { width: calc(100vw - 28px); padding: 14px 0; }
      .summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .section-head { align-items: flex-start; flex-direction: column; gap: 0; }
      .range-buttons { padding: 0 12px 12px; flex-wrap: wrap; }
      .chart-grid { grid-template-columns: 1fr; }
      .worker-card-grid { grid-template-columns: 1fr; }
      .worker-stats { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .worker-detail-grid { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <header>
    <h1>LLM Swap Gateway</h1>
    <div class="toolbar">
      <span id="updated">Loading...</span>
      <span class="muted">Auto-refresh 5s</span>
    </div>
  </header>
  <main>
    <div id="summary" class="summary"></div>
    <section>
      <div class="section-head">
        <h2>History</h2>
        <div id="metricsRangeButtons" class="range-buttons"><button type="button" data-range="15m">15m</button><button type="button" data-range="1h" class="active">1h</button><button type="button" data-range="6h">6h</button><button type="button" data-range="24h">24h</button></div>
      </div>
      <div id="history"></div>
    </section>
    <div class="dashboard-stack">
      <section>
        <h2>Models</h2>
        <div id="models"></div>
      </section>
      <section>
        <h2>Workers</h2>
        <div id="workers"></div>
      </section>
    </div>
    <section>
      <h2>Recent worker events</h2>
      <div id="events"></div>
      <div class="event-actions"><button id="loadMoreEvents" type="button">Load more</button></div>
    </section>
  </main>
  <script>
    const statusURL = "/ui/status";
    const eventsURL = "/ui/events";
    const metricsRanges = [
      { label: "15m", range: "15m", step: "15s" },
      { label: "1h", range: "1h", step: "1m" },
      { label: "6h", range: "6h", step: "5m" },
      { label: "24h", range: "24h", step: "15m" },
    ];
    let metricsRange = metricsRanges[1];
    const eventLimit = 50;
    let eventOffset = 0;
    let eventItems = [];
    let eventExpanded = false;
    const esc = (value) => String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
    const pill = (text, cls) => '<span class="pill ' + esc(cls || text || "muted") + '">' + esc(text || "-") + '</span>';
    const age = (ms) => {
      if (!Number.isFinite(ms)) return "-";
      if (ms < 1000) return ms + "ms";
      const sec = Math.round(ms / 1000);
      if (sec < 60) return sec + "s";
      return Math.round(sec / 60) + "m";
    };
    const bytes = (value) => {
      if (!value) return "";
      const units = ["B", "KiB", "MiB", "GiB"];
      let n = value, i = 0;
      while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
      return n.toFixed(i ? 1 : 0) + units[i];
    };
    const compact = (value) => {
      const n = Number(value || 0);
      if (n >= 1000000) return (n / 1000000).toFixed(1) + "M";
      if (n >= 1000) return (n / 1000).toFixed(1) + "K";
      return String(n);
    };
    const eventTime = (value) => {
      if (!value || value.startsWith("0001-")) return "-";
      return new Date(value).toLocaleTimeString();
    };
    function renderSummary(summary) {
      const items = [
        ["Healthy workers", summary.healthy_workers + "/" + summary.total_workers],
        ["Available models", summary.available_models + "/" + summary.configured_models],
        ["Active requests", summary.active_requests],
        ["Draining", summary.draining_workers],
        ["Stale", summary.stale_workers],
        ["Recent errors", summary.recent_error_events + summary.workers_with_errors],
      ];
      document.getElementById("summary").innerHTML = items.map(([label, value]) => '<div class="metric"><div class="value">' + esc(value) + '</div><div class="label">' + esc(label) + '</div></div>').join("");
    }
    function renderHistory(data) {
      const series = data.series || [];
      if (!series.length) { document.getElementById("history").innerHTML = '<div class="empty">No historical metrics yet.</div>'; return; }
      document.getElementById("history").innerHTML = '<div class="chart-grid">' + series.map((s) => {
        const points = s.points || [];
        const last = points.length ? points[points.length - 1] : null;
        const label = historyLabel(s);
        const value = last ? compactNumber(last.value) : "-";
        const when = last ? new Date(last.ts * 1000).toLocaleTimeString() : "-";
        return '<div class="chart-card"><div class="chart-head"><div><div class="chart-name">' + esc(label) + '</div><div class="chart-value">' + esc(value) + '</div></div><div class="chart-meta mono">' + esc(data.range || "") + ' / ' + esc(data.step || "") + '<br>last ' + esc(when) + '</div></div>' + renderChart(points) + '</div>';
      }).join("") + '</div>';
    }
    function renderChart(points) {
      const clean = (points || []).map((p) => ({ ts: Number(p.ts), value: Number(p.value) })).filter((p) => Number.isFinite(p.ts) && Number.isFinite(p.value));
      if (!clean.length) return '<div class="empty">No points.</div>';
      const width = 320, height = 120, pad = 10;
      const minX = clean[0].ts, maxX = clean[clean.length - 1].ts || minX + 1;
      let minY = Math.min(...clean.map((p) => p.value));
      let maxY = Math.max(...clean.map((p) => p.value));
      if (minY === maxY) {
        minY = minY - 1;
        maxY = maxY + 1;
      }
      const x = (ts) => pad + ((ts - minX) / Math.max(1, maxX - minX)) * (width - pad * 2);
      const y = (value) => height - pad - ((value - minY) / Math.max(1, maxY - minY)) * (height - pad * 2);
      const line = clean.map((p) => x(p.ts).toFixed(1) + "," + y(p.value).toFixed(1)).join(" ");
      const last = clean[clean.length - 1];
      return '<svg class="chart-svg" viewBox="0 0 320 120" preserveAspectRatio="none" role="img"><line class="chart-gridline" x1="10" y1="20" x2="310" y2="20"></line><line class="chart-gridline" x1="10" y1="60" x2="310" y2="60"></line><line class="chart-gridline" x1="10" y1="100" x2="310" y2="100"></line><polyline class="chart-line" points="' + esc(line) + '"></polyline><circle class="chart-point" cx="' + x(last.ts).toFixed(1) + '" cy="' + y(last.value).toFixed(1) + '" r="3"></circle></svg>';
    }
    function historyLabel(series) {
      const labels = series.labels || {};
      const parts = [series.name || "metric"];
      for (const key of ["model", "worker_id", "type"]) {
        if (labels[key]) parts.push(labels[key]);
      }
      return parts.join(" ");
    }
    function compactNumber(value) {
      const n = Number(value || 0);
      if (!Number.isFinite(n)) return "-";
      if (Math.abs(n) >= 1000) return compact(n);
      if (Math.abs(n) >= 10) return n.toFixed(1).replace(/\.0$/, "");
      return n.toFixed(2).replace(/0+$/, "").replace(/\.$/, "");
    }
    function renderModels(models) {
      models = models || [];
      if (!models.length) { document.getElementById("models").innerHTML = '<div class="empty">No configured models.</div>'; return; }
      document.getElementById("models").innerHTML = '<div class="table-wrap"><table class="model-table"><thead><tr><th>Model</th><th>Availability</th><th>Workers</th><th>Policy</th><th>Traffic</th><th>Artifact</th></tr></thead><tbody>' + models.map((m) => {
        const workers = (m.worker_statuses || []).map((w) => {
          const cooldown = w.cooldown_active ? " cooldown " + w.cooldown_remaining_seconds + "s " + w.cooldown_reason : "";
          return '<span class="pill ' + esc(w.cooldown_active ? "error" : w.artifact_status) + '">' + esc(w.worker_id + " " + w.artifact_status + (w.running_state ? "/" + w.running_state : "") + cooldown) + '</span>';
        }).join("");
        const t = m.traffic || {};
        const traffic = '<div class="traffic-grid mono"><span>req=' + esc(compact(t.requests)) + '</span><span>tok=' + esc(compact(t.total_tokens)) + '</span><span>avg=' + esc(t.avg_duration_ms || 0) + 'ms</span><span>max=' + esc(t.max_duration_ms || 0) + 'ms</span><span>2xx=' + esc(t.status_2xx || 0) + '</span><span>4xx=' + esc(t.status_4xx || 0) + ' 5xx=' + esc(t.status_5xx || 0) + '</span><span>cache=' + esc(compact(t.cache_tokens)) + '</span><span>last=' + esc(eventTime(t.last_access)) + '</span></div>';
        return '<tr><td><div class="stack breakable"><strong>' + esc(m.name) + '</strong><span class="muted mono">priority ' + esc(m.priority) + '</span></div></td><td>' + pill(m.availability_note, m.available ? "available" : "unavailable") + '<div class="muted">ready ' + esc(m.ready_workers) + ', running ' + esc(m.running_workers) + '</div></td><td><div class="workers">' + workers + '</div></td><td class="mono">min_loaded=' + esc(m.min_loaded) + '<br>max_loaded=' + esc(m.max_loaded || "-") + '<br>concurrency=' + esc(m.max_concurrency || "-") + '<br>queue=' + esc(m.max_queue || "-") + '</td><td>' + traffic + '</td><td><div class="stack"><span>' + esc(m.artifact.kind) + '</span><span class="muted mono breakable">' + esc(m.artifact.object) + '</span></div></td></tr>';
      }).join("") + '</tbody></table></div>';
    }
    function renderWorkers(workers) {
      workers = workers || [];
      if (!workers.length) { document.getElementById("workers").innerHTML = '<div class="empty">No workers have reported yet.</div>'; return; }
      document.getElementById("workers").innerHTML = '<div class="worker-card-grid">' + workers.map((w) => {
        const tags = (w.tags || []).map((tag) => '<span class="pill muted">' + esc(tag) + '</span>').join("");
        const running = (w.running_models || []).map((m) => '<span class="pill ready">' + esc(m.model + ":" + m.state) + '</span>').join("") || '<span class="muted">none</span>';
        const artifacts = Object.entries(w.artifacts || {}).map(([model, status]) => '<span class="pill ' + esc(status) + '">' + esc(model + " " + status) + '</span>').join("") || '<span class="muted">none</span>';
        const cooldowns = (w.replica_cooldowns || []).map((c) => '<span class="pill error">' + esc(c.model + " " + c.remaining_seconds + "s " + c.reason) + '</span>').join("") || '<span class="muted">none</span>';
        return '<article class="worker-card"><div class="worker-card-head"><div class="worker-title"><strong>' + esc(w.id) + '</strong><span class="muted mono worker-url" title="' + esc(w.llama_swap_url) + '">' + esc(w.llama_swap_url) + '</span><div class="workers">' + tags + '</div></div>' + pill(w.health, w.health) + '</div><div class="worker-stats"><div class="stat"><div class="stat-label">Heartbeat</div><div class="stat-value">' + esc(age(w.last_heartbeat_age_ms)) + '</div></div><div class="stat"><div class="stat-label">Active</div><div class="stat-value">' + esc(w.active_requests) + '</div></div><div class="stat"><div class="stat-label">Max concurrency</div><div class="stat-value">' + esc(w.capacity.max_concurrency || "-") + '</div></div><div class="stat"><div class="stat-label">Scrape failures</div><div class="stat-value">' + esc(w.scrape_failures) + '</div></div></div>' + (w.health_problem ? '<div class="problem">' + esc(w.health_problem) + '</div>' : '') + '<div class="worker-detail-grid"><div class="detail-block"><div class="detail-title">Running models</div><div class="workers">' + running + '</div></div><div class="detail-block"><div class="detail-title">Artifacts</div><div class="workers">' + artifacts + '</div></div><div class="detail-block"><div class="detail-title">Cooldowns</div><div class="workers">' + cooldowns + '</div></div></div></article>';
      }).join("") + '</div>';
    }
    function renderEvents(events, hasMore) {
      events = events || [];
      const button = document.getElementById("loadMoreEvents");
      button.style.display = hasMore ? "" : "none";
      button.disabled = false;
      if (!events.length) { document.getElementById("events").innerHTML = '<div class="empty">No worker events yet.</div>'; return; }
      document.getElementById("events").innerHTML = '<table><thead><tr><th>Received</th><th>Worker</th><th>Event</th><th>Model</th><th>Detail</th></tr></thead><tbody>' + events.map((e) => {
        const detail = eventDetail(e);
        return '<tr><td class="mono">' + esc(new Date(e.received_at).toLocaleTimeString()) + '</td><td>' + esc(e.worker_id) + '</td><td>' + pill(e.event, e.error ? "error" : eventClass(e.event)) + '</td><td>' + esc(e.model || "-") + '</td><td class="mono">' + esc(detail) + '</td></tr>';
      }).join("") + '</tbody></table>';
    }
    function eventDetail(e) {
      if (e.error) return e.error;
      if (e.total_bytes) return progressDetail(e);
      if (e.from_state || e.to_state) return (e.from_state || "-") + " -> " + (e.to_state || "-");
      if (e.duration_ms) return "duration " + Math.round(e.duration_ms / 1000) + "s";
      return e.object || "";
    }
    function progressDetail(e) {
      return (e.percent || 0).toFixed(1) + "% " + bytes(e.downloaded_bytes) + "/" + bytes(e.total_bytes);
    }
    function eventClass(event) {
      if (event.endsWith("_error")) return "error";
      if (event.includes("progress") || event.includes("start")) return "installing";
      return "ready";
    }
    async function loadEvents(reset) {
      if (reset) eventOffset = 0;
      const button = document.getElementById("loadMoreEvents");
      button.disabled = true;
      try {
        const res = await fetch(eventsURL + "?limit=" + eventLimit + "&offset=" + eventOffset, { cache: "no-store" });
        if (!res.ok) throw new Error("status " + res.status);
        const data = await res.json();
        const events = data.events || [];
        eventItems = reset ? events : eventItems.concat(events);
        eventOffset = data.next_offset ?? eventItems.length;
        renderEvents(eventItems, !!data.has_more);
      } catch (err) {
        document.getElementById("events").innerHTML = '<div class="errorbox">Failed to load /ui/events: ' + esc(err.message) + '</div>';
      } finally {
        button.disabled = false;
      }
    }
    async function loadHistory() {
      try {
        const metricsSummaryURL = "/ui/metrics/summary?range=" + encodeURIComponent(metricsRange.range) + "&step=" + encodeURIComponent(metricsRange.step);
        const res = await fetch(metricsSummaryURL, { cache: "no-store" });
        if (res.status === 503) {
          document.getElementById("history").innerHTML = '<div class="empty">Metrics store disabled.</div>';
          return;
        }
        if (!res.ok) throw new Error("status " + res.status);
        renderHistory(await res.json());
      } catch (err) {
        document.getElementById("history").innerHTML = '<div class="errorbox">Failed to load /ui/metrics/summary: ' + esc(err.message) + '</div>';
      }
    }
    function renderMetricsRangeButtons() {
      document.getElementById("metricsRangeButtons").innerHTML = metricsRanges.map((item) => '<button type="button" data-range="' + esc(item.range) + '" class="' + (item.range === metricsRange.range ? "active" : "") + '">' + esc(item.label) + '</button>').join("");
      document.querySelectorAll("#metricsRangeButtons button").forEach((button) => {
        button.addEventListener("click", () => {
          const selected = metricsRanges.find((item) => item.range === button.dataset.range);
          if (!selected) return;
          metricsRange = selected;
          renderMetricsRangeButtons();
          loadHistory();
        });
      });
    }
    async function load() {
      try {
        const res = await fetch(statusURL, { cache: "no-store" });
        if (!res.ok) throw new Error("status " + res.status);
        const data = await res.json();
        renderSummary(data.summary);
        renderModels(data.models);
        renderWorkers(data.workers);
        document.getElementById("updated").textContent = "Updated " + new Date(data.generated_at).toLocaleTimeString();
      } catch (err) {
        document.getElementById("summary").innerHTML = '<div class="errorbox">Failed to load /ui/status: ' + esc(err.message) + '</div>';
      }
    }
    document.getElementById("loadMoreEvents").addEventListener("click", () => {
      eventExpanded = true;
      loadEvents(false);
    });
    renderMetricsRangeButtons();
    load();
    loadHistory();
    loadEvents(true);
    setInterval(load, 5000);
    setInterval(loadHistory, 30000);
    setInterval(() => { if (!eventExpanded) loadEvents(true); }, 10000);
  </script>
</body>
</html>`
