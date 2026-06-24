# VictoriaMetrics Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a VictoriaMetrics-backed historical metrics query path while keeping gateway `/metrics` as the write source and JSONL as detailed audit storage.

**Architecture:** Gateway exposes Prometheus metrics as it does today. `vmagent` scrapes `/metrics` and writes to VictoriaMetrics. Gateway reads VictoriaMetrics through a small Prometheus-compatible query client and serves shaped UI-authenticated JSON under `/ui/metrics/*`.

**Tech Stack:** Go standard library, existing config loader, existing gateway HTTP server, Prometheus text metrics already exposed by gateway, VictoriaMetrics Prometheus-compatible `query_range` API, Docker Compose examples.

---

## File Structure

- Modify `internal/config/config.go`
  - Add `MetricsStoreConfig` under `GatewayConfig`.
- Modify `internal/config/config_test.go`
  - Test YAML load defaults and explicit metrics store config.
- Create `internal/gateway/metrics_store.go`
  - VictoriaMetrics query client, range parsing, result shaping.
- Create `internal/gateway/metrics_store_test.go`
  - Fake VictoriaMetrics server tests.
- Modify `internal/gateway/server.go`
  - Add metrics store client field and `/ui/metrics/*` routes.
- Modify `internal/gateway/metrics.go`
  - Add missing low-cardinality counters/gauges for model active requests, model tokens, and control actions.
- Modify `internal/gateway/proxy.go`
  - Increment model token counters from request log entries.
- Modify `internal/gateway/reconcile.go`
  - Increment control action counters.
- Modify `internal/gateway/ui.go`
  - Add minimal HTML history section that fetches `/ui/metrics/summary`.
- Modify `internal/gateway/ui_test.go`
  - Assert dashboard contains history section and status still works without store.
- Add `deploy/docker-compose.metrics.yml`
  - Example gateway, VictoriaMetrics, and vmagent layout.
- Add `deploy/vmagent/promscrape.yml`
  - Example scrape config for gateway.
- Modify `examples/gateway.yaml`
  - Document disabled `metrics_store` config.
- Modify `docs/agents/project-map.md`
  - Document VictoriaMetrics metrics store boundaries.

---

### Task 1: Config For Metrics Store

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `examples/gateway.yaml`

- [ ] **Step 1: Write failing config test**

Append to `internal/config/config_test.go`:

```go
func TestLoadGatewayConfigAcceptsMetricsStoreConfig(t *testing.T) {
	raw := validGatewayYAML(`
metrics_store:
  enabled: true
  type: victoriametrics
  query_url: http://victoriametrics:8428
  default_range: 2h
  max_range: 14d
  timeout_ms: 2500
`)
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if !cfg.MetricsStore.Enabled {
		t.Fatal("metrics_store.enabled = false, want true")
	}
	if cfg.MetricsStore.Type != "victoriametrics" || cfg.MetricsStore.QueryURL != "http://victoriametrics:8428" {
		t.Fatalf("metrics store = %+v, want victoriametrics query URL", cfg.MetricsStore)
	}
	if cfg.MetricsStore.DefaultRange != "2h" || cfg.MetricsStore.MaxRange != "14d" || cfg.MetricsStore.TimeoutMS != 2500 {
		t.Fatalf("metrics store ranges = %+v", cfg.MetricsStore)
	}
}

func TestLoadGatewayConfigDefaultsMetricsStore(t *testing.T) {
	cfg, err := LoadGateway(strings.NewReader(validGatewayYAML("")))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.MetricsStore.Enabled {
		t.Fatal("metrics_store.enabled = true, want false")
	}
	if cfg.MetricsStore.Type != "victoriametrics" {
		t.Fatalf("metrics_store.type = %q, want victoriametrics", cfg.MetricsStore.Type)
	}
	if cfg.MetricsStore.DefaultRange != "1h" || cfg.MetricsStore.MaxRange != "7d" || cfg.MetricsStore.TimeoutMS != 3000 {
		t.Fatalf("metrics store defaults = %+v", cfg.MetricsStore)
	}
}
```

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/config -run 'TestLoadGatewayConfigAcceptsMetricsStoreConfig|TestLoadGatewayConfigDefaultsMetricsStore' -count=1
```

Expected: FAIL because `GatewayConfig` has no `MetricsStore`.

- [ ] **Step 3: Implement config struct and defaults**

In `internal/config/config.go`:

```go
type GatewayConfig struct {
	Gateway      GatewaySettings      `yaml:"gateway" json:"gateway"`
	OSS          OSSConfig            `yaml:"oss" json:"oss"`
	Tokens       TokenConfig          `yaml:"tokens" json:"tokens"`
	MetricsStore MetricsStoreConfig   `yaml:"metrics_store" json:"metrics_store"`
	Models       map[string]Model     `yaml:"models" json:"models"`
	TagPolicies  map[string]TagPolicy `yaml:"tag_policies" json:"tag_policies"`
}

type MetricsStoreConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	Type         string `yaml:"type" json:"type"`
	QueryURL     string `yaml:"query_url" json:"query_url"`
	DefaultRange string `yaml:"default_range" json:"default_range"`
	MaxRange     string `yaml:"max_range" json:"max_range"`
	TimeoutMS    int    `yaml:"timeout_ms" json:"timeout_ms"`
}
```

In the gateway load/default path, set:

```go
if cfg.MetricsStore.Type == "" {
	cfg.MetricsStore.Type = "victoriametrics"
}
if cfg.MetricsStore.DefaultRange == "" {
	cfg.MetricsStore.DefaultRange = "1h"
}
if cfg.MetricsStore.MaxRange == "" {
	cfg.MetricsStore.MaxRange = "7d"
}
if cfg.MetricsStore.TimeoutMS <= 0 {
	cfg.MetricsStore.TimeoutMS = 3000
}
```

Reject unsupported types only when enabled:

```go
if cfg.MetricsStore.Enabled && cfg.MetricsStore.Type != "victoriametrics" {
	return GatewayConfig{}, fmt.Errorf("metrics_store.type must be victoriametrics")
}
```

- [ ] **Step 4: Update example gateway config**

Add to `examples/gateway.yaml`:

```yaml
# Optional historical metrics query store. Gateway still exposes /metrics;
# vmagent should scrape it and remote_write to VictoriaMetrics.
metrics_store:
  enabled: false
  type: victoriametrics
  query_url: http://victoriametrics:8428
  default_range: 1h
  max_range: 7d
  timeout_ms: 3000
```

- [ ] **Step 5: Run GREEN**

Run:

```bash
go test ./internal/config -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go examples/gateway.yaml
git commit -m "feat: configure metrics store"
```

---

### Task 2: VictoriaMetrics Query Client

**Files:**
- Create: `internal/gateway/metrics_store.go`
- Create: `internal/gateway/metrics_store_test.go`

- [ ] **Step 1: Write failing query client tests**

Create `internal/gateway/metrics_store_test.go`:

```go
package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVictoriaMetricsClientQueryRangeShapesMatrix(t *testing.T) {
	var gotPath string
	var gotQuery string
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		writeJSON(w, map[string]any{
			"status": "success",
			"data": map[string]any{
				"resultType": "matrix",
				"result": []map[string]any{{
					"metric": map[string]string{"model": "qwen"},
					"values": [][]any{{float64(100), "2"}, {float64(130), "4"}},
				}},
			},
		})
	}))
	defer vm.Close()

	client := NewVictoriaMetricsClient(vm.URL, 2*time.Second)
	series, err := client.QueryRange(context.Background(), "requests", `sum(rate(llm_swap_gateway_requests_total[1m])) by (model)`, time.Unix(100, 0), time.Unix(130, 0), 30*time.Second)
	if err != nil {
		t.Fatalf("QueryRange returned error: %v", err)
	}
	if gotPath != "/prometheus/api/v1/query_range" {
		t.Fatalf("path = %q, want query_range", gotPath)
	}
	if gotQuery == "" {
		t.Fatal("query parameter is empty")
	}
	if len(series) != 1 || series[0].Name != "requests" || series[0].Labels["model"] != "qwen" {
		t.Fatalf("series = %+v, want qwen requests", series)
	}
	if len(series[0].Points) != 2 || series[0].Points[1][1] != 4 {
		t.Fatalf("points = %+v, want parsed values", series[0].Points)
	}
}

