# Replica Cooldown And Observability Design

Date: 2026-06-24

## Goal

Improve gateway availability and latency when a model replica is reported
`ready` but fails during proxying. The gateway should quickly stop routing new
requests to the failing `worker_id + model` pair, retry other ready replicas,
and expose enough telemetry to explain the decision.

## Scope

This design covers:

- short-lived replica-level cooldown after retryable proxy failures;
- automatic cooldown clearing after a successful request or TTL expiry;
- scheduler exclusion of cooled-down replicas;
- structured logs, metrics, and UI fields for cooldown and retry state.

This design does not change:

- worker-side model lifecycle ownership;
- agent heartbeat and artifact reporting;
- llama-swap model unload behavior;
- predictive warm or `min_loaded` reconciliation rules.

## Behavior

The gateway keeps an in-memory table keyed by `worker_id + model`. Each entry
contains:

- failure reason;
- first and latest failure time;
- failure count;
- cooldown expiry time.

When a proxy attempt fails with a retryable condition, the gateway marks only
that specific replica unhealthy for a short TTL. Initial TTL is 30 seconds. The
affected replica is excluded from request placement for that model while the
cooldown is active.

Retryable conditions are:

- connection or request errors that the existing proxy retry path already treats
  as retryable;
- upstream 429, 502, 503, or 504;
- platform HTML 404 responses already classified as retryable.

Non-retryable model responses, such as a real OpenAI-style 400 or JSON 404, do
not create a cooldown.

If a later request to the same `worker_id + model` succeeds, the gateway clears
the cooldown immediately. If no request succeeds first, the entry expires after
the TTL and the replica becomes eligible again. The next failure can mark it
again.

Cooldown affects request routing only. It does not make the worker unhealthy,
does not block agent heartbeat, and does not trigger model unload by itself.

## Scheduler Integration

Placement receives a cooldown snapshot and excludes active cooldown entries from
ready candidates. Starting and loading replicas remain non-routable as they are
today.

The gateway still retries within the configured proxy attempt budget. If one
ready replica fails and another ready replica exists, the request can move to
the next replica. If all replicas are cooled down or excluded, the existing
error path returns an unavailable or retry-exhausted response.

Cooldown should not be used by reconcile as direct evidence to unload a model.
Reconcile can continue to observe regular worker heartbeat, running state, and
gateway-owned policy.

## Observability

Structured logs:

- `replica_unhealthy_marked`: emitted when a cooldown is set or extended.
- `replica_unhealthy_cleared`: emitted when success clears an active cooldown.
- `proxy_retry`: emitted for each retryable failed attempt before the next
  placement attempt.
- `proxy_retry_exhausted`: emitted when retryable failures consume the attempt
  budget.

Common fields:

- `request_id`
- `model`
- `worker_id`
- `reason`
- `status_code` when available
- `cooldown_seconds`
- `cooldown_until`
- `attempt`

Metrics:

- gauge: active unhealthy replicas labeled by `worker_id`, `model`, `reason`;
- counter: cooldown marks labeled by `model`, `worker_id`, `reason`;
- counter: cooldown clears labeled by `model`, `worker_id`, `reason`;
- counter: proxy retries labeled by `model`, `worker_id`, `reason`.

UI:

- model worker rows show cooldown state for the affected replica;
- worker cards show cooled-down model pills with remaining seconds and reason;
- recent worker/gateway events include mark and clear events.

## Testing

Unit tests should cover:

- cooldown table mark, clear, expiry, and snapshot behavior;
- scheduler does not pick a cooled-down ready replica;
- proxy marks cooldown on retryable request errors;
- proxy marks cooldown on retryable upstream status;
- proxy does not mark cooldown on non-retryable client/model errors;
- successful proxy clears cooldown;
- UI status includes cooldown details.

Integration-style gateway tests should verify that a request retries from a
failed ready replica to another ready replica and that the failed replica is not
selected for a subsequent request during cooldown.

## Rollout

The initial implementation uses a hard-coded 30 second TTL to keep behavior
simple. A later change can expose this as gateway config if production shows the
TTL needs per-model or per-tag tuning.

The change is gateway-only. Workers and installed runtimes do not need to be
redeployed.
