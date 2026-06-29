# LLM Swap Project Map

Last updated: 2026-06-24.

This document is the current high-level map for future agents. It reflects the
code state after the gateway UI, token unification, worker event persistence,
request accounting, scheduling, install script, vLLM/SGLang wrappers, and
llama.cpp runtime wrapper work.

## System Shape

The system is a Go control plane around worker-local llama-swap instances.

```text
client
  -> gateway /v1/chat/completions
    -> placement chooses a worker by model, tag, artifact readiness, running
       model state, active request count, and replica policy
    -> gateway proxies request to worker llama-swap
      -> llama-swap starts/switches local runtime command from rendered config
        -> vLLM, SGLang, or llama.cpp runtime wrapper

worker agent
  -> polls gateway config
  -> downloads model artifacts
  -> renders llama-swap config
  -> reads local llama-swap /running from 127.0.0.1:swap_port
  -> heartbeats worker state, artifacts, running models, and events to gateway
```

Gateway state is in-process, with append-only JSONL files for request
accounting and worker events. Historical metrics storage is optional:
VictoriaMetrics can be attached through vmagent scraping `/metrics`; when it is
disabled the gateway still runs with no external database.

## Domain Vocabulary

- `gateway`: central HTTP service and control-plane owner.
- `worker`: a machine with a local llama-swap process and one agent process.
- `agent`: thin worker-side controller; it installs artifacts and reports state.
- `llama-swap`: worker-local runtime switcher and proxy target.
- `model`: logical public model name in gateway config.
- `artifact`: downloadable model payload. Supported kinds are `file` and
  `tar_gz`.
- `tag_policy`: gateway policy for workers with a tag. It defines installable
  models, warm model, worker defaults, and tag-level concurrency.
- `running_model`: llama-swap reported model state, usually `loading` or
  `ready`.
- `min_loaded`: target floor for replicas. Ready plus starting/loading replicas
  count toward the floor; the async control loop tries to satisfy it when
  capacity allows.
- `max_loaded`: optional hard ceiling. When omitted, Placement treats the
  ceiling as automatic and bounded by eligible workers, other models'
  `min_loaded`, and priority protection.
- `min_loaded=0`: opportunity-cache model. It is not proactively protected, but
  loaded replicas can remain while capacity is spare and are preferred eviction
  candidates when another model needs capacity.
- `warm_when_idle`: legacy tag policy hint retained in config responses.
  Agent no longer renders it as a worker-local llama-swap startup hook because
  model warm/load decisions must stay gateway-owned.

## Gateway Modules

- `cmd/gateway/main.go`
  - Loads runtime config through `config.LoadGatewayRuntime`.
  - Creates `gateway.NewServerWithGatewayPersistencePaths`.
  - Starts the loaded-replica reconciler every 30 seconds.

- `internal/gateway/server.go`
  - Wires HTTP routes.
  - Agent config and heartbeat endpoints use the agent token.
  - Client model and chat endpoints use the client token.
  - UI routes use the agent token.
  - Heartbeat events are cached and persisted to worker event JSONL.

- `internal/gateway/proxy.go`
  - OpenAI-compatible chat proxy path.
  - Extracts the requested model, normalizes some SGLang request fields, applies
    queue/concurrency gates, schedules a worker, proxies to llama-swap, records
    request stats, records pressure observations, and emits metrics/logs.
  - Retryable proxy failures mark only the failing replica as cooled down for
    30 seconds, then retry another ready replica when available.
  - `top_k: 0` is normalized to `-1` for SGLang-backed models.
  - Transformers-style `image`, `video`, and `audio` content parts are converted
    to OpenAI-style URL objects for SGLang compatibility.

- `internal/gateway/placement.go`
  - Owns request placement and async control-action planning.
  - Request placement only returns ready workers that can handle the current request.
  - Starting/loading runtimes count as occupied but are not routable, and empty workers are warmed only by the async reconcile loop.
  - Active replica cooldowns exclude only the affected `worker_id + model`
    ready replica from request routing.
  - Omitted `max_loaded` is treated as an automatic ceiling bounded by eligible
    workers and protected model floors.
  - `min_loaded=0` models behave as opportunity cache: they can remain loaded
    while capacity is spare, and are preferred eviction candidates when another
    model needs capacity.
  - Plans gateway-owned `min_loaded` warm actions on empty eligible workers
    before evicting another model for capacity.
  - Plans conservative predictive warm actions when sustained demand beats the
    current replica value plus switch cost.