func TestParseMetricsRangeClampsMaxRange(t *testing.T) {
	start, end, step, err := parseMetricsRange("14d", "5m", "1h", "7d", time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("parseMetricsRange returned error: %v", err)
	}
	if end.Sub(start) != 7*24*time.Hour {
		t.Fatalf("range = %s, want clamped 7d", end.Sub(start))
	}
	if step != 5*time.Minute {
		t.Fatalf("step = %s, want 5m", step)
	}
}
```

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/gateway -run 'TestVictoriaMetricsClientQueryRangeShapesMatrix|TestParseMetricsRangeClampsMaxRange' -count=1
```

Expected: FAIL with undefined query client and parser.

- [ ] **Step 3: Implement client and parser**

Create `internal/gateway/metrics_store.go`:

```go
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type HistoricalSeries struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Points [][2]float64      `json:"points"`
}

type VictoriaMetricsClient struct {
	baseURL string
	client  *http.Client
}

func NewVictoriaMetricsClient(baseURL string, timeout time.Duration) *VictoriaMetricsClient {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &VictoriaMetricsClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *VictoriaMetricsClient) QueryRange(ctx context.Context, name, query string, start, end time.Time, step time.Duration) ([]HistoricalSeries, error) {
	if c == nil || c.baseURL == "" {
		return nil, fmt.Errorf("metrics store query url is not configured")
	}
	values := url.Values{}
	values.Set("query", query)
	values.Set("start", strconv.FormatInt(start.Unix(), 10))
	values.Set("end", strconv.FormatInt(end.Unix(), 10))
	values.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/prometheus/api/v1/query_range?"+values.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("metrics store returned %s", resp.Status)
	}

	var payload struct {
		Status string `json:"status"`
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload.Status != "success" {
		return nil, fmt.Errorf("metrics store query failed: %s", payload.Error)
	}
	out := make([]HistoricalSeries, 0, len(payload.Data.Result))
	for _, result := range payload.Data.Result {
		series := HistoricalSeries{Name: name, Labels: result.Metric, Points: make([][2]float64, 0, len(result.Values))}
		for _, rawPoint := range result.Values {
			if len(rawPoint) != 2 {
				continue
			}
			ts, ok := numberAsFloat(rawPoint[0])
			if !ok {
				continue
			}
			value, ok := stringNumberAsFloat(rawPoint[1])
			if !ok {
				continue
			}
			series.Points = append(series.Points, [2]float64{ts, value})
		}
		out = append(out, series)
	}
	return out, nil
}

func parseMetricsRange(rawRange, rawStep, defaultRange, maxRange string, now time.Time) (time.Time, time.Time, time.Duration, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if rawRange == "" {
		rawRange = defaultRange
	}
	if rawStep == "" {
		rawStep = "30s"
	}
	window, err := time.ParseDuration(rawRange)
	if err != nil || window <= 0 {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("invalid range")
	}
	maxWindow, err := time.ParseDuration(maxRange)
	if err != nil || maxWindow <= 0 {
		maxWindow = 7 * 24 * time.Hour
	}
	if window > maxWindow {
		window = maxWindow
	}
	step, err := time.ParseDuration(rawStep)
	if err != nil || step <= 0 {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("invalid step")
	}
	return now.Add(-window), now, step, nil
}
```

Also implement `numberAsFloat` and `stringNumberAsFloat` helpers in this file.

- [ ] **Step 4: Run GREEN**

Run:

```bash
go test ./internal/gateway -run 'TestVictoriaMetricsClientQueryRangeShapesMatrix|TestParseMetricsRangeClampsMaxRange' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/metrics_store.go internal/gateway/metrics_store_test.go
git commit -m "feat: add victoriametrics query client"
```

---

### Task 3: UI Metrics Endpoints

**Files:**
- Modify: `internal/gateway/server.go`
- Modify: `internal/gateway/metrics_store.go`
- Modify: `internal/gateway/metrics_store_test.go`

- [ ] **Step 1: Write failing endpoint tests**

