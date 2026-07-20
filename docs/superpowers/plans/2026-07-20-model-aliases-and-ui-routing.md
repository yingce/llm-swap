# Model Aliases and UI Routing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Support versioned model directories and hot-swappable public aliases, expose both in Config Ops, and give every admin UI tab a refresh-safe browser route.

**Architecture:** Concrete model map keys remain canonical runtime and scheduling identities. Optional model_dir changes only the worker-local artifact path, while top-level model_aliases is resolved before queueing, scheduling, billing, metrics, and dispatch. The React UI edits these fields through the existing YAML round trip and uses the History API with explicit gateway-served page routes.

**Tech Stack:** Go 1.23, yaml.v3, net/http, React 19, TypeScript 5.8, Vite 7, YAML 2.8, Vitest.

## Global Constraints

- Gateway continues to own routing, concurrency, queues, retries, replica policy, records, metrics, and billing attribution.
- Worker agents remain thin and report concrete canonical model state.
- Omitting model_dir and model_aliases preserves current behavior.
- Concrete model names remain directly requestable.
- Aliases point directly to concrete models; chains and collisions are invalid.
- Alias traffic is accounted to the resolved concrete model.
- An explicit model_dir is one safe relative name beneath agent.model_root; omission preserves the current canonical-key path without imposing new model-name validation. Duplicate resolved directories are invalid.
- Alias-only config changes hot-apply; model_dir changes use loaded-model restart impact.
- Old version directories are retained.
- Existing /ui/events and /ui/requests JSON endpoints remain unchanged.
- Preserve the user-owned untracked dist/ directory.

## File Structure

- internal/config/model_identity.go: directory resolution and identity validation.
- internal/agent artifacts, reconcile, and render files: directory-aware install and command generation.
- internal/gateway/model_alias.go: alias resolution and request body rewrite.
- internal/gateway proxy, server, active config, and config manager files: canonical routing, listing, filtering, clone, and diff.
- ui/admin/src/modelAliases.ts: pure alias draft helpers.
- ui/admin/src/routes.ts: pure tab/path mapping.
- ui/admin/src/main.tsx: alias UI, YAML round trip, and History API.
- examples/gateway.yaml and docs/agents/project-map.md: operator contract.

---

### Task 1: Add model identity configuration and validation

**Files:**
- Create: internal/config/model_identity.go
- Modify: internal/config/config.go
- Modify: internal/config/load.go
- Test: internal/config/config_test.go

**Interfaces:**
- Produces: Model.ModelDir string with YAML/JSON name model_dir and omitempty JSON behavior.
- Produces: GatewayConfig.ModelAliases map[string]string with YAML/JSON name model_aliases.
- Produces: ResolvedModelDir(modelName string, model Model) string.
- Produces: validateModelIdentities(cfg GatewayConfig) error.

- [ ] **Step 1: Write failing tests**

Add tests that load a qwen concrete model with model_dir joyfox-model-20260720 and model_aliases mapping joyfox-model-latest to qwen. Assert both fields decode. Add table cases for missing alias target, alias/model collision, blank alias, nested directory, traversal directory, and two concrete models resolving to the same directory.

Representative acceptance test:

~~~go
func TestLoadGatewayAcceptsModelDirectoryAndAlias(t *testing.T) {
    raw := strings.Replace(validGatewayYAML(""), "  qwen:\n", "  qwen:\n    model_dir: joyfox-model-20260720\n", 1)
    raw += "\nmodel_aliases:\n  joyfox-model-latest: qwen\n"
    cfg, err := LoadGateway(strings.NewReader(raw))
    if err != nil { t.Fatal(err) }
    if cfg.Models["qwen"].ModelDir != "joyfox-model-20260720" { t.Fatalf("unexpected model_dir") }
    if cfg.ModelAliases["joyfox-model-latest"] != "qwen" { t.Fatalf("unexpected alias target") }
}
~~~

