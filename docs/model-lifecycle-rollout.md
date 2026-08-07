# Safe model lifecycle rollout

This runbook covers the protocol revision, shared model-directory fencing,
alias billing, and service-name promotion. It intentionally contains no host
names, deployment paths outside the repository defaults, credentials, or
production model values.

## Release unit and durable state

Release the Gateway and every Agent that shares a model root as one compatibility
batch. The Gateway response now carries `config_revision`; upgraded Agents use
it to cancel and fence stale artifact work. Mixed versions do not provide the
rollout safety guaranteed by this release.

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
  'TestTwoReconcilersSharedRootCancelSupersededInstallAndPreserveWinningMarker|TestStaleInstallerCannotCommitAfterNewerFenceIsPublished|TestCancelledStagedInstallPreservesExistingReadyDirectory' \
  -count=1 -v
```

The simulation proves that two independent reconcilers sharing one
`MODEL_ROOT` converge on the newer marker, the old download receives
cancellation, a stale installer cannot commit, and a cancelled staged install
does not replace the previous ready directory. Its event stream must include
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
2. Gateway and Agent image references plus immutable image IDs, current Compose
   service state, health, and restart counts.
3. Free space for image builds, release staging, the Gateway state backup, and
   shared-model staging.
4. Owner-only readability and backup/restore capability for the complete
   Gateway config and state directories, including the revision and promotion
   files.
5. A verified rollback image/reference for both Gateway and Agent, and a way to
   restore their matching config/state snapshot.
6. A deployment path that builds from one committed archive, updates Gateway
   and Agents as the same release unit, health-checks both, and rolls both back
   if either half fails.

Stop before cutover if the candidate commit has not been integrated into the
release source, a remote directory contains changes the tool would overwrite,
state cannot be backed up and restored as one unit, or the available deploy
command updates only one side of the protocol batch.

## Cutover and acceptance

After all gates pass, back up config/state and record immutable rollback image
IDs. Deploy the Gateway and Agents from the same committed archive. Do not edit
the live model configuration as part of the binary cutover.

Accept the batch only after all of the following are observed:

- Gateway health succeeds and the revision file advances across a controlled
  restart without decreasing.
- All expected Agents report fresh heartbeats and consume a positive
  `config_revision`.
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

If either protocol side or any acceptance gate fails, stop configuration edits,
restore the recorded Gateway and Agent images as a batch, and restore the
matching Gateway config/state snapshot. Preserve the shared model root and
worker logs for diagnosis. Health-check the Gateway, require fresh Agent
heartbeats, and verify that the restored revision state is consistent before
resuming traffic or model changes.

Alias-only model rollback normally repoints the stable alias to the previous
ready canonical model. Promotion rollback instead uses the stored archive ID
and is refused after conflicting namespace or policy edits; do not bypass that
guard with manual YAML changes.