- `internal/gateway/pressure.go`
  - Tracks rolling in-memory model pressure from request and queue observations.
  - Computes conservative demand scores used by Placement warm scale-out.
  - Rolling queue pressure is not persisted and starts empty after gateway
    restart.

- `internal/gateway/replica_cooldown.go`
  - Tracks short-lived gateway-local cooldowns for retryable proxy failures on
    a specific `worker_id + model` replica.
  - Cooldown affects request routing only. It does not change worker heartbeat
    health and does not trigger unloads by itself.

- `internal/gateway/scheduler.go`
  - Compatibility adapter over Placement.
  - Keeps the older `Pick` and `PickDecision` interface for callers while
    placement logic lives in `placement.go`.

- `internal/gateway/limits.go`
  - Keyed in-memory queue/concurrency limiter.
  - Used for model, tag, and worker gates.
  - `AcquireWithStats` reports admitted, admitted-after-wait, queue-full, and
    queue-timeout outcomes with wait time and active/queued depth at admission.

- `internal/gateway/workers.go`
  - In-memory worker registry.
  - Tracks heartbeat state, active gateway-owned requests, drain state,
    scrape backoff, artifacts, and running models.

- `internal/gateway/reconcile.go`
  - Loaded-replica reconciler.
  - Unloads excess idle replicas over explicit hard `max_loaded`.
  - Executes Placement control actions to warm models below `min_loaded` on
    empty eligible workers or free capacity when no empty worker is available.
  - Executes at most one predictive warm action per cycle after hard ceiling and
    min_loaded capacity actions.
  - Records gateway-initiated unload/warm success/failure as worker events.

- `internal/gateway/request_log.go` and `request_log_parse.go`
  - Append and parse gateway request JSONL.
  - Request log captures status, latency, bytes, media counts, max_tokens,
    temperature/top_p/top_k, usage tokens, cache tokens, reasoning tokens, and
    retry count.

- `internal/gateway/access.go`
  - Replays request logs into access accounting.
  - Used by UI traffic summaries and scheduling/unload decisions.

- `internal/gateway/worker_event_log.go`
  - Append and page worker event JSONL.
  - UI reads recent events from this persistent log when enabled.

- `internal/gateway/metrics.go` and `metrics_scrape.go`
  - Prometheus metrics for gateway, worker, model, queue, request, activity, and
    llama-swap performance data.
  - Scrapes worker llama-swap with the llama-swap token.
  - Low-cardinality counters include gateway model tokens, model active
    requests, queue observations, proxy retries, replica cooldowns, and control
    actions.

- `internal/gateway/metrics_store.go`
  - Optional VictoriaMetrics query client for historical UI reads.
  - Uses `/prometheus/api/v1/query_range`.
  - Range and step are clamped by `metrics_store.default_range` and
    `metrics_store.max_range`.

- `internal/gateway/ui.go` and `internal/gateway/ui_assets.go`
  - Admin dashboard at `/ui`.
  - Vite/React build output is embedded from `internal/gateway/admin_dist`.
    When only the placeholder asset is present, the gateway falls back to the
    older inline dashboard so Go tests and development builds still work before
    running the frontend build.
  - Shows model availability, traffic, workers, health, running models,
    artifacts, and recent worker events.
  - Recent events have columns: Received, Worker, Event, Model, Detail.
  - Optional historical metrics endpoints:
    `/ui/metrics/summary`, `/ui/metrics/model`, and `/ui/metrics/worker`.
    These use the agent token like the rest of the UI and return 503 when the
    metrics store is disabled.

