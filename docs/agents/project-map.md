# LLM Swap Project Map

Last updated: 2026-08-08.

This document is the current high-level map for future agents. It reflects the
code state after the gateway UI, token unification, worker event persistence,
request accounting, scheduling, install script, vLLM/SGLang/llama.cpp runtime
wrappers, versioned model directory, public model alias, and refresh-safe admin
UI routing work. The vLLM environment also contains vLLM-Omni support.

## System Shape

The system is a Go control plane around worker-local llama-swap instances.

```text
client
  -> gateway /v1/chat/completions
    -> placement chooses a worker by model, tag, artifact readiness, running
       model state, active request count, and replica policy
    -> gateway validates the worker's advertised FRPS endpoint, then proxies to
       the gateway-side FRP dial address plus the gateway-leased TCP port
      -> llama-swap starts/switches local runtime command from rendered config
        -> vLLM (including Omni), SGLang, or llama.cpp runtime wrapper

worker agent
  -> polls gateway config
  -> downloads model artifacts
  -> renders llama-swap config
  -> reads local llama-swap /running from 127.0.0.1:swap_port
  -> runs an embedded FRP TCP client and advertises its gateway-leased endpoint
     only after that proxy is ready
  -> heartbeats worker state, GPU device stats, artifacts, running models, and
     events to gateway
```

Most Gateway scheduling state is in-process, with append-only JSONL files for
local request and worker event debugging/backups. The Gateway state directory
also holds durable control-plane metadata: the globally monotonic configuration
revision, FRP leases, and service-name promotion archives. A Postgres
`records_store` can be enabled
as the query source for request records and worker events; when it is enabled,
the gateway still writes local JSONL but UI detail pages read from Postgres.
Historical metrics storage is optional:
VictoriaMetrics can be attached through vmagent scraping `/metrics`; when it is
disabled the gateway still runs with no external database.

## Domain Vocabulary

- `gateway`: central HTTP service and control-plane owner.
- `worker`: a machine with a local llama-swap process and one agent process.
- `agent`: thin worker-side controller; it installs artifacts and reports state.
- `llama-swap`: worker-local runtime switcher and proxy target.
- `model`: concrete canonical identity defined by a `models` map key. Gateway
  policy, worker state, llama-swap, metrics, billing, and request records use
  this name.
- `model_dir`: optional worker-local install/runtime directory for a canonical
  model. It changes only the path beneath `agent.model_root`; when omitted, the
  canonical model name remains the directory name.
- `model_alias`: stable public request name that maps directly to one canonical
  model through top-level `model_aliases`. Aliases are not worker identities and
  cannot chain or collide with canonical model names.
- `service-name promotion`: a guarded Config Ops transaction for reclaiming one
  disabled, idle canonical name as an alias to a ready replacement. This is not
  ordinary alias editing and does not relax canonical/alias collision checks.
- `artifact`: downloadable model payload. Supported kinds are `file` and
  `tar_gz`.
- `tag_policy`: gateway policy for workers with a tag. It defines installable
  models, a legacy warm hint, compatibility worker defaults, and optional
  tag-wide concurrency ceilings.
- `tag_capacity`: model-specific per-ready-worker concurrency and queue limits
  keyed by runnable tag. The gateway defaults a newly selected model/tag pair
  to `max_concurrency: 1` and `max_queue: 1`; legacy `worker_defaults` remain a
  fallback for configurations that have not migrated.
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
  - Creates `gateway.NewServerWithConfigRevisionStore`, passing the standard
    request/event JSONL paths plus a file-backed revision allocator at
    `gateway.DefaultGatewayConfigRevisionPath`.
  - Allocates and persists a new global configuration revision during server
    construction. Startup fails instead of publishing an unversioned snapshot
    when the state directory or revision document cannot be used.
  - Starts the loaded-replica reconciler every 30 seconds.

- `internal/gateway/server.go`
  - Wires HTTP routes.
  - Agent config and heartbeat endpoints use the agent token.
  - Client model and chat endpoints use the client token.
  - UI routes use the agent token.
  - Heartbeat events are cached and persisted to worker event JSONL.
  - `/v1/models` lists available canonical models and aliases whose canonical
    targets are currently available.

- `internal/gateway/service_name_promotion.go`
  - Implements the audited `Promote service name` and rollback transactions.
  - Promotion requires the old canonical to be disabled with no running,
    installing, active, or queued work. The target must have at least its
    configured ready floor, with a minimum of one routable ready replica even
    when `min_loaded` is zero.
  - The transaction archives the old definition and touched tag-policy fields,
    removes the old active namespace entry, and creates the alias in one
    serialized config operation. Rollback verifies the alias, target, archive,
    and touched policies still match before restoring anything, so it cannot
    overwrite later operator edits.
  - Production archives are stored atomically under
    `/opt/llmswap/state/service-name-promotions.json`; the model artifact
    directory is not deleted. Requests, billing, and historical canonical
    identities are never rewritten.

