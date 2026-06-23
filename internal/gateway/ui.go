package gateway

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
	WorkerID       string `json:"worker_id"`
	ArtifactStatus string `json:"artifact_status"`
	RunningState   string `json:"running_state,omitempty"`
	Health         string `json:"health"`
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
	Artifacts            map[string]string       `json:"artifacts"`
	Capacity             config.WorkerDefaults   `json:"capacity"`
	NeedsRestart         bool                    `json:"needs_restart"`
	LastError            string                  `json:"last_error,omitempty"`
	ScrapeFailures       int                     `json:"scrape_failures"`
	ScrapeBackoffUntil   time.Time               `json:"scrape_backoff_until,omitempty"`
	ScrapeBackoffSeconds int64                   `json:"scrape_backoff_seconds,omitempty"`
	AllowedModels        []string                `json:"allowed_models"`
	HealthProblem        string                  `json:"health_problem,omitempty"`
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

type uiEventsResponse struct {
	Events     []uiAgentEvent `json:"events"`
	NextOffset int            `json:"next_offset"`
	HasMore    bool           `json:"has_more"`
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(gatewayUIHTML))
}

func (s *Server) handleUIStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildUIStatus(time.Now()))
}

func (s *Server) handleUIEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
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

func (s *Server) buildUIStatus(now time.Time) uiStatusResponse {
	workers := s.workers.Snapshot(now)
	active := s.workers.ActiveSnapshot()
	events := s.recentAgentEvents()
	resp := uiStatusResponse{
		GeneratedAt: now.UTC(),
		Models:      s.buildUIModels(workers, now),
		Workers:     s.buildUIWorkers(workers, active, now),
		Events:      events,
	}
	resp.Summary = buildUISummary(resp.Models, resp.Workers, events)
	return resp
}

