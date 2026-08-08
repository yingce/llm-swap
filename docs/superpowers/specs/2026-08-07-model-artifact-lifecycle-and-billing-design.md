# Model Artifact Lifecycle, Service Names, and Billing Views

## Purpose

Make artifact replacement safe on workers that share one model root, retain a
simple operator-facing model lifecycle, and report both the actual runtime cost
and the stable product-facing cost across version upgrades.

## Terms

- **canonical model**: the operator-chosen immutable version identity, such as
  `A-Pro-0808`. It is concise and does not receive an automatic UUID, timestamp,
  or checksum suffix.
- **patch update**: a bug fix to the same canonical model. The artifact object
  and checksum change, but the canonical model remains `A-Pro-0808`.
- **service name**: the stable public API name. It is represented by a model
  alias, such as `A-Pro -> A-Pro-0808`.
- **breaking upgrade**: a new architecture or separately operated version. It
  gets a new canonical model and the service alias is moved after it is ready.

## Artifact installation safety

### Problem

The eight GPU containers share `agent.model_root`. Existing artifact locks are
keyed by artifact object, kind, and checksum. They prevent duplicate download of
the same payload, but they do not serialize commits to the same `model_dir` when
the desired artifact changes. An old and a new artifact can therefore unpack and
replace the same directory concurrently.

### Design

Keep the existing artifact-source lock and add a second lock keyed by resolved
`model_dir`.

1. The artifact-source lock protects download and the shared cached source file.
   Different artifacts may download concurrently.
2. Each Agent records the newest Gateway configuration revision and desired
   artifact fingerprint for every `model_dir` in shared state.
3. A newer revision supersedes an older one. The Agent cancels its own older
   download immediately. Active download, extraction, and staging operations
   receive a cancellable context.
4. Before committing a staged directory, an installer acquires the model-dir
   lock, rechecks the shared desired revision, and aborts if superseded.
5. It then rechecks the marker. If another Agent has already committed the
   desired artifact, it reports ready without another replacement.
6. Only an installer whose revision and fingerprint still match the shared
   desired state may write the marker and atomically replace the target
   directory.

The tag-scoped Agent response also carries `desired_model_dirs`: the sorted
union of resolved directories referenced by active models in every tag policy.
An explicit empty list means no directory is globally desired; a missing/null
field identifies an older Gateway. When a model leaves only one worker tag, its
Agent cancels local work but must not publish a tombstone while another tag
still desires that directory. A tombstone is published only for an explicit
global removal. With an older Gateway the Agent takes the conservative path and
cancels locally without publishing a tombstone.

The Gateway remains the authority for desired configuration. The shared state is
only a cross-process fence for Agents that mount the same model root; it does
not move scheduling or replica policy to workers.

Gateway configuration revisions are globally increasing across process
restarts. The first backend stores the revision atomically under the Gateway
state directory behind a small storage interface; a future Redis backend may
implement the same allocation contract.

### Failure handling and observability

- Superseded work emits `artifact_install_cancelled` with the old and winning
  fingerprints; it is not reported as an installation error.
- A failed download/extract keeps the existing ready directory untouched.
- Directory-lock wait, cancellation, commit, and stale-fence events are visible
  in the worker event log.
- No secrets, raw artifact URLs, or unbounded values are added to metric labels.

### Tests

- Two reconcilers sharing one model root cannot replace one model directory at
  the same time with distinct artifacts.
- A newer configuration cancels an older blocked download.
- A stale installer cannot commit after a newer desired revision is published.
- An already-correct directory is reused after waiting on its directory lock.
- Existing single-artifact cache de-duplication remains intact.
- At one revision, a removal-side Reconciler cannot tombstone a shared
  directory still desired by another tag, and the allowed-side Reconciler can
  install it.
- An explicit global empty desired set publishes a tombstone; a missing legacy
  field does not.

## Model upgrade lifecycle

### Patch update

A bug fix to `A-Pro-0808` updates that canonical model's artifact object and
checksum in place. Billing and ordinary UI rows remain under `A-Pro-0808`.
An internal artifact revision snapshot is recorded for events, ready intervals,
and request records so the exact payload remains auditable without creating a
new visible model name. Rollback restores the previous configuration revision.

### Breaking upgrade

A different architecture creates a new canonical model, for example
`A-Pro-0901`. The stable service name moves only after the new canonical model
has its configured ready floor:

```text
A-Pro (service alias) -> A-Pro-0808
A-Pro (after rollout) -> A-Pro-0901
```

The worker and Gateway continue to account, route, and meter by canonical model.

### Reclaiming an existing canonical name as a service name

For an existing system where `A-Pro` is already a canonical model, provide a
single audited Gateway operation named **Promote service name**. It is not a
general relaxation of alias/canonical collision validation.

The operation requires that the old canonical is disabled and has no active or
pending replicas. It then atomically archives its active configuration,
removes it from the active model namespace, and creates:

```text
A-Pro -> A-Pro-0808
```

The new target must already meet its ready floor, so client calls to `A-Pro`
have no routing gap. The old artifact directory is retained for the configured
rollback window. Reversal removes the alias and restores the archived canonical
configuration as one transaction. Historical requests remain attached to their
original canonical model.

## Billing views

The raw billing ledger remains canonical-model based. It is the source of truth
for ready occupancy, actual runtime identity, configured pricing snapshots, and
operational audit.

Add reporting-only grouping modes:

- `canonical` (default): existing actual-model view.
- `alias`: aggregate request usage and configured usage cost by the alias used
  at request time (`requested_model`). A model requested directly by canonical
  remains in canonical-only/unattributed output.
- `billing_group` (future explicit field): allows direct canonical traffic and
  multiple aliases to belong to one product line without guessing ownership.

Alias aggregation must use the alias snapshot stored on each request, never the
alias's current target. Retargeting an alias therefore leaves historic reports
stable while combining the product's old and new versions in one row.

Ready occupancy cost remains canonical by default. If it is shown in an alias or
billing-group view, allocate it by request share for the selected period and
label it as an allocation rather than actual runtime cost. The alias row exposes
its canonical-version breakdown.

## Non-goals

- Do not make worker code own placement, queues, routing, or replica policy.
- Do not permit arbitrary live alias/canonical name collisions.
- Do not create noisy automatic canonical names for patch updates.
- Do not rewrite historical request, billing, or metric identity after an alias
  is retargeted.
