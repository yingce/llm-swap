# Worker Pressure and Config Density Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show each worker's live request and queue pressure against model/tag limits while making Worker and Config Ops layouts materially denser.

**Architecture:** The gateway derives a worker's live limits by summing the configured model/tag capacity for distinct ready replicas on that worker and exposes the result through the existing UI status response. The React view model carries those limits into compact pressure indicators. Config Ops uses independent base-config column flows plus a narrow capacity rail so tall controls no longer create shared-grid holes.

**Tech Stack:** Go 1.24 HTTP control plane and tests; React 19, TypeScript, Vitest, Vite, and shared CSS tokens.

---

## File map

- `internal/gateway/ui.go`: add worker live limit fields and derive them from ready model/tag replicas.
- `internal/gateway/ui_test.go`: protect model/tag ownership, aggregation, and zero-ready behavior.
- `ui/admin/src/api.ts`: describe the two additive worker status fields.
- `ui/admin/src/domain/testFixtures.ts`: provide representative worker limits to frontend tests.
- `ui/admin/src/workers/workerView.ts`: normalize limits into the worker presentation model.
- `ui/admin/src/workers/workerView.test.ts`: cover current/max data and rolling-upgrade defaults.
- `ui/admin/src/workers/WorkersPage.tsx`: render accessible compact pressure indicators.
- `ui/admin/src/app/App.tsx`: regroup base model fields, move Tag Capacity into a right rail, and make alias targets inspectable.
- `ui/admin/src/configOpsModelModal.test.ts`: protect the Config Ops source structure used by both existing and new model editors.
- `ui/admin/src/styles.css`: narrow worker cards and implement the new Config Ops layout.
- `internal/gateway/admin_dist/`: update embedded production frontend assets after source verification.
- `docs/agents/project-map.md`: document that worker cards display derived live limits without owning policy.

### Task 1: Expose live model/tag limits per worker

**Files:**
- Modify: `internal/gateway/ui_test.go`
- Modify: `internal/gateway/ui.go`

- [ ] **Step 1: Write the failing gateway tests**

Extend `TestUIWorkersExposeCurrentQueuedRequests` so its config has two runnable models with explicit capacities and its worker reports both as ready:

```go
cfg := testGatewayConfig()
cfg.Models["qwen"].TagCapacity = map[string]config.WorkerDefaults{
	"gpu-4090": {MaxConcurrency: 2, MaxQueue: 3},
}
second := cfg.Models["qwen"]
second.TagCapacity = map[string]config.WorkerDefaults{
	"gpu-4090": {MaxConcurrency: 4, MaxQueue: 5},
}
cfg.Models["audio"] = second
policy := cfg.TagPolicies["gpu-4090"]
policy.AllowedModels = []string{"qwen", "audio"}
policy.WorkerDefaults = config.WorkerDefaults{MaxConcurrency: 99, MaxQueue: 99}
cfg.TagPolicies["gpu-4090"] = policy
srv := NewServer(cfg)
```

Report duplicate `qwen` rows plus `audio` to prove distinct-model aggregation, then assert:

```go
if worker.MaxConcurrency != 6 || worker.MaxQueue != 8 {
	t.Fatalf("worker limits = %d/%d, want 6/8", worker.MaxConcurrency, worker.MaxQueue)
}
```

Add `TestUIWorkersExposeZeroLimitsWithoutReadyModels`, using a `loading` running model, and assert both live limits are zero.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```powershell
go test ./internal/gateway -run 'TestUIWorkersExpose(CurrentQueuedRequests|ZeroLimitsWithoutReadyModels)' -count=1
```

Expected: compilation fails because `uiWorker` does not yet contain `MaxConcurrency` and `MaxQueue`.

- [ ] **Step 3: Implement the worker live-limit derivation**

Add fields to `uiWorker`:

```go
MaxConcurrency int `json:"max_concurrency"`
MaxQueue       int `json:"max_queue"`
```

Add a focused helper beside `buildUIWorkers`:

```go
func (s *Server) workerLiveCapacity(cfg config.GatewayConfig, worker Worker, now time.Time) modelCapacity {
	if !s.workers.Healthy(worker.ID, now) {
		return modelCapacity{}
	}
	seen := map[string]struct{}{}
	var total modelCapacity
	for _, running := range worker.RunningModels {
		if running.State != "ready" || !workerAllowsModel(cfg, worker, running.Model) || !artifactReady(worker, running.Model) {
			continue
		}
		if _, duplicate := seen[running.Model]; duplicate {
			continue
		}
		seen[running.Model] = struct{}{}
		tag := selectedWorkerTag(cfg, worker, running.Model)
		if _, ok := tagPolicy(cfg, tag); !ok {
			continue
		}
		capacity := modelTagCapacity(cfg, running.Model, tag)
		total.MaxConcurrency += capacity.MaxConcurrency
		total.MaxQueue += capacity.MaxQueue
	}
	return total
}
```

