# Config Ops Model Lifecycle Design

Date: 2026-07-21

## Goal

Extend Config Ops so an operator can create, copy, and safely remove concrete
model configurations without editing YAML outside the UI. This extends the
previous model-alias design: canonical model names remain immutable identities;
aliases remain the mechanism for moving stable client-facing names between
versions.

## Scope and Non-Goals

The UI supports:

- blank model creation;
- creation by copying an existing model;
- selection of one or more worker tags during creation;
- guarded deletion of a model configuration.

The UI does not support renaming an existing canonical model. A model name is
immutable after it is created. Renaming would alter the identity used by
routing, worker state, artifact markers, llama-swap keys, metrics, billing,
and historical request records. Version upgrades instead create a new concrete
model, validate it, then retarget an alias.

Deletion removes only gateway configuration. It does not delete worker-local
model directories, downloaded artifacts, runtime environments, or historical
records. This retains a rollback path and avoids a destructive remote-file
operation in Config Ops.

## UI Flow

The Models card gains `New model` and `Copy` actions. Both open the same model
editor as the current selected model, but in create mode.

Blank creation starts with a safe inactive model:

- `disabled: true`;
- `min_loaded: 0`;
- no selected worker tags;
- empty artifact and runtime fields that the operator must complete.

Copy creation deep-copies the source model's model policy, runtime settings,
artifact metadata, billing, and selected Tag memberships into an editable
draft. The copied model is always reset to `disabled: true` and
`min_loaded: 0`, so creating a version cannot trigger an automatic download or
warm operation before review. Its source model's Tags are preselected but may
be changed before saving.

The create editor requires a new canonical model name and exposes the complete
existing model form: local directory, policy, artifact, runtime, runtime args,
check endpoint, stop command, and billing. It also exposes a multi-select of
configured worker Tags. Saving the form changes only the Config Ops draft;
existing `Dry run` and `Apply` remain the only persistence operations.

The existing-model editor displays its canonical model name as read-only. It
continues to support editing model properties, including `model_dir`, but has
no rename action.

## Identity and Validation

Client-side validation provides immediate feedback, while gateway YAML
validation remains authoritative at dry-run and apply time.

- A new canonical model name is required after trimming whitespace.
- It must not equal an existing canonical model name or a configured alias.
- New `model_dir` values retain the existing validation rules: when set, they
  must be safe relative directory names, must resolve uniquely, and must not
  collide with reserved directories or artifact source-cache basenames.
- A blank `model_dir` resolves to the newly created canonical name.
- Runtime and artifact validation remains the existing backend configuration
  validation rather than a second UI-only rule set.
- Selecting a worker Tag adds the new name to that Tag's `allowed_models`;
  deselecting it removes the name. Tag lists and model maps are serialized in a
  deterministic order for readable drafts and dry-run diffs.

The UI prevents local duplicate names before a draft is saved. Concurrent or
external YAML changes are still rejected by the versioned Config API and
gateway validation; the UI preserves the current draft and displays the error.

## Protected Delete

Each existing-model editor has a `Delete model` action with an explicit
confirmation dialog. Before allowing deletion, the UI computes and displays
all blocking references:

- aliases targeting the model;
- Tag policies whose `allowed_models` include the model;
- currently reported ready, loading, or otherwise running worker replicas.

Deletion is blocked while any reference or reported replica exists. The
operator must first retarget or remove aliases, remove Tag references, and
unload the model through the existing gateway-owned runtime controls. This
avoids automatically changing traffic policy or interrupting an active
runtime. Once unblocked and confirmed, deletion removes the model from the
draft map only. It never makes remote cleanup calls.

The backend remains defensive: submitted YAML with dangling aliases or Tag
references fails validation even if it did not originate from this UI.

## Apply and Operational Effects

Creating, copying, deleting, or changing Tag membership is rendered as normal
draft changes. The existing config dry-run displays both model and Tag-policy
paths, then reports restart/reload impacts for any affected loaded models.
New models start disabled and with no minimum replica floor, so creating a
draft itself never schedules installation or warming. Enabling or warming a
new model remains an explicit later operator action after apply.

An alias is never created, retargeted, or removed implicitly by model
creation/copy/deletion. The recommended rollout remains: create the new
concrete version, allow the desired Tags, apply and validate it by its
canonical name, warm it to ready, then retarget the stable alias.

## Failure Handling

- Validation errors remain attached to the create or edit workflow and do not
  mutate the running config.
- Reset or leaving the editor discards unsaved local create state; Reset restores
  the entire draft from the saved config as it does today.
- A failed dry-run or apply leaves the draft available and shows the server
  error, including config version conflicts.
- A blocked delete remains non-destructive and names every reference that must
  be resolved first.

## Testing

Frontend tests cover blank creation defaults, copy defaults and deep-copy
independence, name/alias collision feedback, `model_dir` draft normalization,
Tag membership synchronization, immutable existing names, guarded deletion,
draft reset, and YAML round-trip ordering.

Go tests cover Config API dry-run and apply for newly added and removed model
maps, rejection of dangling alias and Tag references, changes to loaded model
impacts, and backward compatibility for configurations that omit new UI-driven
fields. Existing agent, proxy, and artifact tests remain the regression net for
the canonical-identity and `model_dir` behavior.

Final verification runs frontend tests and build, focused config/gateway tests,
and `go test ./...`.