Append to `internal/gateway/metrics_store_test.go`:

```go
func TestUIMetricsModelEndpointQueriesVictoriaMetrics(t *testing.T) {
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prometheus/api/v1/query_range" {
			t.Fatalf("path = %q, want query_range", r.URL.Path)
		}
		writeJSON(w, map[string]any{
			"status": "success",
			"data": map[string]any{"result": []map[string]any{{
				"metric": map[string]string{"model": "qwen"},
				"values": [][]any{{float64(100), "1"}},
			}}},
		})
	}))
	defer vm.Close()

	cfg := testUIGatewayConfig()
	cfg.MetricsStore.Enabled = true
	cfg.MetricsStore.QueryURL = vm.URL
	cfg.MetricsStore.DefaultRange = "1h"
	cfg.MetricsStore.MaxRange = "7d"
	cfg.MetricsStore.TimeoutMS = 3000
	srv := NewServer(cfg)

	req := httptest.NewRequest(http.MethodGet, "/ui/metrics/model?model=qwen&range=1h&step=30s", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp historicalMetricsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Series) == 0 || resp.Series[0].Name == "" {
		t.Fatalf("series = %+v, want historical series", resp.Series)
	}
}

func TestUIMetricsEndpointReturnsUnavailableWhenDisabled(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/metrics/summary", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rr.Code, rr.Body.String())
	}
}
```

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/gateway -run 'TestUIMetricsModelEndpointQueriesVictoriaMetrics|TestUIMetricsEndpointReturnsUnavailableWhenDisabled' -count=1
```

Expected: FAIL because routes and response type do not exist.

- [ ] **Step 3: Implement endpoints**

Add server fields:

```go
metricsStore *VictoriaMetricsClient
```

Initialize when enabled and query URL is set:

```go
if cfg.MetricsStore.Enabled && cfg.MetricsStore.QueryURL != "" {
	s.metricsStore = NewVictoriaMetricsClient(cfg.MetricsStore.QueryURL, time.Duration(cfg.MetricsStore.TimeoutMS)*time.Millisecond)
}
```

Register routes:

```go
s.mux.Handle("GET /ui/metrics/summary", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIMetricsSummary)))
s.mux.Handle("GET /ui/metrics/model", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIMetricsModel)))
s.mux.Handle("GET /ui/metrics/worker", uiAuth(cfg.Tokens.Agent, http.HandlerFunc(s.handleUIMetricsWorker)))
```

Add response type:

```go
type historicalMetricsResponse struct {
	Range  string             `json:"range"`
	Step   string             `json:"step"`
	Series []HistoricalSeries `json:"series"`
}
```

For first version, use fixed query sets:

```go
func modelHistoryQueries(model string) map[string]string {
	return map[string]string{
		"requests": `sum(rate(llm_swap_gateway_requests_total{model="` + model + `"}[1m])) by (model)`,
		"errors": `sum(rate(llm_swap_gateway_requests_total{model="` + model + `",status_code=~"5.."}[1m])) by (model)`,
		"p95_latency": `histogram_quantile(0.95, sum(rate(llm_swap_gateway_request_duration_seconds_bucket{model="` + model + `"}[5m])) by (le,model))`,
	}
}
```

Reject empty `model` and `worker_id` with HTTP 400. Return HTTP 503 if disabled or query fails.

- [ ] **Step 4: Run GREEN**

Run:

```bash
go test ./internal/gateway -run 'TestUIMetricsModelEndpointQueriesVictoriaMetrics|TestUIMetricsEndpointReturnsUnavailableWhenDisabled' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/server.go internal/gateway/metrics_store.go internal/gateway/metrics_store_test.go
git commit -m "feat: serve historical metrics queries"
```

---

### Task 4: Missing Low-Cardinality Metrics

**Files:**
- Modify: `internal/gateway/metrics.go`
- Modify: `internal/gateway/proxy.go`
- Modify: `internal/gateway/reconcile.go`
- Modify: `internal/gateway/metrics_test.go`
- Modify: `internal/gateway/reconcile_test.go`

- [ ] **Step 1: Write failing metrics tests**

Add tests that assert:

```go
llm_swap_gateway_model_tokens_total{model="qwen",type="total"} 7
llm_swap_gateway_control_actions_total{action="warm",model="qwen",reason="warm_for_min_loaded_empty_worker",worker_id="empty"} 1
```

Use existing proxy request usage test and `TestLoadedReconcilerWarmsMinLoadedOnEmptyWorker` patterns.

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/gateway -run 'TestProxyRecordsModelTokenMetric|TestLoadedReconcilerRecordsControlActionMetric' -count=1
```