- `internal/gateway/config_manager.go` and `config_admin.go`
  - Own the gateway config snapshot used by gateway handlers.
  - Config snapshots are versioned and normalized with the same important
    defaults as startup config loading.
  - Admin config routes under `/ui/api/config` support reading the current
    config, validation/dry-run, and apply.
  - Apply validates the submitted YAML and writes it to the configured
    `gateway.yaml` path when available. If the change is hot-applicable, it then
    atomically replaces the in-memory gateway config. If the dry-run contains a
    process-level `requires_gateway_restart` change, apply only persists the
    file and leaves the running config snapshot untouched.
  - The config editor reads the original YAML from disk when available instead
    of marshaling the runtime struct. This preserves omitted fields such as
    `max_loaded`, where omission has distinct automatic-expansion semantics.
  - Dry-run/apply responses include `apply_mode`: `hot_apply` for changes that
    take effect immediately, or `save_requires_gateway_restart` when the YAML
    was persisted but the running snapshot was intentionally left unchanged.
  - The admin UI now treats config as a structured operations console by
    default: `Config Ops` edits models and tag policies, while `Advanced` is a
    read-only YAML viewer for full config inspection and copy/paste.
  - Dry-run returns coarse impact changes plus loaded-worker impacts. Model
    policy changes are hot-update candidates. Runtime command/artifact changes
    only require worker restart/reload when the affected model is currently
    loaded on a worker; the response lists those model/worker pairs in
    `impacts`. Process-level fields such as listen address and tokens are marked
    as requiring gateway restart. `gateway.proxy_attempts` can hot-apply from
    YAML unless it was overridden by gateway env/CLI at process startup; in that
    case UI apply persists the YAML but keeps the running override until restart.
  - Admin action routes support worker drain/undrain, model warm/unload, and
    replica cooldown clear. These actions stay gateway-owned, use the existing
    llama-swap client for runtime actions, and record gateway worker events so
    the UI/event log shows operator interventions.

## Agent Modules

- `cmd/agent/main.go`
  - Loads runtime config through `config.LoadAgentRuntime`.
  - Uses the advertised public swap URL for gateway heartbeat.
  - Uses local `127.0.0.1:swap_port` for local llama-swap `/running` and health.

- `internal/agent/reconcile.go`
  - Main worker reconcile loop.
  - Fetches tag-scoped config from gateway.
  - Installs allowed artifacts, one active install at a time.
  - Fetches local llama-swap running models.
  - Renders llama-swap config only after all allowed artifacts are ready.
  - Marks pending restart only when a config change affects currently loaded
    models.
  - Heartbeats artifacts, running models, needs_restart, last_error, and events.
  - Records local lifecycle events: artifact install/download events,
    `llama_swap_config_changed`, restart events, `model_loaded`,
    `model_state_changed`, and `model_unloaded`.
  - Running model diff events are only emitted after a successful `/running`
    fetch; failed fetches do not imply unload.

- `internal/agent/artifacts.go`
  - Downloads artifacts from `oss.base_url`.
  - Verifies CRC64 ECMA and writes marker files.
  - Emits progress callbacks for download progress.
  - Uses shared `flock` locks under `<model_root>/.locks` so workers sharing a
    model root do not download or install the same artifact concurrently.
  - Reuses a matching source artifact already present at
    `<model_root>/<basename(artifact.object)>` before downloading.
  - Persists downloaded source artifacts at
    `<model_root>/<basename(artifact.object)>`; model directories still get
    their own installed files and `.llm-agent-artifact.json` marker.

- `internal/agent/render.go`
  - Renders local llama-swap config.
  - `{{model_path}}` expands to `<model_root>/<model_name>`.
  - Writes `apiKeys` when a llama-swap token is configured.
  - Wraps each model command with shell logging to
    `/opt/llmswap/logs/model-runtime.log`.
  - `check_endpoint` maps to llama-swap `checkEndpoint`.
  - `cmd_stop` maps to llama-swap `cmdStop`; normal model stopping should still
    rely on llama-swap unless custom cleanup is needed.
  - Does not render tag `warm_when_idle` into llama-swap startup hooks.

- `internal/agent/llamaswap_client.go`
  - Reads local llama-swap health and running models.
  - The agent intentionally calls local `127.0.0.1:swap_port`, not the public
    advertised `swap_url`.

- `internal/agent/service.go`
  - Restart implementations: shell command, systemd service, logging fallback.
  - Production worker install currently writes supervisor restart command.

## Config Rules

Gateway config:

