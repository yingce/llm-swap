# Dual-Host Release and Production Tailscale Cleanup Design

## Goal

Provide a repeatable, operator-run release command that deploys one committed
source revision to the llm-swap Gateway and the eight-GPU Worker host without
requiring a local GPU or a container registry. The production release path must
not install, configure, or run Tailscale; FRP remains the only Worker transport.

## Scope

The release tool runs on the operator workstation and uses SSH to:

- build and deploy the Gateway on `root@llm-swap-gateway`;
- build and deploy the Worker image on `root@gpu4090` over port `8230`;
- preserve protected server-side configuration, model data, logs, database
  data, and secret files;
- create verified rollback records before every service cutover.

The tool does not deploy from a mutable working tree, create a registry, alter
model policy, rotate tokens, or modify the unrelated host-level Tailscale,
DERP, GOST, or cloud-route services.

## Release Interface

`scripts/fabfile.py` becomes the supported release entry point. It exposes
separate Gateway and Worker operations plus explicit full orchestration:

```sh
fab -f scripts/fabfile.py status.gateway
fab -f scripts/fabfile.py status.worker
fab -f scripts/fabfile.py deploy.gateway
fab -f scripts/fabfile.py deploy.worker
fab -f scripts/fabfile.py deploy.all
fab -f scripts/fabfile.py rollback.gateway
fab -f scripts/fabfile.py rollback.worker
```

`deploy.all` uploads the same `git archive HEAD` release to both hosts, deploys
and health-checks Gateway first, then deploys Worker. A Gateway failure stops
the command before any Worker change. A Worker failure leaves the verified
Gateway release in place and reports the worker rollback command; it does not
silently roll Gateway back because Gateway may already be serving traffic.

Every deploy command records the full commit, short commit, UTC timestamp,
image reference, and immutable Docker image ID in a root-readable deployment
record. Source archives contain committed files only; untracked or modified
local files are never included and are reported before transfer.

## Gateway Release

Gateway release files are staged at
`/opt/llmswap/deploy/releases/<commit>-<timestamp>`. The active Compose control
directory remains `/opt/llmswap/deploy/production`; its root-readable `.env`
and runtime data directories are never replaced from the repository.

The release process performs these steps in order:

1. upload and unpack the committed source archive;
2. run Gateway-focused Go tests in the pinned Go container;
3. build `llmswap-gateway:<commit>` on the Gateway server;
4. validate the staged production Compose definition using the protected `.env`;
5. back up current Compose metadata and image identity;
6. atomically update only tracked non-secret Compose assets, recreate Gateway,
   and wait for `/healthz` to return HTTP 204;
7. persist the successful release record.

On a failed health check, the tool restores the previous Gateway image and
Compose definition while preserving bind-mounted configuration, state, logs,
PostgreSQL, and VictoriaMetrics data.

## Worker Release

Worker release files are staged at
`/data0/images/llm-swap-8x4090/releases/<commit>-<timestamp>`. The active
Compose directory remains
`/data0/images/llm-swap-8x4090/deploy/worker-frp`; its existing private `.env`,
agent token file, worker state, model root, and logs are not copied from the
operator workstation or overwritten by a source archive.

The release process performs these steps in order:

1. upload and unpack the same committed source archive;
2. run Agent/config/installer Go tests in a Go container on the Worker host;
3. render and validate the existing private Worker `.env` using `verify.sh`,
   require eight visible GPUs, and validate the Compose model;
4. build one local immutable image tagged `llmswap-agent:frp-<commit>`;
5. record the prior `WORKER_IMAGE` reference and Docker image ID with mode 0600;
6. atomically replace only `WORKER_IMAGE` in the protected `.env` after the new
   image is inspectable;
7. recreate the eight Worker services with `--no-build`, verify every service
   remains running, and require each container to expose exactly one GPU;
8. persist the successful release record.

Worker rollback verifies that the saved image reference still resolves to the
saved image ID before restoring `WORKER_IMAGE` and recreating the eight
services. It does not pull or rebuild an old tag.

## Production Tailscale Cleanup

The cleanup applies only to the Gateway and FRP Worker production path:

- remove Tailscale environment variables, capability/device requests, mounts,
  installation stages, runtime entrypoint behavior, and documentation from the
  Gateway production image and Compose release path;
- make the Worker production image build Tailscale-free by default and remove
  the production Compose build argument and verification/doc references that
  only exist to disable it;
- remove stale Tailscale-specific logic from the existing Gateway Fabric
  release implementation;
- retain optional Tailscale support in the legacy Worker installer and Agent
  compatibility code, including its tests and configuration fallback.

The host-level `tailscaled` service remains out of scope because other host
services currently use it.

## Failure Handling and Safety

- All remote scripts run with strict shell settings and create temporary files
  with restrictive permissions.
- Secret values are read only on their owning host and are neither printed nor
  transferred by the release tool.
- Deploy commands refuse missing protected configuration, missing rollback
  records, invalid Compose, unavailable Docker image references, unhealthy
  services, or fewer than eight Worker GPUs.
- Image builds and validation finish before service replacement begins.
- Gateway and Worker rollback actions are separate and explicit.

## Verification

Automated tests cover command routing, source archive identity, host ordering,
release-record creation, Tailscale-free production definitions, and rollback
image identity checks. Repository verification runs Go tests, Worker deployment
verification tests, frontend tests/build when Gateway image assets change, and
Compose config validation. Deployment verification checks Gateway `/healthz`,
PostgreSQL readiness, VictoriaMetrics health, all eight Worker container
statuses, one-GPU-per-container visibility, and Worker transport-ready logs.