Calculate this once per worker in `buildUIWorkers` and populate the new JSON fields. Leave `Capacity: worker.Capacity` unchanged for compatibility.

- [ ] **Step 4: Run gateway tests and verify GREEN**

Run:

```powershell
go test ./internal/gateway -run 'TestUIWorkersExpose(CurrentQueuedRequests|ZeroLimitsWithoutReadyModels)' -count=1
go test ./internal/gateway -count=1
```

Expected: both commands pass.

- [ ] **Step 5: Commit the gateway API change**

```powershell
git add internal/gateway/ui.go internal/gateway/ui_test.go
git commit -m "feat: expose worker live request limits"
```

### Task 2: Carry current/max pressure through the worker UI

**Files:**
- Modify: `ui/admin/src/api.ts`
- Modify: `ui/admin/src/domain/testFixtures.ts`
- Modify: `ui/admin/src/workers/workerView.ts`
- Modify: `ui/admin/src/workers/workerView.test.ts`
- Modify: `ui/admin/src/workers/WorkersPage.tsx`

- [ ] **Step 1: Write the failing frontend view-model tests**

Update the fixture's first worker with `max_concurrency: 4` and `max_queue: 6`. Extend the first `toMatchObject` assertion:

```ts
active_requests: 2,
max_concurrency: 4,
queued_requests: 1,
max_queue: 6
```

Add a rolling-upgrade test that deletes both fields from a copied fixture and asserts they normalize to zero:

```ts
it("defaults live limits to zero for an older gateway response", () => {
  const status = createStatusFixture();
  delete status.workers[0].max_concurrency;
  delete status.workers[0].max_queue;

  const [row] = buildWorkerRows(status, { query: "" });
  expect(row).toMatchObject({ max_concurrency: 0, max_queue: 0 });
});
```

- [ ] **Step 2: Run the focused frontend test and verify RED**

Run:

```powershell
npm test -- src/workers/workerView.test.ts
```

Working directory: `ui/admin`.

Expected: TypeScript/test assertions fail because worker max fields are absent.

- [ ] **Step 3: Add typed, backward-compatible worker limits**

In `WorkerStatus`, add optional API fields:

```ts
max_concurrency?: number;
max_queue?: number;
```

In `WorkerRow`, add required presentation fields and normalize them:

```ts
max_concurrency: Math.max(0, Number(worker.max_concurrency ?? 0)),
max_queue: Math.max(0, Number(worker.max_queue ?? 0)),
```

- [ ] **Step 4: Render the compact pressure indicators**

Replace the two plain counters with a reusable local `PressureMeter` component:

```tsx
function PressureMeter({ label, current, max }: { label: string; current: number; max: number }) {
  const percent = max > 0 ? Math.min(100, Math.round((current / max) * 100)) : 0;
  const limit = max > 0 ? String(max) : "—";
  return (
    <div className="worker-pressure-meter" title={`${label}: ${current} current / ${limit} max`}>
      <span>{label}</span>
      <strong>{current}<small>/{limit}</small></strong>
      <i aria-hidden="true" style={{ ["--pressure" as string]: `${percent}%` }} />
    </div>
  );
}
```

Use it for `REQ` and `QUEUE`, and update the strip's `aria-label` to include both maximum values.

- [ ] **Step 5: Run frontend tests and verify GREEN**

Run from `ui/admin`:

```powershell
npm test -- src/workers/workerView.test.ts
npm test
```

Expected: worker tests and the complete Vitest suite pass.

- [ ] **Step 6: Commit the worker presentation change**

```powershell
git add ui/admin/src/api.ts ui/admin/src/domain/testFixtures.ts ui/admin/src/workers/workerView.ts ui/admin/src/workers/workerView.test.ts ui/admin/src/workers/WorkersPage.tsx
git commit -m "feat: show worker pressure against live limits"
```

### Task 3: Reflow Config Ops and tighten the worker grid

