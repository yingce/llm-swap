# Config Ops Model Actions Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove Model Copy and Delete from Config Ops while preserving New model, existing-model editing, and Advanced Copy YAML.

**Architecture:** This is a frontend-only removal. `ConfigOps` retains the blank-model Modal and existing `onCreateModel`/`onModelChange` draft flow, but no longer constructs copied drafts or deletes entries from `draft.models`. Remove lifecycle helpers whose sole callers are these actions; Gateway/Agent configuration APIs and existing YAML compatibility stay unchanged.

**Tech Stack:** React 19, TypeScript 5.8, Vite 7, Vitest 4, existing embedded Admin UI build.

## Global Constraints

- Keep New model, existing model editing, model aliases, Tag policies, Dry run, Apply, and Advanced Copy YAML.
- Remove all Model Copy and Delete buttons, modal modes, state, callbacks, helpers, tests, and documentation.
- Blank model defaults remain `runtime: vllm`, `disabled: true`, `min_loaded: 0`, with no selected Tags.
- Existing raw `run` models remain read-only compatible; Runtime options remain exactly `vllm`, `sglang`, and `llamacpp`.
- Do not change Gateway/Agent code, configuration APIs, YAML schema, or existing model configuration compatibility.
- Preserve user-owned untracked `dist/` files.

---

## File Structure

- Modify: `ui/admin/src/main.tsx` — remove copy/delete callbacks, modal copy mode, delete UI, and related lifecycle imports; retain creation-only modal and Advanced Copy YAML.
- Modify: `ui/admin/src/modelLifecycle.ts` — remove `copyEditableModel`, `modelDeleteBlockers`, and their no-longer-needed types/imports.
- Modify: `ui/admin/src/modelLifecycle.test.ts` — remove copy/delete tests and fixtures; retain blank defaults, runtime options, name validation, Tag synchronization, and modal-dirty tests.
- Modify: `ui/admin/src/configOpsModelModal.test.ts` — remove Copy/Delete source-contract assertions; retain New model, cancellation, focus, runtime, raw-command, disabled-control, and Advanced Copy YAML contracts.
- Modify: `ui/admin/src/styles.css` — remove delete-panel-only styles; preserve modal and existing editor styles.
- Modify: `docs/agents/project-map.md` — replace copy/delete operational wording with creation/edit-only behavior.
- Modify (generated): `internal/gateway/admin_dist/index.html` and `internal/gateway/admin_dist/assets/*` — Vite output after frontend build.

### Task 1: Remove Model Copy/Delete code paths and cover the remaining UI contract

**Files:**

- Modify: `ui/admin/src/main.tsx`
- Modify: `ui/admin/src/modelLifecycle.ts`
- Modify: `ui/admin/src/modelLifecycle.test.ts`
- Modify: `ui/admin/src/configOpsModelModal.test.ts`
- Modify: `ui/admin/src/styles.css`
- Modify: `internal/gateway/admin_dist/index.html`
- Modify: `internal/gateway/admin_dist/assets/*`

**Interfaces:**

- Consumes: existing `emptyEditableModel`, `validateNewModelName`, `setModelTagMembership`, `ModelCreateDraft`, `isModelCreateDraftDirty`, `onCreateModel`, and `onModelChange`.
- Produces: a creation-only `ModelCreateModal` whose `mode` prop is removed; no `onDeleteModel`, `copyEditableModel`, `modelDeleteBlockers`, or model deletion path remains.

- [ ] **Step 1: Turn the current source-contract tests into failing removal assertions**

  Update `ui/admin/src/configOpsModelModal.test.ts` so the source text must not contain model-action identifiers and must retain creation/YAML affordances:

  ```ts
  expect(source).not.toContain('data-model-create-trigger="copy"');
  expect(source).not.toContain("Delete model");
  expect(source).not.toContain("onDeleteModel");
  expect(source).toContain('data-model-create-trigger="new"');
  expect(source).toContain("Copy YAML");
  ```

  Remove copy/delete-specific lifecycle test cases from `modelLifecycle.test.ts`, then add an assertion that `emptyEditableModel()` remains `{ runtime: "vllm", disabled: true, min_loaded: 0 }`.

- [ ] **Step 2: Run focused tests to verify they fail before removal**

  Run from `ui/admin`: `npm test -- src/configOpsModelModal.test.ts src/modelLifecycle.test.ts`

  Expected: FAIL because the current UI still contains model Copy/Delete code and `modelLifecycle.ts` still exports copy/delete helpers.

