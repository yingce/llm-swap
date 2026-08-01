# All-in-one Workers, Model Capacity, and Admin UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and deploy one cache-cleaned CUDA 12.8 Agent image to eight single-GPU workers, derive model capacity from ready worker capacity, and ship the approved compact Models ledger and Worker/Config refinements.

**Architecture:** The gateway remains the admission and routing owner. `tag_policies.<tag>.worker_defaults` is per-worker/GPU capacity; the gateway sums ready workers for the model gate and status API. The all-in-one image uses one vLLM/vLLM-Omni venv, a separate SGLang venv, native llama.cpp binaries, uv hardlinks, a non-editable vLLM wheel, and same-layer cache cleanup.

**Tech Stack:** Go 1.23, React 19, TypeScript 5.8, Vitest, Vite, Bash, uv, Docker BuildKit and Compose, CUDA 12.8, vLLM, vLLM-Omni, SGLang, llama.cpp.

---

## File map and worktree rule

- Create `internal/gateway/model_capacity.go` and `model_capacity_test.go` for derived limits.
- Modify `internal/gateway/proxy.go`, `proxy_test.go`, `ui.go`, and `ui_test.go` for enforcement and status.
- Modify `ui/admin/src/api.ts`, `models/*`, `workers/*`, `app/App.tsx`, `config/*`, and `styles.css` for the operator UI.
- Modify `scripts/install-worker.sh`, `scripts/install_worker_test.go`, and `Dockerfile.agent` for the optimized runtime image.
- Modify `deploy/worker-frp/*` and delete `compose.omni.yaml` for the canonical eight-worker rollout.
- Regenerate `internal/gateway/admin_dist/*` only after source UI tests pass.

The worktree is already dirty. Every commit below must stage only named files and run `git diff --cached --name-only` before committing.

## Phase 1: Gateway capacity

### Task 1: Derive ready-worker model capacity

**Files:**
- Create: `internal/gateway/model_capacity.go`
- Create: `internal/gateway/model_capacity_test.go`

- [ ] **Step 1: Write failing table-driven tests**

Cover one worker, two mixed-capacity workers, unhealthy workers, missing artifacts, and `loading` replicas. The desired API is:

```go
type modelCapacity struct {
	ReadyReplicas  int
	MaxConcurrency int
	MaxQueue       int
}

got := srv.modelCapacity("qwen", now)
want := modelCapacity{ReadyReplicas: 2, MaxConcurrency: 3, MaxQueue: 7}
```

- [ ] **Step 2: Verify RED**

Run `go test ./internal/gateway -run TestModelCapacity -count=1`.

Expected: FAIL because the type and method do not exist.

- [ ] **Step 3: Implement the minimal method**

Iterate `s.workers.Snapshot(now)`. Count only workers that are healthy, allow the model, have a ready artifact, and report running state `ready`. Resolve the selected tag and sum `policy.WorkerDefaults.MaxConcurrency` and `MaxQueue`.

```go
func (s *Server) modelCapacity(model string, now time.Time) modelCapacity {
	cfg := activeGatewayConfig(s.currentConfig())
	out := modelCapacity{}
	for _, worker := range s.workers.Snapshot(now) {
		if !s.workers.Healthy(worker.ID, now) || !workerAllowsModel(cfg, worker, model) || !artifactReady(worker, model) {
			continue
		}
		state, running := runningModelState(worker, model)
		if !running || !strings.EqualFold(state, "ready") {
			continue
		}
		policy, ok := tagPolicy(cfg, selectedWorkerTag(cfg, worker, model))
		if !ok {
			continue
		}
		out.ReadyReplicas++
		out.MaxConcurrency += policy.WorkerDefaults.MaxConcurrency
		out.MaxQueue += policy.WorkerDefaults.MaxQueue
	}
	return out
}
```

- [ ] **Step 4: Verify GREEN**

Run `go test ./internal/gateway -run TestModelCapacity -count=1` and `go test ./internal/gateway -count=1`.

Expected: PASS.

- [ ] **Step 5: Commit only the new capacity files**

Stage the two files and commit `feat: derive model capacity from ready workers`.

### Task 2: Use derived capacity with legacy ceilings

**Files:**
- Modify: `internal/gateway/model_capacity.go`
- Modify: `internal/gateway/model_capacity_test.go`
- Modify: `internal/gateway/proxy.go`
- Modify: `internal/gateway/proxy_test.go`