- [ ] **Step 2: Verify RED**

Run:

~~~powershell
go test ./internal/config -run 'TestLoadGateway(AcceptsModelDirectoryAndAlias|RejectsInvalidModelIdentity|RejectsDuplicateModelDirectories)' -count=1
~~~

Expected: compilation fails because ModelDir and ModelAliases do not exist.

- [ ] **Step 3: Implement fields and helper**

Add ModelDir to Model and ModelAliases to GatewayConfig with the interface tags above. Create:

~~~go
package config

import (
    "fmt"
    "sort"
    "strings"
)

func ResolvedModelDir(modelName string, model Model) string {
    if dir := strings.TrimSpace(model.ModelDir); dir != "" { return dir }
    return modelName
}

func validateModelIdentities(cfg GatewayConfig) error {
    names := make([]string, 0, len(cfg.Models))
    for name := range cfg.Models { names = append(names, name) }
    sort.Strings(names)

    dirs := map[string]string{}
    for _, name := range names {
        model := cfg.Models[name]
        dir := ResolvedModelDir(name, model)
        if model.ModelDir != "" && (dir == "" || dir == "." || dir == ".." || dir != model.ModelDir || strings.ContainsAny(dir, "/\\:")) {
            return fmt.Errorf("model %s model_dir must be a safe relative directory name", name)
        }
        if previous, exists := dirs[dir]; exists {
            return fmt.Errorf("models %s and %s resolve to duplicate model_dir %s", previous, name, dir)
        }
        dirs[dir] = name
    }

    aliases := make([]string, 0, len(cfg.ModelAliases))
    for alias := range cfg.ModelAliases { aliases = append(aliases, alias) }
    sort.Strings(aliases)
    for _, alias := range aliases {
        rawTarget := cfg.ModelAliases[alias]
        target := strings.TrimSpace(rawTarget)
        if alias == "" || alias != strings.TrimSpace(alias) || target == "" || target != rawTarget {
            return fmt.Errorf("model_aliases entries require non-empty trimmed alias and target")
        }
        if _, exists := cfg.Models[alias]; exists {
            return fmt.Errorf("model alias %s collides with model %s", alias, alias)
        }
        if _, exists := cfg.Models[target]; !exists {
            return fmt.Errorf("model alias %s target %s is not defined", alias, target)
        }
    }
    return nil
}
~~~

Call validateModelIdentities from validateGateway after concrete model validation.

- [ ] **Step 4: Verify GREEN**

~~~powershell
go test ./internal/config -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Commit**

~~~powershell
git add internal/config/config.go internal/config/load.go internal/config/model_identity.go internal/config/config_test.go
git commit -m "feat: add model directory and alias config"
~~~

---

### Task 2: Install and render concrete models from model_dir

**Files:**
- Modify: internal/agent/artifacts.go
- Modify: internal/agent/reconcile.go
- Modify: internal/agent/render.go
- Test: internal/agent/artifacts_test.go
- Test: internal/agent/reconcile_test.go
- Test: internal/agent/render_test.go

**Interfaces:**
- Consumes: config.ResolvedModelDir.
- Produces: InstallArtifactAt and InstallArtifactWithProgressAt with modelDirName argument.
- Preserves: existing InstallArtifact wrappers, marker canonical Model value, and canonical heartbeat keys.

- [ ] **Step 1: Write failing tests**

Add a render test for canonical joyfox-model-v2 with ModelDir joyfox-model-20260720 and runtime vllm. Assert the command contains /models/joyfox-model-20260720, --served-model-name, and joyfox-model-v2.

Add an artifact test calling InstallArtifactAt with canonical joyfox-model-v2 and directory joyfox-model-20260720. Assert payload and marker live under the custom directory and no joyfox-model-v2 directory exists.

Add an async reconcile test proving model_dir participates in artifactInstallKey, so a stale completion from the old directory cannot satisfy a new directory install.

