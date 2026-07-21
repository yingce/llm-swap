# Config Ops Model Modal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the inline Config Ops create/copy form with a reusable modal, compact the disabled control, and constrain Runtime selection to vLLM, SGLang, and llama.cpp.

**Architecture:** Keep model lifecycle mutations in the existing frontend draft and existing `modelLifecycle.ts` helper layer. A reusable modal owns only temporary create/copy state; successful save delegates to the existing `onCreateModel` callback, while close actions discard only that local state. Existing models remain inline editors, so model deletion and operations controls keep their established location.

**Tech Stack:** React 19, TypeScript 5.8, Vite 7, Vitest 4, existing YAML-backed Config Ops API.

## Global Constraints

- Do not add a model-specific API or bypass Draft → Dry run → Apply.
- Supported Runtime select values are exactly `vllm`, `sglang`, and `llamacpp`.
- Blank models default to `runtime: vllm`, `disabled: true`, and `min_loaded: 0`.
- Copied models retain their source runtime and Tag memberships, then reset `disabled: true` and `min_loaded: 0`.
- Existing raw `run` models remain compatible and read-only; this feature must not add raw-command creation or editing.
- Existing canonical model names remain immutable.
- The modal must warn before discarding changed local create/copy state; it must not mutate gateway config on cancel.
- The disabled control must not occupy a full-width field-grid row.
- Preserve user-owned untracked `dist/` files.

---

## File Structure

- Modify: `ui/admin/src/modelLifecycle.ts` — expose supported runtime constants, default vLLM for blank drafts, and pure modal-dirty comparison.
- Modify: `ui/admin/src/modelLifecycle.test.ts` — verify runtime defaults/options, copy retention, and dirty comparison.
- Modify: `ui/admin/src/main.tsx` — replace inline create editor with reusable modal, use Runtime select, preserve raw-command read-only behavior, and move disabled control to the header.
- Modify: `ui/admin/src/styles.css` — add modal overlay/dialog and compact header-control styles.
- Modify: `docs/agents/project-map.md` — document the modal create/copy UX and runtime selector contract.
- Modify (generated): `internal/gateway/admin_dist/index.html` and `internal/gateway/admin_dist/assets/*` — embedded frontend output from `npm run build`.

### Task 1: Add runtime and modal-state helper contracts

**Files:**

- Modify: `ui/admin/src/modelLifecycle.ts`
- Modify: `ui/admin/src/modelLifecycle.test.ts`

**Interfaces:**

- Consumes: `EditableModelConfig`, `emptyEditableModel()`, and `copyEditableModel(source)` already defined in `modelLifecycle.ts`.
- Produces: `MODEL_RUNTIME_OPTIONS`, vLLM blank defaults, and `isModelCreateDraftDirty(initial, current)` for the React modal.

- [ ] **Step 1: Write failing tests for supported runtime values and blank/copy behavior**

  Extend `ui/admin/src/modelLifecycle.test.ts`:

  ```ts
  import { MODEL_RUNTIME_OPTIONS, isModelCreateDraftDirty } from "./modelLifecycle";

  it("defaults a blank model to vllm and exposes only supported runtime options", () => {
    expect(MODEL_RUNTIME_OPTIONS).toEqual(["vllm", "sglang", "llamacpp"]);
    expect(emptyEditableModel()).toMatchObject({ runtime: "vllm", disabled: true, min_loaded: 0 });
  });

  it("keeps the copied runtime while resetting safe lifecycle defaults", () => {
    const copied = copyEditableModel({ ...sourceModel, runtime: "sglang" });
    expect(copied).toMatchObject({ runtime: "sglang", disabled: true, min_loaded: 0 });
  });
  ```

- [ ] **Step 2: Write failing tests for modal dirty-state detection**

  Define a local `initial` fixture with a name, copied model, and Tags. Require no change to be clean and each name, model, or Tag change to be dirty:

  ```ts
  expect(isModelCreateDraftDirty(initial, initial)).toBe(false);
  expect(isModelCreateDraftDirty(initial, { ...initial, name: "v2" })).toBe(true);
  expect(isModelCreateDraftDirty(initial, { ...initial, tags: ["gpu-b"] })).toBe(true);
  expect(isModelCreateDraftDirty(initial, { ...initial, model: { ...initial.model, runtime: "llamacpp" } })).toBe(true);
  ```