func (s *Server) buildUIModels(workers []Worker, now time.Time) []uiModelStatus {
	models := make([]uiModelStatus, 0, len(s.config.Models))
	for name, model := range s.config.Models {
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
			if !workerAllowsModel(s.config, worker, name) {
				continue
			}
			artifactStatus := worker.Artifacts[name]
			if artifactStatus == "" {
				artifactStatus = "missing"
			}
			runningState := runningStateForModel(worker, name)
			health := workerHealth(worker, now)
			item.WorkerStatuses = append(item.WorkerStatuses, uiModelWorker{
				WorkerID:       worker.ID,
				ArtifactStatus: artifactStatus,
				RunningState:   runningState,
				Health:         health,
			})
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

func (s *Server) buildUIWorkers(workers []Worker, active map[string]int, now time.Time) []uiWorker {
	out := make([]uiWorker, 0, len(workers))
	for _, worker := range workers {
		backoffSeconds := int64(0)
		if now.Before(worker.ScrapeBackoffUntil) {
			backoffSeconds = int64(time.Until(worker.ScrapeBackoffUntil).Seconds())
		}
		out = append(out, uiWorker{
			ID:                   worker.ID,
			Tags:                 append([]string(nil), worker.Tags...),
			Health:               workerHealth(worker, now),
			State:                string(worker.State),
			LlamaSwapURL:         worker.LlamaSwapURL,
			LastHeartbeat:        worker.LastHeartbeat.UTC(),
			LastHeartbeatAgeMS:   now.Sub(worker.LastHeartbeat).Milliseconds(),
			ActiveRequests:       active[worker.ID],
			RunningModels:        append([]protocol.RunningModel(nil), worker.RunningModels...),
			Artifacts:            copyStringMap(worker.Artifacts),
			Capacity:             worker.Capacity,
			NeedsRestart:         worker.NeedsRestart,
			LastError:            worker.LastError,
			ScrapeFailures:       worker.ScrapeFailures,
			ScrapeBackoffUntil:   worker.ScrapeBackoffUntil.UTC(),
			ScrapeBackoffSeconds: backoffSeconds,
			AllowedModels:        allowedModelsForWorker(s.config, worker),
			HealthProblem:        workerHealthProblem(worker, now),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Health != out[j].Health {
			return healthRank(out[i].Health) < healthRank(out[j].Health)
		}
		return out[i].ID < out[j].ID
	})
	return out
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
		return "worker metrics scrape is in backoff"
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
    function renderModels(models) {
      models = models || [];
      if (!models.length) { document.getElementById("models").innerHTML = '<div class="empty">No configured models.</div>'; return; }
      document.getElementById("models").innerHTML = '<div class="table-wrap"><table class="model-table"><thead><tr><th>Model</th><th>Availability</th><th>Workers</th><th>Policy</th><th>Traffic</th><th>Artifact</th></tr></thead><tbody>' + models.map((m) => {
        const workers = (m.worker_statuses || []).map((w) => '<span class="pill ' + esc(w.artifact_status) + '">' + esc(w.worker_id + " " + w.artifact_status + (w.running_state ? "/" + w.running_state : "")) + '</span>').join("");
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
        return '<article class="worker-card"><div class="worker-card-head"><div class="worker-title"><strong>' + esc(w.id) + '</strong><span class="muted mono worker-url" title="' + esc(w.llama_swap_url) + '">' + esc(w.llama_swap_url) + '</span><div class="workers">' + tags + '</div></div>' + pill(w.health, w.health) + '</div><div class="worker-stats"><div class="stat"><div class="stat-label">Heartbeat</div><div class="stat-value">' + esc(age(w.last_heartbeat_age_ms)) + '</div></div><div class="stat"><div class="stat-label">Active</div><div class="stat-value">' + esc(w.active_requests) + '</div></div><div class="stat"><div class="stat-label">Max concurrency</div><div class="stat-value">' + esc(w.capacity.max_concurrency || "-") + '</div></div><div class="stat"><div class="stat-label">Scrape failures</div><div class="stat-value">' + esc(w.scrape_failures) + '</div></div></div>' + (w.health_problem ? '<div class="problem">' + esc(w.health_problem) + '</div>' : '') + '<div class="worker-detail-grid"><div class="detail-block"><div class="detail-title">Running models</div><div class="workers">' + running + '</div></div><div class="detail-block"><div class="detail-title">Artifacts</div><div class="workers">' + artifacts + '</div></div></div></article>';
      }).join("") + '</div>';
    }
    function renderEvents(events, hasMore) {
      events = events || [];
      const button = document.getElementById("loadMoreEvents");
      button.style.display = hasMore ? "" : "none";
      button.disabled = false;
      if (!events.length) { document.getElementById("events").innerHTML = '<div class="empty">No worker events yet.</div>'; return; }
      document.getElementById("events").innerHTML = '<table><thead><tr><th>Received</th><th>Worker</th><th>Event</th><th>Model</th><th>Progress</th><th>Detail</th></tr></thead><tbody>' + events.map((e) => {
        const progress = e.total_bytes ? '<div class="bar"><span style="width:' + Math.max(0, Math.min(100, e.percent || 0)) + '%"></span></div><span class="muted">' + (e.percent || 0).toFixed(1) + '% ' + bytes(e.downloaded_bytes) + '/' + bytes(e.total_bytes) + '</span>' : '<span class="muted">-</span>';
        const detail = e.error || (e.from_state || e.to_state ? (e.from_state || "-") + " -> " + (e.to_state || "-") : (e.duration_ms ? "duration " + Math.round(e.duration_ms / 1000) + "s" : e.object || ""));
        return '<tr><td class="mono">' + esc(new Date(e.received_at).toLocaleTimeString()) + '</td><td>' + esc(e.worker_id) + '</td><td>' + pill(e.event, e.error ? "error" : eventClass(e.event)) + '</td><td>' + esc(e.model || "-") + '</td><td>' + progress + '</td><td class="mono">' + esc(detail) + '</td></tr>';
      }).join("") + '</tbody></table>';
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
    load();
    loadEvents(true);
    setInterval(load, 5000);
    setInterval(() => { if (!eventExpanded) loadEvents(true); }, 10000);
  </script>
</body>
</html>`
