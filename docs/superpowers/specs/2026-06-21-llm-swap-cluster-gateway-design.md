# llama-swap Cluster Gateway Design

Date: 2026-06-21

## Goal

Build a multi-model, multi-worker inference cluster on top of llama-swap.

The system must support models served by vLLM, SGLang, llama.cpp, and other OpenAI-compatible runtimes. GPU servers are limited, so the cluster must decide which model to run on which worker, when to unload lower-value models, and when to reject or queue requests.

The gateway is the single public inference entry point. It owns scheduling, queueing, concurrency limits, request metrics, request logs, and worker/model state. Worker agents stay intentionally thin: they download model artifacts, generate local llama-swap configuration, restart llama-swap when needed, and report local state.

## Non-Goals For V1

- No Kubernetes dependency.
- No SSH dependency for workers.
- No distributed gateway state in the first version.
- No full KV-cache-aware scheduler in the first version.
- No agent-side request routing or global scheduling.
- No custom model process manager in the agent when llama-swap can manage the process.

V1 reserves interfaces for later Redis-backed gateway state and cache-affinity routing.

## Architecture

```text
client
  -> gateway
    -> worker llama-swap
      -> vLLM / SGLang / llama.cpp / other OpenAI-compatible runtime

gateway
  <- heartbeat from agent
  -> pull llama-swap /running, /metrics, /api/metrics, /api/performance

agent
  -> GET gateway tag config
  -> download OSS artifacts
  -> render llama-swap.yaml
  -> restart local llama-swap when rendered config changes
  -> POST heartbeat to gateway
```

## Components

### Gateway

Gateway responsibilities:

- Serve OpenAI-compatible inference endpoints.
- Authenticate clients and internal agents with simple bearer tokens.
- Store cluster model configuration and tag policies.
- Expose tag-scoped config to agents.
- Maintain worker registry from heartbeats.
- Track request lifecycle and in-flight request ownership.
- Enforce model, tag, and worker concurrency limits.
- Enforce model, tag, and worker queue limits.
- Choose workers for requests.
- Maintain `min_loaded` and `max_loaded` model replica policy.
- Decide when to unload a model from a worker.
- Call llama-swap unload APIs on selected workers.
- Pull worker metrics and expose merged gateway metrics.
- Record request logs and scheduler decision logs.

### Agent

Agent responsibilities:

- Start as a Go binary on each worker.
- Know its local tags from local config.
- Know its local model root from local config.
- Pull tag-scoped config from gateway.
- Download model artifacts from Aliyun OSS.
- Verify artifact object integrity with `x-oss-hash-crc64ecma`.
- Store artifact marker files to avoid duplicate downloads.
- Render local `llama-swap.yaml`.
- Restart local llama-swap only when rendered config content changes.
- Apply a local lock around download, extraction, config write, and restart.
- Report heartbeat to gateway.

Agent does not:

- Route client requests.
- Choose workers.
- Maintain global queues.
- Decide global model eviction.
- Kill model runtime processes directly except as part of restarting llama-swap service.

### llama-swap

llama-swap remains the local process manager on each worker.

The design uses existing llama-swap capabilities:

- `${PORT}` macro for per-model local ports.
- `cmd` and `cmdStop` for runtime startup and shutdown.
- `ttl` for local idle unload behavior.
- `concurrencyLimit` as local per-model safety limit.
- `hooks.on_startup.preload` for worker startup warm model.
- `GET /running` for local running model state.
- `POST /api/models/unload` to unload all local models.
- `POST /api/models/unload/{model}` to unload one local model.
- `GET /metrics` for Prometheus performance metrics when enabled.
- `GET /api/metrics` for llama-swap activity log JSON.
- `GET /api/performance` for system/GPU samples.
- `GET /api/events` for SSE event stream if later needed.

Gateway should unload models through llama-swap APIs instead of asking the agent to kill processes.

## Configuration Model

V1 uses YAML for gateway configuration.

### Model Config