- [ ] **Step 3: Run the focused tests to verify they fail**

  Run from `ui/admin`: `npm test -- src/modelLifecycle.test.ts`

  Expected: FAIL because the exported runtime options and dirty comparator do not exist, and the blank default is not vLLM.

- [ ] **Step 4: Implement minimal immutable helpers**

  In `modelLifecycle.ts`, add exact runtime options and a named modal-state type:

  ```ts
  export const MODEL_RUNTIME_OPTIONS = ["vllm", "sglang", "llamacpp"] as const;

  export type ModelCreateDraft = {
    name: string;
    model: EditableModelConfig;
    tags: string[];
  };

  export function isModelCreateDraftDirty(initial: ModelCreateDraft, current: ModelCreateDraft) {
    return initial.name !== current.name
      || initial.tags.join("\u0000") !== current.tags.join("\u0000")
      || JSON.stringify(initial.model) !== JSON.stringify(current.model);
  }
  ```

  Set `runtime: "vllm"` in `emptyEditableModel()`. Leave `copyEditableModel()` runtime untouched and preserve its existing deep-copy behavior. Keep Tag arrays sorted before the modal stores them so equivalent selections compare deterministically.

- [ ] **Step 5: Run lifecycle tests and commit**

  Run from `ui/admin`: `npm test -- src/modelLifecycle.test.ts`

  Expected: PASS.

  ```powershell
  git add ui/admin/src/modelLifecycle.ts ui/admin/src/modelLifecycle.test.ts
  git commit -m "feat: define config ops runtime options"
  ```

### Task 2: Replace inline creation with a reusable modal and compact editor controls

**Files:**

- Modify: `ui/admin/src/main.tsx`
- Modify: `ui/admin/src/styles.css`
- Modify: `internal/gateway/admin_dist/index.html`
- Modify: `internal/gateway/admin_dist/assets/*`

**Interfaces:**

- Consumes: `MODEL_RUNTIME_OPTIONS`, `ModelCreateDraft`, and `isModelCreateDraftDirty` from Task 1; existing `emptyEditableModel`, `copyEditableModel`, `validateNewModelName`, and `onCreateModel` callbacks.
- Produces: `ModelCreateModal` rendered by `ConfigOps`, a fixed Runtime select, raw-command compatibility notice, and header-level disabled control.

- [ ] **Step 1: Replace scattered create state with one `ModelCreateDraft` value and initial snapshot**

  In `ConfigOps`, replace `createName`, `createModel`, and `createTags` with:

  ```ts
  const [createDraft, setCreateDraft] = useState<ModelCreateDraft | null>(null);
  const [createInitialDraft, setCreateInitialDraft] = useState<ModelCreateDraft | null>(null);
  const [discardCreateConfirm, setDiscardCreateConfirm] = useState(false);
  ```

  `startCreate("blank")` creates `{ name: "", model: emptyEditableModel(), tags: [] }`. `startCreate("copy")` copies the selected model and sorted source Tags. Store distinct deep snapshots for current and initial draft. `saveCreatedModel()` validates `createDraft.name`, delegates to `onCreateModel(name.trim(), createDraft.model, createDraft.tags)`, enables `showDisabledModels`, selects the new name, and clears modal state.

- [ ] **Step 2: Add `ModelCreateModal` using the existing field editor**

  Add a focused component in `main.tsx` immediately before `ModelEditor`:

  ```tsx
  function ModelCreateModal({ mode, draft, initialDraft, tagNames, onChange, onSave, onRequestClose }: {
    mode: "blank" | "copy";
    draft: ModelCreateDraft;
    initialDraft: ModelCreateDraft;
    tagNames: string[];
    onChange: (next: ModelCreateDraft) => void;
    onSave: () => void;
    onRequestClose: () => void;
  }) { /* render dialog and reuse ModelEditor */ }
  ```

  Render it from `ConfigOps` instead of the current `.model-create` inline card. Use `role="dialog"`, `aria-modal="true"`, and an accessible title. Backdrop click and Escape call `onRequestClose`. If `isModelCreateDraftDirty(initialDraft, draft)` is true, show an inline discard-confirmation panel with `Keep editing` and `Discard changes`; only the latter closes the modal. Reuse `ModelEditor` with `editableName`, Tag checkboxes, and Save/Cancel controls. Do not render a second implementation of model inputs.

