# VictoriaMetrics Store Design

Date: 2026-06-24

## Goal

Add a durable metrics store for gateway, model, worker, queue, and replica
signals. The first version uses VictoriaMetrics for aggregated time-series
history while keeping existing JSONL files for request and worker-event detail.

## Architecture

The gateway remains the source of live routing state. It continues exposing
Prometheus metrics at `/metrics`. A local `vmagent` scrapes that endpoint and
remote-writes samples to VictoriaMetrics. The gateway reads historical data
from VictoriaMetrics through the Prometheus-compatible query API.

```text
gateway /metrics
  -> vmagent scrape
    -> VictoriaMetrics remote_write

dashboard /ui
  -> gateway /ui/status for live state
  -> gateway /ui/metrics/* for historical ranges
    -> VictoriaMetrics query_range
```

This keeps write buffering, retry, and remote-write protocol handling outside
the gateway. Later, single-node VictoriaMetrics can be replaced with a
VictoriaMetrics cluster by changing the vmagent remote write URL and gateway
query URL.

## Non-Goals

- Do not store per-request detail in VictoriaMetrics.
- Do not use high-cardinality labels such as `request_id`, raw prompt text,
  upstream URL, full error message, or artifact object.
- Do not replace `gateway-requests.jsonl` or `gateway-worker-events.jsonl`.
- Do not make routing depend on VictoriaMetrics availability.
- Do not implement automatic scale decisions in this first version.

## Storage Model

VictoriaMetrics stores aggregated time-series metrics only.

Allowed labels:

- `model`
- `worker_id`
- `tag`
- `status_code`
- `result`
- `reason`
- `state`
- `key_type`

Disallowed labels:

- `request_id`
- client IP
- auth token
- prompt or response text
- raw error body
- full URL
- model artifact object

Existing JSONL files continue to serve separate purposes:

- `/opt/llmswap/logs/gateway-requests.jsonl`: request detail, token accounting,
  replay into `AccessTracker`, and audit.
- `/opt/llmswap/logs/gateway-worker-events.jsonl`: worker lifecycle and
  gateway action events for recent event UI and debugging.

## Gateway Metrics

The gateway already exposes many Prometheus metrics. The first implementation
should keep those and add only missing low-cardinality series needed for useful
history.

Existing series to retain:

- `llm_swap_gateway_requests_total`
- `llm_swap_gateway_request_duration_seconds`
- `llm_swap_gateway_queue_events_total`
- `llm_swap_gateway_queue_wait_seconds`
- `llm_swap_gateway_worker_up`
- `llm_swap_gateway_worker_active_requests`
- `llm_swap_gateway_worker_model_running`
- `llm_swap_gateway_worker_model_state`
- `llm_swap_gateway_worker_requests_total`
- `llm_swap_gateway_worker_request_tokens_total`
- `llm_swap_gateway_worker_request_duration_seconds`
- `llm_swap_gateway_worker_tokens_per_second`
- `llm_swap_gateway_model_loaded_replicas`
- `llm_swap_gateway_model_underprovisioned`
- `llm_swap_gateway_replica_unhealthy`
- `llm_swap_gateway_replica_cooldown_marks_total`
- `llm_swap_gateway_proxy_retries_total`

New or adjusted series:

- `llm_swap_gateway_control_actions_total{action,model,worker_id,reason}`
  - Increment when gateway plans a warm or unload action.
- `llm_swap_gateway_control_action_errors_total{action,model,worker_id,reason}`
  - Increment when a gateway warm or unload action fails.
- `llm_swap_gateway_model_active_requests{model}`
  - Gauge for active gateway requests by model.
- `llm_swap_gateway_model_tokens_total{model,type}`
  - Counter from gateway response usage tokens with `type` values `prompt`,
    `completion`, `total`, `cache`, and `reasoning`.

These additions avoid reading JSONL for common dashboard and capacity-planning
queries.

## Gateway Config

Add optional gateway config:

```yaml
metrics_store:
  enabled: true
  type: victoriametrics
  query_url: http://victoriametrics:8428
  default_range: 1h
  max_range: 7d
  timeout_ms: 3000
```

Defaults:

- `enabled`: false
- `type`: `victoriametrics`
- `query_url`: empty
- `default_range`: `1h`
- `max_range`: `7d`
- `timeout_ms`: `3000`

If `enabled` is true but `query_url` is empty, gateway starts normally and
history endpoints return a clear unavailable error. Request routing, worker
heartbeat, and `/metrics` remain unaffected.

## Compose Layout

Add an example compose file, not production secrets:

```yaml
services:
  gateway:
    image: llm-swap-gateway
    ports:
      - "8080:8080"
    volumes:
      - /opt/llmswap:/opt/llmswap

  victoriametrics:
    image: victoriametrics/victoria-metrics
    command:
      - -storageDataPath=/victoria-metrics-data
      - -retentionPeriod=30d
    volumes:
      - vm-data:/victoria-metrics-data

  vmagent:
    image: victoriametrics/vmagent
    command:
      - -promscrape.config=/etc/vmagent/promscrape.yml
      - -remoteWrite.url=http://victoriametrics:8428/api/v1/write
    volumes:
      - ./vmagent/promscrape.yml:/etc/vmagent/promscrape.yml:ro

volumes:
  vm-data:
```

`promscrape.yml`:

```yaml
global:
  scrape_interval: 5s

scrape_configs:
  - job_name: llmswap-gateway
    static_configs:
      - targets:
          - gateway:8080
```

## Query API

Gateway adds historical metrics endpoints under UI auth:

- `GET /ui/metrics/summary?range=1h&step=30s`
- `GET /ui/metrics/model?model=qwen&range=6h&step=1m`
- `GET /ui/metrics/worker?worker_id=worker-real-1&range=6h&step=1m`

All endpoints use the agent token, matching existing `/ui` routes.

Responses are gateway-shaped JSON, not raw Prometheus API payloads. The gateway
client converts VictoriaMetrics `query_range` results into arrays like:

```json
{
  "range": "1h",
  "step": "30s",
  "series": [
    {
      "name": "requests",
      "labels": {"model": "qwen"},
      "points": [[1782297017, 12]]
    }
  ]
}
```

If VictoriaMetrics is unavailable, endpoints return HTTP 503 with an OpenAI-like
error body or simple JSON error. The live dashboard status endpoint still works.

## Query Set

Summary endpoint:

- request rate by model;
- p95 latency by model;
- error rate by model;
- active requests by model;
- queue full/timeout rate by model;
- cooldown count by model;
- underprovisioned model gauge.

Model endpoint:

- request rate;
- token rate by type;
- p50/p95 latency;
- status-code rate;
- ready loaded replicas;
- active requests;
- queue wait p95;
- cooldown marks and proxy retries.

Worker endpoint:

- worker up;
- active requests;
- running model state;
- worker request rate;
- worker token throughput;
- scrape failures;
- cooldown state.

## UI

The first UI version adds a compact historical section below live status:

- range selector: `15m`, `1h`, `6h`, `24h`;
- model summary table with request rate, p95 latency, error rate, token rate,
  queue waits, and cooldown count;
- worker summary table with up status history, active requests, running models,
  and scrape failures.

Charts can be added later. The first version may render compact tables and
sparklines if simple enough. The live status UI must continue rendering when
history queries fail.

## Error Handling

- Gateway `/metrics` must work even when VictoriaMetrics and vmagent are absent.
- History endpoints must timeout using `metrics_store.timeout_ms`.
- History endpoints clamp requested range to `metrics_store.max_range`.
- Invalid range or step values return HTTP 400.
- Query failures log structured events with `event=metrics_store_query_error`.
- Do not log query URLs with credentials if future URLs include auth.

## Testing

Unit tests:

- config default and YAML loading for `metrics_store`;
- query client builds correct VictoriaMetrics URLs;
- query client parses matrix and scalar responses used by the gateway;
- query endpoints validate range and step;
- query endpoints return 503 when disabled or unavailable;
- metrics additions increment on request and control-action paths;
- UI history section handles empty and unavailable responses.

Integration-style tests:

- fake VictoriaMetrics server returns `query_range` payloads;
- `/ui/metrics/model` returns shaped series for a configured model;
- gateway `/ui/status` still works when metrics store is down.

Manual deployment check:

- compose starts VictoriaMetrics and vmagent;
- VM target receives gateway metrics;
- gateway history endpoint returns non-empty series after one scrape interval.

## Rollout

1. Add gateway config and query client with metrics store disabled by default.
2. Add history endpoints using a fake VictoriaMetrics server in tests.
3. Add missing low-cardinality Prometheus metrics.
4. Add example Docker Compose and vmagent scrape config.
5. Add UI history tables with graceful degradation.
6. Deploy gateway with `metrics_store.enabled=false` first.
7. Start VictoriaMetrics and vmagent.
8. Enable `metrics_store` and verify history endpoints.

## Future Work

- Use historical queue wait, request rate, and cooldown signals for predictive
  warm decisions.
- Support VictoriaMetrics cluster URLs separately for write and query paths.
- Add downsampling or recording rules for long retention periods.
- Add chart rendering once the query API is stable.