- `internal/gateway/proxy.go`
  - OpenAI-compatible chat proxy path.
  - Resolves a requested alias to its canonical target before model policy,
    queue/concurrency gates, placement, tag selection, active accounting,
    cooldowns, metrics, billing, and request records. Direct canonical requests
    pass through unchanged.
  - Rewrites an alias request body's `model` field to the canonical name before
    SGLang normalization and dispatch, so llama-swap and the runtime receive the
    name they were configured to serve.
  - Request, scheduler, and retry structured logs keep the canonical name in
    `model` and add `requested_model` when the client used an alias. Persistent
    accounting and low-cardinality metric labels remain canonical so traffic
    and cost are attributed to the concrete version that served them.
  - When no ready worker exists but the scheduler reports eligible capacity,
    records a `no_ready` pressure observation so the async reconciler can warm
    an empty worker for the resolved canonical model. The current request still
    fails fast; requests only route to ready replicas.
  - Retryable proxy failures mark only the failing replica as cooled down for
    30 seconds, then retry another ready replica when available.
  - `top_k: 0` is normalized to `-1` for SGLang-backed models.
  - Transformers-style `image`, `video`, and `audio` content parts are converted
    to OpenAI-style URL objects for SGLang compatibility.
  - Builds upstream requests only from the worker's advertised
    `llama_swap_url`. There is no gateway-to-agent tunnel fallback.

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
  - Repeated `queue_full`/timeout observations and single `no_ready`
    cold-start observations can trigger predictive warm on eligible empty
    workers.
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
  - Used for model, tag, and `worker + model` gates. Model capacity is the sum
    of the selected tag capacity across healthy, ready replicas; each replica
    is independently protected by that model/tag limit.
  - `AcquireWithStats` reports admitted, admitted-after-wait, queue-full, and
    queue-timeout outcomes with wait time and active/queued depth at admission.

- `internal/gateway/workers.go`
  - In-memory worker registry.
  - Tracks heartbeat state, active gateway-owned requests, drain state,
    scrape backoff, artifacts, and running models.
  - Workers become stale and unavailable for routing after 6 seconds without a
    heartbeat. If they still have not reported after 10 minutes, registry
    snapshots prune them from gateway/UI state and clear associated active/drain
    bookkeeping.

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
    temperature/top_p/top_k, usage tokens, cache tokens, reasoning tokens,
    retry count, and filtered incoming `x-` request headers as strings.
  - `x-request-id` and `request-id` are omitted from `request_headers` because
    the canonical gateway request id is already stored as top-level
    `request_id`.

- `internal/gateway/records_store.go` and `internal/gateway/migrations/`
  - Optional Postgres record store for request records and worker events.
  - `records_store.auto_migrate` runs the embedded SQL migration at gateway
    startup.
  - The schema includes `request_records`, `worker_events`, and
    `worker_model_ready_intervals`; cost/billing queries should build on these
    PG tables instead of local JSONL.
  - Billing API parameters, time parsing, ready interval semantics, USD
    conversion, and configured usage-cost fields are documented in
    `docs/billing-api.md`.
  - Imported historical JSONL rows use `source_hash` unique indexes so
    interrupted imports can be rerun without duplicating rows.

- `cmd/import-records` and `scripts/import-records-jsonl.sh`
  - Import historical `gateway-requests.jsonl` and
    `gateway-worker-events.jsonl` into the Postgres records store.
  - The script expects `PG_DSN` or `LLMSWAP_RECORDS_STORE_DSN`; when `go` is
    unavailable on the host, it falls back to running the command in the Go
    Docker image on the compose network.

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
  - Exposes worker heartbeat GPU memory, utilization, and temperature gauges for
    Grafana and VictoriaMetrics.
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
  - Shows model availability, traffic, workers, health, GPU memory/utilization,
    running models, artifacts, and recent worker events.
  - Worker cards expose current executing/queued counts and gateway-derived
    live model/tag limits, but do not own capacity policy. Config Ops provides
    model-centric controls for tag-policy membership and model-owned per-tag
    capacity settings because serving capacity depends on both model and
    hardware tag.
  - Config Ops exposes the optional local model directory and a Model aliases
    card for adding, retargeting, or removing stable names. Alias rows show the
    canonical target's ready/running replica counts and warn on zero-ready
    targets without blocking cold-start or recovery configurations.
  - Config drafts round-trip `model_dir` and `model_aliases` through YAML, omit
    empty directories, and sort aliases for deterministic diffs.
  - Recent events have columns: Received, Worker, Event, Model, Detail.
  - Optional historical metrics endpoints:
    `/ui/metrics/summary`, `/ui/metrics/model`, and `/ui/metrics/worker`.
    These use the agent token like the rest of the UI and return 503 when the
    metrics store is disabled.
  - `/ui/traffic` aggregates request count, response classes, tokens, and
    latency for the Overview range selector. It uses the records store, defaults
    to the latest 24 hours, and caps queries at 7 days.