- [ ] **Step 2: Verify RED**

~~~powershell
go test ./internal/agent -run 'Test(RenderLlamaSwapConfigUsesModelDir|InstallArtifactAtUsesCustomDirectory|AsyncInstallKeyIncludesModelDirectory)' -count=1
~~~

Expected: new symbols are missing and render still uses the canonical name as directory.

- [ ] **Step 3: Add directory-aware wrappers**

Keep existing callers compatible:

~~~go
func InstallArtifact(ctx context.Context, client *http.Client, baseURL, root, modelName string, artifact config.Artifact) (bool, error) {
    return InstallArtifactAt(ctx, client, baseURL, root, modelName, modelName, artifact)
}

func InstallArtifactAt(ctx context.Context, client *http.Client, baseURL, root, modelName, modelDirName string, artifact config.Artifact) (bool, error) {
    return InstallArtifactWithProgressAt(ctx, client, baseURL, root, modelName, modelDirName, artifact, nil)
}

func InstallArtifactWithProgress(ctx context.Context, client *http.Client, baseURL, root, modelName string, artifact config.Artifact, progress ArtifactProgressFunc) (bool, error) {
    return InstallArtifactWithProgressAt(ctx, client, baseURL, root, modelName, modelName, artifact, progress)
}
~~~

Move the current implementation body into InstallArtifactWithProgressAt and set modelDir with filepath.Join(root, modelDirName). Keep marker matching and marker content keyed by modelName.

- [ ] **Step 4: Carry directory through reconcile and render**

Add ModelDir string to artifactInstallKey and its constructor. For each allowed model use:

~~~go
modelDirName := config.ResolvedModelDir(modelName, model)
key := artifactKey(modelName, modelDirName, model.Artifact.Object, model.Artifact.Kind, model.Artifact.CRC64ECMA)
modelDir := filepath.Join(r.ModelRoot, modelDirName)
~~~

Use InstallArtifactWithProgressAt in async reconcile and InstallArtifactAt in synchronous reconcile. In render.go compute:

~~~go
modelPath := filepath.ToSlash(filepath.Join(modelRoot, config.ResolvedModelDir(modelName, model)))
~~~

- [ ] **Step 5: Verify GREEN**

~~~powershell
go test ./internal/agent -count=1
~~~

Expected: PASS.

- [ ] **Step 6: Commit**

~~~powershell
git add internal/agent/artifacts.go internal/agent/artifacts_test.go internal/agent/reconcile.go internal/agent/reconcile_test.go internal/agent/render.go internal/agent/render_test.go
git commit -m "feat: install models into versioned directories"
~~~

---

### Task 3: Resolve aliases on the gateway request path

**Files:**
- Create: internal/gateway/model_alias.go
- Modify: internal/gateway/proxy.go
- Modify: internal/gateway/server.go
- Test: internal/gateway/proxy_test.go
- Test: internal/gateway/models_test.go

**Interfaces:**
- Produces: resolveRequestedModel(cfg, requested) returning resolved name, Model, and success.
- Produces: rewriteRequestModel(body, resolved) returning JSON bytes or error.
- All gateway policy and accounting consumes resolved concrete names.

- [ ] **Step 1: Write failing gateway tests**

Create a qwen-v2 config with alias qwen-latest, a ready qwen-v2 worker, and an upstream handler that decodes the body. Assert alias requests reach upstream as qwen-v2, X-Gateway-Model is qwen-v2, request records and metrics use qwen-v2, and logs contain model qwen-v2 plus requested_model qwen-latest.

Add a direct qwen-v2 test asserting requested_model is absent.

Add /v1/models cases: ready target lists qwen-v2 and qwen-latest; unavailable target lists neither alias nor unavailable concrete model.

- [ ] **Step 2: Verify RED**

~~~powershell
go test ./internal/gateway -run 'Test(ProxyAlias|ModelsEndpointListsAvailableAliases)' -count=1
~~~

