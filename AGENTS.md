# LLM Swap Agent Notes

Read `docs/agents/project-map.md` before making non-trivial changes. It is the
authoritative map of modules, configuration semantics, persistence, deployment
assets, and regression tests.

## Product Overview

LLM Swap is an internal operations platform for serving many LLM versions
across a fleet of GPU workers. Its purpose is to give operators one place to:

- configure models and worker eligibility;
- route OpenAI-compatible inference traffic to ready replicas;
- observe worker health, GPU usage, requests, events, traffic, and billing;
- safely warm, unload, drain, and roll out model versions.

The product is an operator console, not a customer-facing application. Favor
correct operational state, explicit impact, and reversible actions over visual
novelty or hidden automation.

## Core Model

- **Gateway** is the control-plane authority. It owns routing, concurrency,
  queues, retries, request accounting, replica policy, worker registry state,
  metrics, persistence, and dashboard APIs.
- **Worker agent** is deliberately thin and mostly stateless. It installs
  artifacts, renders local runtime configuration, reports local state, and
  restarts llama-swap only when Gateway permits it.
- **llama-swap** is the worker-local runtime switcher. Gateway does not launch
  model runtimes directly; it proxies to ready worker llama-swap endpoints.
- **FRP transport** exposes worker-local llama-swap to Gateway. Gateway owns
  transport leases and only routes to a worker after it advertises a ready URL.

Do not move Gateway-owned scheduling, queueing, active-request accounting,
retry behavior, or replica policy into Worker code.

## Domain Rules

- A canonical model name is an immutable concrete model identity used for
  policy, worker state, metrics, request records, and billing.
- A `model_alias` is a stable public name pointing directly to one canonical
  model. Aliases cannot chain or collide with canonical model names.
- Version upgrades create a new canonical model and optional `model_dir`; move
  an alias only after the new model is ready. Roll back by retargeting the alias.
- Request routing uses only ready replicas. Loading or starting replicas occupy
  capacity but are not routable.
- `min_loaded` is a Gateway-owned replica floor. `max_loaded` is a ceiling;
  omitted `max_loaded` has automatic-expansion semantics, not a fixed default.
- Models with `min_loaded: 0` are opportunity-cache replicas: retain them while
  spare capacity exists, but evict them first when another model needs space.
- Worker tags express hardware eligibility; model/tag capacity expresses the
  per-ready-worker concurrency and queue policy. Keep these concepts separate.

## Product and UI Expectations

- Lead with fleet health and actionable exceptions rather than a wall of equal
  priority metrics.
- Keep observation, diagnosis, and action connected around worker, model, and
  request identity.
- Show the impact and scope of consequential actions before execution, then
  make the resulting event or state observable.
- Optional services such as historical metrics or record storage must degrade
  honestly. Their absence must not look like serving-plane failure.
- Config Ops is a structured operations workflow; Advanced YAML is inspection
  and copy support, not the default editing surface.
- Canonical model identity is immutable in the UI. Do not introduce rename or
  implicit-copy behavior that breaks versioned rollout semantics.

## Development Constraints

- Do not place real tokens, credentials, DSNs, or production-only values in
  source, examples, tests, documentation, or terminal output.
- Preserve user changes in a dirty working tree. Never reset, checkout, delete,
  or stage unrelated files unless explicitly asked.
- Use tests first for behavioral changes. Go tests are the primary regression
  net; installer tests require a POSIX shell.
- Keep request and persistent accounting canonical: alias requests may retain a
  `requested_model` field, but capacity, metrics, billing, and records belong
  to the resolved canonical model.
- Keep metric labels low-cardinality. Do not add request IDs, prompt content,
  raw URLs, or unbounded user values as labels.
- Preserve JSONL logs even when optional Postgres storage is enabled; Postgres
  is a query source, not permission to remove local debugging history.
- Gateway configuration hot-apply rules matter. Process-level changes require
  restart; runtime-affecting model changes affect only workers currently
  running that model; alias changes are Gateway hot updates.
- Admin UI assets are embedded under `internal/gateway/admin_dist`. When
  changing `ui/admin`, run its tests and production build so embedded assets
  match source.

## Validation

```bash
go test ./...
go test ./internal/gateway -count=1
go test ./internal/agent -count=1
go test ./internal/config -count=1
go test ./scripts -count=1
```

Main entry points:

- Gateway: `cmd/gateway/main.go`
- Worker agent: `cmd/agent/main.go`
- Worker installer: `scripts/install-worker.sh`
- Gateway Compose template: `deploy/production/docker-compose.yaml`
- FRP Worker Compose template: `deploy/worker-frp/compose.yaml`