**Files:**
- Modify: `ui/admin/src/configOpsModelModal.test.ts`
- Modify: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/styles.css`

- [ ] **Step 1: Add structure-level frontend assertions**

Extend the existing source-contract test with exact markers for one base region, two independent columns, one capacity rail, and inspectable alias targets:

```ts
expect(source.match(/className="model-config-base"/g)).toHaveLength(1);
expect(source.match(/className="model-config-column"/g)).toHaveLength(2);
expect(source.match(/className="model-capacity-rail"/g)).toHaveLength(1);
expect(source).toContain("title={aliasTarget}");
expect(source).toContain("title={target}");
```

- [ ] **Step 2: Run the Config Ops tests and verify RED**

Run from `ui/admin`:

```powershell
npm test -- src/configOpsModelModal.test.ts
```

Expected: assertions fail because the new layout classes and alias titles are absent.

- [ ] **Step 3: Regroup the ModelEditor markup**

Replace `.config-field-groups` with this explicit structure:

```tsx
<div className="model-config-layout">
  <div className="model-config-base">
    <div className="model-config-column">
      {identityFieldset}
      {replicaPolicyFieldset}
      {placementFieldset}
    </div>
    <div className="model-config-column">
      {runtimeFieldset}
      {artifactFieldset}
    </div>
  </div>
  <aside className="model-capacity-rail">
    {tagCapacityFieldset}
  </aside>
</div>
```

Move the existing fieldset JSX without changing field names, draft mutation logic, defaults, validation, or compatibility notes. In `ModelAliases`, set `title={target}` on existing target selects and `title={newTarget}` on the add form select.

- [ ] **Step 4: Implement the compact responsive CSS**

Apply these layout boundaries:

```css
.worker-tile-grid {
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 238px), 278px));
  justify-content: start;
}

.config-grid {
  grid-template-columns: minmax(330px, 360px) minmax(0, 1fr);
}

.model-config-layout {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(248px, 276px);
  gap: var(--space-2);
  align-items: start;
}

.model-config-base {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-2);
  align-items: start;
}

.model-config-column {
  display: grid;
  gap: var(--space-2);
}
```

Make `.model-tag-capacity-list` a single-column stack in the rail. Give its numeric fields a two-column compact grid. Make `.alias-add` one column and existing `.alias-row` use a shrink-safe two-column layout with the target select spanning the full row. Style `.worker-pressure-meter` with existing soft surface and accent tokens; do not introduce new saturated colors.

At the existing tablet breakpoint, collapse `.model-config-layout` to one column while retaining the two base columns. At the mobile breakpoint, collapse `.model-config-base`, `.config-grid`, aliases, and worker tiles to one column/full width.

- [ ] **Step 5: Run frontend tests and production build**

Run from `ui/admin`:

```powershell
npm test
npm run build
```

Expected: Vitest passes and Vite writes production assets successfully.

- [ ] **Step 6: Commit the layout source change**

```powershell
git add ui/admin/src/configOpsModelModal.test.ts ui/admin/src/app/App.tsx ui/admin/src/styles.css
git commit -m "style: densify workers and model config"
```

### Task 4: Embed assets, document semantics, and verify the repository

**Files:**
- Modify: `internal/gateway/admin_dist/index.html`
- Modify/Create/Delete: hashed files under `internal/gateway/admin_dist/assets/` as produced by Vite
- Modify: `docs/agents/project-map.md`

- [ ] **Step 1: Update the project map**

Change the worker-card note to state that cards expose current executing/queued counts and gateway-derived live model/tag limits, while capacity policy remains model-owned in Config Ops.

- [ ] **Step 2: Verify the frontend build embedded the new assets**

The Vite `outDir` writes directly to `internal/gateway/admin_dist` and clears its old contents. Run `npm run build` from `ui/admin`, then verify every `/ui/assets/<name>` reference in `internal/gateway/admin_dist/index.html` resolves to an existing file under `internal/gateway/admin_dist/assets`.

- [ ] **Step 3: Run formatting and focused verification**

```powershell
gofmt -w internal/gateway/ui.go internal/gateway/ui_test.go
go test ./internal/gateway -count=1
Push-Location ui/admin
npm test
npm run build
Pop-Location
git diff --check
```

Expected: all commands pass and `git diff --check` is silent.

- [ ] **Step 4: Run the full Go regression suite**

```powershell
go test ./...
```

Expected: all Go packages pass. Preserve the pre-existing untracked `dist/` directory and restart-marker files.

- [ ] **Step 5: Inspect the final scope**

```powershell
git status --short
git diff --stat HEAD
```

Expected: only the planned gateway, frontend source, embedded assets, and project-map changes appear; pre-existing untracked files remain untouched.

- [ ] **Step 6: Commit generated assets and documentation**

```powershell
git add internal/gateway/admin_dist docs/agents/project-map.md
git commit -m "build: embed denser admin interface"
```