- `tokens.client` is for client-facing OpenAI-compatible routes.
- `tokens.agent` is for internal agent routes and the UI.
- `tokens.llama_swap` is optional. If omitted, it defaults to `tokens.agent`.
- `models.<name>.run` is the command rendered into llama-swap config.
- `models.<name>.runtime` can be used instead of `run` for standard wrappers:
  `vllm`, `sglang`, or `llamacpp`. The agent generates `PORT=${PORT}`,
  model path, served model name/alias, and appends `runtime_args`.
- `runtime_args` accepts either raw argv entries (`["--dtype", "half"]`) or
  compact shell-like entries (`["--dtype half"]`). Prefer one logical argument
  pair per YAML item for readability; quote JSON values inside the string.
- `run` remains the escape hatch and takes precedence when both `run` and
  `runtime` are set.
- `models.<name>.check_endpoint` should be set for runtimes whose health route
  is not `/health`, for example SGLang `/model_info`.
- `runtime: sglang` defaults `check_endpoint` to `/model_info` unless explicitly
  overridden.
- `models.<name>.max_loaded` omitted means automatic expansion bounded by
  eligible workers, protected `min_loaded` floors, and priority policy. Set it
  explicitly to impose a hard ceiling.
- `max_queue` omitted means no queueing for that gate. Existing limiter semantics
  should be checked before changing this behavior.
- Tag policies are the only source of which workers can install/run which
  models.

Agent config:

- `agent.token` is the gateway agent token.
- `agent.llama_swap_token` is optional. If omitted, it defaults to
  `agent.token`.
- `swap_url` is the public URL advertised to gateway. If omitted, runtime config
  tries Tailscale IPv4 first, then local IPv4, using `swap_port`.
- The agent uses `swap_port` to access local llama-swap.

## Runtime Layout

Default root is `/opt/llmswap`.

```text
/opt/llmswap/
  agent.yaml
  gateway.yaml
  llama-swap.yaml
  bin/
    llm-swap-agent
    llm-swap-gateway
    vllm.server
    vllm-python
    sglang.server
    sglang-python
    llamacpp.server
    llama-server
  models/
  venvs/
    vllm/
    sglang/
  runtimes/
    llamacpp/<cuda-arch>/
  logs/
    gateway-requests.jsonl
    gateway-worker-events.jsonl
    model-runtime.log
    agent.out.log
    agent.err.log
    llama-swap.out.log
    llama-swap.err.log
```

## Worker Install Script

`scripts/install-worker.sh` is the worker bootstrap script.

It can:

- create `/opt/llmswap` directories;
- install base apt packages, uv, optional Tailscale, and supervisor config;
- run a single stage with `--only base|runtime|agent|supervisor|tailscale`
  without replaying the full bootstrap;
- when a Tailscale auth key is provided, write a supervisor-managed
  `llmswap-tailscaled` program before running `tailscale up`;
- create uv-managed Python venvs for vLLM and SGLang using Python 3.12 by
  default;
- install torch for vLLM with CUDA-aware PyTorch index selection;
- install vLLM with the `audio` extra by default (`vllm[audio]`) so PyAV and
  other audio parser dependencies are available;
- install SGLang and patch MiniCPMV4.6 config compatibility;
- install prebuilt llama.cpp CUDA runtime archives from OSS;
- write wrappers into `/opt/llmswap/bin`;
- initialize agent config without overwriting an existing one unless
  `--force-config` is passed.

Important env vars:

- `LLMSWAP_ROOT`
- `LLMSWAP_ONLY`
- `LLMSWAP_RUNTIME`
- `LLMSWAP_CUDA_VERSION`
- `LLMSWAP_AGENT_ID`
- `LLMSWAP_AGENT_TAGS`
- `LLMSWAP_GATEWAY_URL`
- `LLMSWAP_AGENT_TOKEN`
- `LLMSWAP_LLAMA_SWAP_TOKEN`
- `LLMSWAP_SWAP_PORT`
- `LLMSWAP_UV_CACHE_DIR`
- `LLMSWAP_UV_PYTHON_INSTALL_DIR`
- `LLMSWAP_UV_PYTHON_INSTALL_MIRROR`

## Agent Container Image

`Dockerfile.agent` builds a worker image that preinstalls the same base
dependencies, uv-managed Python environments, runtime wrappers, supervisor
configuration, and `llm-swap-agent` binary that `scripts/install-worker.sh`
would normally install on a host.