Expected: alias proxy returns model_not_available and alias is absent from model listing.

- [ ] **Step 3: Implement alias resolver and rewrite**

Create model_alias.go:

~~~go
package gateway

import (
    "bytes"
    "encoding/json"
    "fmt"
    "llm-swap/internal/config"
)

func resolveRequestedModel(cfg config.GatewayConfig, requested string) (string, config.Model, bool) {
    resolved := requested
    if target, ok := cfg.ModelAliases[requested]; ok { resolved = target }
    model, ok := cfg.Models[resolved]
    return resolved, model, ok
}

func rewriteRequestModel(body []byte, resolved string) ([]byte, error) {
    decoder := json.NewDecoder(bytes.NewReader(body))
    decoder.UseNumber()
    var payload map[string]any
    if err := decoder.Decode(&payload); err != nil { return nil, fmt.Errorf("decode request body: %w", err) }
    payload["model"] = resolved
    encoded, err := json.Marshal(payload)
    if err != nil { return nil, fmt.Errorf("encode request body: %w", err) }
    return encoded, nil
}
~~~

- [ ] **Step 4: Resolve before gateway-owned behavior**

In handleModelProxy keep requestedModel from ExtractModel. Resolve it against active config. If alias differs, rewrite the body before existing SGLang normalization. Rewrite errors return HTTP 400 with OpenAI code invalid_request.

Below resolution, use only concrete model for limits, placement, tag policy, worker accounting, metrics, billing, request records, cooldowns, X-Gateway-Model, and dispatch.

Add a small helper that inserts requested_model into structured log maps only when requested and resolved differ. Apply it to scheduler, retry, exhausted retry, and request events.

In handleModels, add an alias entry only when Scheduler.Pick succeeds for its concrete target. Sort all output IDs.

- [ ] **Step 5: Verify GREEN**

~~~powershell
go test ./internal/gateway -run 'Test(ProxyAlias|ModelsEndpoint)' -count=1
go test ./internal/gateway -count=1
~~~

Expected: PASS.

- [ ] **Step 6: Commit**

~~~powershell
git add internal/gateway/model_alias.go internal/gateway/proxy.go internal/gateway/proxy_test.go internal/gateway/server.go internal/gateway/models_test.go
git commit -m "feat: route public model aliases"
~~~

---

### Task 4: Make aliases safe under active config and hot apply

**Files:**
- Modify: internal/gateway/active_config.go
- Modify: internal/gateway/config_manager.go
- Create: internal/gateway/active_config_test.go
- Test: internal/gateway/config_admin_test.go

**Interfaces:**
- Produces immutable alias snapshots.
- Filters aliases whose targets are disabled.
- Produces per-alias diff paths model_aliases.NAME.
- Treats ModelDir as runtime-affecting.

- [ ] **Step 1: Write failing tests**

Add active config test with v1 enabled, v2 disabled, aliases stable to v1 and latest to v2. Assert stable remains and latest is removed.

Add dry-run test changing latest from v1 to v2. Assert path model_aliases.latest, type changed, apply_mode hot_apply, and no restart flags.

Add dry-run test changing model_dir on a running model. Assert runtime-change detail and loaded-worker restart impact.

- [ ] **Step 2: Verify RED**

~~~powershell
go test ./internal/gateway -run 'Test(ActiveGatewayConfigFiltersAliases|UIConfigDryRunReportsAliasHotApply|UIConfigDryRunReportsModelDirImpact)' -count=1
~~~

Expected: alias clone/filter/diff and directory runtime classification are missing.

- [ ] **Step 3: Implement clone, filter, and diff**

Deep-copy aliases:

~~~go
out.ModelAliases = make(map[string]string, len(cfg.ModelAliases))
for alias, target := range cfg.ModelAliases { out.ModelAliases[alias] = target }
~~~

After disabled concrete models are removed, delete every alias whose target is absent.

