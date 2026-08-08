# Safe model lifecycle rollout

This runbook covers the protocol revision, shared model-directory fencing,
alias billing, and service-name promotion. It intentionally contains no host
names, deployment paths outside the repository defaults, credentials, or
production model values.

## Release unit and durable state

Treat a Gateway and the Agents affected by a protocol change as one
compatibility batch. The Gateway response now carries `config_revision`;
upgraded Agents use it to cancel and fence stale artifact work. It also carries
the sorted global `desired_model_dirs` set so removal from one tag cannot
tombstone a directory still desired by another tag. An explicit empty set
authorizes a global tombstone; a missing field from an older Gateway does not.
A Gateway-only release that keeps this contract and the supported Agent
protocol range unchanged does not require publishing or deploying a new Agent
image.

The first release that adopts `config_revision`, global desired-directory
tombstones, and artifact fencing is protocol v3 and is an Agent compatibility
batch: deploy the Gateway and every affected Agent together. Protocol v2 does
not implement these safety behaviors. Its heartbeat remains HTTP-accepted so
operators can observe rollout progress, but the Gateway must report it as
`upgrade_agent` rather than `compatible`. Later Gateway-only releases may
retain the existing v3 Agent fleet when they do not change the Agent protocol
contract.

### Agent release metadata and protocol rollout

Record the Agent's release identity separately from its source provenance:

- `LLMSWAP_BUILD_VERSION` is the human-readable Agent release identifier; the
  v3 fencing release is `2026.08.08.1`.
- `LLMSWAP_BUILD_COMMIT` is the exact source commit SHA. Never copy that SHA
  into `LLMSWAP_BUILD_VERSION`.

The production worker Compose requires both as independent build args, and
`verify.sh` rejects equal values before a build or cutover. The Gateway-only
Fabric build injects commit and build time provenance but intentionally leaves
the Agent release-version field unset.

The Gateway compares every reported Agent protocol version to its supported
inclusive range and reports `agent_version_status` as follows. The current
Gateway safety range is v3 through v3:

- `compatible`: protocol is in range.
- `upgrade_agent`: protocol is below the Gateway minimum; deploy a newer Agent.
- `upgrade_gateway`: protocol is above the Gateway maximum; deploy a newer
  Gateway.
- `legacy`: the Agent did not report a protocol version.

For compatible Agents, the normal worker UI shows the release version; the
commit is provenance rather than the release label. Treat any other status as
an explicit upgrade gate before advancing a protocol rollout.

For this first fencing release, roll Gateway and Agents as one v3 compatibility
batch and require every expected Agent heartbeat to report v3. HTTP acceptance
of v2 is only rollout visibility and does not authorize a prolonged mixed
serving state.

For a later protocol change that is explicitly designed to support overlap,
roll it forward in this order:

1. Deploy a Gateway that supports both the existing and new protocol versions.
2. Deploy the new Agents and verify their fresh heartbeats are `compatible`.
3. After no old Agents remain, raise the Gateway minimum protocol version.

Do not assume overlap from release versions or commits. After the v3 fencing
batch, a pure Gateway release that preserves the protocol contract does not
require an Agent release.

Preserve and back up the Gateway state directory before cutover. It includes:

- `config-revision.json` and its lock, which keep revisions increasing across
  Gateway restarts;
- `service-name-promotions.json`, which is required for guarded promotion
  rollback;
- the configured FRP lease store.

Restore the state directory together with the matching Gateway configuration
and binary. Do not reset only the revision file while Agents retain a shared
model root. The current file implementation is behind `ConfigRevisionStore` so
a later Redis allocator can provide the same strictly increasing contract.

## Repeatable pre-production simulation

Run from the repository root with Go 1.23. These tests create temporary model
roots and HTTP artifact sources; they do not require a GPU, object-store
credentials, or production configuration.

```bash
go test ./internal/agent -run \
  'TestTwoReconcilersSharedRootCancelSupersededInstallAndPreserveWinningMarker|TestTwoReconcilersSharedRootSameRevisionLocalRemovalDoesNotTombstoneGloballyDesiredDir|TestLegacyGatewayLocalRemovalCancelsWithoutPublishingTombstone|TestRemovingAllowedModelCancelsInstallAndPublishesTombstoneFence|TestStaleInstallerCannotCommitAfterNewerFenceIsPublished|TestCancelledStagedInstallPreservesExistingReadyDirectory' \
  -count=1 -v
```

