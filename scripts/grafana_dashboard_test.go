package scripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type grafanaDashboard struct {
	Panels     []grafanaPanel    `json:"panels"`
	Templating grafanaTemplating `json:"templating"`
}

type grafanaPanel struct {
	Title   string          `json:"title"`
	Targets []grafanaTarget `json:"targets"`
}

type grafanaTarget struct {
	Expr string `json:"expr"`
}

type grafanaTemplating struct {
	List []grafanaVariable `json:"list"`
}

type grafanaVariable struct {
	Name       string          `json:"name"`
	Query      json.RawMessage `json:"query"`
	Definition string          `json:"definition"`
}

func TestGrafanaDashboardOverviewUsesStableGatewayMetrics(t *testing.T) {
	dashboard := readGrafanaDashboard(t)

	modelOverview := dashboard.panel(t, "Model overview")
	modelExprs := modelOverview.exprs()
	assertExprContains(t, modelExprs, `last_over_time(llm_swap_gateway_model_loaded_replicas{model=~"$model"}[$__range])`)
	assertExprContains(t, modelExprs, `increase(llm_swap_gateway_requests_total{model=~"$model"}[$__range])`)
	assertExprContains(t, modelExprs, `increase(llm_swap_gateway_model_tokens_total{model=~"$model",type="total"}[$__range])`)
	assertExprContains(t, modelExprs, `increase(llm_swap_gateway_request_duration_seconds_bucket{model=~"$model"}[$__range])`)

	workerOverview := dashboard.panel(t, "Worker overview")
	workerExprs := workerOverview.exprs()
	assertExprContains(t, workerExprs, `last_over_time(llm_swap_gateway_worker_up{worker_id=~"$worker"}[$__range])`)
	assertExprContains(t, workerExprs, `increase(llm_swap_gateway_worker_requests_total{worker_id=~"$worker"}[$__range])`)
	assertExprContains(t, workerExprs, `llm_swap_gateway_worker_capacity_max_concurrency{worker_id=~"$worker"}`)
	assertExprContains(t, workerExprs, `llm_swap_gateway_worker_metrics_scrape_errors_total{worker_id=~"$worker"}`)
}

func TestGrafanaDashboardUsesRangeBasedOperationalPanels(t *testing.T) {
	dashboard := readGrafanaDashboard(t)

	requestsPerMinute := dashboard.panel(t, "Gateway requests per minute")
	assertExprContains(t, requestsPerMinute.exprs(), `sum(increase(llm_swap_gateway_requests_total[$__range])) / ($__range_s / 60)`)

	errorRate := dashboard.panel(t, "Gateway error rate")
	assertExprContains(t, errorRate.exprs(), `100 * sum(increase(llm_swap_gateway_requests_total{status_code=~"5.."}[$__range]))`)

	tokenCount := dashboard.panel(t, "Token count in range")
	assertExprContains(t, tokenCount.exprs(), `increase(llm_swap_gateway_model_tokens_total{model=~"$model"}[$__range])`)

	requestSummary := dashboard.panel(t, "Request summary/min by model")
	assertExprContains(t, requestSummary.exprs(), `sum by (model) (increase(llm_swap_gateway_queue_events_total{model=~"${model:regex}",result="admitted_after_wait"}[$__range])) / ($__range_s / 60)`)

	workerSaturation := dashboard.panel(t, "Worker saturation")
	assertExprContains(t, workerSaturation.exprs(), `llm_swap_gateway_worker_active_requests{worker_id=~"${worker:regex}"}`)
	assertExprContains(t, workerSaturation.exprs(), `llm_swap_gateway_worker_capacity_max_concurrency{worker_id=~"${worker:regex}"}`)

	gpuTemperature := dashboard.panel(t, "GPU temperature")
	assertExprContains(t, gpuTemperature.exprs(), `llm_swap_gateway_worker_gpu_temperature_celsius{worker_id=~"${worker:regex}"}`)
}