In diffGatewayConfig, compare the sorted union of old/new alias keys and emit added, changed, or removed uiConfigChange values at model_aliases.NAME without restart flags.

Add a.ModelDir != b.ModelDir to modelRuntimeFieldsChanged.

- [ ] **Step 4: Verify GREEN**

~~~powershell
go test ./internal/gateway -run 'Test(ActiveGatewayConfig|UIConfig)' -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Commit**

~~~powershell
git add internal/gateway/active_config.go internal/gateway/active_config_test.go internal/gateway/config_manager.go internal/gateway/config_admin_test.go
git commit -m "feat: hot apply model alias changes"
~~~

---

### Task 5: Add Config Ops directory and alias editing

**Files:**
- Create: ui/admin/src/modelAliases.ts
- Create: ui/admin/src/modelAliases.test.ts
- Modify: ui/admin/package.json
- Modify: ui/admin/package-lock.json
- Modify: ui/admin/src/api.ts
- Modify: ui/admin/src/main.tsx
- Modify: ui/admin/src/styles.css

**Interfaces:**
- Produces: setAliasTarget, removeAlias, validateAliasDraft.
- Extends GatewayConfigView with model_aliases.
- Extends ModelConfig with optional model_dir.
- Serializes stable alias ordering and omits empty model_dir.

- [ ] **Step 1: Add Vitest and the test command**

From ui/admin run:

~~~powershell
npm install --save-dev vitest
~~~

Add package script test with value vitest run.

- [ ] **Step 2: Write failing helper tests**

Create modelAliases.test.ts:

~~~ts
import { describe, expect, it } from "vitest";
import { removeAlias, setAliasTarget, validateAliasDraft } from "./modelAliases";

describe("model aliases", () => {
  it("retargets immutably", () => {
    const source = { latest: "v1" };
    expect(setAliasTarget(source, "latest", "v2")).toEqual({ latest: "v2" });
    expect(source).toEqual({ latest: "v1" });
  });
  it("removes an alias", () => {
    expect(removeAlias({ latest: "v1", stable: "v1" }, "latest")).toEqual({ stable: "v1" });
  });
  it("validates names and targets", () => {
    expect(validateAliasDraft("", "v1", ["v1"], {})).toContain("required");
    expect(validateAliasDraft("v1", "v1", ["v1"], {})).toContain("collides");
    expect(validateAliasDraft("latest", "missing", ["v1"], {})).toContain("target");
  });
});
~~~

- [ ] **Step 3: Verify RED**

~~~powershell
npm test
~~~

Expected: modelAliases module is missing.

- [ ] **Step 4: Implement pure helpers**

Create modelAliases.ts with immutable sorted updates:

~~~ts
export function setAliasTarget(source: Record<string, string>, alias: string, target: string) {
  return Object.fromEntries([...Object.entries(source).filter(([name]) => name !== alias.trim()), [alias.trim(), target]]
    .sort(([a], [b]) => a.localeCompare(b)));
}

export function removeAlias(source: Record<string, string>, alias: string) {
  return Object.fromEntries(Object.entries(source).filter(([name]) => name !== alias)
    .sort(([a], [b]) => a.localeCompare(b)));
}

export function validateAliasDraft(alias: string, target: string, modelNames: string[], aliases: Record<string, string>) {
  const name = alias.trim();
  if (!name || !target) return "Alias name and target are required.";
  if (modelNames.includes(name)) return "Alias collides with a concrete model.";
  if (!modelNames.includes(target)) return "Alias target is not defined.";
  if (Object.hasOwn(aliases, name)) return "Alias already exists.";
  return "";
}
~~~

- [ ] **Step 5: Wire config types and YAML round trip**

Add model_dir to ModelConfig and model_aliases to GatewayConfigView. Extend EditableGatewayConfig, cloneEditableConfig, toEditableConfig, and toGatewayConfigView.

