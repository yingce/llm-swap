# Agent Version and Protocol Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show only the human Agent release version in the Workers UI and derive upgrade warnings exclusively from the Agent heartbeat protocol.

**Architecture:** Gateway defines an inclusive supported Agent protocol range and maps each heartbeat to `compatible`, `legacy`, `upgrade_agent`, or `upgrade_gateway`. The UI displays the Agent release version normally and renders compatibility guidance only for non-compatible states; commit metadata stays in the API for diagnostics but is not shown.

**Tech Stack:** Go 1.23, React 19, TypeScript, Vitest, Vite.

## Global Constraints

- Release-version strings and Git commits never participate in compatibility decisions.
- Gateway-only changes do not change the Agent release version and do not require an Agent rollout.
- Retain `BuildInfo.commit`, `BuildInfo.build_time`, and `BuildInfo.protocol_version` in heartbeat and UI APIs.
- Normal Workers UI shows only **Agent version**; compatible workers receive no `latest` or `old` label.
- Missing protocol is `legacy`; below the supported range is `upgrade_agent`; above it is `upgrade_gateway`; inside it is `compatible`.
- Modify source tests before production code and rebuild `internal/gateway/admin_dist` after UI changes.

---

### Task 1: Derive Agent compatibility from protocol range

**Files:**
- Modify: `internal/gateway/ui.go`
- Modify: `internal/gateway/ui_test.go`

**Interfaces:**
- Consumes: `protocol.BuildInfo.ProtocolVersion int` and `protocol.AgentProtocolVersion`.
- Produces: `agentVersionStatus(build protocol.BuildInfo) string` returning `legacy`, `upgrade_agent`, `compatible`, or `upgrade_gateway`.

- [ ] **Step 1: Replace the exact-version test with protocol-range cases**

  Update `TestUIStatusIncludesAgentBuildAndVersionStatus` so a worker reporting a deliberately different `Version` but an in-range protocol expects `compatible`. Add workers with protocol `min-1`, `max+1`, and zero and assert `upgrade_agent`, `upgrade_gateway`, and `legacy`. Continue asserting that the commit is preserved in `AgentBuild`.

- [ ] **Step 2: Run the Gateway test and verify RED**

  Run:

  ```bash
  go test ./internal/gateway -run 'TestUIStatusIncludesAgentBuildAndVersionStatus' -count=1
  ```

  Expected: FAIL because current code returns `outdated` for a different release version and does not distinguish the two protocol directions.

- [ ] **Step 3: Implement explicit inclusive protocol bounds**

  Add unexported Gateway constants near `agentVersionStatus`:

  ```go
  const (
      minSupportedAgentProtocolVersion = 2
      maxSupportedAgentProtocolVersion = protocol.AgentProtocolVersion
  )
  ```

  Replace the release comparison with:

  ```go
  func agentVersionStatus(build protocol.BuildInfo) string {
      switch {
      case build.ProtocolVersion <= 0:
          return "legacy"
      case build.ProtocolVersion < minSupportedAgentProtocolVersion:
          return "upgrade_agent"
      case build.ProtocolVersion > maxSupportedAgentProtocolVersion:
          return "upgrade_gateway"
      default:
          return "compatible"
      }
  }
  ```

  Do not inspect `build.Version` or `build.Commit` in this function.

- [ ] **Step 4: Run focused and package tests**

  Run:

  ```bash
  go test ./internal/gateway -run 'TestUIStatusIncludesAgentBuildAndVersionStatus' -count=1
  go test ./internal/gateway -count=1
  ```

  Expected: PASS.

- [ ] **Step 5: Commit the backend behavior**

  ```bash
  git add internal/gateway/ui.go internal/gateway/ui_test.go
  git commit -m "fix: compare agent protocol compatibility"
  ```

### Task 2: Show Agent version and conditional upgrade guidance

**Files:**
- Modify: `ui/admin/src/api.ts`
- Modify: `ui/admin/src/api.test.ts`
- Modify: `ui/admin/src/domain/testFixtures.ts`
- Modify: `ui/admin/src/workers/workerView.ts`
- Modify: `ui/admin/src/workers/workerView.test.ts`
- Modify: `ui/admin/src/workers/WorkersPage.tsx`

**Interfaces:**
- Consumes: `WorkerStatus.agent_version_status` with the four Task 1 values.
- Produces: Worker diagnostics that always display `agent_build.version`, never display `agent_build.commit`, and show a compatibility row only when status is not `compatible`.