Expected: FAIL because series are missing.

- [ ] **Step 3: Implement metrics**

Add to `Metrics`:

```go
modelActiveRequests *prometheus.GaugeVec
modelTokens         *prometheus.CounterVec
controlActions      *prometheus.CounterVec
controlActionErrors *prometheus.CounterVec
```

Increment from:

- proxy `recordRequestStats` or final request path for model tokens;
- accounting acquire/release path for model active gauge, or existing active request acquire path with model label;
- `LoadedReconciler.logControlAction` or action execution path for control actions.

- [ ] **Step 4: Run GREEN**

Run:

```bash
go test ./internal/gateway -run 'TestProxyRecordsModelTokenMetric|TestLoadedReconcilerRecordsControlActionMetric' -count=1
go test ./internal/gateway -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/metrics.go internal/gateway/proxy.go internal/gateway/reconcile.go internal/gateway/metrics_test.go internal/gateway/reconcile_test.go
git commit -m "feat: add historical planning metrics"
```

---

### Task 5: Compose And UI History Shell

**Files:**
- Add: `deploy/docker-compose.metrics.yml`
- Add: `deploy/vmagent/promscrape.yml`
- Modify: `internal/gateway/ui.go`
- Modify: `internal/gateway/ui_test.go`

- [ ] **Step 1: Write failing UI HTML test**

Extend `TestUIPageServesDashboardHTML` to require:

```go
"History", "/ui/metrics/summary", "renderHistory"
```

- [ ] **Step 2: Run RED**

Run:

```bash
go test ./internal/gateway -run TestUIPageServesDashboardHTML -count=1
```

Expected: FAIL because history shell is absent.

- [ ] **Step 3: Add deploy files**

Create `deploy/docker-compose.metrics.yml` and `deploy/vmagent/promscrape.yml` using the spec contents. Do not include secrets.

- [ ] **Step 4: Add UI history shell**

Add a dashboard section:

```html
<section>
  <h2>History</h2>
  <div id="history"></div>
</section>
```

Add JS `renderHistory` and fetch `/ui/metrics/summary?range=1h&step=30s`. On 503, show a muted disabled state.

- [ ] **Step 5: Run GREEN**

Run:

```bash
go test ./internal/gateway -run 'TestUIPageServesDashboardHTML|TestUIStatusEndpointUsesEmptyArraysInsteadOfNull' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add deploy/docker-compose.metrics.yml deploy/vmagent/promscrape.yml internal/gateway/ui.go internal/gateway/ui_test.go
git commit -m "feat: add metrics history shell"
```

---

### Task 6: Docs And Full Verification

**Files:**
- Modify: `docs/agents/project-map.md`

- [ ] **Step 1: Update project map**

Add:

```markdown
- `internal/gateway/metrics_store.go`
  - Queries VictoriaMetrics through the Prometheus-compatible API for historical
    UI metrics. This is read-only and never participates in request routing.
```

Add runtime layout:

```text
deploy/
  docker-compose.metrics.yml
  vmagent/promscrape.yml
```

- [ ] **Step 2: Run full tests**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Build gateway**

Run:

```bash
GOOS=linux GOARCH=amd64 go build -o /tmp/llm-swap-gateway-vmstore ./cmd/gateway
sha256sum /tmp/llm-swap-gateway-vmstore
```

Expected: build succeeds and prints SHA256.

- [ ] **Step 4: Commit docs**

```bash
git add docs/agents/project-map.md
git commit -m "docs: document victoriametrics metrics store"
```

- [ ] **Step 5: Final status**

Run:

```bash
git status --short
git log --oneline -8
```

Expected: clean worktree.
