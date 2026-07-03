package gateway

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"llm-swap/internal/config"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry                     *prometheus.Registry
	activeRequests               *prometheus.GaugeVec
	modelActiveRequests          *prometheus.GaugeVec
	workerUp                     *prometheus.GaugeVec
	workerActive                 *prometheus.GaugeVec
	workerLastHeartbeat          *prometheus.GaugeVec
	workerState                  *prometheus.GaugeVec
	workerNeedsRestart           *prometheus.GaugeVec
	workerLastErrorPresent       *prometheus.GaugeVec
	workerCapacityMaxConcurrency *prometheus.GaugeVec
	workerCapacityMaxQueue       *prometheus.GaugeVec
	workerRunningModels          *prometheus.GaugeVec
	workerGPUMemoryTotalMiB      *prometheus.GaugeVec
	workerGPUMemoryUsedMiB       *prometheus.GaugeVec
	workerGPUMemoryFreeMiB       *prometheus.GaugeVec
	workerGPUUtilizationPercent  *prometheus.GaugeVec
	workerGPUTemperatureCelsius  *prometheus.GaugeVec
	workerModelReady             *prometheus.GaugeVec
	workerModelRunning           *prometheus.GaugeVec
	workerModelState             *prometheus.GaugeVec
	workerActivityRows           *prometheus.CounterVec
	workerRequests               *prometheus.CounterVec
	workerRequestTokens          *prometheus.CounterVec
	workerRequestDuration        *prometheus.HistogramVec
	workerTokensPerSecond        *prometheus.GaugeVec
	workerPerformanceSamples     *prometheus.CounterVec
	workerScrapeErrors           *prometheus.CounterVec
	requests                     *prometheus.CounterVec
	modelTokens                  *prometheus.CounterVec
	requestDuration              *prometheus.HistogramVec
	queueEvents                  *prometheus.CounterVec
	queueWaitDuration            *prometheus.HistogramVec
	dispatchFailures             *prometheus.CounterVec
	replicaUnhealthy             *prometheus.GaugeVec
	replicaCooldownMarks         *prometheus.CounterVec
	replicaCooldownClears        *prometheus.CounterVec
	proxyRetries                 *prometheus.CounterVec
	controlActions               *prometheus.CounterVec
	modelLoadedReplicas          *prometheus.GaugeVec
	modelUnderprovisioned        *prometheus.GaugeVec
	modelQueueDepth              *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	activeRequests := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_active_requests",
		Help: "Current active requests handled by the gateway, labeled by worker and model.",
	}, []string{"worker_id", "model"})
	modelActiveRequests := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_model_active_requests",
		Help: "Current active requests handled by the gateway, labeled by model.",
	}, []string{"model"})
	workerUp := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_up",
		Help: "Whether the gateway currently considers a worker healthy.",
	}, []string{"worker_id"})
	workerActive := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_active_requests",
		Help: "Current active requests on a worker.",
	}, []string{"worker_id"})
	workerLastHeartbeat := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_last_heartbeat_seconds",
		Help: "Unix timestamp of the latest worker heartbeat observed by the gateway.",
	}, []string{"worker_id"})
	workerState := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_state",
		Help: "Worker state reported by the gateway registry as a one-hot gauge.",
	}, []string{"worker_id", "state"})
	workerNeedsRestart := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_needs_restart",
		Help: "Whether a worker reports that llama-swap needs a restart.",
	}, []string{"worker_id"})
	workerLastErrorPresent := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_last_error_present",
		Help: "Whether a worker heartbeat includes a non-empty last_error.",
	}, []string{"worker_id"})
	workerCapacityMaxConcurrency := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_capacity_max_concurrency",
		Help: "Worker-reported default maximum concurrency.",
	}, []string{"worker_id"})
	workerCapacityMaxQueue := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_capacity_max_queue",
		Help: "Worker-reported default maximum queue length.",
	}, []string{"worker_id"})
	workerRunningModels := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_running_models",
		Help: "Number of running model entries reported by a worker.",
	}, []string{"worker_id"})
	workerGPULabels := []string{"worker_id", "gpu_index", "gpu_name"}
	workerGPUMemoryTotalMiB := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_gpu_memory_total_mib",
		Help: "Worker GPU total memory in MiB reported by the latest heartbeat.",
	}, workerGPULabels)
	workerGPUMemoryUsedMiB := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_gpu_memory_used_mib",
		Help: "Worker GPU used memory in MiB reported by the latest heartbeat.",
	}, workerGPULabels)
	workerGPUMemoryFreeMiB := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_gpu_memory_free_mib",
		Help: "Worker GPU free memory in MiB reported by the latest heartbeat.",
	}, workerGPULabels)
	workerGPUUtilizationPercent := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_gpu_utilization_percent",
		Help: "Worker GPU utilization percentage reported by the latest heartbeat.",
	}, workerGPULabels)
	workerGPUTemperatureCelsius := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_gpu_temperature_celsius",
		Help: "Worker GPU temperature in Celsius reported by the latest heartbeat.",
	}, workerGPULabels)
	workerModelReady := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_model_ready",
		Help: "Whether a worker reports a model artifact as ready.",
	}, []string{"worker_id", "model"})
	workerModelRunning := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_model_running",
		Help: "Whether llama-swap reports a model running and ready on a worker.",
	}, []string{"worker_id", "model"})
	workerModelState := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_model_state",
		Help: "Running model state reported by llama-swap as a one-hot gauge.",
	}, []string{"worker_id", "model", "state"})
	workerActivityRows := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_worker_activity_rows_total",
		Help: "Unique llama-swap activity rows scraped from workers.",
	}, []string{"worker_id"})
	workerRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_worker_requests_total",
		Help: "Unique worker llama-swap request rows scraped by the gateway.",
	}, []string{"worker_id", "model", "path", "status_code"})
	workerRequestTokens := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_worker_request_tokens_total",
		Help: "Tokens reported by worker llama-swap request rows.",
	}, []string{"worker_id", "model", "type"})
	workerRequestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_swap_gateway_worker_request_duration_seconds",
		Help:    "Worker llama-swap request duration from scraped activity rows.",
		Buckets: prometheus.DefBuckets,
	}, []string{"worker_id", "model"})
	workerTokensPerSecond := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_tokens_per_second",
		Help: "Latest token throughput reported by worker llama-swap activity rows.",
	}, []string{"worker_id", "model", "type"})
	workerPerformanceSamples := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_worker_performance_samples_total",
		Help: "Unique llama-swap performance samples scraped from workers.",
	}, []string{"worker_id"})
	workerScrapeErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_worker_metrics_scrape_errors_total",
		Help: "Worker llama-swap metrics scrape errors observed by the gateway.",
	}, []string{"worker_id"})
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_requests_total",
		Help: "Requests handled by the gateway.",
	}, []string{"model", "worker_id", "status_code"})
	modelTokens := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_model_tokens_total",
		Help: "Tokens reported in gateway proxied OpenAI-compatible responses.",
	}, []string{"model", "type"})
	requestDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_swap_gateway_request_duration_seconds",
		Help:    "End-to-end gateway request duration.",
		Buckets: prometheus.DefBuckets,
	}, []string{"model", "worker_id"})
	queueEvents := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_queue_events_total",
		Help: "Gateway queue outcomes.",
	}, []string{"model", "result"})
	queueWaitDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_swap_gateway_queue_wait_seconds",
		Help:    "Gateway queue wait duration by model, gate type, and outcome.",
		Buckets: []float64{0, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"model", "key_type", "result"})
	dispatchFailures := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_dispatch_failures_total",
		Help: "Gateway dispatch failures by selected worker and reason.",
	}, []string{"model", "worker_id", "reason"})
	replicaUnhealthy := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_replica_unhealthy",
		Help: "Whether a worker/model replica is temporarily excluded by gateway cooldown.",
	}, []string{"worker_id", "model", "reason"})
	replicaCooldownMarks := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_replica_cooldown_marks_total",
		Help: "Replica cooldown marks by worker, model, and reason.",
	}, []string{"worker_id", "model", "reason"})
	replicaCooldownClears := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_replica_cooldown_clears_total",
		Help: "Replica cooldown clears by worker, model, and reason.",
	}, []string{"worker_id", "model", "reason"})
	proxyRetries := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_proxy_retries_total",
		Help: "Gateway proxy retries by worker, model, and reason.",
	}, []string{"worker_id", "model", "reason"})
	controlActions := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_control_actions_total",
		Help: "Gateway model control actions by action, worker, model, reason, and result.",
	}, []string{"action", "worker_id", "model", "reason", "result"})
	modelLoadedReplicas := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_model_loaded_replicas",
		Help: "Number of healthy workers reporting a model as loaded and ready.",
	}, []string{"model"})
	modelUnderprovisioned := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_model_underprovisioned",
		Help: "Whether a model is below its configured min_loaded target.",
	}, []string{"model"})
	modelQueueDepth := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_model_queue_depth",
		Help: "Current number of queued requests at the gateway model gate.",
	}, []string{"model"})

	registry := prometheus.NewRegistry()
	registry.MustRegister(activeRequests, modelActiveRequests, workerUp, workerActive, workerLastHeartbeat, workerState, workerNeedsRestart, workerLastErrorPresent, workerCapacityMaxConcurrency, workerCapacityMaxQueue, workerRunningModels, workerGPUMemoryTotalMiB, workerGPUMemoryUsedMiB, workerGPUMemoryFreeMiB, workerGPUUtilizationPercent, workerGPUTemperatureCelsius, workerModelReady, workerModelRunning, workerModelState, workerActivityRows, workerRequests, workerRequestTokens, workerRequestDuration, workerTokensPerSecond, workerPerformanceSamples, workerScrapeErrors, requests, modelTokens, requestDuration, queueEvents, queueWaitDuration, dispatchFailures, replicaUnhealthy, replicaCooldownMarks, replicaCooldownClears, proxyRetries, controlActions, modelLoadedReplicas, modelUnderprovisioned, modelQueueDepth)

	return &Metrics{
		registry:                     registry,
		activeRequests:               activeRequests,
		modelActiveRequests:          modelActiveRequests,
		workerUp:                     workerUp,
		workerActive:                 workerActive,
		workerLastHeartbeat:          workerLastHeartbeat,
		workerState:                  workerState,
		workerNeedsRestart:           workerNeedsRestart,
		workerLastErrorPresent:       workerLastErrorPresent,
		workerCapacityMaxConcurrency: workerCapacityMaxConcurrency,
		workerCapacityMaxQueue:       workerCapacityMaxQueue,
		workerRunningModels:          workerRunningModels,
		workerGPUMemoryTotalMiB:      workerGPUMemoryTotalMiB,
		workerGPUMemoryUsedMiB:       workerGPUMemoryUsedMiB,
		workerGPUMemoryFreeMiB:       workerGPUMemoryFreeMiB,
		workerGPUUtilizationPercent:  workerGPUUtilizationPercent,
		workerGPUTemperatureCelsius:  workerGPUTemperatureCelsius,
		workerModelReady:             workerModelReady,
		workerModelRunning:           workerModelRunning,
		workerModelState:             workerModelState,
		workerActivityRows:           workerActivityRows,
		workerRequests:               workerRequests,
		workerRequestTokens:          workerRequestTokens,
		workerRequestDuration:        workerRequestDuration,
		workerTokensPerSecond:        workerTokensPerSecond,
		workerPerformanceSamples:     workerPerformanceSamples,
		workerScrapeErrors:           workerScrapeErrors,
		requests:                     requests,
		modelTokens:                  modelTokens,
		requestDuration:              requestDuration,
		queueEvents:                  queueEvents,
		queueWaitDuration:            queueWaitDuration,
		dispatchFailures:             dispatchFailures,
		replicaUnhealthy:             replicaUnhealthy,
		replicaCooldownMarks:         replicaCooldownMarks,
		replicaCooldownClears:        replicaCooldownClears,
		proxyRetries:                 proxyRetries,
		controlActions:               controlActions,
		modelLoadedReplicas:          modelLoadedReplicas,
		modelUnderprovisioned:        modelUnderprovisioned,
		modelQueueDepth:              modelQueueDepth,
	}
}

