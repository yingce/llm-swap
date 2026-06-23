# LLM Swap Project Map

Last updated: 2026-06-23.

This document is the current high-level map for future agents. It reflects the
code state after the gateway UI, token unification, worker event persistence,
request accounting, scheduling, install script, vLLM/SGLang wrappers, and
llama.cpp runtime wrapper work.

## System Shape

The system is a Go control plane around worker-local llama-swap instances.

```text
client
  -> gateway /v1/chat/completions
    -> scheduler chooses a worker by model, tag, artifact readiness, running
       model state, active request count, and max_loaded policy
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

There is no external database. Gateway state is in-process, with append-only
JSONL files for request accounting and worker events.

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
- `min_loaded`: target floor for loaded replicas.
- `max_loaded`: loaded replica ceiling. If omitted, it effectively equals
  `min_loaded`.
- `warm_when_idle`: tag policy model preloaded through rendered llama-swap
  startup hooks. It can still be unloaded if policy and traffic justify it.

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
    request stats, and emits metrics/logs.
  - `top_k: 0` is normalized to `-1` for SGLang-backed models.
  - Transformers-style `image`, `video`, and `audio` content parts are converted
    to OpenAI-style URL objects for SGLang compatibility.

- `internal/gateway/scheduler.go`
  - Picks a worker for a model.
  - Requires healthy worker, allowed tag policy, ready artifact, and not excluded
    by an earlier failed dispatch attempt.
  - Prefers idle workers when loading additional replicas is useful; otherwise
    prefers already-running ready models with lower active request counts.

- `internal/gateway/limits.go`
  - Keyed in-memory queue/concurrency limiter.
  - Used for model, tag, and worker gates.

- `internal/gateway/workers.go`
  - In-memory worker registry.
  - Tracks heartbeat state, active gateway-owned requests, drain state,
    scrape backoff, artifacts, and running models.

- `internal/gateway/reconcile.go`
  - Loaded-replica reconciler.
  - Unloads excess idle replicas over `max_loaded`.
  - Can unload a cold idle model to free a worker for an underloaded hot model.
  - Records gateway-initiated unload success/failure as worker events.

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

- `internal/gateway/ui.go`
  - Minimal dashboard at `/ui`.
  - Shows model availability, traffic, workers, health, running models,
    artifacts, and recent worker events.
  - Recent events have columns: Received, Worker, Event, Model, Detail.

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

- `internal/agent/render.go`
  - Renders local llama-swap config.
  - `{{model_path}}` expands to `<model_root>/<model_name>`.
  - Writes `apiKeys` when a llama-swap token is configured.
  - Wraps each model command with shell logging to
    `/opt/llmswap/logs/model-runtime.log`.
  - `check_endpoint` maps to llama-swap `checkEndpoint`.
  - `cmd_stop` maps to llama-swap `cmdStop`; normal model stopping should still
    rely on llama-swap unless custom cleanup is needed.

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
- `models.<name>.check_endpoint` should be set for runtimes whose health route
  is not `/health`, for example SGLang `/model_info`.
- `models.<name>.max_loaded` omitted means `min_loaded`.
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
- create uv-managed Python venvs for vLLM and SGLang using Python 3.12 by
  default;
- install torch for vLLM with CUDA-aware PyTorch index selection;
- install SGLang and patch MiniCPMV4.6 config compatibility;
- install prebuilt llama.cpp CUDA runtime archives from OSS;
- write wrappers into `/opt/llmswap/bin`;
- initialize agent config without overwriting an existing one unless
  `--force-config` is passed.

Important env vars:

- `LLMSWAP_ROOT`
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
    dir, maps a leading positional model path to `-m`, and applies default host
    and port if not already supplied.
  - llama.cpp only supports GGUF models. Do not route HF/AWQ directories through
    llama.cpp.

## Logging and UI

Gateway structured stdout logs include scheduler decisions, requests, queue
events, agent events, and log write errors.

Persistent gateway files:

- `/opt/llmswap/logs/gateway-requests.jsonl`
- `/opt/llmswap/logs/gateway-worker-events.jsonl`

Worker-side model runtime logs:

- `/opt/llmswap/logs/model-runtime.log`

UI routes:

- `/ui`
- `/ui/status`
- `/ui/events?limit=50&offset=0`

UI authentication uses the agent token. `/ui?token=<agent-token>` sets an
HTTP-only cookie scoped to `/ui`.

## Known Compatibility Notes

- SGLang-backed models may reject `top_k: 0`; gateway normalizes it to `-1`.
- Some SGLang multimodal models expect OpenAI-style `image_url`, `video_url`,
  or `audio_url`; gateway normalizes transformers-style parts.
- SGLang MiniCPMV4.6 config compatibility is patched in the installed venv by
  `scripts/install-worker.sh`.
- vLLM and SGLang compatibility for specific VL/AWQ models can depend on
  upstream transformers, torch, torchcodec, ffmpeg, and CUDA shared libraries.
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