- `internal/gateway/config_manager.go` and `config_admin.go`
  - Own the gateway config snapshot used by gateway handlers.
  - Config snapshots are versioned and normalized with the same important
    defaults as startup config loading.
  - `internal/gateway/config_revision_store.go` allocates a globally increasing
    revision on every Gateway startup and successful hot apply. Production uses
    the atomic state file `/opt/llmswap/state/config-revision.json`; tests may
    use the in-memory implementation. Keep the allocation behind
    `ConfigRevisionStore` so a future Redis backend can preserve the same
    monotonic contract.
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
  - The admin UI treats config as a structured operations console by default:
    `Config Ops` creates blank concrete models through `New model`, edits
    existing model policy and model directories in place, manages model
    aliases, and selects Tag membership. It preserves canonical model names as
    immutable identities. `Advanced` is a read-only YAML viewer for full config
    inspection and includes `Copy YAML` for copy/paste.
  - New blank models start disabled with the `vLLM` runtime and
    `min_loaded: 0`. Runtime selection is limited to `vLLM`, `SGLang`, and
    `llama.cpp`. The old `vllm-omni` value remains a config compatibility alias
    for the unified vLLM runtime. Legacy models configured with a raw `run`
    command remain compatible as a read-only `Custom command` state.
  - Saving the modal updates only the local configuration draft. Canceling it
    (including a dirty-draft discard) cannot change gateway configuration;
    validation and persistence remain explicit Dry run and Apply actions.
  - Dry-run returns coarse impact changes plus loaded-worker impacts. Model
    policy changes are hot-update candidates. Runtime command/artifact changes
    only require worker restart/reload when the affected model is currently
    loaded on a worker; the response lists those model/worker pairs in
    `impacts`. Process-level fields such as listen address and tokens are marked
    as requiring gateway restart. `gateway.proxy_attempts` can hot-apply from
    YAML unless it was overridden by gateway env/CLI at process startup; in that
    case UI apply persists the YAML but keeps the running override until restart.
  - Alias additions, removals, and target changes have independent
    `model_aliases.<name>` diff paths and hot-apply without a gateway or worker
    restart. A `model_dir` change is a runtime-affecting model change; it reports
    restart/reload impact only for workers where that canonical model is loaded.
  - Admin action routes support worker drain/undrain, model warm/unload, and
    replica cooldown clear. These actions stay gateway-owned, use the existing
    llama-swap client for runtime actions, and record gateway worker events so
    the UI/event log shows operator interventions.

## Agent Modules

- `cmd/agent/main.go`
  - Loads runtime config through `config.LoadAgentRuntime`.
  - Starts the FRP transport manager and advertises its leased public swap URL
    only while the local FRP proxy is ready.
  - Uses local `127.0.0.1:swap_port` for local llama-swap `/running` and health.

- `internal/agent/transport_manager.go` and `frp_client.go`
  - Fetch encrypted gateway transport bootstrap configuration, acquire and
    renew a gateway-owned TCP port lease, and run the embedded FRP client.
  - Publish the direct worker URL only after FRP reports the proxy ready; clear
    it before replacement or shutdown so the gateway never routes to an
    unready transport.
  - All workers currently share one agent bearer. It authenticates the API but
    is not bound to `agent_id`, so FRP workers must belong to one fully trusted
    trust domain; untrusted-worker multi-tenancy is not supported.