func (m *Metrics) AcquireActiveRequest(workerID, model string) func() {
	active := m.activeRequests.WithLabelValues(workerID, model)
	modelActive := m.modelActiveRequests.WithLabelValues(model)
	active.Inc()
	modelActive.Inc()
	return func() {
		active.Dec()
		modelActive.Dec()
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) ObserveRequest(model, workerID string, statusCode int, duration time.Duration) {
	if statusCode <= 0 {
		statusCode = 0
	}
	m.requests.WithLabelValues(model, workerID, strconv.Itoa(statusCode)).Inc()
	m.requestDuration.WithLabelValues(model, workerID).Observe(duration.Seconds())
}

func (m *Metrics) ObserveRequestTokens(entry RequestLogEntry) {
	if entry.PromptTokens > 0 {
		m.modelTokens.WithLabelValues(entry.Model, "prompt").Add(float64(entry.PromptTokens))
	}
	if entry.CompletionTokens > 0 {
		m.modelTokens.WithLabelValues(entry.Model, "completion").Add(float64(entry.CompletionTokens))
	}
	if entry.TotalTokens > 0 {
		m.modelTokens.WithLabelValues(entry.Model, "total").Add(float64(entry.TotalTokens))
	}
	if entry.CacheTokens > 0 {
		m.modelTokens.WithLabelValues(entry.Model, "cache").Add(float64(entry.CacheTokens))
	}
	if entry.ReasoningTokens > 0 {
		m.modelTokens.WithLabelValues(entry.Model, "reasoning").Add(float64(entry.ReasoningTokens))
	}
}

func (m *Metrics) ObserveControlAction(action, model, workerID, reason, result string) {
	if action == "" {
		action = "unknown"
	}
	if reason == "" {
		reason = "unknown"
	}
	if result == "" {
		result = "unknown"
	}
	m.controlActions.WithLabelValues(action, workerID, model, reason, result).Inc()
}

func (m *Metrics) ObserveQueueEvent(model, result string) {
	m.queueEvents.WithLabelValues(model, result).Inc()
}

func (m *Metrics) ObserveQueueWait(model, keyType, result string, wait time.Duration) {
	if wait < 0 {
		wait = 0
	}
	m.queueEvents.WithLabelValues(model, result).Inc()
	m.queueWaitDuration.WithLabelValues(model, keyType, result).Observe(wait.Seconds())
}

func (m *Metrics) ObserveDispatchFailure(model, workerID, reason string) {
	m.dispatchFailures.WithLabelValues(model, workerID, reason).Inc()
}

func (m *Metrics) ObserveReplicaCooldowns(snapshot ReplicaCooldownSnapshot, now time.Time) {
	for workerID, byModel := range snapshot {
		for model, entry := range byModel {
			if entry.CooldownUntil.After(now) {
				m.replicaUnhealthy.WithLabelValues(workerID, model, entry.Reason).Set(1)
			}
		}
	}
}

func (m *Metrics) ObserveReplicaCooldownMark(entry ReplicaCooldown) {
	m.replicaCooldownMarks.WithLabelValues(entry.WorkerID, entry.Model, entry.Reason).Inc()
}

func (m *Metrics) ObserveReplicaCooldownClear(entry ReplicaCooldown) {
	m.replicaCooldownClears.WithLabelValues(entry.WorkerID, entry.Model, entry.Reason).Inc()
}

func (m *Metrics) ObserveProxyRetry(model, workerID, reason string) {
	m.proxyRetries.WithLabelValues(workerID, model, reason).Inc()
}

func (m *Metrics) ObserveModelProvisioning(cfg config.GatewayConfig, workers []Worker, now time.Time) {
	for modelName, model := range cfg.Models {
		loaded := 0
		for _, worker := range workers {
			if !workerAvailableForProvisioning(worker, now) {
				continue
			}
			if runningModelReady(worker, modelName) {
				loaded++
			}
		}

		m.modelLoadedReplicas.WithLabelValues(modelName).Set(float64(loaded))
		underprovisioned := 0.0
		if model.MinLoaded > 0 && loaded < model.MinLoaded {
			underprovisioned = 1
		}
		m.modelUnderprovisioned.WithLabelValues(modelName).Set(underprovisioned)
	}
}

func (m *Metrics) ObserveModelQueues(cfg config.GatewayConfig, limiter *QueueLimiter) {
	for modelName := range cfg.Models {
		queued := 0
		if limiter != nil {
			queued = limiter.Queued("model:" + modelName)
		}
		m.modelQueueDepth.WithLabelValues(modelName).Set(float64(queued))
	}
}

func (m *Metrics) ObserveWorkers(workers []Worker, active map[string]int, now time.Time, pullActivity func(Worker) (ActivityStats, error), pullPerformance func(Worker) (int, error)) {
	var wg sync.WaitGroup
	for _, worker := range workers {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.observeWorker(worker, active, now, pullActivity, pullPerformance)
		}()
	}
	wg.Wait()
}

