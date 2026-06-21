package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry            *prometheus.Registry
	activeRequests      *prometheus.GaugeVec
	workerUp            *prometheus.GaugeVec
	workerActive        *prometheus.GaugeVec
	workerLastHeartbeat *prometheus.GaugeVec
	workerModelReady    *prometheus.GaugeVec
	workerModelRunning  *prometheus.GaugeVec
	workerActivityRows  *prometheus.CounterVec
	workerScrapeErrors  *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	activeRequests := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_active_requests",
		Help: "Current active requests handled by the gateway, labeled by worker and model.",
	}, []string{"worker_id", "model"})
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
	workerModelReady := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_model_ready",
		Help: "Whether a worker reports a model artifact as ready.",
	}, []string{"worker_id", "model"})
	workerModelRunning := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_worker_model_running",
		Help: "Whether llama-swap reports a model running and ready on a worker.",
	}, []string{"worker_id", "model"})
	workerActivityRows := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_worker_activity_rows_total",
		Help: "Unique llama-swap activity rows scraped from workers.",
	}, []string{"worker_id"})
	workerScrapeErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "llm_swap_gateway_worker_metrics_scrape_errors_total",
		Help: "Worker llama-swap metrics scrape errors observed by the gateway.",
	}, []string{"worker_id"})

	registry := prometheus.NewRegistry()
	registry.MustRegister(activeRequests, workerUp, workerActive, workerLastHeartbeat, workerModelReady, workerModelRunning, workerActivityRows, workerScrapeErrors)

	return &Metrics{
		registry:            registry,
		activeRequests:      activeRequests,
		workerUp:            workerUp,
		workerActive:        workerActive,
		workerLastHeartbeat: workerLastHeartbeat,
		workerModelReady:    workerModelReady,
		workerModelRunning:  workerModelRunning,
		workerActivityRows:  workerActivityRows,
		workerScrapeErrors:  workerScrapeErrors,
	}
}

func (m *Metrics) AcquireActiveRequest(workerID, model string) func() {
	active := m.activeRequests.WithLabelValues(workerID, model)
	active.Inc()
	return func() {
		active.Dec()
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) ObserveWorkers(workers []Worker, active map[string]int, now time.Time, pullActivity func(Worker) (int, error)) {
	for _, worker := range workers {
		up := 0.0
		if now.Sub(worker.LastHeartbeat) < 6*time.Second && worker.State == WorkerActive {
			up = 1
		}
		m.workerUp.WithLabelValues(worker.ID).Set(up)
		m.workerActive.WithLabelValues(worker.ID).Set(float64(active[worker.ID]))
		m.workerLastHeartbeat.WithLabelValues(worker.ID).Set(float64(worker.LastHeartbeat.Unix()))

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
		}
		if pullActivity != nil && up == 1 {
			rows, err := pullActivity(worker)
			if err != nil {
				m.workerScrapeErrors.WithLabelValues(worker.ID).Inc()
				continue
			}
			if rows > 0 {
				m.workerActivityRows.WithLabelValues(worker.ID).Add(float64(rows))
			} else {
				m.workerActivityRows.WithLabelValues(worker.ID).Add(0)
			}
		}
	}
}
