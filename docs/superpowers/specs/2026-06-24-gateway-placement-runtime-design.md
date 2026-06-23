# Gateway Placement and Runtime Design

Date: 2026-06-24

## Purpose

This design fixes several architectural pressure points in the current
single-gateway, many-worker llm-swap control plane:

- scheduling decisions are spread across request proxying, loaded-replica
  reconciliation, worker registry state, and access accounting;
- request routing and scale-out intent are mixed in the same path;
- `min_loaded` and `max_loaded` semantics are too easy to misread;
- worker restarts must be scoped to loaded models affected by runtime config
  changes;
- the agent reconciler is growing too many responsibilities;
- runtime-specific command generation is inferred from free-form shell strings;
- the gateway UI is embedded as one large Go string.

The design keeps the existing architecture: gateway owns control-plane policy,
worker agents stay thin, and llama-swap remains the worker-local runtime
switcher.

## Confirmed Semantics

### Request Placement

Current client requests must be routed only to workers that are immediately
usable for that request.

- A ready same-model worker is routable.
- A starting or loading runtime is not routable.
- A cold or other-model worker is only routable when no ready same-model worker
  exists and the placement policy explicitly allows cold start for the current
  path.
- Scale-out may be recommended by request pressure, but the current request does
  not wait for or route to the scale-out worker.

### Async Control Actions

Scale-out, preload, unload, and model-cache eviction are async gateway control
actions. They run from a periodic control loop, not directly in the request
proxy path.

The async loop may use recent request and queue metrics:

- `admitted_after_wait`;
- `queue_full`;
- `queue_timeout`;
- p95 wait time;
- ready replica count;
- occupied replica count;
- model request rate;
- token throughput;
- configured priority and capacity constraints.

### Replica Counts

`min_loaded` is the active target floor. The async control loop tries to keep at
least this many ready replicas when capacity allows.

`max_loaded` is a ceiling:

- omitted `max_loaded` means automatic expansion, bounded by available workers,
  other models' `min_loaded`, and priority protection;
- explicit `max_loaded: N` is a hard upper bound;
- `loading` and `starting` replicas count as occupied, so the gateway does not
  duplicate startup work.

`min_loaded=0` means opportunity cache:

- the model is not actively protected as a floor;
- if loaded and there is spare capacity, it can remain loaded;
- when another model needs capacity, `min_loaded=0` replicas are preferred
  eviction candidates;
- recent traffic can still keep such a model resident and can trigger auto
  scale-out within capacity limits.

Newly started replicas get a short protection window. During this window they
are not evicted merely because no request has arrived yet.

### Config Changes and Restarts

Gateway config changes must not cause full worker restarts by default. The
agent writes updated llama-swap config, but marks restart pending only when the
changed config affects currently running, loading, or starting models.

Gateway-only fields do not restart workers:

- `priority`;
- `min_loaded`;
- `max_loaded`;
- `max_concurrency`;
- `max_queue`;
- `queue_timeout_ms`.

Runtime-affecting fields can require restart when the model is active on that
worker:

- `runtime`;
- `runtime_args`;
- `run`;
- `cmd_stop`;
- `check_endpoint`;
- `ttl`;
- artifact changes for a running model.

Artifact changes for unloaded models install the new artifact and write config
without restarting llama-swap.

## Placement Module

Add a gateway-internal Placement module that owns cluster placement reasoning.
It should provide a deep interface over these details:

- worker health and scrape backoff;
- allowed tag policies;
- artifact readiness;
- current running model states;
- active request counts;
- loaded and occupied replica counts;
- `min_loaded`, automatic or explicit `max_loaded`, and priority protection;
- recent access and queue pressure.

The request path calls a request-oriented method:

```text
PickReadyWorker(model, now, exclude) -> PlacementDecision
```

The async control loop calls a control-oriented method:

```text
PlanControlActions(now) -> []ControlAction
```

`PlacementDecision` includes the selected worker, ready and occupied replica
counts, effective ceiling information, candidate reasons, and non-routable
rejection reasons for logs and UI.

`ControlAction` can represent future async operations such as unload, warm,
prepare scale-out, or hold. The initial implementation can keep the existing
unload behavior but route its decisions through this module.

## Request Path

The request proxy path becomes:

```text
read and normalize request
acquire model queue/concurrency gate
ask Placement for a routable worker
acquire tag and worker queue/concurrency gates
acquire worker active slot
proxy to worker llama-swap
record request log, metrics, and access stats
```

It should not:

- start scale-out directly;
- send traffic to starting/loading runtimes;
- unload a model to make room inline;
- decide global model-cache policy itself.

## Async Control Loop

The existing loaded-replica reconciler becomes a control loop backed by
Placement.

Control actions should respect:

- active request count equals zero before unload;
- the runtime is ready, not starting/loading;
- newly started replica protection window;
- `min_loaded` protection;
- automatic `max_loaded` capacity calculations;
- model priority;
- recent access and queue pressure.

Eviction preference is:

1. `min_loaded=0` and no recent pressure;
2. replicas above `min_loaded`;
3. lower priority models;
4. less recently accessed models;
5. workers better suited for the target model.

## Runtime Configuration

Add explicit runtime configuration while preserving the old `run` escape hatch.

Recommended model shape:

```yaml
models:
  qwen:
    runtime: sglang
    runtime_args:
      served_model_name: qwen
      trust_remote_code: true
      tp_size: 1
      dtype: bfloat16
      max_total_tokens: 2048
      disable_cuda_graph: true
      attention_backend: triton
      sampling_backend: pytorch
```

Runtime adapters generate llama-swap commands:

- inject `PORT=${PORT}`;
- inject `/opt/llmswap/bin/<runtime>.server`;
- inject rendered model path;
- add runtime-specific defaults such as check endpoint;
- keep existing command logging wrapper.

Supported runtimes:

- `sglang`;
- `vllm`;
- `llamacpp`;
- `custom`.

Compatibility rules:

- `runtime` with `runtime_args` is the preferred path;
- existing `run` remains supported for backward compatibility;
- `runtime: custom` requires `run`;
- non-custom models should not specify both `runtime` and `run` unless a later
  migration explicitly defines override precedence.

Runtime adapters also become the place for request normalization decisions. For
example, SGLang-specific `top_k: 0` normalization and media-part conversion
should be selected by `runtime: sglang`, not by searching the shell command
string.

## Agent Reconciler Split

The agent remains a thin worker-side controller, but its internal modules should
be split for locality:

- `ConfigSync`: fetch tag-scoped config from gateway;
- `ArtifactManager`: install artifacts, shared locks, progress events, markers;
- `LlamaSwapConfigManager`: render and write llama-swap config, then determine
  whether active runtime config changed;
- `RunningModelObserver`: read local `/running` and emit loaded, state changed,
  and unloaded events;
- `RestartCoordinator`: pending marker, gateway restart allowance, supervisor
  restart, health verification;
- `EventBuffer`: local event buffering and heartbeat acknowledgement.

`Reconciler` remains the orchestrator:

```text
fetch config
install artifacts
fetch running models
render config
decide restart need
heartbeat
restart when gateway allows
```

This split must not move routing, queues, active counts, retries, or replica
policy into the worker.

## UI Embed

Move gateway UI static assets out of a Go raw string and embed them:

```text
internal/gateway/ui_static/
  index.html
  app.js
  styles.css
internal/gateway/ui_embed.go
```

Gateway routes:

- `/ui` returns `index.html`;
- `/ui/assets/app.js`;
- `/ui/assets/styles.css`;
- `/ui/status`;
- `/ui/events`.

The UI continues to use agent-token authentication.

## Migration Plan

1. Add Placement tests around current scheduling regressions.
2. Move existing scheduler selection into Placement without changing behavior.
3. Split request placement from async control recommendations.
4. Change `max_loaded` omission semantics from `min_loaded` to automatic
   capacity-bounded expansion.
5. Add replica protection metadata and eviction ordering.
6. Add runtime config fields and runtime adapters while preserving existing
   `run` configs.
7. Move SGLang request normalization behind runtime detection.
8. Split agent reconciler internals without changing external heartbeat
   protocol.
9. Move UI static files to embedded assets.

Each step should keep the Go test suite passing before proceeding to the next.

## Testing Strategy

Placement tests:

- ready worker is always chosen before cold or starting workers;
- starting/loading workers count as occupied but are not routable;
- omitted `max_loaded` allows automatic expansion only when capacity remains;
- `min_loaded=0` models are retained when spare capacity exists;
- `min_loaded=0` models are evicted before protected replicas when capacity is
  needed;
- newly started replicas are protected from immediate eviction.

Proxy tests:

- request path does not route to starting workers;
- queue pressure is logged but does not synchronously send the current request
  to a scale-out worker;
- runtime-specific request normalization is selected from `runtime`, not `run`.

Agent tests:

- gateway-only config changes do not mark restart pending;
- unloaded model runtime changes write config without restart;
- active model runtime changes mark restart pending;
- artifact changes for inactive models install without restart;
- artifact changes for active models mark restart pending after install.

UI tests:

- embedded `/ui` serves HTML;
- embedded JS and CSS routes serve non-empty content;
- `/ui/status` and `/ui/events` remain token protected.

## Non-Goals

- Do not add an external database in this change.
- Do not move queueing, retry, active counts, or placement policy into worker
  agents.
- Do not bypass llama-swap by having gateway start model runtimes directly.
- Do not remove existing `run` configs until a separate migration is planned.