func (m *Metrics) observeWorker(worker Worker, active map[string]int, now time.Time, pullActivity func(Worker) (ActivityStats, error), pullPerformance func(Worker) (int, error)) {
	freshActive := now.Sub(worker.LastHeartbeat) < 6*time.Second && worker.State == WorkerActive
	up := 0.0
	if freshActive && !now.Before(worker.ScrapeBackoffUntil) {
		up = 1
	}
	m.workerUp.WithLabelValues(worker.ID).Set(up)
	m.workerActive.WithLabelValues(worker.ID).Set(float64(active[worker.ID]))
	m.workerLastHeartbeat.WithLabelValues(worker.ID).Set(float64(worker.LastHeartbeat.Unix()))
	m.observeWorkerState(worker)
	m.observeWorkerGPUDevices(worker)

	for model, status := range worker.Artifacts {
		ready := 0.0
		if strings.EqualFold(status, "ready") {
			ready = 1
		}
		m.workerModelReady.WithLabelValues(worker.ID, model).Set(ready)
	}
	for _, running := range worker.RunningModels {
		value := 0.0
		if strings.EqualFold(running.State, "ready") {
			value = 1
		}
		m.workerModelRunning.WithLabelValues(worker.ID, running.Model).Set(value)
		if strings.TrimSpace(running.State) != "" {
			m.workerModelState.WithLabelValues(worker.ID, running.Model, running.State).Set(1)
		}
	}
	if pullActivity != nil && freshActive {
		activity, err := pullActivity(worker)
		if err != nil {
			m.workerScrapeErrors.WithLabelValues(worker.ID).Inc()
		} else {
			m.workerActivityRows.WithLabelValues(worker.ID).Add(float64(activity.Rows))
			for _, request := range activity.Requests {
				m.ObserveWorkerActivity(worker.ID, request)
			}
		}
	}
	if pullPerformance != nil && freshActive {
		samples, err := pullPerformance(worker)
		if err != nil {
			m.workerScrapeErrors.WithLabelValues(worker.ID).Inc()
		} else if samples > 0 {
			m.workerPerformanceSamples.WithLabelValues(worker.ID).Add(float64(samples))
		} else {
			m.workerPerformanceSamples.WithLabelValues(worker.ID).Add(0)
		}
	}
}