- `internal/agent/reconcile.go`
  - Main worker reconcile loop.
  - Fetches tag-scoped config from gateway.
  - Consumes the Gateway's global `desired_model_dirs` union in addition to the
    local tag policy. Removal from one tag cancels that Agent's local install
    without publishing a shared tombstone while another active tag still
    desires the directory. Only an explicit global absence permits a
    tombstone; a missing field from an older Gateway is handled conservatively.
  - Treats `config_revision` as the ordering fence for desired artifacts.
    Newer revisions cancel superseded work, including a revision-only change or
    removal from the allowed model set, without turning cancellation into an
    install error.
  - Installs allowed artifacts, one active install at a time.
  - Resolves each canonical model's install directory from `model_dir`, falling
    back to the canonical name. Directory identity participates in the async
    install key so completion for an old path cannot satisfy a new-path install.
  - Fetches local llama-swap running models.
  - Renders llama-swap config from the ready allowed-model subset so a worker
    can start serving already-installed models while other artifacts continue
    downloading in the background.
  - Marks pending restart only when a config change affects currently loaded
    models.
  - Heartbeats artifacts, running models, GPU device stats from `nvidia-smi`,
    needs_restart, last_error, and events.
  - Records local lifecycle events: artifact install/download events,
    `llama_swap_config_changed`, restart events, `model_loaded`,
    `model_state_changed`, and `model_unloaded`.
  - Running model diff events are only emitted after a successful `/running`
    fetch; failed fetches do not imply unload.

- `internal/agent/artifacts.go`
  - Downloads artifacts from `oss.base_url`.
  - Verifies CRC64 ECMA and writes marker files.
  - Emits progress callbacks for download progress.
  - Uses shared OS locks under `<model_root>/.locks`. A blob lock de-duplicates
    one source download, while a separate OS-aware `model_dir` lock serializes
    final commits by destination directory.
  - Persists a desired-state fence containing configuration revision and a
    bounded artifact fingerprint. Staged data can replace a ready directory
    only after the installer holds the directory lock and still matches that
    fence. A cancelled, failed, or stale install leaves the current ready
    directory and marker intact.
  - Emits `artifact_model_dir_lock_wait`, `artifact_install_stale_fence`,
    `artifact_install_cancelled`, and `artifact_install_commit` for operator
    diagnosis; fingerprints are bounded hashes and events do not contain raw
    artifact URLs or credentials.
  - Reuses a matching source artifact already present at
    `<model_root>/<basename(artifact.object)>` before downloading.
  - Persists downloaded source artifacts at
    `<model_root>/<basename(artifact.object)>`; model directories still get
    their own installed files and `.llm-agent-artifact.json` marker.
  - Installs each payload beneath `<model_root>/<model_dir or canonical name>`.
    Marker identity and heartbeat artifact keys remain canonical; changing the
    local directory does not rename the model seen by gateway or llama-swap.

- `internal/agent/render.go`
  - Renders local llama-swap config.
  - `{{model_path}}` and standard runtime wrapper paths expand to
    `<model_root>/<model_dir or canonical model name>`.
  - The llama-swap model key and vLLM/SGLang served model name or llama.cpp alias
    remain the canonical model name, independent of the local directory.
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

## Agent Release Identity and Protocol Compatibility

Agent build metadata has two separate identities:

- `LLMSWAP_BUILD_VERSION` is the human-readable Agent release identifier. The
  current fencing-capable source fallback is `2026.08.08.1`.
- `LLMSWAP_BUILD_COMMIT` is the exact source commit SHA for build provenance.
  Do not copy a commit SHA into the version field; release automation must keep
  the two values distinct.

A Gateway-only release does not require publishing a new Agent image. Publish
an Agent only when its worker-side behavior or the supported Agent protocol
range requires it.

Gateway evaluates each Agent's reported protocol version against its supported
inclusive range. The current Agent protocol is v3 and the current Gateway's
safe range is v3 through v3. A protocol-v2 heartbeat remains HTTP-accepted for
cutover visibility but is below the safety minimum because v2 lacks
`config_revision`/artifact fencing. `agent_version_status` has these meanings:

- `compatible`: the Agent protocol is within the Gateway-supported range.
- `upgrade_agent`: the Agent protocol is below the Gateway minimum; upgrade
  the Agent.
- `upgrade_gateway`: the Agent protocol is above the Gateway maximum; upgrade
  the Gateway before relying on that Agent.
- `legacy`: the Agent did not report a protocol version.

The normal worker UI presents the Agent release version. Compatibility guidance
is shown only for a non-compatible status; the commit remains build provenance,
not the operator-facing release name.

Adopt fencing with Gateway and Agents in the same v3 compatibility batch; v2
must not be treated as a supported mixed state even though its heartbeat is
accepted. After v3 adoption, a pure Gateway release that preserves the Agent
protocol contract does not require publishing or deploying an Agent. For a
future overlap-compatible change, first deploy a Gateway that supports both
protocols, then Agents, then raise the minimum after the old Agents retire.
Release versions and commits never substitute for this explicit protocol
decision.

## Config Rules

Gateway config:

- `tokens.client` is for client-facing OpenAI-compatible routes.
- `tokens.agent` is for internal agent routes and the UI.
- `tokens.llama_swap` is optional. If omitted, it defaults to `tokens.agent`.
- Each `models.<name>` key is a concrete canonical identity. Tag policies,
  worker reports, lifecycle controls, runtime names, and direct client requests
  use canonical keys, not aliases.