- [ ] **Step 3: Make Runtime a constrained select and preserve raw commands**

  Replace the Runtime `<input>` in `ModelEditor` with:

  ```tsx
  {isRawRunModel ? (
    <label><span>Runtime</span><input value="Custom command" readOnly /></label>
  ) : (
    <label>
      <span>Runtime</span>
      <select value={model.runtime ?? "vllm"} onChange={(event) => onChange({ ...model, runtime: event.target.value })}>
        {MODEL_RUNTIME_OPTIONS.map((runtime) => <option key={runtime} value={runtime}>{runtime}</option>)}
      </select>
    </label>
  )}
  ```

  Keep `run` untouched for raw-command models, retain the existing read-only explanatory notice, and do not add a `run` textarea or an Ollama option.

- [ ] **Step 4: Move Disabled out of the form grid**

  Remove the `checkbox-item field-span` Disabled label from `detail-grid`. In `ModelEditor` card header, next to the existing state/actions area, render a compact checkbox switch:

  ```tsx
  <label className="model-disabled-toggle">
    <input type="checkbox" checked={Boolean(model.disabled)} onChange={(event) => onChange({ ...model, disabled: event.target.checked || undefined })} />
    <span>Disabled</span>
  </label>
  ```

  It stays available in create/copy mode and existing-model edit mode. Do not change the existing picker Disabled pill or `Show disabled` filter behavior.

- [ ] **Step 5: Add modal and compact-control styles**

  In `styles.css`, add `.modal-backdrop`, `.model-create-modal`, `.model-create-modal-body`, `.modal-discard-confirm`, and `.model-disabled-toggle`. The backdrop is fixed and visually separates the form; the dialog has a constrained viewport height with scrolling body, so long runtime/artifact forms remain usable. Use the existing `danger`, `primary`, `config-card`, and responsive design tokens. At the narrow breakpoint, stack modal actions and keep the header disabled toggle compact rather than restoring a full-width grid row.

- [ ] **Step 6: Run frontend verification and generate embedded assets**

  Run from `ui/admin`: `npm test`

  Expected: all lifecycle, alias, and routes tests PASS.

  Run from `ui/admin`: `npm run build`

  Expected: TypeScript compilation and Vite build PASS; generated files under `internal/gateway/admin_dist` update.

- [ ] **Step 7: Commit UI changes and generated assets**

  ```powershell
  git add ui/admin/src/main.tsx ui/admin/src/styles.css internal/gateway/admin_dist
  git commit -m "feat: use modal for config ops model creation"
  ```

### Task 3: Document and verify the modal workflow

**Files:**

- Modify: `docs/agents/project-map.md`

**Interfaces:**

- Consumes: the confirmed design at `docs/superpowers/specs/2026-07-21-config-ops-model-modal-design.md` and the completed UI behavior.
- Produces: operator documentation that accurately distinguishes runtime selection from legacy raw-command compatibility.

- [ ] **Step 1: Update the Config Ops project-map section**

  Add concise notes that New model and Copy use a shared modal, blank models default to disabled/vLLM/min_loaded zero, copied models retain their runtime but reset lifecycle defaults, Runtime is limited to vLLM/SGLang/llama.cpp, and raw `run` configurations remain read-only compatible. State that canceling the modal cannot change gateway configuration before Dry run/Apply.

- [ ] **Step 2: Run complete verification from the repository root**

  Run: `git diff --check`

  Expected: no output.

  Run from `ui/admin`: `npm test && npm run build`

  Expected: all Vitest files pass and Vite emits the embedded admin bundle.

  Run: `docker run --rm -v "${PWD}:/src" -w /src golang:1.23 go test ./... -count=1`

  Expected: all Go packages PASS.

- [ ] **Step 3: Commit documentation**

  ```powershell
  git add docs/agents/project-map.md
  git commit -m "docs: explain config ops model modal"
  ```

## Plan Self-Review

- Spec coverage: Task 1 locks runtime/default/modal-dirty behavior into pure tests. Task 2 implements reusable Modal interaction, constrained Runtime selection, raw-command compatibility, compact disabled control, and generated UI assets. Task 3 documents the operator behavior and runs final frontend and Go verification.
- Placeholder scan: every task includes exact files, named interfaces, expected commands, and required behavior; no deferred requirements remain.
- Type consistency: Task 1 defines `ModelCreateDraft` and `isModelCreateDraftDirty`; Task 2 consumes those exact names. Runtime options use the same `MODEL_RUNTIME_OPTIONS` constant in tests and the selector.
