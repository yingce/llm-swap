# Phase 1 Gap Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the remaining Phase 1 runtime gaps in gateway scheduling, agent heartbeat state, unload reconciliation, and worker metrics scraping.

**Architecture:** Keep all state in-process. Gateway request limiting is a keyed limiter used by the proxy path. Agent `/running` collection is a small client dependency used by the reconciler. Gateway unload reconciliation is conservative and only unloads extra idle replicas. Metrics scraping remains normalized counters rather than raw worker metric merging.

**Tech Stack:** Go 1.23, standard `net/http`, standard synchronization primitives, existing Prometheus registry, Docker Compose test runner.

---

### Task 1: Gateway Queue And Concurrency Limits

**Files:**
- Create: `internal/gateway/limits.go`
- Modify: `internal/gateway/proxy.go`
- Modify: `internal/gateway/server.go`
- Test: `internal/gateway/limits_test.go`
- Test: `internal/gateway/proxy_test.go`

- [x] Write failing tests for immediate acquire/release, queue-full, queue-timeout, and proxy `queue_full`.
- [x] Implement `QueueLimiter` with keyed active and queued counts.
- [x] Add `limiter *QueueLimiter` to `Server`.
- [x] Acquire model limit before scheduling, then tag and worker limits after worker selection.
- [x] Release all gates exactly once on success, retry, upstream error, or client cancellation.
- [x] Skip a queue-full worker candidate when another worker can still serve the request.
- [x] Run `docker compose run --rm dev go test ./internal/gateway`.

### Task 2: Agent Reports llama-swap Running Models

**Files:**
- Create: `internal/agent/llamaswap_client.go`
- Modify: `internal/agent/reconcile.go`
- Test: `internal/agent/reconcile_test.go`

- [x] Write a failing test that a reconcile heartbeat includes `/running` models.
- [x] Implement a small `RunningModelsClient` that parses common llama-swap `/running` shapes.
- [x] Add the client dependency to `Reconciler`.
- [x] Include running models in `BuildHeartbeat`.
- [x] Keep reconcile alive and report `last_error` when `/running` fails.
- [x] Run `docker compose run --rm dev go test ./internal/agent`.

### Task 3: Gateway Idle Unload Reconciler

**Files:**
- Create: `internal/gateway/reconcile.go`
- Modify: `cmd/gateway/main.go`
- Test: `internal/gateway/reconcile_test.go`

- [x] Write failing tests that excess idle replicas unload and active workers are preserved.
- [x] Implement `LoadedReconciler`.
- [x] Wire a background loop in `cmd/gateway/main.go`.
- [x] Run `docker compose run --rm dev go test ./internal/gateway`.

### Task 4: Performance Metrics Dedupe

**Files:**
- Modify: `internal/gateway/metrics_scrape.go`
- Modify: `internal/gateway/metrics.go`
- Test: `internal/gateway/metrics_test.go`

- [x] Write a failing test for `/api/performance` sample dedupe.
- [x] Add `PullPerformance` with bounded seen-key storage.
- [x] Export `llm_swap_gateway_worker_performance_samples_total`.
- [x] Invoke activity and performance pulls from `/metrics`.
- [x] Run `docker compose run --rm dev go test ./internal/gateway`.

### Task 5: Final Verification

**Files:**
- Modify only if verification finds a defect.

- [x] Run `gofmt -w cmd internal`.
- [x] Run `docker compose run --rm dev go test ./...`.
- [x] Inspect `git status --short`.

### Task 6: Stateless Agent Runtime Configuration

**Files:**
- Create: `internal/config/agent_runtime.go`
- Modify: `cmd/agent/main.go`
- Modify: `examples/agent.yaml`
- Test: `internal/config/agent_runtime_test.go`
- Test: `internal/agent/render_test.go`
- Test: `internal/gateway/proxy_test.go`

- [x] Add tests for Viper/pflag/env/config priority.
- [x] Add tests for explicit `swap_url`, Tailscale-derived URL, and local-IP fallback.
- [x] Add tests proving worker concurrency limits come from gateway tag policy.
- [x] Remove worker-side `concurrencyLimit` rendering from agent output.
- [x] Wire `cmd/agent` to the Viper runtime loader.
- [x] Run `docker compose run --rm dev go test ./...`.

### Task 7: Gateway Runtime Configuration

**Files:**
- Create: `internal/config/gateway_runtime.go`
- Modify: `cmd/gateway/main.go`
- Modify: `examples/gateway.yaml`
- Test: `internal/config/gateway_runtime_test.go`
- Test: `internal/gateway/proxy_test.go`

- [x] Add gateway runtime defaults under `/opt/llmswap`.
- [x] Add Viper/pflag support for `--addr`, `--proxy-attempts`, and token environment overrides.
- [x] Move proxy retry count into gateway config.
- [x] Wire `cmd/gateway` to the runtime loader.
- [x] Add tests proving configured proxy attempts are honored.