In renderDraftYAML sort alias keys, set model_aliases when non-empty, and delete the YAML key when empty. In createYamlModelsMap include model_dir before artifact only when its trimmed value is non-empty.

- [ ] **Step 6: Build the Config Ops controls**

Add Model directory to ModelEditor, with placeholder equal to canonical name and help text saying empty uses the concrete model name.

Add ModelAliasesEditor below the model editor. It must:
- add alias name plus concrete target selector;
- retarget existing aliases with one selector change;
- remove aliases;
- show target ready/running counts from ModelStatus;
- show a warning badge for zero ready replicas without blocking apply;
- show helper validation messages inline.

Use parent immutable updateDraft flow through onAliasesChange.

- [ ] **Step 7: Add layout styles**

Add alias-list, alias-row, and alias-add grid classes. Reuse current warning variables/classes and collapse to one column below 760px.

- [ ] **Step 8: Verify GREEN and production build**

~~~powershell
npm test
npm run build
~~~

Expected: Vitest PASS, TypeScript PASS, Vite build succeeds and refreshes internal/gateway/admin_dist.

- [ ] **Step 9: Commit**

~~~powershell
git add ui/admin/package.json ui/admin/package-lock.json ui/admin/src/api.ts ui/admin/src/main.tsx ui/admin/src/styles.css ui/admin/src/modelAliases.ts ui/admin/src/modelAliases.test.ts internal/gateway/admin_dist
git commit -m "feat: configure model aliases in admin UI"
~~~

---

### Task 6: Add refresh-safe independent UI routes

**Files:**
- Create: ui/admin/src/routes.ts
- Create: ui/admin/src/routes.test.ts
- Modify: ui/admin/src/main.tsx
- Modify: internal/gateway/server.go
- Test: internal/gateway/ui_test.go

**Interfaces:**
- Produces: exported Tab, pathForTab, and tabFromPath.
- Gateway serves the embedded index for seven additional page paths.
- Existing JSON and API paths remain exact handlers.

- [ ] **Step 1: Write failing frontend and gateway tests**

Create routes.test.ts with bidirectional cases:
- dashboard /ui
- models /ui/models
- workers /ui/workers
- billing /ui/billing
- events /ui/event-log
- requests /ui/request-log
- configOps /ui/config
- advanced /ui/advanced

Assert unknown path returns dashboard.

Add TestUIPageRoutesServeEmbeddedApp. Authenticate and GET every path; expect 200, text/html, and admin root marker. Also assert /ui/events and /ui/requests remain JSON.

- [ ] **Step 2: Verify RED**

~~~powershell
Set-Location ui/admin
npm test -- routes.test.ts
Set-Location ../..
go test ./internal/gateway -run TestUIPageRoutesServeEmbeddedApp -count=1
~~~

Expected: routes module is absent and page paths return 404.

- [ ] **Step 3: Implement route mapping**

Create routes.ts:

~~~ts
export type Tab = "dashboard" | "models" | "workers" | "billing" | "events" | "requests" | "configOps" | "advanced";

const paths: Record<Tab, string> = {
  dashboard: "/ui", models: "/ui/models", workers: "/ui/workers", billing: "/ui/billing",
  events: "/ui/event-log", requests: "/ui/request-log", configOps: "/ui/config", advanced: "/ui/advanced"
};

export function pathForTab(tab: Tab) { return paths[tab]; }
export function tabFromPath(pathname: string): Tab {
  return (Object.entries(paths).find(([, path]) => path === pathname)?.[0] as Tab | undefined) ?? "dashboard";
}
~~~

- [ ] **Step 4: Wire History API**

Initialize tab from window.location.pathname. On tab click, push pathForTab(next) then set state. Register and clean up one popstate listener. If initial path is unknown, replace it with /ui so URL and dashboard state agree.

- [ ] **Step 5: Register gateway page paths**

