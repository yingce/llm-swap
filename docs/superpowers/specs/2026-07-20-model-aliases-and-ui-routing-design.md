# Model Aliases, Versioned Directories, and UI Routing Design

Date: 2026-07-20

## Goal

Allow several versions of a model to coexist while clients use a stable public
alias such as `joyfox-model-latest`. Keep the concrete version independently
addressable, preserve existing configurations, and expose the new controls in
the admin UI. Admin UI pages must also have stable URLs so a browser refresh or
back/forward navigation keeps the selected page.

## Configuration Model

Add an optional `model_dir` field to each concrete model and a top-level
`model_aliases` map:

```yaml
models:
  joyfox-model-v1:
    model_dir: joyfox-model-20260701
    priority: 10
    min_loaded: 0
    artifact:
      object: models/joyfox-model-20260701.tar.gz
      kind: tar_gz
      crc64ecma: example-v1-crc
    runtime: vllm

  joyfox-model-v2:
    model_dir: joyfox-model-20260720
    priority: 10
    min_loaded: 1
    artifact:
      object: models/joyfox-model-20260720.tar.gz
      kind: tar_gz
      crc64ecma: example-v2-crc
    runtime: vllm

model_aliases:
  joyfox-model-latest: joyfox-model-v2
```

The `models` map key remains the canonical model name used by gateway policy,
worker state, llama-swap, and direct client requests. `model_dir` controls only
the local installation and runtime path. If it is omitted, the resolved
directory remains `<model_root>/<canonical-model-name>`, preserving current
behavior.

Each alias maps directly to one canonical model. Alias chains are not
supported. An alias cannot equal a canonical model name, and every target must
exist in `models`. Concrete model names remain directly requestable.

An explicitly configured `model_dir` must be a non-empty, safe relative
directory name. Absolute paths, `.`/`..`, and path separators are rejected.
When `model_dir` is omitted, the existing canonical-key directory behavior is
preserved without adding new model-name restrictions. Duplicate resolved
directories are rejected so two artifact installers cannot replace the same
directory.

## Request and Routing Flow

For an OpenAI-compatible request, the gateway keeps both concepts locally:

- requested model: the client-provided canonical name or alias;
- resolved model: the concrete canonical model selected after alias lookup.

If the request names a concrete model, resolution returns that model unchanged.
If it names an alias, resolution returns the configured target. Unknown names
continue to return `model_not_available`.

All gateway-owned operational decisions use the resolved model: model config,
queue and concurrency gates, placement, tag selection, accounting, replica
cooldowns, metrics, billing, and request records. This intentionally separates
traffic and cost by the actual version that served it. Structured gateway logs
include `requested_model` when it differs from `model`, while the canonical
`model` field remains the resolved version.

Before dispatch, the gateway rewrites the request body's `model` field to the
resolved model name. llama-swap and the runtime therefore receive the concrete
name they were configured to serve. Existing request normalization continues
to run against the resolved model config.

`GET /v1/models` returns every available concrete model plus each alias whose
target is currently available. Because concrete versions remain listed,
operators can send validation traffic to a new version before moving the
stable alias.

## Worker Artifact and Runtime Behavior

The agent derives one resolved install directory for every concrete model:

```text
<model_root>/<model.model_dir or canonical model name>
```

Artifact marker identity remains tied to the canonical model and its artifact
metadata. Artifact status and heartbeat maps also remain keyed by canonical
model name. Rendering uses the resolved install directory for `{{model_path}}`
and standard runtime wrapper arguments, but uses the canonical model name for
the llama-swap model key and runtime served-model alias.

Changing `model_dir` is a runtime-affecting model configuration change. The
agent installs and verifies the target artifact directory before it renders the
new command. If the concrete model is already running, the existing
gateway-authorized restart flow controls activation. Old version directories
are not deleted automatically; retaining them enables fast rollback and avoids
adding lifecycle cleanup policy to this feature.

## Seamless Upgrade Workflow

1. Add the new concrete model with a unique `model_dir` and artifact.
2. Allow it on the intended worker tags and warm it until at least one replica
   is ready.
