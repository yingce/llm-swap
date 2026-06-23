# Conservative Predictive Scale-Out Design

Date: 2026-06-24

## Purpose

This design adds conservative, predictive model scale-out to the gateway
control loop. It builds on the existing Placement module without changing the
main architecture:

- gateway owns routing, queues, active counts, retries, replica policy, and
  control actions;
- worker agents stay thin and mostly stateless;
- llama-swap remains the per-worker runtime switcher;
- client requests route only to ready workers and never wait on a new runtime
  startup.

The goal is lower steady-state latency and better worker utilization without
thrashing models after short traffic bursts.

## Confirmed Policy

The default mode is stable and conservative.

Scale-out should not require `queue_full`. A full queue is a late signal. The
control loop should instead consider recent demand, wait pressure, active
utilization, token volume, priority, and loaded replica count.

The control loop should prefer keeping the current cluster stable:

- use empty idle workers before unloading another model;
- count `starting` and `loading` replicas as occupied capacity;
- avoid scale-out on a single burst;
- unload another model only when the target model's demand score is clearly
  higher than the victim model's keep score plus a switch cost;
- preserve `min_loaded` floors and newly-started protection windows.

## Demand Signals

Add an in-memory pressure tracker inside gateway. It records model pressure
observations from existing request and queue paths.

Signals per model:

- request count in a recent rolling window;
- token count in the same window;
- active request depth at observation time;
- ready replica count;
- occupied replica count;
- queue events:
  - `admitted_after_wait`;
  - `queue_full`;
  - `queue_timeout`;
- wait durations, including p95 wait;
- last access time.

The first implementation can keep this state in memory only. Request log replay
already restores coarse access counters after restart, but rolling queue
pressure can start empty after gateway restart.

Default window:

- 5 minutes for recent pressure;
- 30 seconds reconciler cadence, matching the existing control loop.

## Scoring

Each reconciler cycle computes a `ModelDemand` score for every model.

Inputs:

- configured model priority;
- recent request rate;
- recent token rate;
- p95 wait time;
- count of waited requests;
- count of queue full and timeout events;
- active utilization against ready replicas;
- ready replica count;
- occupied replica count;
- time since last access.

Conservative scoring rules:

- no recent access means no predictive scale-out;
- one short burst is not enough;
- waited requests raise the score, but queue full is not required;
- `starting` and `loading` replicas reduce pressure because capacity is already
  being added;
- higher priority increases demand score;
- demand decays when traffic stops.

Each currently loaded model on a worker gets a `KeepScore`.

Inputs:

- model priority;
- `min_loaded` protection;
- recent worker-model access;
- recent worker-model token volume when available;
- active request count;
- newly-started `ProtectedUntil`;
- whether the model is an opportunity cache (`min_loaded=0`).

Conservative keep rules:

- active workers are not eviction candidates;
- `min_loaded` floors are protected;
- protected replicas are not evicted;
- `min_loaded=0` replicas are cheaper to evict than protected replicas;
- older and lower-priority replicas are cheaper to evict.

Scale-out with eviction is allowed only when:

```text
target_demand_score > victim_keep_score + switch_cost
```

The initial switch cost should be intentionally high and constant. It can become
configurable later after real traffic data is available.

## Control Actions

Extend Placement control actions:

```text
ControlActionWarm
ControlActionUnload
```

`ControlActionWarm` means "ask the worker-local llama-swap to load or preload
this model asynchronously." It does not route the current request to that worker.

Action planning order:

1. Enforce explicit hard `max_loaded` unloads, as today.
2. Protect or free capacity for models below `min_loaded`, as today.
3. Compute predictive demand scores.
4. For high-demand models below effective `max_loaded`:
   - choose an empty idle worker first;
   - otherwise choose an idle worker with the lowest keep score if the demand
     score beats keep score plus switch cost.
5. Plan at most one warm or unload action per reconciler cycle.

This keeps behavior observable and avoids multi-worker churn from one cycle.

## Worker Selection

A worker is eligible for warm scale-out only when:

- healthy;
- artifact for the target model is ready;
- tag policy allows the target model;
- no active gateway-owned request is running on the worker;
- the target model is not already running, loading, or starting on the worker.

Worker preference:

1. empty idle worker;
2. worker with only opportunity-cache model;
3. worker with lower-priority, less-recently-used model above its floor.

Starting/loading replicas count as occupied and block duplicate warm actions.

## Executing Warm Actions

Gateway still does not start model runtimes directly. It asks llama-swap through
the worker's llama-swap URL.

Implementation should add a narrow method to `LlamaSwapClient`:

```text
Load(ctx, baseURL, model)
```

The method should call the llama-swap model load/preload endpoint used by the
installed llama-swap version. If the exact endpoint is not available in tests,
keep the method behind a fakeable interface and document the endpoint used.

Warm action execution should record gateway worker events:

- `gateway_model_warm_start`;
- `gateway_model_warm_done`;
- `gateway_model_warm_error`.

The worker agent will later observe the running state through local
`/running` and report `model_loaded` / `model_state_changed`.

## Logging and Metrics

Add structured gateway logs:

- `pressure_observation`
  - model;
  - request ID when available;
  - queue result;
  - wait ms;
  - active and queued depth;
  - ready and occupied replicas.
- `control_action_planned`
  - action;
  - model;
  - worker;
  - demand score;
  - victim model if any;
  - keep score;
  - switch cost;
  - reason.
- `control_action_done` and `control_action_error`.

Metrics can reuse existing queue and request metrics initially. A later UI pass
can expose model demand and keep scores.

## Request Path Boundaries

The request proxy path may record pressure observations, but it must not:

- synchronously start a new worker;
- route to starting/loading workers;
- unload another model;
- block waiting for scale-out.

If no ready worker is available and queue policy rejects the request, the
client-facing error remains an ordinary capacity error. The async loop may use
that signal in later cycles.

## Testing Strategy

Pressure tracker tests:

- records waited requests and computes p95 wait;
- expires old observations outside the window;
- ignores models with no recent access.

Placement tests:

- empty idle worker is selected for warm scale-out before eviction;
- starting/loading target replica prevents duplicate warm action;
- no warm action is planned for a single low-pressure burst;
- higher priority sustained demand can evict an opportunity-cache model;
- protected and active replicas are not evicted;
- explicit `max_loaded` blocks warm scale-out.

Reconciler tests:

- executes one warm action per cycle;
- records warm start/done/error events;
- does not warm when the selected worker is active by execution time;
- preserves existing unload behavior.

Proxy tests:

- queue observations are recorded with ready and occupied replica counts;
- request routing still rejects starting replicas.

## Non-Goals

- Do not add an external database.
- Do not add machine-learning prediction.
- Do not expose many tuning knobs in the first implementation.
- Do not move policy into worker agents.
- Do not replace llama-swap or start runtimes directly from gateway.
- Do not build UI controls for scale-out scores in this slice.

## Rollout Notes

The first rollout should keep thresholds conservative. Operators should be able
to inspect logs before relying on aggressive automatic expansion. If the warm
endpoint behaves differently across llama-swap versions, leave scale-out logging
in place and gate warm execution behind a small compatibility layer.