- [ ] **Step 1: Write failing UI behavior tests**

  Change fixtures from `current/outdated` to the new protocol statuses. In `workerView.test.ts`, assert:

  ```ts
  expect(row.agent_version).toBe("1.0.0");
  expect(row.diagnostics).toBe("agent 1.0.0 · heartbeat 2s ago · scrape failures 0");
  expect(workerHasDiagnosticWarning(compatibleWorker)).toBe(false);
  expect(workerHasDiagnosticWarning(upgradeAgentWorker)).toBe(true);
  ```

  Add source/render assertions that the diagnostics popover contains `Agent version`, omits `Commit`, `latest`, and `old`, and maps:

  ```text
  legacy -> Legacy Agent; upgrade Agent
  upgrade_agent -> Upgrade Agent
  upgrade_gateway -> Upgrade Gateway
  compatible -> no compatibility row
  ```

  Update `api.test.ts` to expect `compatible` and verify a missing status normalizes to `legacy`.

- [ ] **Step 2: Run focused UI tests and verify RED**

  Run:

  ```bash
  npm test -- --run src/api.test.ts src/workers/workerView.test.ts
  ```

  Expected: FAIL because the current types and UI still use `current/outdated`, append status to the version, and render Commit/latest/old.

- [ ] **Step 3: Implement the new UI contract**

  Change the API union to:

  ```ts
  export type AgentCompatibilityStatus = "compatible" | "upgrade_agent" | "upgrade_gateway" | "legacy";
  ```

  Use it for `WorkerStatus.agent_version_status`. Keep the normalizer fallback as `legacy`.

  In `buildWorkerRows`, set:

  ```ts
  agent_version: worker.agent_build.version || "unknown",
  diagnostics: `agent ${worker.agent_build.version || "unknown"} · heartbeat ${heartbeatAge} · scrape failures ${worker.scrape_failures}`,
  ```

  Make `workerHasDiagnosticWarning` treat only `compatible` as compatibility-clean. In `WorkersPage.tsx`, replace Build/Commit rows with `Agent version` and a conditional compatibility row. Use a small pure label helper with the exact guidance from Step 1 so it can be tested without reading Git metadata.

- [ ] **Step 4: Run focused and complete UI tests**

  Run:

  ```bash
  npm test -- --run src/api.test.ts src/workers/workerView.test.ts
  npm test -- --run
  ```

  Expected: PASS with all UI test files green.

- [ ] **Step 5: Build production assets**

  Run:

  ```bash
  npm run build
  ```

  Normalize generated `internal/gateway/admin_dist/index.html` to repository LF line endings if Windows Vite emits CRLF-only noise. Verify the generated asset hashes referenced by `index.html` exist.

- [ ] **Step 6: Commit UI source and embedded assets**

  ```bash
  git add ui/admin/src internal/gateway/admin_dist
  git commit -m "fix: show agent protocol compatibility"
  ```

### Task 3: Document release and compatibility ownership

**Files:**
- Modify: `docs/agents/project-map.md`
- Modify: `docs/model-lifecycle-rollout.md`
- Test: `internal/buildinfo/buildinfo_test.go`

**Interfaces:**
- Consumes: Task 1 protocol statuses and existing `BuildInfo` fields.
- Produces: operator guidance separating Agent release version, source commit, and protocol compatibility.

- [ ] **Step 1: Strengthen build metadata regression coverage**

  Extend `internal/buildinfo/buildinfo_test.go` to set:

  ```go
  Version, Commit, BuildTime = "2026.08.08.1", "abc123", "2026-08-08T00:00:00Z"
  ```

  Assert that `Current(...)` preserves distinct `Version` and `Commit` values. This protects release automation from collapsing the fields again.

- [ ] **Step 2: Run the focused build-info test**

  Run:

  ```bash
  go test ./internal/buildinfo -count=1
  ```

  Expected: PASS; this is a characterization guard for already-separated fields rather than a production behavior change.

- [ ] **Step 3: Update operator documentation**

  Document all of the following in `project-map.md` and the rollout guide:

  - `LLMSWAP_BUILD_VERSION` is the human Agent release identifier.
  - `LLMSWAP_BUILD_COMMIT` is the exact source SHA and must not be copied into the version field.
  - Gateway-only releases do not require Agent publication.
  - Compatibility uses the supported protocol range, with the four status meanings.
  - Coordinated protocol rollout order is Gateway supporting old+new, then Agents, then raising the minimum.

- [ ] **Step 4: Run final verification**

  Run:

  ```bash
  go test ./... -count=1
  go test -race ./internal/gateway ./internal/buildinfo -count=1
  npm test -- --run
  npm run build
  git diff --check
  ```

  Expected: all commands PASS and the worktree contains only intended documentation or normalized generated-asset changes.

- [ ] **Step 5: Commit documentation and regression guard**

  ```bash
  git add internal/buildinfo/buildinfo_test.go docs/agents/project-map.md docs/model-lifecycle-rollout.md internal/gateway/admin_dist
  git commit -m "docs: explain agent release compatibility"
  ```