- [ ] **Step 3: Remove draft and UI action paths**

  In `App`, remove `deleteModel` and stop passing `onDeleteModel` to `ConfigOps`. In `ConfigOps`:

  ```tsx
  const [createDraft, setCreateDraft] = useState<ModelCreateDraft | null>(null);
  // startCreate has no mode/source parameter and always uses:
  const draft: ModelCreateDraft = { name: "", model: emptyEditableModel(), tags: [] };
  ```

  Remove `createMode`, copy initialization, copy trigger refs, delete confirmation state, `modelDeleteBlockers`, `deleteSelectedModel`, delete button/panel, and both Model Copy buttons. Render only the New model trigger and creation-only `ModelCreateModal`; change the modal title/copy to `New model` without a mode prop. Existing selected models still render `ModelEditor` with `onChange` and no action section.

  Remove imports and exports that are no longer used: `copyEditableModel`, `modelDeleteBlockers`, `ModelDeleteBlockers`, and `WorkerStatus` if it becomes lifecycle-only. Remove `.delete-model-panel` and `.delete-blocker-list` styles, retaining the generic `.danger` class because other UI code may still use it.

- [ ] **Step 4: Preserve Advanced Copy YAML and creation safety**

  Do not modify `copyAdvancedYAML`, its `AdvancedConfig` `onCopy` prop, or the Advanced `Copy YAML` button. Keep the modal dirty-discard confirmation, focus management, portal/inert behavior, canonical-name validation, Tag checkboxes, runtime selector, raw-command read-only state, compact Disabled toggle, and post-save disabled-model visibility.

- [ ] **Step 5: Run frontend tests and build embedded assets**

  Run from `ui/admin`: `npm test`

  Expected: all frontend test files PASS with no Copy/Delete model feature assertions remaining.

  Run from `ui/admin`: `npm run build`

  Expected: TypeScript and Vite build PASS; `internal/gateway/admin_dist` references the newly emitted JS/CSS files only.

- [ ] **Step 6: Commit the UI simplification**

  ```powershell
  git add ui/admin/src/main.tsx ui/admin/src/modelLifecycle.ts ui/admin/src/modelLifecycle.test.ts ui/admin/src/configOpsModelModal.test.ts ui/admin/src/styles.css internal/gateway/admin_dist
  git commit -m "feat: simplify config ops model actions"
  ```

### Task 2: Update documentation and verify repository compatibility

**Files:**

- Modify: `docs/agents/project-map.md`

**Interfaces:**

- Consumes: the confirmed design at `docs/superpowers/specs/2026-07-22-config-ops-model-actions-simplification-design.md` and implemented UI behavior.
- Produces: operator documentation that describes creation/edit-only Model controls and preserves Advanced Copy YAML guidance.

- [ ] **Step 1: Update the Config Ops project-map description**

  Replace model-copy/delete claims with: Config Ops creates blank models through New model and edits existing canonical models in place. State the blank vLLM/disabled/min_loaded-zero defaults and retain the description of Advanced as a read-only YAML viewer with Copy YAML. Do not alter Alias/Tag/Dry run/Apply documentation.

- [ ] **Step 2: Run final verification**

  Run: `git diff --check`

  Expected: no output.

  Run from `ui/admin`: `npm test && npm run build`

  Expected: all Vitest suites and Vite build PASS.

  Run: `docker run --rm -v "${PWD}:/src" -w /src golang:1.23 go test ./... -count=1`

  Expected: all Go packages PASS; no Gateway/Agent regressions from this frontend-only removal.

- [ ] **Step 3: Commit documentation**

  ```powershell
  git add docs/agents/project-map.md
  git commit -m "docs: simplify config ops model controls"
  ```

## Plan Self-Review

- Spec coverage: Task 1 removes all requested Model Copy/Delete entry points and dead helpers while explicitly preserving New model, editing, Advanced Copy YAML, modal accessibility, and runtime/raw compatibility. Task 2 documents the narrowed workflow and runs frontend, build, Go, and whitespace checks.
- Placeholder scan: every step contains exact files, removal targets, retained interfaces, commands, and expected results.
- Type consistency: `ModelCreateDraft`, `emptyEditableModel`, `validateNewModelName`, `setModelTagMembership`, and `isModelCreateDraftDirty` remain the only lifecycle interfaces consumed by the creation-only modal.
