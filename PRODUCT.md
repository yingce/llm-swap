# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

LLM Swap is used by an internal technical team whose members routinely move
through the full operational workflow: configuring models, rolling out model
versions, observing runtime state, and diagnosing serving problems. The UI is
an operator console rather than a customer-facing product.

## Product Purpose

LLM Swap provides one control plane for operating model serving across many GPU
workers. It should let an operator determine whether the serving fleet is
healthy, understand which worker, GPU, model, or request needs attention, and
then carry out the appropriate model or worker operation safely.

Success means that system health and actionable problems are legible first,
while configuration, rollout, traffic, and diagnostic workflows remain
available without requiring operators to reconstruct state across separate
tools.

## Positioning

LLM Swap combines a gateway-owned serving control plane with thin, mostly
stateless workers and worker-local llama-swap runtimes. Canonical model versions
remain explicit operational identities, while stable public aliases can move
between versions for ready-first rollout and rollback.

## Operating Context

Operators work with gateway health, worker heartbeats, GPU utilization, model
artifact and runtime state, request traffic, event history, configuration
drafts, and rollout impacts. Their common path spans observation and action:
find a problem, identify the affected resource, inspect relevant evidence, and
perform or validate a control-plane change.

## Capabilities and Constraints

- Gateway owns routing, scheduling, concurrency, queueing, retries, replica
  policy, request accounting, and operator actions.
- Worker agents install artifacts, render local llama-swap configuration,
  report state, and restart llama-swap only when permitted by the gateway.
- The UI exposes fleet and model health, workers and GPUs, traffic, requests,
  events, billing when its records store is enabled, structured config
  operations, and read-only advanced YAML.
- Canonical model names are immutable. Model upgrades use a new canonical model
  and optionally retarget a stable alias after the new version is ready.
- Metrics and PostgreSQL records storage are optional. Their absence must not
  imply that core model serving is unavailable.
- Common low-risk operations may execute directly. Higher-risk operations must
  show their scope and require explicit confirmation before execution.
- The UI authenticates with the gateway agent token.

## Evidence on Hand

- Current architecture and product vocabulary: `docs/agents/project-map.md`
- Existing admin application: `ui/admin/src/main.tsx`
- Existing gateway and UI API contracts: `ui/admin/src/api.ts` and
  `internal/gateway/server.go`
- Existing standalone routes and route restoration tests:
  `ui/admin/src/routes.ts` and `ui/admin/src/routes.test.ts`
- No customer-facing claims, benchmarks, testimonials, or brand assets are
  present and future design work must not invent them.

## Product Principles

1. Lead with fleet health and actionable exceptions, not a wall of equal-weight
   metrics.
2. Keep observation, diagnosis, and action connected around the same worker,
   model, or request identity.
3. Preserve the full configuration-to-runtime workflow for one technical
   audience instead of splitting the product into disconnected personas.
4. Make operational risk visible before consequential changes and keep the
   resulting state or event easy to verify.
5. Degrade optional observability features honestly without presenting them as
   failures of the serving plane.
