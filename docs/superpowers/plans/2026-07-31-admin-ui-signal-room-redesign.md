# LLM Swap Admin UI Signal Room Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the equal-weight admin dashboard with the approved exception-first Signal Room Console while preserving every existing route, API contract, authentication path, model/configuration semantic, and operational endpoint.

**Architecture:** Keep `/ui/status` as the only global five-second live-state feed, derive fleet/attention/relationship views with tested pure functions, lazy-load route-specific capabilities, and decompose the React entry into an application shell, resource pages, shared primitives, and focused configuration modules. The Gateway remains unchanged except for rebuilding its tracked embedded UI assets and retaining route/static-asset regression coverage.

**Tech Stack:** React 19, TypeScript 5.8 strict mode, Vite 7, Vitest 4, YAML, CSS, Go embedded assets and Go gateway tests. Add no frontend runtime dependency.

---

## Scope and execution rules

- Preserve the user-owned root `dist/` directory. The only generated assets in scope are `internal/gateway/admin_dist/**`.
- Do not add Gateway or Agent endpoints. Do not expose FRP as a product resource.
- Do not infer a Model-to-specific-GPU assignment. A Worker may branch to reported GPUs and Models, but those branches are siblings.
- Keep canonical Model names immutable. Keep create, alias, disabled-model, runtime, Tag Policy, YAML omission, dry-run, and apply behavior unchanged.
- Use tests first for every behavior change. Mechanical moves get a green-before/green-after verification.
- Immediately before the first visual implementation task, read `C:/Users/admin/.agents/skills/impeccable/references/craft-floor.md` completely and apply its constraints.
- Run the Impeccable detector only once, after the implementation stabilizes.

## Task 1: Establish a green baseline and split the entry point mechanically

**Files:**

- Create: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/main.tsx`
- Modify: `ui/admin/src/configOpsModelModal.test.ts`

- [ ] From `ui/admin`, run the existing baseline before editing:

```bash
npm test
npm run build
```

Expected: all Vitest files pass; TypeScript and Vite build succeed. Record any pre-existing failure instead of hiding it in the refactor.

- [ ] Move the current application body from `main.tsx` into `app/App.tsx` without changing rendered markup, state ownership, request timing, or action semantics. Export the application component:

```tsx
export function App() {
  // existing App body, unchanged
}
```

All imports that remain in `App.tsx` change from `./api` to `../api`, and similarly for `modelAliases`, `modelLifecycle`, and `routes`.

- [ ] Reduce `main.tsx` to the mount boundary:

```tsx
import React from "react";
import { createRoot } from "react-dom/client";

import { App } from "./app/App";
import "./styles.css";