Important properties:

- Default base image is `nvidia/cuda:12.8.1-cudnn-runtime-ubuntu22.04`.
- The build runs `install-worker.sh` inside the image, so runtime installation
  logic stays in one place.
- The image preinstalls `vllm`, `sglang`, or `llamacpp` based on
  `--build-arg LLMSWAP_RUNTIME=...`.
- The image installs the Tailscale binaries by default, but does not run
  `tailscale up` at build time.
- The image removes the placeholder `agent.yaml` after build. At container
  start, `scripts/agent-container-entrypoint.sh` writes `/opt/llmswap/agent.yaml`
  from env only when the file is absent or `LLMSWAP_FORCE_CONFIG=1`.
- `llama-swap` is not built from this repository.
- If `--build-arg LLAMA_SWAP_DOWNLOAD_URL=...` is provided, the image stores a
  bundled `/opt/llmswap/bin/llama-swap.bundled`.
- If the base image already contains `/opt/llmswap/bin/llama-swap`,
  `Dockerfile.agent` preserves it as the bundled binary.
- If neither source exists, the image build still succeeds, but the container
  must receive either a runtime override URL or a mounted llama-swap binary
  before it can start successfully.
- On container start, `scripts/agent-container-entrypoint.sh` restores
  `/opt/llmswap/bin/llama-swap` from the bundled binary by default.
- If runtime env `LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL` or `LLAMA_SWAP_DOWNLOAD_URL`
  is set, entrypoint downloads that binary and replaces the active
  `/opt/llmswap/bin/llama-swap` before starting supervisor.

Typical build:

```bash
docker build \
  -f Dockerfile.agent \
  --build-arg BASE_IMAGE=nvidia/cuda:12.8.1-cudnn-runtime-ubuntu22.04 \
  --build-arg LLMSWAP_CUDA_VERSION=12.8 \
  --build-arg LLMSWAP_RUNTIME=all \
  --build-arg LLAMA_SWAP_DOWNLOAD_URL=https://example.invalid/llama-swap-linux-amd64 \
  --build-arg UV_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
  --build-arg PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
  -t llmswap-agent:cu128 .
```

When the build machine cannot reliably access `pypi.org`, pass package index
mirror args such as:

- `UV_INDEX_URL`
- `UV_EXTRA_INDEX_URL`
- `PIP_INDEX_URL`
- `PIP_EXTRA_INDEX_URL`
- `LLMSWAP_UV_PYTHON_INSTALL_MIRROR`

Typical runtime env when no config file is mounted:

- `LLMSWAP_AGENT_ID`
- `LLMSWAP_AGENT_TAGS`
- `LLMSWAP_GATEWAY_URL`
- `LLMSWAP_AGENT_TOKEN`
- `LLMSWAP_LLAMA_SWAP_TOKEN` (optional; defaults to agent token)
- `LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL` (optional runtime override for the active
  llama-swap binary; when omitted, the bundled binary is used)
- `LLMSWAP_SWAP_PORT`
- `LLMSWAP_SWAP_URL` or `SWAP_URL` (optional explicit public worker URL)
- `LLMSWAP_FORCE_CONFIG=1` when the container should rewrite `agent.yaml`
- `LLMSWAP_ENABLE_TAILSCALE=1` and `LLMSWAP_TAILSCALE_AUTHKEY` only when
  running Tailscale in the same container

Default container startup path:

- verifies `/opt/llmswap/bin/llm-swap-agent`;
- verifies `/opt/llmswap/bin/llama-swap`;
- optionally writes `/opt/llmswap/agent.yaml`;
- optionally starts `tailscaled` and runs `tailscale up`;
- starts `supervisord` in the foreground, which manages `llama-swap` and
  `llm-swap-agent`.

## Runtime Wrappers

- `vllm.server MODEL_PATH [args...]`
  - Runs `vllm serve "$MODEL_PATH" --host "$HOST" --port "$PORT"`.
  - Default `HOST=0.0.0.0`, `PORT=8000`.

- `sglang.server MODEL_PATH [args...]`
  - Runs `python -m sglang.launch_server --model-path "$MODEL_PATH" --host
    "$HOST" --port "$PORT"`.
  - Default `HOST=0.0.0.0`, `PORT=30000`.

