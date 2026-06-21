package gateway

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry       *prometheus.Registry
	activeRequests *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	activeRequests := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_active_requests",
		Help: "Current active requests handled by the gateway, labeled by worker and model.",
	}, []string{"worker_id", "model"})

	registry := prometheus.NewRegistry()
	registry.MustRegister(activeRequests)

	return &Metrics{
		registry:       registry,
		activeRequests: activeRequests,
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
