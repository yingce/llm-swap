package scripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type grafanaDashboard struct {
	Panels []grafanaPanel `json:"panels"`
}

type grafanaPanel struct {
	Title   string          `json:"title"`
	Targets []grafanaTarget `json:"targets"`
}

type grafanaTarget struct {
	Expr string `json:"expr"`
}

func TestGrafanaDashboardOverviewUsesStableGatewayMetrics(t *testing.T) {
	dashboard := readGrafanaDashboard(t)

	modelOverview := dashboard.panel(t, "Model overview")
	modelExprs := modelOverview.exprs()
	assertExprContains(t, modelExprs, `llm_swap_gateway_model_loaded_replicas`)
	assertExprContains(t, modelExprs, `llm_swap_gateway_requests_total{model=~"$model"}`)
	assertExprContains(t, modelExprs, `llm_swap_gateway_queue_events_total{model=~"$model"`)
	assertExprContains(t, modelExprs, `llm_swap_gateway_request_duration_seconds_bucket{model=~"$model"}`)
	assertExprContains(t, modelExprs, `llm_swap_gateway_dispatch_failures_total{model=~"$model"}`)

	workerOverview := dashboard.panel(t, "Worker overview")
	workerExprs := workerOverview.exprs()
	assertExprContains(t, workerExprs, `llm_swap_gateway_worker_up{worker_id=~"$worker"}`)
	assertExprContains(t, workerExprs, `llm_swap_gateway_requests_total{worker_id=~"$worker"}`)
	assertExprContains(t, workerExprs, `llm_swap_gateway_worker_model_running{worker_id=~"$worker"}`)
	assertExprContains(t, workerExprs, `llm_swap_gateway_worker_gpu_memory_used_mib`)
	assertExprContains(t, workerExprs, `llm_swap_gateway_worker_gpu_utilization_percent`)
}

func readGrafanaDashboard(t *testing.T) grafanaDashboard {
	t.Helper()
	path := filepath.Join(repoRoot(t), "deploy", "grafana", "llm-swap-dashboard.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var dashboard grafanaDashboard
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatalf("dashboard JSON is invalid: %v", err)
	}
	return dashboard
}

func (d grafanaDashboard) panel(t *testing.T, title string) grafanaPanel {
	t.Helper()
	for _, panel := range d.Panels {
		if panel.Title == title {
			return panel
		}
	}
	t.Fatalf("dashboard panel %q not found", title)
	return grafanaPanel{}
}

func (p grafanaPanel) exprs() string {
	var b strings.Builder
	for _, target := range p.Targets {
		b.WriteString(target.Expr)
		b.WriteByte('\n')
	}
	return b.String()
}

func assertExprContains(t *testing.T, exprs, want string) {
	t.Helper()
	if !strings.Contains(exprs, want) {
		t.Fatalf("dashboard expressions missing %q; expressions:\n%s", want, exprs)
	}
}
