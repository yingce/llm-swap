# All-in-one workers, model capacity, and operator UI design

Date: 2026-08-02

## Outcome

Build one CUDA 12.8 Agent image containing vLLM, vLLM-Omni, SGLang, and
llama.cpp; deploy the same immutable image as eight one-GPU workers; simplify
request admission around per-worker GPU capacity; and restore a compact,
model-centric traffic and capacity view in the admin UI.

The test gateway remains the control plane. Workers stay thin and stateless:
the gateway still owns routing, queueing, concurrency, retries, replica policy,
request accounting, and operator actions.

## Confirmed product decisions

- Use one all-in-one Agent image so a worker can switch runtime without being
  rebuilt or replaced.
- vLLM and vLLM-Omni share one Python environment. SGLang uses a separate
  environment because its Torch and serving dependencies may diverge.
- llama.cpp remains packaged as native binaries.
- One container is bound to one physical GPU. The eight containers use agent
  IDs `worker-gpu0` through `worker-gpu7` and all report tag `gpu-4090`.
- Merge the test gateway's `gpu-4090-omni` policy into `gpu-4090`; all three
  configured models become eligible on all eight workers.
- Per-GPU request limits come from
  `tag_policies.<tag>.worker_defaults.max_concurrency` and `max_queue`.
- A model's live total capacity is computed from its ready workers, not entered
  as another independent number.
- Models use the selected compact ledger design. Workers use compact bounded
  tiles and no longer present request, concurrency, or queue data as worker
  content.

## Image architecture

### Runtime layout

The image keeps these runtime boundaries:

```text
/opt/llmswap/venvs/vllm       vLLM plus vLLM-Omni
/opt/llmswap/venvs/sglang     SGLang
/opt/llmswap/runtimes/llamacpp
/opt/llmswap/bin              runtime wrappers, llama-swap, and Agent
```

The CUDA 12.8 devel base remains for this iteration. vLLM and FlashInfer may
compile kernels at model start, so removing the CUDA toolchain before proving
all target models work would trade disk space for a runtime regression. The
base includes the required compiler toolchain, Ninja, and Jinja support.

### Package sharing and cleanup

Do not symlink installed packages to the uv cache. Cache paths are disposable,
and removing or rotating the cache would break those links.

Use `UV_LINK_MODE=hardlink` while all Python environments and the shared uv
cache live on the same filesystem. Install the runtime environments in one
Docker build layer so identical package files can retain shared inodes. Remove
the cache in that same layer after installation; the installed hardlinks remain
valid because the venv entries still reference the inode.

Build vLLM into a wheel and install it non-editably. The final image must not
depend on the checked-out vLLM source tree. Delete source checkouts, wheel build
directories, uv and pip caches, downloaded runtime archives, temporary files,
and Python bytecode caches before the layer is committed.

The final image must verify:

- no installed venv entry is a symlink into a deleted cache or source tree;
- vLLM and vLLM-Omni import from the shared vLLM environment;
- SGLang imports from its independent environment;
- llama.cpp wrappers resolve their packaged shared libraries;
- the image has no uv, pip, VCS, source-build, or runtime-download cache;
- the measured image and directory sizes are reported against the previous
  65.7 GB all-in-one image.

Image-size reduction is measured, not guaranteed to a preset number. Runtime
correctness takes precedence over removing CUDA libraries required for JIT.

## Eight-worker deployment

The Compose project defines eight explicit worker services, preferably using a
YAML extension for the shared service definition. Each service has:

- the same immutable all-in-one image tag;
- a unique hostname and `LLMSWAP_AGENT_ID` from `worker-gpu0` to
  `worker-gpu7`;
- `LLMSWAP_AGENT_TAGS=gpu-4090`;
- exactly one NVIDIA `device_id`, from host GPU `0` through `7`;
- the shared read-only model payload root where possible;
- an independent logs/state path;
- the existing Agent token secret and test gateway URL;
- `unless-stopped` restart behavior.

Inside each container, the assigned physical device is expected to appear as
the container's only visible CUDA device, normally logical GPU 0.

The old `worker-pawsense-omni` container is removed after the new image passes
runtime smoke tests. The test gateway configuration is updated so the single
`gpu-4090` policy allows:

- `JoyFox-PawSense-AudioVision-Pro`;
- `JoyFox-RP-35B-0703`;
- `Qwen3.6-35B-NSFW`.

Deployment succeeds only when Docker shows eight worker containers, the
gateway reports eight healthy workers, all eight FRP leases/endpoints are
unique, and each worker reports one GPU.

## Capacity and queue semantics

`worker_defaults` is the canonical configured capacity for one worker/GPU.
For a model at a point in time:

```text
model max active = sum(max_concurrency of ready eligible workers)
model max queue  = sum(max_queue of ready eligible workers)
```

Summation, rather than `worker count × one constant`, also handles future
mixed-capacity tags correctly.

The gateway keeps a model-level admission gate, but its limits are derived from
the current ready replica set. The worker gate remains the final per-worker
enforcement boundary. A replica becoming ready increases aggregate capacity; a
replica becoming unavailable reduces capacity for new admissions without
canceling requests already running.