- `models.<name>.model_dir` is optional. It must be one safe relative directory
  name beneath `agent.model_root`; absolute/nested/traversal paths, `.locks`,
  source-cache basename collisions, and duplicate resolved directories are
  rejected. Omitting it preserves `<model_root>/<canonical-name>`.
- `model_aliases.<alias>` must target a defined canonical model directly. Alias
  chains, blank/untrimmed entries, and aliases colliding with canonical names are
  invalid. Disabled targets are removed from the active runtime view.
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
  agent-prestart.sh
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
- when a Tailscale auth key or hostname is provided, write supervisor-managed
  `llmswap-tailscaled` and one-shot `llmswap-tailscale-init` programs;
- create uv-managed Python venvs for vLLM and SGLang using Python 3.12 by
  default;
- install one CUDA-aware vLLM environment containing vLLM, vLLM-Omni, and the
  audio dependencies. CUDA 12.8 builds compile the selected vLLM source ref
  against the pinned PyTorch CUDA 12.8 environment, install the resulting
  wheel, and then remove source, wheel, and uv caches;
- install audio parser dependencies separately for vLLM so OpenAI audio payloads
  are supported;
- install SGLang from the pinned CUDA-compatible default package, using an
  isolated SGLang torch/torchao compatibility set for the selected backend,
  and patch MiniCPMV4.6 config compatibility;
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
- `LLMSWAP_AGENT_PRESTART_SCRIPT`
- `LLMSWAP_SWAP_PORT`
- `LLMSWAP_UV_CACHE_DIR`
- `LLMSWAP_UV_PYTHON_INSTALL_DIR`
- `LLMSWAP_UV_PYTHON_INSTALL_MIRROR`
- `LLMSWAP_TORCH_INDEX_URL`
- `LLMSWAP_TORCH_INDEX_URL_BASE`
- `LLMSWAP_TORCH_VERSION`
- `LLMSWAP_TORCHAUDIO_VERSION`
- `LLMSWAP_TORCHVISION_VERSION`
- `LLMSWAP_VLLM_PACKAGE`
- `LLMSWAP_VLLM_OMNI_PACKAGE`
- `LLMSWAP_VLLM_OMNI_VLLM_PACKAGE`
- `LLMSWAP_VLLM_OMNI_VLLM_SOURCE_REPO`
- `LLMSWAP_VLLM_OMNI_VLLM_SOURCE_REF`
- `LLMSWAP_VLLM_OMNI_BUILD_VLLM_FROM_SOURCE`
- `LLMSWAP_TORCH_CUDA_ARCH_LIST`

## Agent Container Image

`Dockerfile.agent` builds a worker image that preinstalls the same base
dependencies, uv-managed Python environments, runtime wrappers, supervisor
configuration, and `llm-swap-agent` binary that `scripts/install-worker.sh`
would normally install on a host.

Important properties:

- Default base image is `nvidia/cuda:12.8.1-cudnn-devel-ubuntu22.04`.
  vLLM's flashinfer path can JIT-compile CUDA ops at model startup, so the
  default image must include CUDA development headers and `nvcc`.
- The Dockerfile intentionally builds a stable `runtime-base` stage before the
  Go `agent-build` stage. Heavy base, Python, vLLM, SGLang, llama.cpp,
  Tailscale, and llama-swap release layers must not copy `cmd/` or `internal/`;
  normal agent code changes should only rebuild the Go binary and the final
  lightweight agent/supervisor setup layer.
- The production worker Compose passes `LLMSWAP_BUILD_VERSION` and
  `LLMSWAP_BUILD_COMMIT` as separate required build args. Its verifier rejects
  using the same value for both. Gateway-only Fabric builds inject commit and
  build time provenance but do not inject the Agent release-version field.
- The build runs `install-worker.sh` inside the image, so runtime installation
  logic stays in one place.
- The image preinstalls `vllm`, `sglang`, or `llamacpp` based on
  `--build-arg LLMSWAP_RUNTIME=...`; `all` includes every supported runtime for
  stateless workers that may dynamically switch models. `vllm-omni` is
  accepted only as a legacy alias for `vllm`.
- The image installs the Tailscale binaries by default, but does not run
  `tailscale up` at build time.
- The image removes the placeholder `agent.yaml` after build. At container
  start, `scripts/agent-container-entrypoint.sh` writes `/opt/llmswap/agent.yaml`
  from env only when the file is absent or `LLMSWAP_FORCE_CONFIG=1`.
- By default the image downloads the official `llama-swap` release artifact
  during Docker build.