- `llamacpp.server [MODEL_PATH] [args...]`
  - Wraps `llama-server`, sets `LD_LIBRARY_PATH` to the packaged llama.cpp bin
    and lib dirs plus common CUDA/NCCL library dirs, maps a leading positional
    model path to `-m`, and applies default host and port if not already
    supplied.
  - llama.cpp only supports GGUF models. Do not route HF/AWQ directories through
    llama.cpp.

## Logging and UI

Gateway structured stdout logs include scheduler decisions, requests, queue
events, agent events, and log write errors.

Scheduler decision logs include the selected reason, ready replica count,
occupied replica count, effective `max_loaded`, and a compact candidate list.
The important reasons are:

- `ready_idle`: selected an already-ready model with no active gateway request.
- `ready_busy`: selected a ready model because the loaded ceiling is satisfied.
- `ready_busy_scale_out`: selected a ready model while scale-out may be useful;
  the current request still routes to ready.
- `same_model_loading`: legacy reason name kept in code for compatibility;
  non-ready same-model runtimes are not routable candidates.
- `empty_scale_out` and `switch_scale_out`: only possible when there is no ready
  same-model replica for the current request path.

Queue observation logs use `event=queue_observation`. They are emitted for
configured model, tag, and worker gates and include:

- `result`: `admitted`, `admitted_after_wait`, `queue_full`, or
  `queue_timeout`.
- `wait_ms`, `active`, `queued`, `max_concurrency`, and `max_queue`.
- `ready_replicas`, `occupied_replicas`, and effective `max_loaded`.

Client-facing queue errors currently use OpenAI error code `queue_full` for
both full and timeout cases. Internal logs and metrics still distinguish
`queue_full` from `queue_timeout`. Conservative warm scale-out uses rolling
request and queue pressure, including `admitted_after_wait`, `queue_full`,
`queue_timeout`, p95 `wait_ms`, p95 request duration, ready replicas, occupied
replicas, active depth, and model priority. It avoids expanding from a single
burst.

Control action logs use:

- `control_action_planned`
- `control_action_done`
- `control_action_error`

Warm action log fields include `action`, `model`, `worker_id`, `reason`,
`demand_score`, `keep_score`, `switch_cost`, and `victim_model`.

Persistent gateway files:

- `/opt/llmswap/logs/gateway-requests.jsonl`
- `/opt/llmswap/logs/gateway-worker-events.jsonl`

Worker-side model runtime logs:

- `/opt/llmswap/logs/model-runtime.log`

UI routes:

- `/ui`
- `/ui/status`
- `/ui/events?limit=50&offset=0`
- `/ui/api/config`
- `/ui/api/config/validate`
- `/ui/api/config/dry-run`
- `/ui/api/config/apply`
- `/ui/api/workers/{id}/drain`
- `/ui/api/workers/{id}/undrain`
- `/ui/api/models/{model}/warm`
- `/ui/api/models/{model}/unload`
- `/ui/api/cooldowns/clear`
- `/ui/metrics/summary?range=1h&step=1m`
- `/ui/metrics/model?model=<name>&range=1h&step=1m`
- `/ui/metrics/worker?worker_id=<id>&range=1h&step=1m`

UI authentication uses the agent token. `/ui?token=<agent-token>` sets an
HTTP-only cookie scoped to `/ui`.

## Historical Metrics Store

VictoriaMetrics is optional and is disabled by default in `gateway.yaml`.

Gateway config:

```yaml
metrics_store:
  enabled: true
  type: victoriametrics
  query_url: http://victoriametrics:8428
  default_range: 1h
  max_range: 7d
  timeout_ms: 3000
```

Deployment helpers:

- `deploy/docker-compose.metrics.yml`
- `deploy/vmagent/promscrape.yml`
- `Dockerfile.gateway`
- `deploy/production/compose.yaml`
- `deploy/production/vmagent/promscrape.yml`

vmagent scrapes gateway `/metrics` and remote-writes to VictoriaMetrics. The
default scrape target is `gateway:8080`; adjust it when gateway runs outside the
compose network. Request and worker event JSONL files remain the source for
request detail replay and recent event pages; VictoriaMetrics is for aggregate
time-series history only.