```yaml
models:
  qwen3-32b-awq:
    priority: 100
    min_loaded: 2
    max_loaded: 4
    max_concurrency: 32
    max_queue: 128
    queue_timeout_ms: 30000
    ttl: 900
    artifact:
      object: qwen3-32b-awq.tar.gz
      kind: tar_gz
      crc64ecma: "3161812495027030000"
    run: >
      vllm serve {{model_path}}
      --host 127.0.0.1
      --port ${PORT}
      --served-model-name qwen3-32b-awq

  qwen2.5-7b-gguf:
    priority: 50
    min_loaded: 0
    max_loaded: 2
    max_concurrency: 16
    max_queue: 64
    queue_timeout_ms: 30000
    ttl: 900
    artifact:
      object: qwen2.5-7b-q4_k_m.gguf
      kind: file
      crc64ecma: "9876543210000000000"
    run: >
      llama-server
      -m {{model_path}}
      --host 127.0.0.1
      --port ${PORT}
```

Fields:

- `priority`: Higher values are protected first and can evict lower-priority models.
- `min_loaded`: Minimum number of loaded workers the gateway tries to preserve globally.
- `max_loaded`: Maximum number of loaded workers the model may occupy globally.
- `max_concurrency`: Global active request cap for this model.
- `max_queue`: Global queue cap for this model.
- `queue_timeout_ms`: Maximum time a request may wait in gateway queue.
- `ttl`: Rendered into llama-swap model config.
- `artifact.object`: OSS object name, not a full URL.
- `artifact.kind`: `file` or `tar_gz`.
- `artifact.crc64ecma`: OSS CRC64 ECMA value used for download integrity and update detection.
- `run`: Runtime command template rendered into llama-swap `cmd`.

No model `version` field is used. A model is uniquely identified by its model name. Artifact update detection uses `model_name + object + kind + crc64ecma`.

### OSS Config

```yaml
oss:
  base_url: https://llm-models.oss-cn-hangzhou.aliyuncs.com
```

Agent builds the download URL as:

```text
{oss.base_url}/{artifact.object}
```

V1 assumes all model artifacts are in one OSS base URL.

### Tag Policy

```yaml
tag_policies:
  gpu-4090:
    max_concurrency: 64
    max_queue: 256
    worker_defaults:
      max_concurrency: 8
      max_queue: 16
    allowed_models:
      - qwen3-32b-awq
      - qwen2.5-7b-gguf
    warm_when_idle: qwen3-32b-awq
```

Fields:

- `allowed_models`: Models this tag must download and may serve.
- `warm_when_idle`: One model to preload when the worker starts or when running state is empty.
- `max_concurrency`: Global active request cap across workers with this tag.
- `max_queue`: Global queue cap across workers with this tag.
- `worker_defaults.max_concurrency`: Default per-worker cap for workers with this tag.
- `worker_defaults.max_queue`: Default per-worker queue cap for workers with this tag.

`allowed_models` intentionally combines "download this model" and "this tag may serve this model". V1 does not keep separate `download` and `allowed` lists.

### Agent Local Config

```yaml
agent:
  id: gpu-01
  tags:
    - gpu-4090
  model_root: /data/models
  llama_swap_config: /etc/llama-swap/config.yaml
  llama_swap_service: llama-swap
  llama_swap_url: http://10.0.0.11:8080
  gateway_url: https://gateway.internal
  token: internal-shared-token
```

The agent ID is used for heartbeat identity only. Gateway model assignment is tag-scoped, not node-scoped.

## Agent Config API

The agent requests tag-scoped config:

```http
GET /internal/agent/config?tags=gpu-4090
Authorization: Bearer <internal-token>
```

Response:

```yaml
oss:
  base_url: https://llm-models.oss-cn-hangzhou.aliyuncs.com
models:
  qwen3-32b-awq:
    ttl: 900
    artifact:
      object: qwen3-32b-awq.tar.gz
      kind: tar_gz
      crc64ecma: "3161812495027030000"
    run: >
      vllm serve {{model_path}}
      --host 127.0.0.1
      --port ${PORT}
      --served-model-name qwen3-32b-awq
tag_policy:
  tag: gpu-4090
  allowed_models:
    - qwen3-32b-awq
  warm_when_idle: qwen3-32b-awq
  worker_defaults:
    max_concurrency: 8
    max_queue: 16
```

If multiple tags are sent, V1 requires exactly one workload tag match. Ambiguous matches return an error.

## Artifact Download And Markers

Agent stores models under its local `model_root`.

For `tar_gz`:

```text
/data/models/qwen3-32b-awq/
  .llm-agent-artifact.json
  ...
```

For `file`:

```text
/data/models/qwen2.5-7b-q4_k_m/
  qwen2.5-7b-q4_k_m.gguf
  .llm-agent-artifact.json
```

Marker file:

```json
{
  "model": "qwen3-32b-awq",
  "object": "qwen3-32b-awq.tar.gz",
  "kind": "tar_gz",
  "crc64ecma": "3161812495027030000",
  "installed_path": "/data/models/qwen3-32b-awq",
  "installed_at": "2026-06-21T02:00:00Z"
}
```

Download rules:

- If marker exists and `model + object + kind + crc64ecma` match, skip download.
- If marker is missing, download.
- If `object`, `kind`, or `crc64ecma` changes, download into a temporary path.
- Verify downloaded OSS object CRC64 ECMA.
- For `tar_gz`, extract into a temporary directory, then atomically replace the target directory.
- For `file`, write to a temporary file, then atomically replace the target file.
- Write marker only after successful verification and install.
- On failure, keep the old installed artifact if one exists and report the error in heartbeat.

`x-oss-hash-crc64ecma` is treated as an OSS object integrity value, not as a cryptographic security hash.

## Rendering llama-swap Config

Agent renders only allowed models for its tag.

Example output:

```yaml
startPort: 10001
globalTTL: 0
apiKeys:
  - "${env.LLAMA_SWAP_INTERNAL_TOKEN}"
performance:
  enable: true
  every: 5s
hooks:
  on_startup:
    preload:
      - qwen3-32b-awq
models:
  qwen3-32b-awq:
    cmd: >
      vllm serve /data/models/qwen3-32b-awq
      --host 127.0.0.1
      --port ${PORT}
      --served-model-name qwen3-32b-awq
    ttl: 900
    concurrencyLimit: 8
```

Important rules:

- Gateway run templates use `{{model_path}}` and `${PORT}`.
- Agent replaces `{{model_path}}`.
- llama-swap owns `${PORT}` expansion.
- Agent does not allocate per-model runtime ports.
- Agent compares the rendered file content with the existing config.
- If content is unchanged, it does not restart llama-swap.
- If content changed, it writes the file atomically and restarts llama-swap.
- `warm_when_idle` renders to `hooks.on_startup.preload`.

If runtime commands require a custom stop command, the gateway model config can include `cmd_stop`, and agent renders it to llama-swap `cmdStop`.

## Heartbeat

Agent posts heartbeat:

```http
POST /internal/agent/heartbeat
Authorization: Bearer <internal-token>
Content-Type: application/json
```

Payload:

```json
{
  "agent_id": "gpu-01",
  "tags": ["gpu-4090"],
  "llama_swap_url": "http://10.0.0.11:8080",
  "running_models": [
    {"model": "qwen3-32b-awq", "state": "ready"}
  ],
  "artifacts": {
    "qwen3-32b-awq": "ready",
    "qwen2.5-7b-gguf": "ready"
  },
  "capacity": {
    "max_concurrency": 8,
    "max_queue": 16
  },
  "last_error": null
}
```

Agent may derive `running_models` from llama-swap `GET /running`.

Gateway marks a worker unhealthy when heartbeat is stale or when repeated llama-swap health/metrics pulls fail.

## Request Lifecycle

For every client request:

```text
1. Gateway receives request.
2. Gateway creates request_id.
3. Gateway extracts target model.
4. Gateway checks model exists and is allowed by at least one healthy tag.
5. Gateway checks model, tag, and worker concurrency and queue limits.
6. Gateway selects worker.
7. Gateway records in_flight[request_id].
8. Gateway increments model/tag/worker active counters.
9. Gateway forwards request to selected worker llama-swap.
10. Gateway streams response to client.
11. On success, error, timeout, or client disconnect, gateway releases counters once.
12. Gateway records request metrics and logs.
13. Gateway wakes queued requests.
```

For streaming/SSE responses, release occurs only when the stream ends, the upstream errors, the client disconnects, or the gateway timeout/cancel path runs.

In-memory state for V1:

```text
model_active[model]
tag_active[tag]
worker_active[worker_id]
in_flight[request_id]
model_queue[model]
tag_queue[tag]
worker_queue[worker_id]
```

If the gateway later runs multiple replicas, this state moves to Redis with expiring in-flight records.

## Concurrency And Queueing

Gateway is the authoritative concurrency and queue control point.

Limits checked:

- Model global active requests.
- Tag global active requests.
- Worker active requests.
- Model queue length.
- Tag queue length.
- Worker queue length.