- [ ] **Step 1: Write failing ceiling and admission tests**

Test `effectiveCapacity(derived, legacyCeiling int)` with `8/0 -> 8`, `8/4 -> 4`, `2/4 -> 2`, `0/0 -> 0`, and `0/1 -> 1`. Add a proxy test proving two ready `1/1` workers admit two concurrent requests and queue the third.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/gateway -run "TestEffectiveCapacity|TestProxyUsesReadyWorkerCapacity" -count=1`.

Expected: FAIL on the missing helper or fixed model gate.

- [ ] **Step 3: Replace fixed model gate inputs**

Compute `capacity := s.modelCapacity(model, time.Now())`, then pass `effectiveCapacity(capacity.MaxConcurrency, modelCfg.MaxConcurrency)` and the queue equivalent to `acquireObservedLimit`. Keep tag gates for non-zero legacy tag ceilings and keep the worker gate as final per-GPU enforcement. With zero ready capacity and no legacy ceiling, leave the model gate disabled so the existing scheduler and worker gate retain cold-start behavior.

- [ ] **Step 4: Verify GREEN**

Run the focused tests, all proxy queue tests, then `go test ./internal/gateway -count=1`.

Expected: PASS.

- [ ] **Step 5: Commit proxy capacity behavior**

Stage the four named files and commit `feat: scale model admission with ready replicas`.

### Task 3: Expose active, queued, and computed capacity

**Files:**
- Modify: `internal/gateway/ui.go`
- Modify: `internal/gateway/ui_test.go`
- Modify: `ui/admin/src/api.ts`

- [ ] **Step 1: Write a failing UI status test**

Create two ready workers, acquire one `Accounting` model request, create one model limiter waiter, and assert:

```go
if model.ActiveRequests != 1 || model.QueuedRequests != 1 {
	t.Fatalf("pressure = active %d queued %d", model.ActiveRequests, model.QueuedRequests)
}
if model.MaxConcurrency != 2 || model.MaxQueue != 4 {
	t.Fatalf("capacity = %d/%d", model.MaxConcurrency, model.MaxQueue)
}
```

- [ ] **Step 2: Verify RED**

Run `go test ./internal/gateway -run TestUIStatus.*ModelCapacity -count=1`.

Expected: FAIL because active/queued fields are absent and maxima come from model YAML.

- [ ] **Step 3: Serialize computed status**

Add `ActiveRequests` and `QueuedRequests` JSON fields. In `buildUIModels`, use `s.accounting.ModelActive(name)`, `s.limiter.Queued("model:"+name)`, and the derived capacity with optional legacy ceilings. Add matching TypeScript properties to `ModelStatus`.

- [ ] **Step 4: Verify backend and UI type regression**

Run `go test ./internal/gateway -count=1`, then `npm test` from `ui/admin`.

Expected: PASS.

- [ ] **Step 5: Commit the status contract**

Stage the three named files and commit `feat: expose model capacity pressure`.

## Phase 2: Admin UI

### Task 4: Build ledger-ready model rows

**Files:**
- Modify: `ui/admin/src/models/modelView.ts`
- Modify: `ui/admin/src/models/modelView.test.ts`

- [ ] **Step 1: Write failing traffic and capacity tests**

Assert rows contain:

```ts
expect(row.capacity).toEqual({ active: 3, max_active: 8, queued: 1, max_queue: 16 });
expect(row.traffic).toMatchObject({
  requests: "5.2K",
  total_tokens: "27.9M",
  cache_tokens: "18.7M",
  avg_latency: "4.65s",
  errors_4xx: "366",
  errors_5xx: "0"
});
```

Cover zero traffic and missing last access.

- [ ] **Step 2: Verify RED**

Run `npx vitest run src/models/modelView.test.ts` from `ui/admin`.

Expected: FAIL because row capacity and traffic formatting do not exist.

- [ ] **Step 3: Implement typed compact formatting**

Add `capacity`, `traffic`, `modelCapacityLabel(row)`, and `modelTrafficLabel(row)`. Compact numbers only at 1,000 or above. Format latency as ms below one second, seconds below one minute, otherwise minutes.

- [ ] **Step 4: Verify GREEN and commit**

Run the focused test. Stage only `modelView.ts` and its test; commit `feat: prepare model traffic ledger rows`.

### Task 5: Render the approved Models ledger

**Files:**
- Modify: `ui/admin/src/models/ModelsPage.tsx`
- Modify: `ui/admin/src/styles.css`
- Test: `ui/admin/src/models/modelView.test.ts`

- [ ] **Step 1: Add failing accessible-label assertions**

Assert `modelCapacityLabel(row)` is `3 active of 8; 1 queued of 16` and traffic label contains `5.2K requests`.

- [ ] **Step 2: Verify RED, then implement ledger markup**

Replace the initial master-detail workspace with columns Model, Replicas, Capacity, and Traffic. Capacity contains explicitly labeled Requests and Queue values. Traffic permanently shows requests, total tokens, cache tokens, average latency, 4xx, and 5xx. Maximum latency and last access move to on-demand row detail. Preserve Warm and Unload in expandable detail.

- [ ] **Step 3: Implement responsive layout**

Desktop uses four columns. Intermediate widths wrap Traffic beneath Model. Mobile stacks each model row in DOM order. Use top-edge state plus accessible text instead of another prominent Ready pill.

- [ ] **Step 4: Verify and commit**

Run `npm test` and `npm run build` from `ui/admin`. Stage the Models files and relevant CSS only; commit `feat: restore compact model traffic ledger`.

### Task 6: Bound and simplify Worker tiles

**Files:**
- Modify: `ui/admin/src/workers/workerView.ts`
- Modify: `ui/admin/src/workers/workerView.test.ts`
- Modify: `ui/admin/src/workers/WorkersPage.tsx`
- Modify: `ui/admin/src/styles.css`

- [ ] **Step 1: Write failing diagnostic-summary tests**

Add a `diagnostics` shape containing build state/detail, heartbeat age, and scrape failures. Assert the obsolete `request_capacity` display property is absent.

- [ ] **Step 2: Verify RED**

Run `npx vitest run src/workers/workerView.test.ts`.

- [ ] **Step 3: Implement compact tile content**

Remove request/queue meters and permanent build/heartbeat/scrape rows. Keep worker ID, endpoint, health edge, one compact GPU row, current model, and exceptional restart/error state. Add one keyboard-focusable information affordance containing the three diagnostics.

- [ ] **Step 4: Bound the grid**

```css
.worker-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(100%, 320px), 420px));
  justify-content: start;
}
```

Keep the GPU name small; truncate only with the full value in `title`.

- [ ] **Step 5: Verify and commit**

Run the focused test and `npm run build`. Stage the Worker files and relevant CSS only; commit `refactor: compact worker operational tiles`.

### Task 7: Group Config fields and expose per-GPU capacity once

**Files:**
- Modify: `ui/admin/src/app/App.tsx`
- Modify: `ui/admin/src/config/configDraft.ts`
- Modify: `ui/admin/src/config/configDraft.test.ts`
- Modify: `ui/admin/src/styles.css`

- [ ] **Step 1: Write YAML-preservation tests**

Start with legacy model/tag ceilings and worker defaults. Edit only worker defaults and assert legacy values, runtime args, artifact CRC, aliases, and billing survive rendering. If this characterization passes immediately, add a failing test for helper labels `Max concurrent requests per GPU` and `Max queued requests per GPU`.

- [ ] **Step 2: Organize ModelEditor**

Group Identity, Runtime, Replica policy, Artifact, Placement, and Billing. Remove normal model concurrency/queue inputs but preserve loaded legacy values in the draft. Cap normal desktop inputs at 22rem; allow runtime args and artifact paths to span columns.

- [ ] **Step 3: Simplify TagPolicyEditor**

Hide tag-global concurrency/queue from normal controls. Render only `worker_defaults` under Per-GPU capacity with the approved labels. If loaded global values are non-zero, preserve them and show a quiet compatibility note pointing to Advanced YAML.

- [ ] **Step 4: Verify and commit**

Run `npm test` and `npm run build`. Stage only Config/App/CSS paths; commit `refactor: organize model and gpu capacity settings`.

## Phase 3: Agent image and deployment

### Task 8: Install vLLM from a clean wheel

**Files:**
- Modify: `scripts/install-worker.sh`
- Modify: `scripts/install_worker_test.go`

- [ ] **Step 1: Write failing dry-run tests**

Assert vLLM and vLLM-Omni share `venvs/vllm`, SGLang uses `venvs/sglang`, supplied hardlink mode is retained, vLLM invokes `uv build --wheel --no-build-isolation`, no `--editable` install remains, cleanup removes source/wheel/cache paths, and no `venvs/vllm-omni` is created.

- [ ] **Step 2: Verify RED**

Run `go test ./scripts -run "Test.*VLLM|Test.*Runtime" -count=1`.

Expected: FAIL on editable install/copy mode.

- [ ] **Step 3: Implement wheel build and cleanup helpers**

```bash
build_and_install_vllm_wheel() {
  local src="$1" python="$2" wheelhouse="$LLMSWAP_ROOT/cache/wheels"
  run mkdir -p "$wheelhouse"
  run bash -lc "cd '$src' && TORCH_CUDA_ARCH_LIST='$LLMSWAP_TORCH_CUDA_ARCH_LIST' uv build --wheel --no-build-isolation --out-dir '$wheelhouse'"
  run bash -lc "wheel=\$(find '$wheelhouse' -maxdepth 1 -name 'vllm-*.whl' -print -quit); test -n \"\$wheel\"; uv pip install --python '$python' --no-deps \"\$wheel\""
}
```

After import/version checks, remove only known build paths: `$LLMSWAP_ROOT/cache/uv`, `cache/wheels`, runtime archive cache, and `$LLMSWAP_ROOT/src`. Do not remove runtime JIT caches from running containers.

- [ ] **Step 4: Verify and commit**

Run `go test ./scripts -count=1`. Stage the installer and test; commit `build: install vllm from a clean wheel`.

### Task 9: Preserve hardlinks in one Docker layer

**Files:**
- Modify: `Dockerfile.agent`
- Test: `scripts/install_worker_test.go`

- [ ] **Step 1: Write a failing Dockerfile source test**

Assert the Dockerfile uses `UV_LINK_MODE=hardlink`, installs selected vLLM/SGLang/llama.cpp runtimes in one RUN block, and performs one cleanup after all installs.

- [ ] **Step 2: Verify RED**

Run `go test ./scripts -run TestDockerfileAgentRuntimeLayer -count=1`.

- [ ] **Step 3: Merge runtime RUN blocks**

Export all runtime build variables once, conditionally invoke all three installer paths, then remove cache/source before the RUN ends. Keep `build-essential`, `ninja-build`, and `python3-jinja2` in the CUDA devel base for model-start JIT compatibility.

- [ ] **Step 4: Verify and commit**

Run `go test ./scripts -count=1` and `docker build --check -f Dockerfile.agent .`. Stage Dockerfile and its source test; commit `build: share runtime packages in one image layer`.

### Task 10: Finalize the canonical eight-worker Compose project

**Files:**
- Modify: `deploy/worker-frp/compose.yaml`
- Modify: `deploy/worker-frp/verify_test.go`
- Modify: `deploy/worker-frp/verify.sh`
- Modify: `deploy/worker-frp/README.md`
- Delete: `deploy/worker-frp/compose.omni.yaml`

- [ ] **Step 1: Extend tests**

Assert exactly eight services, one immutable image, tags `gpu-4090`, hostnames/derived IDs `worker-gpu0..7`, physical device IDs `0..7`, shared model root, independent logs, runtime build arg `all`, no FRPC sidecar, and no Tailscale or `NVIDIA_VISIBLE_DEVICES` environment.

- [ ] **Step 2: Verify tests and remove special-case Compose**

Run `go test ./deploy/worker-frp -count=1`, delete `compose.omni.yaml`, and update README with the merged policy and all-in-one validation sequence without tokens.

- [ ] **Step 3: Verify and commit**

Render `docker compose config --services` using placeholder paths and run the package test again. Stage only deploy files; commit `deploy: standardize eight all-in-one workers`.

## Phase 4: Integration and rollout

### Task 11: Full local verification and embedded UI

**Files:**
- Regenerate: `internal/gateway/admin_dist/index.html`
- Replace hashes: `internal/gateway/admin_dist/assets/*`

- [ ] **Step 1: Run source verification**

Run `go test ./...`, then `npm test` and `npm run build` from `ui/admin`.

Expected: all tests PASS and Vite exits 0.

- [ ] **Step 2: Verify embedded assets**

Confirm every asset referenced by embedded `index.html` exists and obsolete hashes are removed. Run `go test ./internal/gateway -run "Test.*UI|Test.*Admin" -count=1` and `git diff --check`.

- [ ] **Step 3: Run Impeccable mechanical verification**

Run:

```powershell
node C:\Users\admin\.agents\skills\impeccable\scripts\detect.mjs --json ui/admin/src/models/ModelsPage.tsx ui/admin/src/workers/WorkersPage.tsx ui/admin/src/app/App.tsx ui/admin/src/styles.css
```

Resolve unexplained layout, density, responsiveness, and control findings, then rerun tests and detector.

- [ ] **Step 4: Commit embedded assets**

Stage the generated admin distribution and only already-tested UI source paths; commit `build: embed model capacity admin ui`.

### Task 12: Build and inspect the optimized image on the GPU host

**Remote host:** `root@111.2.199.31:8230`

- [ ] **Step 1: Copy the exact source revision**

Create `/data0/images/llm-swap-src-<short-sha>-all` without overwriting older builds. Exclude `.git`, `.superpowers`, node_modules, tokens, gateway runtime config, and scratch dist files.

- [ ] **Step 2: Build an immutable all-in-one tag**

Use `llmswap-agent:all-cu128-<short-sha>-20260802`, `LLMSWAP_RUNTIME=all`, the already-proven CUDA 12.8 vLLM inputs, and the supplied proxy only as BuildKit secret `llmswap_runtime_proxy`. Never put proxy credentials in ARG, ENV, labels, Compose, or committed files.

- [ ] **Step 3: Inspect size and contents**

Record venv/runtime directory sizes, Docker image size, files with link count above one, and any symlink into cache. Expected: vLLM and SGLang venvs plus llama.cpp exist; `/opt/llmswap/src` and cache are absent; no cache symlink exists. Compare measured image size with 65.7 GB. A zero hardlink count is a failed optimization investigation, not an automatic release failure; explain whether package versions genuinely overlap.

- [ ] **Step 4: Smoke all runtime families**

Verify vLLM, vLLM-Omni, and SGLang imports/versions plus `llama-server --version`. Start representative configured models. AudioVision vLLM must use `--trust-remote-code --dtype half --max-model-len 8192`. Require healthy runtime HTTP responses.

### Task 13: Merge the test policy and deploy eight workers

- [ ] **Step 1: Back up and validate gateway YAML**

Create a timestamped backup. Merge all three models into `gpu-4090.allowed_models`, preserve `worker_defaults: 1/1`, remove `gpu-4090-omni`, preserve tokens byte-for-byte, and validate through `/ui/api/config/validate` before restart.

- [ ] **Step 2: Verify remote Compose before mutation**

Set immutable image, current gateway URL, existing secret path, worker state root, and model root. Run `docker compose config` and `deploy/worker-frp/verify.sh`.

- [ ] **Step 3: Replace the old special worker and start eight services**

Remove `worker-frp-worker-pawsense-omni-1` only after image smoke passes. Start `worker-gpu0..7` with `--force-recreate`.

- [ ] **Step 4: Verify the fleet**

Require eight running containers, eight healthy gateway workers, IDs `worker-gpu0..7`, tag `gpu-4090`, one visible GPU per container, host devices `0..7`, and unique FRP leases/endpoints.

- [ ] **Step 5: Verify serving and UI**

Send real gateway requests through representative vLLM, SGLang, and llama.cpp models. Confirm Models capacity equals ready worker sums and Traffic increments. Confirm a single filtered Worker tile remains at most 420 px and Config edits per-GPU capacity only under worker defaults.

- [ ] **Step 6: Preserve rollback and prune only unused cache**

Keep the previous working image and gateway YAML backup. Remove stopped probes and unused BuildKit cache only; do not delete models, gateway state, the new image, or rollback image.

## Final acceptance

- [ ] `go test ./...`, UI tests, and UI build pass from the final worktree.
- [ ] Impeccable detector has no unexplained findings.
- [ ] The new image contains all runtime families and no source/build/download cache.
- [ ] Measured image size and directory breakdown are reported against 65.7 GB.
- [ ] Exactly eight healthy one-GPU workers use the merged `gpu-4090` policy.
- [ ] Models ledger shows replicas, active/max, queued/max, requests, tokens, cache tokens, average latency, 4xx, and 5xx.
- [ ] Worker diagnostics are on demand and a lone tile does not stretch.
- [ ] Config forms are grouped and per-GPU capacity is edited only once.
- [ ] The previous image and gateway YAML remain available for rollback.