The simulation proves that two independent reconcilers sharing one
`MODEL_ROOT` converge on the newer marker, the old download receives
cancellation, a stale installer cannot commit, and a cancelled staged install
does not replace the previous ready directory. It also proves that one tag's
removal at a shared revision cannot poison another tag's desired fingerprint,
that a legacy Gateway causes no tombstone, and that an explicit global removal
does publish one. Its event stream must include
`artifact_install_cancelled` for the loser and `artifact_install_commit` for the
winner; an expected supersession must not appear as `artifact_install_error`.

Before a release also run:

```bash
go test ./... -count=1
go test ./scripts -count=1
go test -race ./internal/agent ./internal/gateway ./internal/config -count=1
```

In `ui/admin`, run `npm ci`, `npm test`, and `npm run build`. The production
build refreshes the embedded files under `internal/gateway/admin_dist`; the
tree must remain clean when the committed assets are current.

## Read-only preflight

Do not alter configuration during preflight. Record without printing protected
environment values:

1. The committed release ID and whether each remote release/control directory
   has uncommitted or locally modified content.
2. Gateway image reference plus immutable image ID, current Compose service
   state, health, and restart counts. For an Agent compatibility batch, also
   record the matching Agent image references and IDs.
3. Free space for image builds, release staging, the Gateway state backup, and
   shared-model staging.
4. Owner-only readability and backup/restore capability for the complete
   Gateway config and state directories, including the revision and promotion
   files.
5. A verified rollback Gateway image/reference and a way to restore its
   matching config/state snapshot. For an Agent compatibility batch, also
   verify the matching Agent rollback image/reference.
6. A deployment path that builds from one committed archive. An Agent
   compatibility batch must update, health-check, and roll back Gateway and
   Agents as one unit; a Gateway-only release deploys only Gateway and retains
   the current Agent release/version and protocol.

Stop before cutover if the candidate commit has not been integrated into the
release source, a remote directory contains changes the tool would overwrite,
state cannot be backed up and restored as one unit, or an Agent compatibility
batch's deploy command updates only one side of that protocol batch.

## Cutover and acceptance

After all gates pass, back up config/state and record immutable rollback image
IDs. For an Agent compatibility batch, deploy Gateway and Agents from the same
committed archive. For a Gateway-only release, deploy only Gateway; do not
publish an Agent image or change Agent release/version or protocol metadata.
Do not edit the live model configuration as part of the binary cutover.

Accept an Agent compatibility batch only after all of the following are
observed:

- Gateway health succeeds and the revision file advances across a controlled
  restart without decreasing.
- All expected Agents report fresh heartbeats and consume a positive
  `config_revision`, protocol v3, and the Gateway-provided global desired
  directory set. No v2 Agent may remain `compatible`.
- Replacing a test model artifact produces cancellation/stale-fence/commit
  events in the expected order, the winning marker matches the new artifact,
  the previous ready directory remains available until commit, and the model
  returns to ready.
- Canonical billing remains the actual ledger. `group_by=alias` combines the
  request-time alias across canonical versions, leaves direct canonical traffic
  unattributed in that view, and labels occupancy as allocated.
- A non-production service-name exercise rejects unsafe candidates, promotes a
  disabled idle canonical name only to a ready target, records the archive ID,
  and rolls back through the dedicated action. Both transactions are visible
  in the Gateway event history.

## Rollback

If an Agent compatibility batch fails on either protocol side or any acceptance
gate, stop configuration edits, restore the recorded Gateway and Agent images
as a batch, and restore the matching Gateway config/state snapshot. For a
Gateway-only release, restore only the recorded Gateway image and matching
Gateway config/state; do not publish or change an Agent. Preserve the shared
model root and worker logs for diagnosis. Health-check the Gateway, require
fresh Agent heartbeats when an Agent compatibility batch was rolled back, and
verify that the restored revision state is consistent before resuming traffic
or model changes.

Alias-only model rollback normally repoints the stable alias to the previous
ready canonical model. Promotion rollback instead uses the stored archive ID
and is refused after conflicting namespace or policy edits; do not bypass that
guard with manual YAML changes.