createRoot(document.getElementById("llmswap-admin-root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
```

- [ ] Update `configOpsModelModal.test.ts` to read `./app/App.tsx`. Preserve every existing modal contract assertion at this stage.

- [ ] Run:

```bash
npm test
npm run build
```

Expected: identical green result; only file ownership changes.

- [ ] Commit:

```bash
git add ui/admin/src/main.tsx ui/admin/src/app/App.tsx ui/admin/src/configOpsModelModal.test.ts internal/gateway/admin_dist
git commit -m "refactor(ui): split admin app entry point"
```

## Task 2: Align TypeScript with the existing status contract

**Files:**

- Modify: `ui/admin/src/api.ts`
- Create: `ui/admin/src/api.test.ts`

- [ ] Add a failing compile-time/runtime normalization test with a representative response containing the fields already emitted by `internal/gateway/ui.go`:

```ts
import { describe, expect, it } from "vitest";
import { normalizeStatus, type StatusResponse } from "./api";

describe("status normalization", () => {
  it("preserves replica cooldown and model provisioning fields", () => {
    const status = normalizeStatus({
      generated_at: "2026-07-31T08:00:00Z",
      summary: {
        total_workers: 1, healthy_workers: 1, draining_workers: 0,
        available_models: 0, configured_models: 1,
        underprovisioned_models: 1, active_requests: 0,
        stale_workers: 0, workers_with_errors: 0, recent_error_events: 0
      },
      models: [{
        name: "joyfox", priority: 1, min_loaded: 1, max_loaded: 1,
        max_concurrency: 1, max_queue: 1, queue_timeout_ms: 1000, ttl: 60,
        available: false, ready_workers: 0, running_workers: 0,
        installing_workers: 1, missing_workers: 0, error_workers: 0,
        artifact: { object: "joyfox.tar.gz", kind: "tar_gz" },
        availability_note: "installing", traffic: emptyTraffic(),
        worker_statuses: [{
          worker_id: "worker-1", artifact_status: "installing", health: "healthy",
          cooldown_active: true, cooldown_reason: "load_failed",
          cooldown_remaining_seconds: 12
        }]
      }],
      workers: [{
        id: "worker-1", tags: ["gpu-4090"], health: "healthy", state: "ready",
        llama_swap_url: "http://worker-1:6006", active_requests: 0,
        running_models: [], gpu_devices: [], allowed_models: ["joyfox"],
        capacity: { max_concurrency: 1, max_queue: 1 }, scrape_failures: 0,
        replica_cooldowns: [{
          worker_id: "worker-1", model: "joyfox", reason: "load_failed",
          first_failure: "2026-07-31T07:58:00Z",
          last_failure: "2026-07-31T07:59:00Z", failure_count: 2,
          cooldown_until: "2026-07-31T08:00:12Z", remaining_seconds: 12
        }],
        agent_build: {}, agent_version_status: "current"
      }],
      events: []
    } satisfies StatusResponse);

    expect(status.models[0].installing_workers).toBe(1);
    expect(status.workers[0].replica_cooldowns[0].model).toBe("joyfox");
  });
});
```

Provide a local `emptyTraffic()` fixture with every required numeric traffic field set to zero.

- [ ] Run `npm test -- api.test.ts` and confirm it fails because the existing TypeScript types omit current Go response fields and `normalizeStatus` is private.

- [ ] Add only fields that already exist in `uiModelStatus`, `uiModelWorker`, and `uiWorker`:

```ts
export type ReplicaCooldown = {
  worker_id: string;
  model: string;
  reason: string;
  first_failure: string;
  last_failure: string;
  failure_count: number;
  cooldown_until: string;
  remaining_seconds: number;
};

// ModelStatus additions
queue_timeout_ms: number;
ttl: number;
installing_workers: number;
missing_workers: number;
error_workers: number;

// worker_statuses additions
cooldown_reason?: string;
cooldown_remaining_seconds?: number;
cooldown_until?: string;

// WorkerStatus additions
scrape_backoff_until?: string;
replica_cooldowns: ReplicaCooldown[];

// WorkerEvent additions already emitted by uiAgentEvent
time?: string;
kind?: string;
```

Export `normalizeStatus` for focused testing and normalize `replica_cooldowns` to `[]` for backward-compatible stored/cached payloads.

- [ ] Run `npm test -- api.test.ts` then `npm test && npm run build`.

Expected: focused test turns green; full suite and strict build remain green.

- [ ] Commit:

```bash
git add ui/admin/src/api.ts ui/admin/src/api.test.ts
git commit -m "test(ui): align status response types"
```

## Task 3: Lock fleet, attention, and relationship semantics with pure functions

**Files:**

- Create: `ui/admin/src/domain/testFixtures.ts`
- Create: `ui/admin/src/domain/fleet.ts`
- Create: `ui/admin/src/domain/fleet.test.ts`
- Create: `ui/admin/src/domain/attention.ts`
- Create: `ui/admin/src/domain/attention.test.ts`
- Create: `ui/admin/src/domain/relationships.ts`
- Create: `ui/admin/src/domain/relationships.test.ts`

- [ ] Create shared test factories in `testFixtures.ts`:

```ts
export function worker(overrides: Partial<WorkerStatus> = {}): WorkerStatus;
export function model(overrides: Partial<ModelStatus> = {}): ModelStatus;
export function status(overrides: Partial<StatusResponse> = {}): StatusResponse;
```

Factories must produce fully valid strict TypeScript objects and accept explicit IDs, tags, models, GPU devices, health, and events.

- [ ] Write failing `fleet.test.ts` cases for:

  - one Worker in one Tag;
  - one Worker contributing to multiple Tag summaries without being duplicated inside a single Tag;
  - GPU totals from `memory_total_mib` and `memory_used_mib`;
  - tag-scoped available/configured Model counts based on Workers belonging to that Tag;
  - active request and attention totals;
  - deterministic Tag-name ordering.

- [ ] Implement the fleet contract:

```ts
export type TagFleetSummary = {
  tag: string;
  workerIds: string[];
  healthyWorkers: number;
  totalWorkers: number;
  gpuDevices: number;
  gpuMemoryTotalMiB: number;
  gpuMemoryUsedMiB: number;
  configuredModels: number;
  availableModels: number;
  activeRequests: number;
  attentionCount: number;
};

export function buildTagFleetSummaries(
  status: StatusResponse,
  attention: AttentionItem[]
): TagFleetSummary[];
```

Use `worker.allowed_models` as the existing API's Tag-scoped configuration evidence. A Model is available in a Tag only when one of that Tag's Workers has a `worker_statuses` entry whose `running_state` is `ready`; do not reuse the global `model.available` flag for a Tag.

- [ ] Write failing `attention.test.ts` cases covering every approved category and exclusion:

  - stale/unhealthy Worker;
  - `health_problem`, `last_error`, and `needs_restart` without duplicate items for the same reason;
  - positive `min_loaded` with too few ready replicas;
  - artifact error;
  - zero-warm (`min_loaded === 0`) with no replicas is not an incident by itself;
  - disabled Models passed through the explicit options set are excluded;
  - outdated/legacy Agent version;
  - recent event with `error`;
  - current-session saved configuration that requires Gateway restart;
  - severity/category order, newest evidence, then stable identity;
  - records/metrics capability failures are not input to this function.

- [ ] Implement:

```ts
export type AttentionKind =
  | "worker-health"
  | "worker-problem"
  | "model-readiness"
  | "agent-version"
  | "worker-event"
  | "gateway-restart";

export type AttentionItem = {
  id: string;
  kind: AttentionKind;
  severity: "critical" | "warning";
  resourceType: "worker" | "model" | "gateway";
  resourceId: string;
  reason: string;
  evidenceAt?: string;
  workerId?: string;
  modelName?: string;
};

export type AttentionOptions = {
  disabledModels?: ReadonlySet<string>;
  gatewayRestartRequired?: boolean;
};

export function buildAttentionItems(
  status: StatusResponse,
  options?: AttentionOptions
): AttentionItem[];
```

Use a stable ID assembled from category/resource/evidence identity. Treat the event order as evidence recency when timestamps tie or are absent. Never use requests, Billing, or metrics errors as input.

- [ ] Write failing `relationships.test.ts` cases for default Tag aggregation, selected Tag expansion, Worker incident selection, Model incident selection, unknown resources, and the explicit absence of GPU-to-Model edges.

- [ ] Implement:

```ts
export type RelationshipNode = {
  id: string;
  kind: "tag" | "worker" | "gpu" | "model";
  label: string;
  state: "healthy" | "warning" | "critical" | "neutral";
};

export type RelationshipEdge = {
  from: string;
  to: string;
  kind: "membership" | "reports-gpu" | "runs-model" | "allows-model";
};

export type RelationshipView = {
  scope: "cluster" | "tag" | "attention";
  nodes: RelationshipNode[];
  edges: RelationshipEdge[];
};

export function buildRelationshipView(args: {
  status: StatusResponse;
  tag?: string;
  attention?: AttentionItem;
}): RelationshipView;
```

Assert in tests that no edge has `from` starting with `gpu:` and `to` starting with `model:` (or the reverse), and that no node kind/string mentions FRP.

- [ ] Run:

```bash
npm test -- domain
npm test
npm run build
```

Expected: domain tests and all existing tests pass.

- [ ] Commit:

```bash
git add ui/admin/src/domain
git commit -m "feat(ui): derive fleet attention and relationships"
```

## Task 4: Introduce grouped navigation, live-status state, and route load isolation

**Files:**

- Create: `ui/admin/src/app/navigation.ts`
- Create: `ui/admin/src/app/navigation.test.ts`
- Create: `ui/admin/src/app/liveStatus.ts`
- Create: `ui/admin/src/app/liveStatus.test.ts`
- Create: `ui/admin/src/app/routeData.ts`
- Create: `ui/admin/src/app/routeData.test.ts`
- Create: `ui/admin/src/app/AppShell.tsx`
- Modify: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/routes.ts`
- Modify: `ui/admin/src/routes.test.ts`

- [ ] Add failing navigation tests for the four intent groups and unchanged paths:

```ts
expect(NAVIGATION_GROUPS.map((group) => group.label)).toEqual([
  "", "Resources", "Observe", "Change"
]);
expect(NAVIGATION_GROUPS.flatMap((group) => group.items.map((item) => item.label))).toEqual([
  "Overview", "Models", "Workers", "Requests", "Activity", "Billing",
  "Configuration", "Advanced"
]);
```

Keep the existing `Tab` IDs and path mapping so no route contract changes.

- [ ] Add failing live-status reducer tests for `loading`, first success, refresh success, refresh failure with stale data retained, and first-load failure:

```ts
export type LiveStatusState = {
  data: StatusResponse | null;
  loading: boolean;
  refreshing: boolean;
  error: string;
  lastSuccessfulAt?: string;
};

export function reduceLiveStatus(
  state: LiveStatusState,
  event: LiveStatusEvent
): LiveStatusState;
```

The reducer must retain `data` and `lastSuccessfulAt` on refresh errors.

- [ ] Add failing route-data tests for the endpoint plan:

```ts
expect(dataNeedsForTab("dashboard")).toEqual([]);
expect(dataNeedsForTab("models")).toEqual(["config"]);
expect(dataNeedsForTab("workers")).toEqual([]);
expect(dataNeedsForTab("requests")).toEqual(["requests"]);
expect(dataNeedsForTab("events")).toEqual(["events"]);
expect(dataNeedsForTab("billing")).toEqual(["billing", "config"]);
expect(dataNeedsForTab("configOps")).toEqual(["config"]);
expect(dataNeedsForTab("advanced")).toEqual(["config"]);
```

`Overview` consumes recent events already included in `/ui/status`; it must not eagerly call `/ui/events` or `/ui/requests`.

- [ ] Implement `AppShell` with grouped navigation, current-route state, Gateway state, last success, refresh progress, and a manual refresh control. Its content contract is:

```tsx
type AppShellProps = {
  tab: Tab;
  statusState: LiveStatusState;
  onSelectTab(tab: Tab): void;
  onRefresh(): void;
  children: React.ReactNode;
};
```

- [ ] Refactor `App` so it:

  - starts and cleans up one `/ui/status` five-second poll;
  - loads only the data in `dataNeedsForTab(tab)` when a route first becomes active;
  - keeps per-route `loading`, `error`, and successful data separate;
  - retains `popstate`, `pushState`, direct-route, and unknown-route replacement behavior;
  - refreshes status plus only the currently active route-specific source after actions;
  - does not convert a Billing, requests, events, config, or metrics failure into the global shell error.

- [ ] Run focused tests, full frontend tests, and build:

```bash
npm test -- app routes.test.ts
npm test
npm run build
```

Expected: route contracts remain unchanged and initial `/ui` loading no longer triggers optional/history endpoint calls.

- [ ] Commit:

```bash
git add ui/admin/src/app ui/admin/src/routes.ts ui/admin/src/routes.test.ts
git commit -m "refactor(ui): isolate shell and route data loading"
```

## Task 5: Build the shared Signal Room interaction primitives

**Prerequisite:** Read `C:/Users/admin/.agents/skills/impeccable/references/craft-floor.md` completely immediately before editing these files.

**Files:**

- Create: `ui/admin/src/components/AttentionList.tsx`
- Create: `ui/admin/src/components/ConfirmDialog.tsx`
- Create: `ui/admin/src/components/DetailPanel.tsx`
- Create: `ui/admin/src/components/EmptyState.tsx`
- Create: `ui/admin/src/components/ResourceList.tsx`
- Create: `ui/admin/src/components/StatusIndicator.tsx`
- Create: `ui/admin/src/components/confirmDialog.test.ts`
- Modify: `ui/admin/src/styles.css`

- [ ] Write a failing source/accessibility contract test for `ConfirmDialog` that verifies `role="alertdialog"`, `aria-modal`, title/description IDs, Escape handling, focusable-element cycling, initial focus, and focus restoration. This mirrors the existing no-new-dependency testing approach.

- [ ] Implement the dialog contract without adding a UI package:

```tsx
type ConfirmDialogProps = {
  open: boolean;
  title: string;
  description: React.ReactNode;
  confirmLabel: string;
  tone?: "danger" | "warning";
  busy?: boolean;
  returnFocusRef?: React.RefObject<HTMLElement | null>;
  onConfirm(): void;
  onCancel(): void;
};
```

- [ ] Implement small typed primitives. They own presentation, not domain decisions:

```tsx
type StatusIndicatorProps = {
  state: "healthy" | "warning" | "critical" | "neutral";
  label: string;
};

type EmptyStateProps = {
  kind: "empty" | "unavailable" | "error";
  title: string;
  detail: React.ReactNode;
};

type ResourceListItem = {
  id: string;
  primary: React.ReactNode;
  secondary?: React.ReactNode;
  status?: React.ReactNode;
};

type ResourceListProps = {
  ariaLabel: string;
  items: ResourceListItem[];
  selectedId?: string;
  onSelect(id: string): void;
};

type DetailPanelProps = {
  title: React.ReactNode;
  children: React.ReactNode;
};

type AttentionListProps = {
  items: AttentionItem[];
  selectedId?: string;
  onInspect(id: string): void;
};
```

- [ ] Replace the initial CSS foundation with the documented tokens while leaving legacy selectors available until their page is migrated:

```css
:root {
  --canvas: #eef1f3;
  --surface: #f8f9f8;
  --surface-raised: #ffffff;
  --rail: #26343b;
  --rail-muted: #9eabb0;
  --ink: #182328;
  --muted: #657278;
  --border: #ccd4d7;
  --route: #0f7f82;
  --healthy: #2f7d52;
  --warning: #a76516;
  --critical: #b13a32;
  --radius: 4px;
  --font-ui: Inter, ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
  --font-mono: "SFMono-Regular", Consolas, "Liberation Mono", monospace;
}
```

Do not fetch Inter; the stack intentionally falls through to installed/system fonts. Do not add gradients, glow, glass, decorative topology, or persistent surface shadows.

- [ ] Run `npm test -- components && npm test && npm run build`.

- [ ] Commit:

```bash
git add ui/admin/src/components ui/admin/src/styles.css
git commit -m "feat(ui): add signal room primitives"
```

## Task 6: Implement the exception-first Overview

**Files:**

- Create: `ui/admin/src/pages/overviewView.ts`
- Create: `ui/admin/src/pages/overviewView.test.ts`
- Create: `ui/admin/src/pages/OverviewPage.tsx`
- Modify: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/styles.css`

- [ ] Write failing view-model tests for fleet conclusions:

  - no successful status: `Gateway state unavailable`;
  - successful status, no attention: `Serving plane operational`;
  - attention exists: `Serving plane degraded · N items need attention`;
  - refresh error with data: degraded/unavailable freshness text while the previous topology remains visible;
  - optional capability notices do not affect the conclusion.

- [ ] Implement:

```ts
export type OverviewView = {
  conclusion: string;
  conclusionState: "healthy" | "warning" | "critical" | "neutral";
  attention: AttentionItem[];
  tags: TagFleetSummary[];
  relationships: RelationshipView;
};

export function buildOverviewView(args: {
  status: StatusResponse;
  selectedTag?: string;
  selectedAttentionId?: string;
  disabledModels?: ReadonlySet<string>;
  gatewayRestartRequired?: boolean;
}): OverviewView;
```

- [ ] Implement `OverviewPage` in the approved Exception Rail composition:

  - plain-language conclusion and freshness first;
  - horizontal/high-priority attention rail controlling selection;
  - default cluster summary grouped by Tag;
  - selected Tag or incident relationship view;
  - compact healthy Worker, available Model, active request, and aggregate GPU memory values;
  - concise resource rows linking through `onNavigate` to Models or Workers.

Render relationship data as an accessible ordered layout with orthogonal CSS connectors. At compact widths, render the same nodes as a labeled dependency trail. Do not add FRP nodes or GPU-to-Model connectors.

- [ ] Replace the legacy `Dashboard` branch in `App` with `OverviewPage`. Remove request/event preview dependencies; the page may use only `status.events` for recent Worker evidence.

- [ ] Run:

```bash
npm test -- overview domain
npm test
npm run build
```

- [ ] Commit:

```bash
git add ui/admin/src/pages/OverviewPage.tsx ui/admin/src/pages/overviewView.ts ui/admin/src/pages/overviewView.test.ts ui/admin/src/app/App.tsx ui/admin/src/styles.css
git commit -m "feat(ui): add exception first overview"
```

## Task 7: Replace Models with a searchable master-detail page

**Files:**

- Create: `ui/admin/src/pages/modelView.ts`
- Create: `ui/admin/src/pages/modelView.test.ts`
- Create: `ui/admin/src/pages/ModelsPage.tsx`
- Modify: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/styles.css`

- [ ] Write failing pure view tests for:

  - canonical Models sorted stably;
  - aliases reversed from `model_aliases` to their canonical target;
  - runtime from the current configuration;
  - disabled Models included only when the explicit filter is active;
  - eligible Workers limited to Workers whose `allowed_models` includes the Model;
  - selected Model preserved if still present and otherwise falling back to the first visible Model;
  - case-insensitive search across canonical name, alias, runtime, and Worker identity.

- [ ] Implement the model view contract:

```ts
export type ModelResourceView = {
  name: string;
  aliases: string[];
  runtime?: string;
  disabled: boolean;
  status?: ModelStatus;
  eligibleWorkers: WorkerStatus[];
};

export function buildModelResources(args: {
  status: StatusResponse | null;
  config: ConfigResponse | null;
  includeDisabled: boolean;
  query: string;
}): ModelResourceView[];
```

- [ ] Implement `ModelsPage` with a searchable resource list and selected detail containing availability, ready/running replicas, aliases, artifact state, eligible Workers, traffic, and lifecycle actions.

- [ ] Preserve action semantics:

  - Warm is enabled only after an explicit eligible Worker is selected and runs immediately with inline progress/result.
  - Unload requires `ConfirmDialog` naming both Model and Worker.
  - Configuration entry navigates to `/ui/config`; it does not rename a canonical Model.
  - Disabled state is a compact marker/filter, never a full-width banner.

- [ ] Remove the legacy `Models` and `ModelActions` functions only after the new branch passes strict build.

- [ ] Run `npm test -- modelView modelLifecycle modelAliases && npm test && npm run build`.

- [ ] Commit:

```bash
git add ui/admin/src/pages/ModelsPage.tsx ui/admin/src/pages/modelView.ts ui/admin/src/pages/modelView.test.ts ui/admin/src/app/App.tsx ui/admin/src/styles.css
git commit -m "feat(ui): add model master detail workspace"
```

## Task 8: Replace Workers with the dense resource ledger

**Files:**

- Create: `ui/admin/src/pages/workerView.ts`
- Create: `ui/admin/src/pages/workerView.test.ts`
- Create: `ui/admin/src/pages/WorkersPage.tsx`
- Modify: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/styles.css`

- [ ] Write failing view tests for filtering/sorting by health, Tag, Agent version, running Model, and Worker ID; stable selection; aggregate GPU state; recent events scoped by `worker_id`; and connectivity detail limited to existing `llama_swap_url`, scrape, heartbeat, and health fields.

- [ ] Implement:

```ts
export type WorkerResourceView = {
  worker: WorkerStatus;
  recentEvents: WorkerEvent[];
  gpuMemoryTotalMiB: number;
  gpuMemoryUsedMiB: number;
  runningModelNames: string[];
};

export function buildWorkerResources(args: {
  status: StatusResponse;
  query: string;
  health?: string;
  tag?: string;
}): WorkerResourceView[];
```

- [ ] Implement the master-detail page:

  - dense Worker ledger showing health, Tags, GPU state, active requests, running Models, restart state, and Agent version;
  - detail sections for heartbeat/health, GPU devices, running/allowed Models, artifacts/cooldowns, Agent build, llama-swap connectivity, and recent Worker events;
  - no FRP lease, renewal, or assigned-port UI.

- [ ] Preserve action semantics: Drain uses a confirmation naming routing impact; Undrain retains current direct behavior with inline progress/result.

- [ ] Remove legacy `Workers` and `GPUDeviceView` only after the new page is green.

- [ ] Run `npm test -- workerView && npm test && npm run build`.

- [ ] Commit:

```bash
git add ui/admin/src/pages/WorkersPage.tsx ui/admin/src/pages/workerView.ts ui/admin/src/pages/workerView.test.ts ui/admin/src/app/App.tsx ui/admin/src/styles.css
git commit -m "feat(ui): add worker resource ledger"
```

## Task 9: Isolate Requests, Activity, and Billing capability states

**Files:**

- Create: `ui/admin/src/pages/observeView.ts`
- Create: `ui/admin/src/pages/observeView.test.ts`
- Create: `ui/admin/src/pages/RequestsPage.tsx`
- Create: `ui/admin/src/pages/ActivityPage.tsx`
- Create: `ui/admin/src/pages/BillingPage.tsx`
- Modify: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/styles.css`

- [ ] Write failing tests for local filtering/detail view models and capability classification:

```ts
export type CapabilityState =
  | { kind: "available" }
  | { kind: "unavailable"; message: string }
  | { kind: "error"; message: string };

expect(classifyCapabilityError("records store is not enabled")).toEqual({
  kind: "unavailable",
  message: "Historical records are not enabled on this Gateway. Core model serving remains operational."
});
```

Match case-insensitively and tolerate the Gateway's existing `Billing unavailable:` prefix before the records-store message. Do not classify `unauthorized`, network errors, or arbitrary 5xx errors as neutral unavailable capability.

- [ ] Implement Requests and Activity as dense, horizontally scrollable tables with sticky headers, filters, stable identity columns, selected-row inspector, and current `Load more` pagination.

- [ ] Implement Billing as an Observe page preserving ranges, pricing edits, totals, Model rows, application rows, request-cost evidence, and current config-draft semantics. Show records-store-disabled as local neutral `EmptyState kind="unavailable"` and never feed it into Overview attention.

- [ ] Remove legacy `Events`, `Requests`, `Billing`, `EventList`, and `RequestList` after the extracted pages are green. Move formatting helpers used only by an Observe page into that page or `observeView.ts`; do not create a generic dumping-ground utility file.

- [ ] Run `npm test -- observeView && npm test && npm run build`.

- [ ] Commit:

```bash
git add ui/admin/src/pages/RequestsPage.tsx ui/admin/src/pages/ActivityPage.tsx ui/admin/src/pages/BillingPage.tsx ui/admin/src/pages/observeView.ts ui/admin/src/pages/observeView.test.ts ui/admin/src/app/App.tsx ui/admin/src/styles.css
git commit -m "feat(ui): redesign observe workspaces"
```

## Task 10: Extract Configuration and Advanced without changing YAML semantics

**Files:**

- Create: `ui/admin/src/configuration/configDraft.ts`
- Create: `ui/admin/src/configuration/configDraft.test.ts`
- Create: `ui/admin/src/configuration/ModelCreateModal.tsx`
- Create: `ui/admin/src/configuration/ModelEditor.tsx`
- Create: `ui/admin/src/configuration/ModelAliasesEditor.tsx`
- Create: `ui/admin/src/configuration/TagPolicyEditor.tsx`
- Create: `ui/admin/src/configuration/ImpactReview.tsx`
- Create: `ui/admin/src/pages/ConfigurationPage.tsx`
- Create: `ui/admin/src/pages/AdvancedPage.tsx`
- Modify: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/configOpsModelModal.test.ts`
- Modify: `ui/admin/src/styles.css`

- [ ] First move the existing YAML/draft functions into `configDraft.ts` and export their current contracts:

```ts
export type EditableGatewayConfig = { /* current exact fields */ };
export function cloneEditableConfig(config: EditableGatewayConfig): EditableGatewayConfig;
export function toEditableConfig(response: ConfigResponse): EditableGatewayConfig;
export function toGatewayConfigView(draft: EditableGatewayConfig): GatewayConfigView;
export function renderDraftYAML(baseYaml: string, draft: EditableGatewayConfig): string;
```

- [ ] Before changing behavior, add focused tests copied from real supported config shapes for:

  - omitted `max_loaded` remains omitted after unrelated edits;
  - explicit `max_loaded: 0` remains explicit when auto mode is off;
  - runtime/runtime args round trip;
  - disabled, `model_dir`, billing, aliases, and Tag Policies round trip;
  - clone operations do not mutate the source.

- [ ] Extract the existing modal and editors with behavior preserved. Update `configOpsModelModal.test.ts` to read the new component sources and keep all focus, Escape, inert-background, no-copy, no-delete, supported-runtime, and reusable-modal assertions.

- [ ] Implement `ConfigurationPage` as resource selector + structured editor + persistent draft state + impact review. Keep Model creation in the same Modal Form and keep canonical names immutable.

- [ ] Gate Apply on a successful dry run of the exact current rendered YAML. Invalidate the dry-run result on every draft edit. The confirmation must show:

  - change count and loaded-resource impacts;
  - alias target changes;
  - whether Worker restart is required;
  - whether the apply mode only saves until Gateway restart.

Represent the gate with explicit state rather than message parsing:

```ts
type ValidatedDraft = {
  yaml: string;
  result: ConfigDryRunResponse;
};

const canApply = validatedDraft?.yaml === renderedConfigYaml && validatedDraft.result.valid;
```

- [ ] Implement `AdvancedPage` as read-only current draft YAML with copy support. It must use the same `renderedConfigYaml`, never a second draft.

- [ ] Remove the legacy configuration functions from `App.tsx` only after all draft/modal tests pass.

- [ ] Run:

```bash
npm test -- configDraft configOpsModelModal modelLifecycle modelAliases
npm test
npm run build
```

- [ ] Commit:

```bash
git add ui/admin/src/configuration ui/admin/src/pages/ConfigurationPage.tsx ui/admin/src/pages/AdvancedPage.tsx ui/admin/src/app/App.tsx ui/admin/src/configOpsModelModal.test.ts ui/admin/src/styles.css
git commit -m "refactor(ui): extract configuration workspace"
```

## Task 11: Finish responsive behavior, embedded assets, and acceptance verification

**Files:**

- Modify: `ui/admin/src/styles.css`
- Modify: `DESIGN.md`
- Modify: `.impeccable/surfaces/ui-admin-src-main-tsx.md`
- Modify: `internal/gateway/admin_dist/index.html`
- Replace generated hashes under: `internal/gateway/admin_dist/assets/**`
- Modify only if regression coverage requires it: `internal/gateway/ui_test.go`

- [ ] Finish layout breakpoints and accessibility behavior:

  - `>= 1280px`: persistent rail and dense master-detail;
  - intermediate: narrower rail and collapsible detail while selected identity remains visible;
  - compact: horizontal/drawer navigation, vertically stacked master-detail, labeled dependency trail;
  - wide tables: horizontal scroll, sticky headers, sticky identity column where it does not obscure focus;
  - visible `:focus-visible` on every interactive control;
  - text/shape accompanying semantic colors;
  - `prefers-reduced-motion: reduce` removes non-essential transitions.

- [ ] Run frontend verification and rebuild the tracked embedded bundle:

```bash
cd ui/admin
npm test
npm run build
```

Expected: all frontend tests pass; `internal/gateway/admin_dist/index.html` references newly generated `/ui/assets/*` files; root `dist/` remains untouched.

- [ ] Run Gateway and repository regressions:

```bash
go test ./internal/gateway -count=1
go test ./...
```

Expected: all tests pass, including direct UI page routes, auth, status, admin actions, records, Billing, and configuration APIs.

- [ ] Start a local Gateway or the repo's existing test configuration and verify in a real browser:

  - `/ui` at desktop and compact widths;
  - direct refresh of all eight routes;
  - browser back and forward;
  - five-second status refresh and stale-data retention after a simulated failed refresh;
  - Tag selection and incident relationship selection;
  - no FRP node and no GPU-to-Model line;
  - Model selection, explicit Warm Worker, Unload confirmation;
  - Worker selection, Drain confirmation, Undrain;
  - Requests/Activity pagination and inspector;
  - Billing disabled capability state;
  - Model creation, YAML omission preservation, dry run, Apply gate, and read-only Advanced copy;
  - keyboard-only dialog navigation, Escape, focus restoration, and reduced motion.

- [ ] Run the Impeccable detector exactly once against the changed UI targets:

```bash
node C:/Users/admin/.agents/skills/impeccable/scripts/detect.mjs --json ui/admin/src internal/gateway/admin_dist
```

Fix material findings, then rerun only the affected focused tests plus the full verification commands above. Do not rerun the detector.

- [ ] Re-run the Impeccable `document` workflow in scan mode. Update `DESIGN.md` and `.impeccable/surfaces/ui-admin-src-main-tsx.md` from the implemented tokens/components, replacing seed assumptions with verified current facts.

- [ ] Invoke the Impeccable finish-reviewer subagent specified by the skill. Address verified blocking findings and rerun relevant tests.

- [ ] Confirm scope before the final commit:

```bash
git status --short
git diff --stat
git diff --check
```

Expected: only the planned UI/docs/embedded assets are changed; user-owned root `dist/` remains untracked and unstaged; no whitespace errors.

- [ ] Commit:

```bash
git add ui/admin/src ui/admin/package-lock.json DESIGN.md .impeccable/surfaces/ui-admin-src-main-tsx.md internal/gateway/admin_dist internal/gateway/ui_test.go
git commit -m "feat(ui): ship signal room admin console"
```

If `internal/gateway/ui_test.go` or `ui/admin/package-lock.json` is unchanged, omit it from `git add` rather than forcing it into the commit.

## Final completion evidence

Before claiming completion, use `superpowers:verification-before-completion` and report the exact current results of:

```bash
cd ui/admin && npm test
cd ui/admin && npm run build
go test ./internal/gateway -count=1
go test ./...
git diff --check
git status --short
```

Also report:

- which browser routes and widths were exercised;
- whether optional Billing/records capabilities were available or verified through their disabled state;
- that no backend endpoint or FRP product resource was added;
- that root `dist/` was preserved and not staged;
- the final commit(s), without pushing or deploying unless separately authorized.
