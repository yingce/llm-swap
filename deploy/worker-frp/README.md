# Eight-GPU FRP worker deployment

This deployment runs eight identical worker containers, one for each physical
GPU `0` through `7`. The worker agent embeds FRPC, so there is no FRPC sidecar
and no worker port is published by Docker. The gateway allocates the public TCP
port and sends the FRPS address, token, and lease information to each worker in
its encrypted bootstrap response.

The files are intentionally credential-free. They do not provide a usable
model, object-store location, gateway token, or FRPS token.

This deployment assumes one fully trusted worker trust domain. The shared
agent bearer authenticates access to agent APIs but is not bound to an
`agent_id`; any worker that has it can claim another identity. Do not use this
design for untrusted-worker multi-tenancy. Per-agent credentials and identity
authorization are outside the current implementation.

## Prerequisites

- A Linux GPU host with Docker Engine, the Compose plugin, and NVIDIA Container
  Toolkit configured.
- Eight GPUs visible to `nvidia-smi -L`.
- An authorized gateway agent token in a root-readable file.
- A gateway and FRPS configuration owned by the operator.
- Enough space for one shared model root and eight separate log directories.

## Configure host paths and the token file

Copy the environment template and replace every placeholder path or URL:

```bash
cd deploy/worker-frp
cp .env.example .env
${EDITOR:-vi} .env
```

Set `WORKER_IMAGE` to an immutable build reference. A deployment tag must end
with at least seven hexadecimal Git commit characters, for example
`registry.internal:5000/llmswap/agent:frp-1a2b3c4d5e6f`, or use a full
`@sha256:` image digest. `verify.sh` rejects `latest`, the template value, and
mutable labels such as `frp-cu128`.

Set `LLMSWAP_BUILD_VERSION` to the human Agent release identifier (the current
v3 fencing release is `2026.08.08.1`) and set `LLMSWAP_BUILD_COMMIT` to the
exact source Git SHA. They are separate required Docker build args: never put
the commit SHA in the version field or reuse one value for both. `verify.sh`
rejects equal values without printing either value. These build variables are
not runtime container identity or credentials.

Load the path references without printing them, create private directories,
then write the agent token through a hidden prompt:

```bash
set -a
. ./.env
set +a
install -d -m 0700 "$WORKER_STATE_ROOT" "$MODEL_ROOT" "$(dirname "$AGENT_TOKEN_FILE")"
for gpu in 0 1 2 3 4 5 6 7; do
  install -d -m 0700 "$WORKER_STATE_ROOT/worker-gpu${gpu}/logs"
done
umask 077
read -r -s -p 'Agent token: ' agent_token
printf '\n'
printf '%s' "$agent_token" >"$AGENT_TOKEN_FILE"
unset agent_token
chmod 0600 "$AGENT_TOKEN_FILE"
```

Only each worker's `logs` directory is mounted under `/opt/llmswap`. Do not bind
an empty host directory over `/opt/llmswap` itself: that would hide the agent,
llama-swap, runtime wrappers, and Python environments installed in the image.
The generated `agent.yaml`, `llama-swap.yaml`, and hostname-derived identity can
be rebuilt when a container is recreated. Hostname `worker-gpuN` remains stable,
the gateway persists leases, logs persist separately, and models are shared.

## Prepare a test gateway

Copy `gateway.test.yaml.example` to the gateway's protected configuration
directory. Replace all `replace-*` fields and `frps.example.test` with values
from an authorized source. The agent token in `tokens.agent` must equal the
contents of `AGENT_TOKEN_FILE`. Keep the gateway configuration mode `0600`.

`transport.frp.server_addr` is the address embedded FRPC clients use to reach
FRPS and the public host workers report in heartbeats. `dial_addr` is the host
the gateway uses when calling a leased `remote_port`; omitting it preserves the
old behavior and uses `server_addr`. When gateway and FRPS run on the same host,
set `dial_addr: 127.0.0.1` to avoid an external hairpin. If the gateway instead
dials an FRPS public address, that remote-port hop carries ordinary plaintext
HTTP: bearer tokens, prompts, and responses are not protected by FRP token
authentication. Do not send real traffic over that path without a private
network or external protection such as IPsec. llama-swap does not provide HTTPS
for this hop, so setting an `https://` address is neither valid nor a workaround.

Set `lease_store_path` to an absolute path on durable gateway storage. The
example uses `/opt/llmswap/state/transport-leases.json`; preserve its parent
directory across gateway recreation and rollback. Omitting the field keeps the
compatibility default `transport-leases.json` beside the loaded gateway config.
Changing the path through Config Ops is saved but requires a gateway restart.

The placeholder model is disabled so the template can be parsed safely. Before
inference testing, replace it with a real existing model and object-store
configuration; this repository does not claim that the placeholder artifact
exists. Keep the `gpu-4090` tag policy and the `2000..2007` lease range for this
eight-worker test.