llama-swap `concurrencyLimit` is still rendered as a local safety net. It protects workers from gateway bugs, gateway restart drift, accidental direct traffic, or future multi-gateway race conditions.

If any hard cap is exceeded and queue capacity remains, the request queues. If queue capacity is full, gateway returns an OpenAI-compatible 429 error.

Errors:

- `404 model_not_available`: model is unknown or no tag allows it.
- `503 no_healthy_worker`: no healthy worker can serve the model.
- `429 queue_full`: queue cap reached.
- `429 queue_timeout`: request waited longer than `queue_timeout_ms`.
- `503 model_start_failed`: selected worker failed to load model.
- `503 worker_unavailable`: selected worker disappeared during dispatch.

## Scheduling

Worker candidate filtering:

```text
1. Worker heartbeat is fresh.
2. Worker has a tag whose policy allows the model.
3. Artifact state for the model is ready.
4. Worker is not draining, restarting, or in repeated error backoff.
5. Concurrency and queue limits allow the request.
```

Scoring:

```text
score =
  loaded_bonus
  + model_priority
  + cache_affinity_bonus
  - active_requests_penalty
  - queue_depth_penalty
  - cold_start_penalty
  - switching_penalty
  - recent_error_penalty
```

V1 should make `loaded_bonus` large so already-loaded workers are preferred.

## Loaded Replica Policy

`min_loaded` and `max_loaded` are global per-model policies.

Gateway reconciler loop:

```text
for model by priority desc:
  loaded = count healthy workers where /running contains model ready/loading
  if loaded < min_loaded:
    choose idle or low-value worker
    warm or route a load-triggering request
  if loaded > max_loaded:
    unload extra idle replicas, preferring low-traffic workers
```

V1 must not unload a worker/model if gateway has active requests assigned to that worker/model.

If all `min_loaded` values cannot be satisfied by available workers, gateway marks lower-priority models as underprovisioned and preserves higher-priority models first.

## Eviction And Unload

Eviction is decided only by gateway.

A model on a worker is eligible for gateway-initiated unload when:

```text
worker_active[worker_id] == 0
model_loaded_count[victim_model] > victim_model.min_loaded
target_model.priority > victim_model.priority
worker is healthy
worker is not downloading or restarting
```

Gateway unload call:

```http
POST {worker.llama_swap_url}/api/models/unload/{model}
Authorization: Bearer <llama-swap-internal-token>
```

Gateway then refreshes worker state via heartbeat or `GET /running`.

Gateway can also unload all models on a worker during maintenance:

```http
POST {worker.llama_swap_url}/api/models/unload
```

The agent does not implement model eviction.

## Loading And Warm Models

llama-swap starts a model when it receives a request for that model. It also supports startup preload through:

```yaml
hooks:
  on_startup:
    preload:
      - qwen3-32b-awq
```

V1 uses `warm_when_idle` to render startup preload.

Runtime active warming after llama-swap is already running is optional in V1. If needed, gateway can later add a controlled warm operation that sends a cheap request through llama-swap, but it must avoid expensive generation and must not affect user metrics.

## Metrics

Gateway exposes the merged Prometheus endpoint:

```text
GET /metrics
```

Gateway metrics:

- Requests by model, tag, worker, status.
- Active requests by model, tag, worker.
- Queue depth by model, tag, worker.
- Queue wait time.
- End-to-end latency.
- Time to first token when measurable.
- Streaming duration.
- Input tokens, output tokens, total tokens when available.
- RPM and TPM by model.
- Scheduler decisions.
- Dispatch failures.
- Queue full and queue timeout counts.
- Worker health.
- Loaded replica counts.
- Artifact readiness.
- Unload attempts and failures.
- Cold-start or load latency when measurable.

Worker metrics collection:

- Gateway pulls `GET {llama_swap_url}/metrics`.
- Gateway pulls `GET {llama_swap_url}/api/metrics`.
- Gateway pulls `GET {llama_swap_url}/api/performance`.
- Gateway uses heartbeat for artifact, running model, and worker capacity state.

Prometheus should scrape gateway only in V1. Direct worker scraping is not required.

## Logs

Gateway request log fields:

- `request_id`
- `model`
- `worker_id`
- `worker_tags`
- `queue_ms`
- `status_code`
- `error_code`
- `stream`
- `latency_ms`
- `input_tokens`
- `output_tokens`
- `total_tokens`
- `client_id` when authentication identifies one

Scheduler log fields:

- `request_id`
- `model`
- `candidate_workers`
- `selected_worker`
- `loaded_match`
- `reason`
- `score`

Agent event fields:

- `agent_id`
- `tags`
- `event`
- `model`
- `artifact`
- `error`

Agent does not upload full runtime logs in V1. Full runtime logs remain on the worker or are handled by an external log stack later.

## Authentication

V1 uses simple bearer tokens:

- Client token for public inference APIs.
- Internal agent token for `/internal/agent/*`.
- Internal llama-swap token for gateway calls into worker llama-swap.

Worker llama-swap should not be exposed to external clients. Gateway is the only supported inference entry point.

Gateway forwards `X-Request-Id` to workers.

Gateway may also forward:

- `X-Gateway-Model`
- `X-Gateway-Worker`

## Cache-Affinity Reserved Interface

V1 reserves a cache-affinity score but does not require runtime KV-cache introspection.

Initial behavior:

- If request includes `conversation_id`, `session_id`, or another configured cache key, gateway records `cache_key -> worker_id/model`.
- Future requests with the same key prefer that worker if it is healthy and still serving the model.
- If the worker is unhealthy, at capacity, or has unloaded the model, gateway falls back to normal scheduling.

Later versions can incorporate runtime-specific prefix/KV cache hit metrics.

## Validation

Gateway configuration validation:

- Every tag `allowed_models` entry must exist in `models`.
- Every `warm_when_idle` must exist in the tag `allowed_models`.
- Every model must have an artifact.
- Artifact kind must be `file` or `tar_gz`.
- Every model must have a non-empty `run` command.
- `min_loaded` must be less than or equal to `max_loaded` when `max_loaded` is greater than zero.
- Concurrency and queue limits must be non-negative.
- If configured `min_loaded` demand exceeds known workers, gateway starts but marks lower-priority models underprovisioned.

Agent validation:

- Exactly one workload tag must match gateway response in V1.
- `model_root` must be writable.
- `llama_swap_config` directory must be writable.
- Required local commands must exist for enabled artifact kinds.
- `tar` must support `tar.gz` extraction.

## Testing Strategy

Unit tests:

- Config validation.
- Tag policy matching.
- Artifact marker comparison.
- llama-swap config rendering.
- Scheduler candidate filtering.
- Scheduler scoring.
- Concurrency counter release paths.
- Queue full and timeout behavior.
- Streaming release behavior.

Integration tests:

- Fake worker heartbeat registration.
- Fake llama-swap `/running` state sync.
- Gateway dispatch to fake streaming upstream.
- Client disconnect releases worker active counter.
- Gateway unload call only happens when worker active count is zero.
- Agent downloads fake `file` artifact and writes marker.
- Agent extracts fake `tar_gz` artifact and writes marker.
- Agent rerender does not restart llama-swap when output is unchanged.

Manual smoke tests:

- Start one gateway and two fake workers.
- Send OpenAI chat request for a loaded model.
- Send streaming chat request and verify active counter stays until stream closes.
- Exhaust model queue and confirm 429.
- Force lower-priority idle model unload through gateway decision.
- Change artifact CRC and confirm agent redownloads.

## Implementation Choice

Both gateway and agent are implemented in Go.

Reasons:

- llama-swap is Go.
- Go has strong standard-library support for reverse proxying and streaming HTTP.
- Prometheus, Redis, YAML, and system service integrations are straightforward.
- Agent can ship as a single binary.
- The protocol remains simple enough that a shell script can be used as an emergency compatible agent, but the official implementation is Go.

## Phasing

### Phase 1

- Gateway config loader and validation.
- Agent config API and heartbeat API.
- Go agent artifact download, marker, config render, llama-swap restart.
- Worker registry.
- Single-gateway in-memory scheduler.
- OpenAI-compatible request proxy with streaming support.
- Request lifecycle accounting and release.
- Basic metrics and logs.
- Gateway-driven unload via llama-swap API.

### Phase 2

- More complete metrics aggregation from llama-swap activity and performance APIs.
- Cache-affinity routing based on conversation/session key.
- Runtime warming operation if needed.
- Better queue observability.
- Worker maintenance/drain mode.

### Phase 3

- Redis-backed in-flight counters and queues for multiple gateway replicas.
- Stronger artifact integrity through signed manifests or SHA-256.
- External log stack integration.
- Runtime-specific KV-cache and prefix-cache metrics.