func (m *Metrics) observeWorkerGPUDevices(worker Worker) {
	for _, device := range worker.GPUDevices {
		index := strconv.Itoa(device.Index)
		name := strings.TrimSpace(device.Name)
		if name == "" {
			name = "unknown"
		}
		labels := []string{worker.ID, index, name}
		m.workerGPUMemoryTotalMiB.WithLabelValues(labels...).Set(float64(device.MemoryTotalMiB))
		m.workerGPUMemoryUsedMiB.WithLabelValues(labels...).Set(float64(device.MemoryUsedMiB))
		m.workerGPUMemoryFreeMiB.WithLabelValues(labels...).Set(float64(device.MemoryFreeMiB))
		m.workerGPUUtilizationPercent.WithLabelValues(labels...).Set(device.UtilizationPercent)
		m.workerGPUTemperatureCelsius.WithLabelValues(labels...).Set(device.TemperatureCelsius)
	}
}

func (m *Metrics) observeWorkerState(worker Worker) {
	active := 0.0
	draining := 0.0
	switch worker.State {
	case WorkerDraining:
		draining = 1
	default:
		active = 1
	}
	m.workerState.WithLabelValues(worker.ID, string(WorkerActive)).Set(active)
	m.workerState.WithLabelValues(worker.ID, string(WorkerDraining)).Set(draining)
	m.workerRunningModels.WithLabelValues(worker.ID).Set(float64(len(worker.RunningModels)))
	m.workerCapacityMaxConcurrency.WithLabelValues(worker.ID).Set(float64(worker.Capacity.MaxConcurrency))
	m.workerCapacityMaxQueue.WithLabelValues(worker.ID).Set(float64(worker.Capacity.MaxQueue))
	if worker.NeedsRestart {
		m.workerNeedsRestart.WithLabelValues(worker.ID).Set(1)
	} else {
		m.workerNeedsRestart.WithLabelValues(worker.ID).Set(0)
	}
	if strings.TrimSpace(worker.LastError) != "" {
		m.workerLastErrorPresent.WithLabelValues(worker.ID).Set(1)
	} else {
		m.workerLastErrorPresent.WithLabelValues(worker.ID).Set(0)
	}
}