For same-host testing, set `LLMSWAP_GATEWAY_URL=http://gateway-host:8080` and
run the gateway on the host's port 8080. Compose maps `gateway-host` to Docker's
host gateway, so worker control traffic does not loop through the public
gateway address. The worker still connects outbound to the external FRPS TCP
endpoint delivered by the gateway. For production, replace the gateway URL
with the production control-plane URL; no Compose topology change is needed.

## Verify, build once, and start

Verification renders the complete Compose model into a temporary file, checks
the service/GPU/secret/mount invariants without printing the environment or
secret file, validates the host paths and secret permissions, and checks for
eight host GPUs when `nvidia-smi` is available. It defaults to `./.env`; the
checked-in `.env.example` is deliberately rejected because it is not deployable:

```bash
LLMSWAP_VERIFY_REQUIRE_8_GPUS=1 bash ./verify.sh ./.env
docker compose --env-file ./.env -f ./compose.yaml config >/dev/null
```

Omit `LLMSWAP_VERIFY_REQUIRE_8_GPUS=1` only when validating the artifacts on a
non-deployment machine; the Compose checks still run, but the host GPU count is
reported rather than enforced.

All eight services use one immutable image reference, but commit tags and image
digests have different preparation paths.

### Commit-tag build path

For a tag ending in the current Git commit, build the shared image once on the
GPU host. First put a new commit-suffixed tag plus the distinct Agent release
version and source commit in `.env`; never rebuild or overwrite an older
deployment tag. `docker compose build` passes those two values independently
to `Dockerfile.agent` and can assign this tag to its output:

```bash
LLMSWAP_VERIFY_REQUIRE_8_GPUS=1 bash ./verify.sh ./.env
docker compose --env-file ./.env -f ./compose.yaml build worker-gpu0
docker image inspect "$(sed -n 's/^WORKER_IMAGE=//p' ./.env)" >/dev/null
docker compose --env-file ./.env -f ./compose.yaml up -d --no-build
docker compose --env-file ./.env -f ./compose.yaml ps
```

The image build uses `LLMSWAP_RUNTIME=all`, so one stateless worker image can serve the configured `vllm`, `vllm-omni`,
`sglang`, and `llamacpp` models. If the build host requires network proxying or
package mirrors, provide standard Docker build configuration outside this
credential-free Compose file.

### Digest deployment path

For `WORKER_IMAGE=registry/path@sha256:...`, Compose cannot assign that digest
to a local build result. Pull the exact published digest, or preload it through
an approved offline image-transfer process that makes the exact digest
reference inspectable, before verification. A digest deployment must not run
`docker compose build`; it only starts the already available image with
`--no-build`:

```bash
set -a
. ./.env
set +a
docker pull "$WORKER_IMAGE"
docker image inspect "$WORKER_IMAGE" >/dev/null
LLMSWAP_VERIFY_REQUIRE_8_GPUS=1 bash ./verify.sh ./.env
docker compose --env-file ./.env -f ./compose.yaml up -d --no-build
docker compose --env-file ./.env -f ./compose.yaml ps
unset WORKER_IMAGE
```

`verify.sh` prints either `commit-tag build path` or `preloaded digest
deployment path`. In digest mode it fails unless `docker image inspect` can
resolve the exact digest locally.

Before changing `WORKER_IMAGE`, record the currently running reference and
digest in a private rollback file. This record is not a secret, but mode `0600`
prevents accidental edits or broad disclosure of deployment metadata:

```bash
set -a
. ./.env
set +a
rollback_dir="$WORKER_STATE_ROOT/rollback"
install -d -m 0700 "$rollback_dir"
container_id="$(docker compose --env-file ./.env -f ./compose.yaml ps -q worker-gpu0)"
current_image="$(docker inspect --format '{{.Config.Image}}' "$container_id")"
current_image_id="$(docker image inspect --format '{{.Id}}' "$current_image")"
rollback_tmp="$(mktemp "$rollback_dir/.worker-image.XXXXXX")"
chmod 0600 "$rollback_tmp"
printf 'WORKER_IMAGE=%s\nWORKER_IMAGE_ID=%s\n' \
  "$current_image" "$current_image_id" >"$rollback_tmp"
mv -f -- "$rollback_tmp" "$rollback_dir/worker-image.env"
unset current_image current_image_id
```

The shared model root uses the agent's filesystem locks, so eight workers can
reuse artifacts without independently downloading or installing the same model.

## Validate the running path

Confirm that every container sees exactly one assigned GPU. NVIDIA remaps that
physical device into the container as CUDA device `0`, even though Compose pins
host devices `0` through `7` respectively:

```bash
for gpu in 0 1 2 3 4 5 6 7; do
  docker compose --env-file ./.env -f ./compose.yaml \
    exec -T "worker-gpu${gpu}" nvidia-smi -L
done
```

Then validate in order:

1. `curl --fail http://127.0.0.1:8080/healthz` succeeds on the gateway host.
2. All eight workers remain running and their logs report transport readiness.
3. The gateway worker view shows `worker-gpu0` through `worker-gpu7` with tag
   `gpu-4090`, current heartbeats, distinct leases, and leased ports 2000–2007.
4. Each FRP TCP endpoint reaches that worker's llama-swap health/model path.
5. Only after installing an authorized model, wait for a ready replica, call
   `/v1/models`, and send a small inference request through the gateway.

Do not print the gateway, agent, client, or FRPS tokens while collecting these
checks. A healthy agent registration alone does not prove that a model exists
or is ready for inference.

## Rotate the shared agent token

This first version has one shared agent token and no dual-token overlap. Rotate
it with a short worker outage so the gateway and all eight new container secret
mounts change as one operation:

1. Stop all workers with `docker compose --env-file ./.env -f ./compose.yaml
   down --timeout 45`.
2. Keep a mode-`0600` rollback copy in the token file's directory. Create the
   replacement with `mktemp` in that same directory, write it through a hidden
   prompt, set mode `0600`, and atomically rename it over `AGENT_TOKEN_FILE`.
   Do not pass token text as a command argument or print it.
3. Update `tokens.agent` in the protected gateway configuration through the
   operator's secret-management path, restart the gateway, and require
   `/healthz` to succeed before continuing.
4. Run `verify.sh ./.env`, then start workers with `docker compose --env-file
   ./.env -f ./compose.yaml up -d --force-recreate --no-build`. Force recreation
   is required: it gives every container a new secret mount/inode and rebuilds
   generated agent configuration.
5. Verify all eight worker heartbeats and their fresh/current leases, then
   delete the rollback token copy securely according to host policy.

One safe host-side replacement sequence for step 2 is:

```bash
set -a
. ./.env
set +a
token_dir="$(dirname "$AGENT_TOKEN_FILE")"
rollback_token="$(mktemp "$token_dir/.agent-token.rollback.XXXXXX")"
new_token_file="$(mktemp "$token_dir/.agent-token.new.XXXXXX")"
chmod 0600 "$rollback_token" "$new_token_file"
cp -- "$AGENT_TOKEN_FILE" "$rollback_token"
chmod 0600 "$rollback_token"
read -r -s -p 'New agent token: ' new_agent_token
printf '\n'
printf '%s' "$new_agent_token" >"$new_token_file"
unset new_agent_token
chmod 0600 "$new_token_file"
mv -f -- "$new_token_file" "$AGENT_TOKEN_FILE"
```

If rotation fails, stop the workers, atomically restore the old secret from
`rollback_token`, restore the previous gateway `tokens.agent`, restart and
health-check the gateway, then run `up -d --force-recreate --no-build` again.
Compose's in-container secret mode `0400` does not replace the required secure
host-file mode; `verify.sh` rejects host token files readable by group or other.

```bash
mv -f -- "$rollback_token" "$AGENT_TOKEN_FILE"
# Restore the previous gateway tokens.agent through the secret-management path,
# restart it, and require /healthz before recreating the workers.
docker compose --env-file ./.env -f ./compose.yaml \
  up -d --force-recreate --no-build
```

## Stop and roll back

Stop the workers without deleting bind-mounted logs or models:

```bash
docker compose --env-file ./.env -f ./compose.yaml down --timeout 45
```

For image rollback, do not trust the old tag name alone: a registry or local
retag could make it resolve to different bytes. Validate the recorded reference
against its recorded image ID before editing `.env` or recreating workers. The
helper performs this check without printing the reference:

```bash
set -a
. ./.env
set +a
rollback_record="$WORKER_STATE_ROOT/rollback/worker-image.env"
bash ./verify_image_rollback.sh "$rollback_record"

OLD_WORKER_IMAGE="$(sed -n 's/^WORKER_IMAGE=//p' "$rollback_record")"
recorded_image_id="$(sed -n 's/^WORKER_IMAGE_ID=//p' "$rollback_record")"
docker image inspect "$OLD_WORKER_IMAGE" >/dev/null
resolved_image_id="$(docker image inspect --format '{{.Id}}' "$OLD_WORKER_IMAGE")"
if [[ "$resolved_image_id" != "$recorded_image_id" ]]; then
  printf '%s\n' 'rollback image identity mismatch; refusing rollback' >&2
  exit 1
fi
```

Only after `[[ "$resolved_image_id" == "$recorded_image_id" ]]` is true, put
`OLD_WORKER_IMAGE` back into `.env`, rerun `verify.sh`, and start with
`up -d --force-recreate --no-build`; do not rebuild or pull the old reference
during rollback. Restore the matching previous gateway configuration when the
release changed gateway behavior. Preserve the gateway lease-store directory
during a test or rollback so active/quarantined port ownership is not
forgotten. The shared model root and per-worker logs are not removed by
`docker compose down`.