func TestGrafanaDashboardIncludesBillingAndAppUsage(t *testing.T) {
	dashboard := readGrafanaDashboard(t)

	appVariable := dashboard.variable(t, "app")
	if appVariable.Definition != "label_values(llm_swap_gateway_app_requests_total, app_id)" {
		t.Fatalf("app variable definition = %q", appVariable.Definition)
	}

	modelBillingCost := dashboard.panel(t, "Model billing cost")
	modelBillingCostExprs := modelBillingCost.exprs()
	assertExprContains(t, modelBillingCostExprs, `llm_swap_gateway_billing_model_cost_usd{model=~"$model"}`)
	assertExprContains(t, modelBillingCostExprs, `llm_swap_gateway_billing_model_used_cost_usd{model=~"$model"}`)
	assertExprContains(t, modelBillingCostExprs, `llm_swap_gateway_billing_model_idle_cost_usd{model=~"$model"}`)

	modelUtilization := dashboard.panel(t, "Model utilization")
	modelUtilizationExprs := modelUtilization.exprs()
	assertExprContains(t, modelUtilizationExprs, `increase(llm_swap_gateway_request_duration_seconds_sum{model=~"$model"}[$__range])`)
	assertExprContains(t, modelUtilizationExprs, `last_over_time(llm_swap_gateway_model_loaded_replicas{model=~"$model"}[$__range])`)

	appOverview := dashboard.panel(t, "App overview")
	appOverviewExprs := appOverview.exprs()
	assertExprContains(t, appOverviewExprs, `llm_swap_gateway_app_requests_total{app_id=~"$app",model=~"$model"}`)
	assertExprContains(t, appOverviewExprs, `llm_swap_gateway_app_tokens_total{app_id=~"$app",model=~"$model",type="total"}`)
	assertExprContains(t, appOverviewExprs, `llm_swap_gateway_app_model_used_cost_usd_total{app_id=~"$app",model=~"$model"}`)
	assertExprContains(t, appOverviewExprs, `llm_swap_gateway_app_request_duration_seconds_sum{app_id=~"$app",model=~"$model"}`)
	assertExprContains(t, appOverviewExprs, `llm_swap_gateway_request_duration_seconds_sum{model=~"$model"}`)
	assertExprContains(t, appOverviewExprs, `llm_swap_gateway_billing_model_idle_cost_usd{model=~"$model"}`)
	assertExprNotContains(t, appOverviewExprs, `llm_swap_gateway_billing_app_`)

	appOccupancy := dashboard.panel(t, "App occupancy")
	assertExprContains(t, appOccupancy.exprs(), `llm_swap_gateway_app_request_duration_seconds_sum{app_id=~"$app",model=~"$model"}`)

	appTokenRate := dashboard.panel(t, "App token rate")
	assertExprContains(t, appTokenRate.exprs(), `llm_swap_gateway_app_tokens_total{app_id=~"$app",model=~"$model"}`)

	appBillingCost := dashboard.panel(t, "App billing cost")
	appBillingCostExprs := appBillingCost.exprs()
	assertExprContains(t, appBillingCostExprs, `llm_swap_gateway_app_model_used_cost_usd_total{app_id=~"$app",model=~"$model"}`)
	assertExprContains(t, appBillingCostExprs, `llm_swap_gateway_app_request_duration_seconds_sum{app_id=~"$app",model=~"$model"}`)
	assertExprContains(t, appBillingCostExprs, `llm_swap_gateway_request_duration_seconds_sum{model=~"$model"}`)
	assertExprContains(t, appBillingCostExprs, `llm_swap_gateway_billing_model_idle_cost_usd{model=~"$model"}`)
	assertExprNotContains(t, appBillingCostExprs, `llm_swap_gateway_billing_app_`)
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

func (d grafanaDashboard) variable(t *testing.T, name string) grafanaVariable {
	t.Helper()
	for _, variable := range d.Templating.List {
		if variable.Name == name {
			return variable
		}
	}
	t.Fatalf("dashboard variable %q not found", name)
	return grafanaVariable{}
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

func assertExprNotContains(t *testing.T, exprs, unwanted string) {
	t.Helper()
	if strings.Contains(exprs, unwanted) {
		t.Fatalf("dashboard expressions unexpectedly contain %q; expressions:\n%s", unwanted, exprs)
	}
}