func (m *Metrics) ObserveWorkerActivity(workerID string, request ActivityRequestStats) {
	model := request.Model
	if model == "" {
		model = "unknown"
	}
	path := request.Path
	if path == "" {
		path = "unknown"
	}
	statusCode := request.StatusCode
	m.workerRequests.WithLabelValues(workerID, model, path, strconv.Itoa(statusCode)).Inc()
	if request.DurationMS > 0 {
		m.workerRequestDuration.WithLabelValues(workerID, model).Observe(request.DurationMS / 1000)
	}
	for tokenType, value := range request.Tokens {
		if value < 0 {
			continue
		}
		m.workerRequestTokens.WithLabelValues(workerID, model, tokenType).Add(value)
	}
	if request.PromptTokensPerSec > 0 {
		m.workerTokensPerSecond.WithLabelValues(workerID, model, "prompt").Set(request.PromptTokensPerSec)
	}
	if request.CompletionTokensPerSec > 0 {
		m.workerTokensPerSecond.WithLabelValues(workerID, model, "completion").Set(request.CompletionTokensPerSec)
	}
}

func workerAvailableForProvisioning(worker Worker, now time.Time) bool {
	if now.Sub(worker.LastHeartbeat) >= 6*time.Second {
		return false
	}
	if worker.State != WorkerActive {
		return false
	}
	return !now.Before(worker.ScrapeBackoffUntil)
}