- The default release URL is
  `https://github.com/mostlygeek/llama-swap/releases/download/${LLAMA_SWAP_REF}/llama-swap_${LLAMA_SWAP_REF#v}_linux_amd64.tar.gz`.
- The extracted binary is stored as `/opt/llmswap/bin/llama-swap.bundled` and
  copied to `/opt/llmswap/bin/llama-swap`.
- If `--build-arg LLAMA_SWAP_DOWNLOAD_URL=...` is provided, the downloaded
  binary replaces the built bundled binary.
- `LLAMA_SWAP_REF` controls the upstream ref used for the default build.
- `LLAMA_SWAP_RELEASE_URL` can override the exact release artifact URL when a
  different mirror or pinned tarball is required.
- On container start, `scripts/agent-container-entrypoint.sh` restores
  `/opt/llmswap/bin/llama-swap` from the bundled binary by default.
- If runtime env `LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL` is set, entrypoint downloads
  that binary and replaces the active
  `/opt/llmswap/bin/llama-swap` before starting supervisor.

Typical build:

```bash
docker build \
  -f Dockerfile.agent \
  --build-arg BASE_IMAGE=nvidia/cuda:12.8.1-cudnn-devel-ubuntu22.04 \
  --build-arg LLMSWAP_CUDA_VERSION=12.8 \
  --build-arg LLMSWAP_RUNTIME=all \
  --build-arg LLAMA_SWAP_REF=v232 \
  --build-arg LLAMA_SWAP_RELEASE_URL=https://github.com/mostlygeek/llama-swap/releases/download/v232/llama-swap_232_linux_amd64.tar.gz \
  --build-arg LLMSWAP_TORCH_INDEX_URL_BASE=https://mirror.example.invalid/pytorch \
  --build-arg UV_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
  --build-arg PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
  -t llmswap-agent:cu128 .
```

If the build host must go through an HTTP(S) proxy, pass standard Docker build
args so both stages inherit them:

```bash
docker build \
  -f Dockerfile.agent \
  --build-arg HTTP_PROXY=http://proxy.example.local:7890 \
  --build-arg HTTPS_PROXY=http://proxy.example.local:7890 \
  --build-arg NO_PROXY=localhost,127.0.0.1 \
  -t llmswap-agent:cu128 .
```

When the build machine cannot reliably access `pypi.org`, pass package index
mirror args such as:

- `UV_INDEX_URL`
- `UV_EXTRA_INDEX_URL`
- `PIP_INDEX_URL`
- `PIP_EXTRA_INDEX_URL`
- `LLMSWAP_UV_PYTHON_INSTALL_MIRROR`

When the build machine cannot reliably access `download.pytorch.org`, pass
either:

- `LLMSWAP_TORCH_INDEX_URL_BASE` to map CUDA backends onto a mirror, for example
  `https://mirror.example.invalid/pytorch` -> `.../cu128`
- `LLMSWAP_TORCH_INDEX_URL` to force one exact torch wheel index URL

Typical runtime env when no config file is mounted:

- `LLMSWAP_AGENT_ID`
- `LLMSWAP_AGENT_TAGS`
- `LLMSWAP_GATEWAY_URL`
- `LLMSWAP_AGENT_TOKEN`
- `LLMSWAP_LLAMA_SWAP_TOKEN` (optional; defaults to agent token)
- `LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL` (optional runtime override for the active
  llama-swap binary; when omitted, the build-bundled binary is used)
- `LLMSWAP_AGENT_PRESTART_SCRIPT` (optional path to a shell script sourced by
  `agent-supervisor.sh` before the agent starts; defaults to
  `/opt/llmswap/agent-prestart.sh`)
- `LLMSWAP_SWAP_PORT`
- `LLMSWAP_SWAP_URL` (optional explicit public worker URL)
- `LLMSWAP_FORCE_CONFIG=1` when the container should rewrite `agent.yaml`
- `LLMSWAP_ENABLE_TAILSCALE=1` and `LLMSWAP_TAILSCALE_AUTHKEY` only when
  running Tailscale in the same container
- `LLMSWAP_TAILSCALE_HOSTNAME` when supervisor-managed tailscale init should
  register a specific name
- `LLMSWAP_TAILSCALE_SOCKET` and `LLMSWAP_TAILSCALE_PORT` when the container
  should use a non-default tailscaled socket or port
- `LLMSWAP_TAILSCALE_TUN` to override the container TUN mode. The default
  entrypoint behavior is `userspace-networking`.