Production compose deployment runs gateway, VictoriaMetrics, and vmagent
together. The gateway container mounts `/opt/llmswap/gateway.yaml` read-write so
admin config apply can persist changes, and mounts `/opt/llmswap/logs`
read-write. VictoriaMetrics stores data under
`/opt/llmswap/data/victoriametrics`. Start it from the repository root with:

```bash
docker compose -f deploy/production/compose.yaml up -d --build
```

The gateway Dockerfile builds `ui/admin` with Node/Vite before compiling the Go
binary, then copies the generated `internal/gateway/admin_dist` into the Go
build context so the admin UI is embedded in the final binary.

## Placement Rollout Notes

- Requests route only to ready workers for the requested model.
- Starting/loading workers are visible as occupied replicas but do not receive
  current requests.
- Retryable proxy failures mark only the failing `worker_id + model` replica as
  cooled down for 30 seconds. Requests skip cooled-down ready replicas, while
  reconciliation remains gateway-owned and policy-driven.
- The gateway proactively warms `min_loaded` floors on empty eligible workers;
  worker-local startup hooks are not used for this.
- Omitted `max_loaded` now means automatic expansion rather than `min_loaded`.
  Use explicit `max_loaded` to cap expensive models.
- `min_loaded=0` models behave as opportunity cache and can remain loaded until
  capacity is needed elsewhere.

## Known Compatibility Notes

- SGLang-backed models may reject `top_k: 0`; gateway normalizes it to `-1`.
- Some SGLang multimodal models expect OpenAI-style `image_url`, `video_url`,
  or `audio_url`; gateway normalizes transformers-style parts.
- SGLang MiniCPMV4.6 config compatibility is patched in the installed venv by
  `scripts/install-worker.sh`.
- vLLM and SGLang compatibility for specific VL/AWQ models can depend on
  upstream transformers, torch, torchcodec, ffmpeg, and CUDA shared libraries.
- MiniCPM-o audio AWQ models such as `MiniCPM-PawSense-Audio` are not fully
  supported by SGLang 0.5.13 OpenAI serving in this project. Worker2 testing
  showed these blockers:
  - system `ffmpeg`/`libavdevice.so.58` and Python `librosa` are required for
    the model processor path;
  - SGLang native MiniCPMO initializes vision even with `init_vision=false` and
    is incompatible with the model text backbone weights;
  - `--model-impl transformers` can load after excluding fp16 modules from AWQ
    (`lm_head`, `apm`, `audio_projection_layer`) and ignoring disabled vision
    weights, but generation still fails because `MiniCPMO.forward()` requires a
    remote-code `data` argument that SGLang's generic OpenAI path does not pass.
  Use the model README's `AutoAWQForCausalLM.from_quantized(...)` flow or a
  custom runtime server unless upstream SGLang adds native support.
- llama.cpp CUDA runtime archives require matching CUDA runtime libraries in
  `LD_LIBRARY_PATH`; the installed wrappers set this for packaged binaries.

## Test Map

- Config loading and defaults: `internal/config/*_test.go`
- Gateway auth, heartbeat, UI, persistence: `internal/gateway/*_test.go`
- Gateway scheduling/unload: `internal/gateway/scheduler_test.go`,
  `internal/gateway/reconcile_test.go`
- Gateway proxy, request normalization, logging: `internal/gateway/proxy_test.go`
- Agent reconcile, artifacts, rendering, service restart:
  `internal/agent/*_test.go`
- Worker install script dry-run: `scripts/install_worker_test.go`

Run all tests with:

```bash
go test ./...
```

## Things To Preserve

- Gateway remains the source of truth for routing, active counts, concurrency,
  queues, retries, and replica policy.
- Worker remains stateless enough to containerize; local durable state is limited
  to installed model artifacts, rendered llama-swap config, logs, and runtime
  venvs/binaries.
- Gateway request logs are the source for access counters after restart.
- Worker event logs are the source for recent event UI replay after restart.
- Do not make gateway depend on direct model runtime APIs; gateway talks to
  llama-swap URLs.
- Do not hide model lifecycle events. They are needed to debug model switching,
  unload, download, and restart behavior.
