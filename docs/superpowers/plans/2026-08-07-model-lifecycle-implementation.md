# Model Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make shared artifact replacement safe, add stable alias billing reports, and support one-time promotion of an existing canonical name into a service alias.

**Architecture:** Gateway supplies a monotonic configuration revision. Agents use a blob lock for downloads and a model-directory fence for commits. Billing keeps canonical records immutable while adding request-time alias aggregation. Name promotion is a guarded Gateway transaction, never a generic alias collision.

**Tech Stack:** Go 1.23, filesystem locks, Postgres records store, React/Vite admin UI.

## Global Constraints

- Gateway owns config, placement, queues, routing, and replica policy.
- Canonical model remains the default actual-cost identity.
- Do not permit ordinary alias/canonical collisions.
- Preserve ready directories on failed or cancelled installs.

---

### Task 1: Version Agent configuration

**Files:** `internal/protocol/agent.go`, `internal/gateway/server.go`, `internal/protocol/agent_test.go`, `internal/gateway/workers_test.go`

**Produces:** `AgentConfigResponse.ConfigRevision int64` serialized as `config_revision`.

- [ ] Write endpoint/JSON tests for a positive revision.
- [ ] Run `go test ./internal/protocol ./internal/gateway -run 'AgentConfig|ConfigResponse' -count=1`; verify failure.
- [ ] Populate the response from the Gateway configuration snapshot version.
- [ ] Re-run targeted tests; commit `feat: expose gateway config revision to agents`.

### Task 2: Fence shared artifact commits and cancel stale work

**Files:** `internal/agent/artifacts.go`, `internal/agent/artifacts_lock_unix.go`, `internal/agent/artifacts_lock_windows.go`, `internal/agent/reconcile.go`, `internal/agent/artifacts_test.go`, `internal/agent/reconcile_test.go`

**Produces:** per-model cancellable install state and a model-directory fence containing revision plus artifact fingerprint.

- [ ] Write a two-reconciler shared-root test: block an old download, publish a newer revision, assert the old context cancels and cannot replace the marker.
- [ ] Run the test; verify failure under current artifact-identity-only locking.
- [ ] Keep the blob lock for download cache; add a `model_dir` lock and recheck the fence/marker before `replaceDir`.
- [ ] Cancel an in-flight install when its key or revision is superseded; emit `artifact_install_cancelled`.
- [ ] Add tests for reuse of an already-correct directory and preservation after failed/cancelled work.
- [ ] Run `go test ./internal/agent -count=1`; commit `fix: fence shared model artifact installs`.

### Task 3: Add billing grouping by request-time alias

**Files:** `internal/gateway/billing.go`, `internal/gateway/records_store.go`, `internal/gateway/billing_test.go`, `docs/billing-api.md`

**Produces:** `group_by=canonical|alias`, defaulting to canonical.

- [ ] Write a failing test: requests through one alias to two canonical versions aggregate into one alias row, while direct canonical requests remain unattributed.
- [ ] Parse and validate `group_by`; aggregate from persisted `requested_model`, never current alias targets.
- [ ] Include canonical version breakdown; label any request-share occupancy allocation as allocated rather than actual cost.
- [ ] Run `go test ./internal/gateway -run Billing -count=1`; update API documentation; commit `feat: add alias billing aggregation`.

### Task 4: Present billing views

**Files:** billing components under `ui/admin/src`, generated `internal/gateway/admin_dist`, relevant UI tests.

- [ ] Add Actual models / Service aliases selector, defaulting to Actual models.
- [ ] Show alias canonical-version breakdown and allocation wording.
- [ ] Run UI tests, production build, then `go test ./internal/gateway -count=1`; commit `feat: present service alias billing view`.

### Task 5: Promote an existing canonical name into a service alias

**Files:** `internal/gateway/config_manager.go`, `internal/gateway/config_admin.go`, `internal/gateway/config_admin_test.go`, Config Ops UI sources, `docs/agents/project-map.md`.

**Produces:** audited `Promote service name` and rollback transactions.

- [ ] Write failing tests for disabled/idle old canonical and ready target success; reject enabled, running, installing, or unready cases.
- [ ] Implement one atomic transaction that archives the old active definition, removes it, creates `service_name -> target_model`, and records an operator event.
- [ ] Implement reversal as one transaction: remove alias and restore archived definition.
- [ ] Add UI confirmation showing old model, ready target, and rollback impact.
- [ ] Build UI assets; run `go test ./internal/gateway -count=1`; commit `feat: promote canonical name to service alias`.

### Task 6: Full verification and rollout

**Files:** `docs/agents/project-map.md`, deployment/operator documentation where needed.

- [ ] Run `go test ./...` and `go test ./scripts -count=1`.
- [ ] Stage a two-Agent shared-root simulation and a config-change cancellation simulation before production.
- [ ] Deploy Gateway/Agent together because the new revision field is a protocol addition; verify cancellation, marker state, warm completion, canonical billing, alias billing, promotion, and rollback from events.
- [ ] Commit `docs: document safe model lifecycle rollout`.