User-facing runtime envs should use `LLMSWAP_*`. The agent binary now accepts
the same public names directly, so `LLMSWAP_AGENT_ID`,
`LLMSWAP_AGENT_TAGS`, `LLMSWAP_GATEWAY_URL`, `LLMSWAP_AGENT_TOKEN`,
`LLMSWAP_LLAMA_SWAP_TOKEN`, `LLMSWAP_SWAP_URL`, `LLMSWAP_SWAP_PORT`,
`LLMSWAP_MODEL_ROOT`, and `LLMSWAP_LLAMA_SWAP_CONFIG` work even without
generating `agent.yaml`. Legacy `LLM_SWAP_AGENT_*`, bare `SWAP_URL`, and bare
`TAILSCALE_AUTHKEY` aliases are not accepted.

Gateway runtime envs should also use `LLMSWAP_*`:

- `LLMSWAP_GATEWAY_CONFIG`
- `LLMSWAP_GATEWAY_ADDR`
- `LLMSWAP_GATEWAY_PROXY_ATTEMPTS`
- `LLMSWAP_CLIENT_TOKEN`
- `LLMSWAP_AGENT_TOKEN`
- `LLMSWAP_LLAMA_SWAP_TOKEN`
- `LLMSWAP_RECORDS_STORE_ENABLED`
- `LLMSWAP_RECORDS_STORE_TYPE`
- `LLMSWAP_RECORDS_STORE_DSN`
- `PG_DSN` as the shorter records store DSN alias used by compose files.
- `LLMSWAP_RECORDS_STORE_GATEWAY_ID`
- `LLMSWAP_RECORDS_STORE_AUTO_MIGRATE`
- `LLMSWAP_RECORDS_STORE_TIMEOUT_MS`

Legacy gateway aliases such as `LLM_SWAP_GATEWAY_TOKENS_AGENT` are not
accepted. Keep gateway runtime envs on the `LLMSWAP_*` names above.

Default container startup path:

- verifies `/opt/llmswap/bin/llm-swap-agent`;
- verifies `/opt/llmswap/bin/llama-swap`;
- optionally writes `/opt/llmswap/agent.yaml`;
- optionally writes supervisor programs for
  `llmswap-tailscaled --tun=userspace-networking` and one-shot
  `llmswap-tailscale-init`, which performs `tailscale login` and optional
  hostname setup after the socket is ready;
- rewrites the `llmswap-agent` supervisor program to start through
  `agent-supervisor.sh`. This wrapper sources
  `LLMSWAP_AGENT_PRESTART_SCRIPT`, defaulting to
  `/opt/llmswap/agent-prestart.sh`, when the script exists. When Tailscale is
  requested and no explicit `LLMSWAP_SWAP_URL` is configured, this wrapper
  waits for `tailscale ip -4` before starting the agent so the agent advertises
  the tailnet URL instead of falling back to the container or host local IP;
- starts `supervisord` in the foreground, which manages `llama-swap` and
  `llm-swap-agent`.

`llama-swap` must not wait for model downloads before starting. The supervisor
wrapper writes a valid empty `/opt/llmswap/llama-swap.yaml` with `models: {}`
when the file is absent, then starts llama-swap with `-watch-config`. The agent
downloads artifacts asynchronously and later rewrites the config with only the
models whose artifacts are ready.

Build-time vs runtime split:

- Build args such as proxy settings, Python/package mirrors, torch index
  selection, runtime package names, and llama.cpp archive selectors are used
  only while building the image. They are not persisted as final runtime env in
  the container.
- Runtime env controls instance identity and networking only: agent id, tags,
  gateway URL/token, swap URL/port, optional runtime llama-swap override URL,
  and optional Tailscale startup parameters.

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

When `records_store.enabled=true`, these files remain local logs only. UI
request/event pages query Postgres, and future billing/reporting should query
Postgres records rather than replaying JSONL.
Use `scripts/import-records-jsonl.sh` to backfill existing JSONL history into
Postgres after enabling the records store.

Worker-side model runtime logs:

- `/opt/llmswap/logs/model-runtime.log`

UI routes:

- `/ui`
- `/ui/models`
- `/ui/workers`
- `/ui/billing`
- `/ui/event-log`
- `/ui/request-log`
- `/ui/config`
- `/ui/advanced`

Each path above serves the embedded React application and maps to an independent
Dashboard, Models, Workers, Billing, Events, Requests, Config Ops, or Advanced
page. Tab clicks use browser history, refresh restores the selected page, and
back/forward navigation updates the active tab. The `event-log` and
`request-log` names deliberately avoid the existing JSON endpoint paths below,
whose contracts and handlers are preserved:

- `/ui/status`
- `/ui/requests?limit=50&offset=0`
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
- `deploy/production/docker-compose.yaml`
- `deploy/production/vmagent/promscrape.yml`
- `deploy/worker-frp/compose.yaml` and `deploy/worker-frp/verify.sh`
  - Build one shared agent image and run `worker-gpu0` through `worker-gpu7`,
    each pinned to one physical NVIDIA device with no published worker ports.
  - Runtime environment is limited to the gateway URL, `gpu-4090` tag, and a
    file-backed agent token. FRPS settings and leases come from the gateway's
    encrypted bootstrap response; FRPC runs inside the agent process.
  - Per-worker log binds and one shared models bind preserve mutable data
    without hiding the image-owned `/opt/llmswap` runtime tree.

vmagent scrapes gateway `/metrics` and remote-writes to VictoriaMetrics. The
default scrape target is `gateway:8080`; adjust it when gateway runs outside the
compose network. Request and worker event JSONL files remain the source for
request detail replay and recent event pages; VictoriaMetrics is for aggregate
time-series history only.

Production compose deployment runs gateway, VictoriaMetrics, and vmagent
together. The gateway container mounts `/opt/llmswap/config` as a directory so
admin config apply can persist `gateway.yaml` without a single-file bind mount,
mounts the dedicated
`/opt/llmswap/state` directory for configuration revisions, promotion archives,
and the configured FRP lease store, and mounts
`/opt/llmswap/logs` read-write. A single-file `gateway.yaml` bind mount is not
used because rename-based atomic replacement cannot safely update that mount.
When gateway and FRPS are on the same host, the production example uses
`dial_addr: 127.0.0.1`; public FRPS dial paths are plaintext HTTP and require a
private network or external transport protection before carrying real bearer
tokens or prompts. VictoriaMetrics stores data under
`/opt/llmswap/data/victoriametrics`. Start it from the repository root with:

Before upgrading an existing deployment, copy the protected old
`/opt/llmswap/gateway.yaml` to `/opt/llmswap/config/gateway.yaml`, add the
explicit lease-store path, and create `/opt/llmswap/state` with owner-only
permissions. Keep the old file until the new deployment has passed health and
lease-reload checks.

```bash
docker compose -f deploy/production/docker-compose.yaml up -d --build
```

The gateway Dockerfile builds `ui/admin` with Node/Vite before compiling the Go
binary, then copies the generated `internal/gateway/admin_dist` into the Go
build context so the admin UI is embedded in the final binary.

Gateway and Agent binaries must be released as one protocol-v3 batch when first
adopting `config_revision`, global desired-directory tombstones, and artifact
fencing. Protocol-v2 heartbeats remain visible but require an Agent upgrade.
After the v3 fleet is established, pure Gateway releases that preserve this
contract do not require an Agent rollout. Preserve the complete state directory
through deployment and rollback; deleting only `config-revision.json` can let
an old revision appear newer to Agents sharing an existing model root. Follow
`docs/model-lifecycle-rollout.md` for the repeatable simulation, preflight,
verification, and rollback gates. The production definitions do not require or
start Tailscale.

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
- Canonical model names are immutable identities. For a version upgrade, create
  a new concrete model with a unique `model_dir` and add its canonical key to
  the intended tag policies. New blank models start disabled with
  `min_loaded: 0`: apply the draft, explicitly enable the new model, warm at
  least one replica to ready, validate the concrete name, then retarget the
  stable alias.
- Retargeting only `model_aliases.<alias>` is a gateway hot update: workers keep
  serving their canonical models and new requests immediately resolve through
  the new pointer. The gateway permits an unready target for cold-start and
  recovery cases, but Config Ops exposes zero-ready status so routine rollouts
  can remain ready-first.
- Billing defaults to the canonical actual-cost ledger. Use
  `/api/billing?...&group_by=alias` or the Service aliases UI view to aggregate
  by each request's persisted `requested_model`; this never consults the
  alias's current target. Alias-view occupancy is an allocated report, not the
  actual runtime ledger, and includes a canonical-version breakdown.
- If a desired public service name is already a canonical model, first disable
  and fully unload it, ready the replacement canonical to its floor, then use
  the dedicated Config Ops `Promote service name` confirmation. Do not create a
  colliding alias in Advanced YAML. Keep the returned archive identity for the
  guarded rollback; rollback is refused after the alias or touched placement
  policies have been edited.
- Roll back by repointing the alias to the old, still-ready canonical model.
  Versioned directories are not deleted automatically, preserving the old
  artifact for this pointer rollback. Editing `model_dir` in place is different:
  it changes the runtime path and follows loaded-worker restart/reload impact.
- Service-name promotion rollback is stricter than ordinary alias retargeting:
  retain the returned archive ID and use the dedicated rollback action before
  later namespace or touched-policy edits. A conflict is intentional and
  prevents rollback from overwriting newer operator work.

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
