# Worker Diagnostics Hover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Worker diagnostics transient on pointer hover or keyboard-visible focus, with a muted warning trigger for non-current Agent versions.

**Architecture:** Keep diagnostics presentation local to `WorkersPage` and CSS. Replace the stateful native disclosure with a semantic button and sibling tooltip so no React open state is needed and pointer exit always closes the tooltip.

**Tech Stack:** React 19, TypeScript, CSS, Vitest source-contract tests

---

### Task 1: Replace persistent diagnostics disclosure

**Files:**
- Modify: `ui/admin/src/workers/workerView.test.ts`
- Modify: `ui/admin/src/workers/WorkersPage.tsx:83-108`
- Modify: `ui/admin/src/styles.css:1273-1347`

- [ ] **Step 1: Write the failing source-contract tests**

Read `styles.css` beside the existing page source and assert the new non-persistent structure and warning hook:

```ts
const stylesSource = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

expect(workersPageSource).toContain('className="worker-diagnostics-trigger"');
expect(workersPageSource).toContain('type="button"');
expect(workersPageSource).toContain('buildState === "latest" ? "" : " version-warning"');
expect(workersPageSource).not.toContain("<details");
expect(workersPageSource).not.toContain("<summary");
expect(stylesSource).toContain(".worker-diagnostics-trigger:focus-visible + .worker-diagnostics-popover");
expect(stylesSource).toContain(".worker-diagnostics.version-warning .worker-diagnostics-trigger");
expect(stylesSource).not.toContain(".worker-diagnostics[open]");
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
cd ui/admin
npm test -- src/workers/workerView.test.ts
```

Expected: FAIL because `WorkersPage` still uses `details`/`summary`, has no trigger button or version-warning class, and CSS still contains `[open]` selectors.

- [ ] **Step 3: Implement the semantic transient trigger**

Replace `details`/`summary` with a positioned wrapper and accessible button:

```tsx
<div className={`worker-diagnostics${buildState === "latest" ? "" : " version-warning"}`}>
  <button
    type="button"
    className="worker-diagnostics-trigger"
    aria-label={`Show diagnostics for ${row.id}`}
    aria-describedby={`worker-diagnostics-${row.id}`}
  >?</button>
  <dl id={`worker-diagnostics-${row.id}`} className="worker-diagnostics-popover" role="tooltip">
    <div><dt>Build</dt><dd>{row.worker.agent_build.version || "unknown"} <small>{buildState}</small></dd></div>
    <div><dt>Commit</dt><dd>{row.worker.agent_build.commit || "unknown"}</dd></div>
    <div><dt>Heartbeat</dt><dd>{row.heartbeat}</dd></div>
    <div><dt>Scrape failures</dt><dd>{row.worker.scrape_failures}</dd></div>
  </dl>
</div>
```

Rename the trigger selectors, remove marker and `[open]` rules, reveal the tooltip only for wrapper hover or keyboard-visible focus, and add the quiet version warning:

```css
.worker-diagnostics-trigger {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 999px;
  color: #718491;
  cursor: help;
  display: inline-flex;
  font-size: 11px;
  font-weight: 850;
  height: 24px;
  justify-content: center;
  padding: 0;
  width: 24px;
}

.worker-diagnostics:hover .worker-diagnostics-popover,
.worker-diagnostics-trigger:focus-visible + .worker-diagnostics-popover {
  display: grid;
}

.worker-diagnostics.version-warning .worker-diagnostics-trigger {
  background: #efe4cf;
  color: #8b6938;
}
```

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
cd ui/admin
npm test -- src/workers/workerView.test.ts
```

Expected: all Worker view tests PASS.

- [ ] **Step 5: Run the complete admin UI regression suite and production build**

Run:

```bash
cd ui/admin
npm test
npm run build
```

Expected: all Vitest tests PASS and Vite production build exits 0.

- [ ] **Step 6: Commit only the scoped UI files and plan**

```bash
git add docs/superpowers/plans/2026-08-02-worker-diagnostics-hover.md ui/admin/src/workers/workerView.test.ts ui/admin/src/workers/WorkersPage.tsx ui/admin/src/styles.css
git commit -m "fix: make worker diagnostics hover transient"
```