Existing model-level and tag-level `max_concurrency` and `max_queue` fields
remain decodable for compatibility. In the migrated test configuration they
are omitted or zero, so they do not create a second source of truth. The
standard UI no longer exposes them as normal editable settings. Advanced YAML
may continue to show legacy non-zero global ceilings, clearly labeled as
compatibility settings, until a later destructive schema removal.

The status response supplies, per model:

- current active requests;
- current queued requests;
- ready and running replica counts;
- computed maximum active requests;
- computed maximum queue depth;
- the per-worker capacity inputs used in the computation.

## Admin UI

### Workers

Worker tiles use a bounded responsive grid with a 420 px maximum tile width. A
single worker no longer stretches across the content area.

Each tile keeps only operational worker information:

- worker identity and small, low-contrast advertised endpoint;
- top-edge health signal;
- GPU name, utilization, temperature, and memory in one compact row;
- current running model;
- low-emphasis drain action and exceptional restart/error state.

Build version, heartbeat timestamp, and scrape failures move into one details
popover or tooltip reached from an information affordance. The information
remains available to operators but does not consume permanent card height.
Artifacts remain absent from worker tiles.

### Models ledger

The Models page uses the approved ledger layout. Each model row contains:

- canonical name, aliases, runtime, and priority;
- ready and running replicas;
- current active / computed maximum active;
- current queued / computed maximum queued;
- request count, total tokens, cache tokens, average latency, 4xx count, and
  5xx count from the existing traffic data;
- maximum latency and last-access time in secondary on-demand detail;
- readiness expressed by the row's top-edge signal and accessible text, not an
  additional prominent status pill.

Traffic degrades quietly when its backing data is unavailable. Missing
historical records must not imply that model serving is unavailable.

### Configuration

Model fields are grouped by operator intent instead of rendered as a sequence
of full-width controls:

- Identity and aliases;
- Runtime and runtime arguments;
- Replica policy and lifecycle;
- Artifact source and integrity;
- Placement and allowed tags;
- Billing, when enabled.

Desktop forms use compact two- or three-column grids and semantic maximum field
widths. Textareas span only the sections that need them. Narrow screens stack
the same groups in DOM order.

Per-GPU capacity is edited once in the Tag policy's `worker_defaults` group,
with explicit labels "Max concurrent requests per GPU" and "Max queued
requests per GPU". Model forms display computed capacity as read-only runtime
state and do not duplicate those inputs.

Disabled model and "Show disabled" controls use the shared switch/checkbox
components already established by the UI redesign.

## Compatibility and failure behavior

- Existing canonical model identities, model directories, aliases, and
  artifact paths are unchanged.
- The gateway remains compatible with legacy global limit fields while the test
  configuration migrates to worker-only configured capacity.
- A zero ready-replica model reports zero live capacity and follows the existing
  cold-start/reconciliation path; the UI must distinguish cold from failed.
- Image build failure leaves the currently running immutable image available.
- Runtime smoke failure blocks the eight-worker rollout.
- Compose rollout failure rolls back to the previous Compose file and image
  without deleting model data.
- Gateway configuration is validated before restart, with tokens preserved and
  never copied into source, logs, or design artifacts.

## Test and acceptance plan

### Automated

- Go tests cover derived model limits for zero, one, multiple, and mixed
  capacity ready workers; replica loss; queued requests; and legacy optional
  global ceilings.
- Gateway status tests cover active, queued, computed capacity, replica counts,
  and traffic serialization.
- UI view-model tests cover Models ledger traffic and capacity formatting,
  bounded Worker tiles, metadata disclosure, empty traffic, and disabled state.
- Config draft tests cover grouped worker-default fields and preservation of
  unrelated YAML.
- Worker installer dry-run tests assert the unified vLLM environment, separate
  SGLang environment, hardlink mode, non-editable vLLM install, and cache/source
  cleanup.
- Run `go test ./...`, the UI test/build commands, and the worker install script
  tests before building the remote image.

### Remote runtime and deployment

- Verify imports and versions for all packaged runtimes.
- Start representative vLLM/vLLM-Omni, SGLang, and llama.cpp commands on the
  4090 host before rollout. The already proven AudioVision vLLM invocation uses
  `--trust-remote-code --dtype half --max-model-len 8192`.
- Verify the new image directory breakdown, hardlink count, absence of caches,
  and Docker image size.
- Deploy eight workers and confirm unique IDs, unique FRP leases, one visible
  GPU per container, eight healthy gateway registrations, and successful
  gateway-to-llama-swap requests.
- Visually verify Workers, Models, and Config at desktop and mobile widths, then
  run the Impeccable detector over all changed UI targets.

## Out of scope

- Moving request scheduling, queue state, or replica policy into Agents.
- A custom runtime command UI.
- Removing the CUDA devel toolchain before target-model JIT requirements are
  proven unnecessary.
- Destructive removal of legacy global-limit YAML fields in this release.
- Production gateway rollout; this design targets the 8x4090 test gateway and
  workers first.
