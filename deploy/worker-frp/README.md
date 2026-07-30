# Eight-GPU FRP worker deployment

This deployment runs eight identical worker containers, one for each physical
GPU `0` through `7`. The worker agent embeds FRPC, so there is no FRPC sidecar
and no worker port is published by Docker. The gateway allocates the public TCP
port and sends the FRPS address, token, and lease information to each worker in
its encrypted bootstrap response.

The files are intentionally credential-free. They do not provide a usable
model, object-store location, gateway token, or FRPS token.

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
secret file, and checks for eight host GPUs when `nvidia-smi` is available:

```bash
LLMSWAP_VERIFY_REQUIRE_8_GPUS=1 bash ./verify.sh ./.env
docker compose --env-file ./.env -f ./compose.yaml config >/dev/null
```

Omit `LLMSWAP_VERIFY_REQUIRE_8_GPUS=1` only when validating the artifacts on a
non-deployment machine; the Compose checks still run, but the host GPU count is
reported rather than enforced.

Build the shared image once on the GPU host. All eight services use that exact
image tag, so start the group with `--no-build`:

```bash
docker compose --env-file ./.env -f ./compose.yaml build worker-gpu0
docker compose --env-file ./.env -f ./compose.yaml up -d --no-build
docker compose --env-file ./.env -f ./compose.yaml ps
```

The image build uses `LLMSWAP_RUNTIME=all` and
`LLMSWAP_INSTALL_TAILSCALE=0`. If the build host requires network proxying or
package mirrors, provide standard Docker build configuration outside this
credential-free Compose file.

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

## Stop and roll back

Stop the workers without deleting bind-mounted logs or models:

```bash
docker compose --env-file ./.env -f ./compose.yaml down --timeout 45
```

For rollback, restore the previous gateway configuration and previous
`WORKER_IMAGE` tag, rerun `verify.sh`, and start with `--no-build`. Preserve the
gateway lease-store directory during a test or rollback so active/quarantined
port ownership is not forgotten. The shared model root and per-worker logs are
not removed by `docker compose down`.