Register GET handlers using the existing authenticated handleUI for:
- /ui/models
- /ui/workers
- /ui/billing
- /ui/event-log
- /ui/request-log
- /ui/config
- /ui/advanced

Leave all existing exact data/API route registrations unchanged.

- [ ] **Step 6: Verify GREEN and rebuild**

~~~powershell
Set-Location ui/admin
npm test
npm run build
Set-Location ../..
go test ./internal/gateway -run 'TestUI(PageRoutesServeEmbeddedApp|Auth)' -count=1
~~~

Expected: PASS.

- [ ] **Step 7: Commit**

~~~powershell
git add ui/admin/src/routes.ts ui/admin/src/routes.test.ts ui/admin/src/main.tsx internal/gateway/server.go internal/gateway/ui_test.go internal/gateway/admin_dist
git commit -m "feat: preserve admin page across refresh"
~~~

---

### Task 7: Update operator docs and run full verification

**Files:**
- Modify: examples/gateway.yaml
- Modify: docs/agents/project-map.md
- Modify: internal/gateway/admin_dist only through npm run build

**Interfaces:**
- Documents optional fallback, canonical versus alias identity, ready-first switching, rollback, Config Ops, and independent UI routes.

- [ ] **Step 1: Update gateway example**

Show a concrete version with optional model_dir and a top-level model_aliases mapping latest to that concrete key. Keep tag policies on concrete keys and use only placeholder artifact values.

- [ ] **Step 2: Update project map**

Update Domain Vocabulary, Proxy, Agent Render, Config Rules, Config Manager, Admin UI, and UI Routes. State:
- model map key is canonical;
- model_dir changes only local path;
- alias resolves before all gateway policy/accounting and is rewritten before dispatch;
- alias hot-switch versus directory restart impact;
- concrete metrics/billing identity;
- ready-first upgrade and pointer rollback;
- independent page routes and preserved JSON endpoints.

- [ ] **Step 3: Format Go files**

~~~powershell
gofmt -w internal/config/config.go internal/config/load.go internal/config/model_identity.go internal/config/config_test.go internal/agent/artifacts.go internal/agent/artifacts_test.go internal/agent/reconcile.go internal/agent/reconcile_test.go internal/agent/render.go internal/agent/render_test.go internal/gateway/model_alias.go internal/gateway/proxy.go internal/gateway/proxy_test.go internal/gateway/server.go internal/gateway/models_test.go internal/gateway/active_config.go internal/gateway/active_config_test.go internal/gateway/config_manager.go internal/gateway/config_admin_test.go internal/gateway/ui_test.go
~~~

Expected: no output and only planned Go files are formatted.

- [ ] **Step 4: Run frontend verification**

~~~powershell
Set-Location ui/admin
npm test
npm run build
Set-Location ../..
~~~

Expected: tests and production build PASS.

- [ ] **Step 5: Run focused Go verification**

~~~powershell
go test ./internal/config -count=1
go test ./internal/agent -count=1
go test ./internal/gateway -count=1
~~~

Expected: PASS.

- [ ] **Step 6: Run full repository regression**

~~~powershell
go test ./...
~~~

Expected: PASS. If scripts require a POSIX shell unavailable on Windows, use the repository-documented POSIX or Docker equivalent and record its result.

- [ ] **Step 7: Check scope**

~~~powershell
git diff --check
git status --short
~~~

Expected: only planned source, test, docs, lockfile, and generated admin assets are changed; dist/ remains untracked and unstaged.

- [ ] **Step 8: Commit docs and generated asset refresh**

~~~powershell
git add examples/gateway.yaml docs/agents/project-map.md internal/gateway/admin_dist
git commit -m "docs: explain versioned model alias rollout"
~~~

- [ ] **Step 9: Verify after all commits**

~~~powershell
go test ./...
Set-Location ui/admin
npm test
npm run build
Set-Location ../..
git status --short --branch
~~~

Expected: all checks PASS and only pre-existing untracked dist/ remains outside version control.