3. Validate it through its concrete model name.
4. Change `model_aliases.joyfox-model-latest` to the new concrete model.
5. Apply the alias-only change as a gateway hot update.
6. Roll back by pointing the alias at the old, still-ready concrete model.

The gateway does not reject an alias target merely because it is currently
unavailable. This permits cold-start and disaster-recovery configurations.
The UI must show a clear warning when the selected target has no ready replica,
so the normal operational path remains ready-first switching.

## Config Validation and Hot Apply

Gateway config loading validates alias targets, alias/name collisions, and
resolved model directories. Active config filtering removes aliases whose
canonical target is disabled.

Config dry-run reports alias changes independently, for example:

```text
model_aliases.joyfox-model-latest: changed
```

Alias-only changes are gateway hot updates and do not require worker restart.
Changing `model_dir` is reported as a model runtime change and follows the
existing loaded-worker impact calculation.

Clone and normalization helpers deep-copy the alias map so config snapshots
remain immutable.

## Admin UI

The Model editor gains a `Model directory` input. Empty means the concrete
model name and is displayed as such in the field help text.

Config Ops gains a `Model aliases` card with:

- an alias-name input and target-model selector for adding an alias;
- a target selector for changing an existing alias atomically;
- a delete action;
- target readiness and running-replica status;
- validation feedback for collisions and missing targets;
- a warning, but not a hard block, when the selected target has zero ready
  replicas.

The editor serializes `model_dir` and `model_aliases` through the existing YAML
round-trip path. Empty `model_dir` values are omitted. Alias ordering is stable
to avoid noisy drafts and dry-run diffs.

This feature does not add full model create/delete UI. Concrete model versions
continue to be created through the existing configuration source, while Config
Ops can edit their directory and manage aliases. Full model CRUD is a separate
feature because it requires defaults, artifact validation UX, and tag-policy
coordination beyond alias switching.

## Independent UI Routes

Each existing tab gets a stable browser path:

| Page | Path |
| --- | --- |
| Dashboard | `/ui` |
| Models | `/ui/models` |
| Workers | `/ui/workers` |
| Billing | `/ui/billing` |
| Events | `/ui/event-log` |
| Requests | `/ui/request-log` |
| Config Ops | `/ui/config` |
| Advanced | `/ui/advanced` |

`event-log` and `request-log` deliberately avoid the existing JSON endpoints
at `/ui/events` and `/ui/requests`, preserving those endpoint contracts.

The React app derives the initial tab from `window.location.pathname`, calls
`history.pushState` on tab navigation, and handles `popstate` for browser
back/forward navigation. Unknown UI page paths fall back to the dashboard path.

The gateway serves the embedded admin index for the known page paths while
leaving `/ui/assets/*`, `/ui/api/*`, `/ui/status`, `/ui/events`, `/ui/requests`,
and `/ui/metrics/*` on their existing handlers. Authentication behavior remains
unchanged.

## Error Handling

- Invalid aliases and model directories fail gateway config validation with a
  field-specific message.
- An alias whose target has no ready worker returns the existing
  `no_healthy_worker` response after normal resolved-model scheduling.
- Request-body rewrite failures return `invalid_request` before queue admission
  or worker dispatch.
- UI draft validation errors remain visible in Config Ops and do not mutate the
  running config.
- Direct concrete-version requests do not depend on any alias being configured.

## Testing

Behavior changes are implemented test-first and cover:

- config parsing, defaults, alias validation, and directory safety/uniqueness;
- backward compatibility when `model_dir` and `model_aliases` are omitted;
- artifact marker lookup, installation, and rendered commands using
  `model_dir` while runtime names remain canonical;
- direct and alias proxy requests, request-body rewrite, canonical accounting,
  structured logging, and unknown aliases;
- `/v1/models` availability for concrete names and aliases;
- config clone, diff, dry-run, alias hot apply, and `model_dir` restart impact;
- Config Ops TypeScript types, YAML round-trip, alias add/change/delete, and
  unready-target warning;
- direct loading of every UI page route plus tab click, refresh initialization,
  and browser `popstate` behavior;
- preservation of the existing JSON UI endpoints.

The final verification runs focused Go tests, frontend tests/build, and
`go test ./...`.
