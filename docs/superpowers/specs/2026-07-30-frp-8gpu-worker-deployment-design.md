# FRP 8-GPU Worker Deployment Design

## Goal

Deploy one eight-GPU NVIDIA RTX 4090 host as eight independent llm-swap
workers, with one worker container pinned to each GPU. The workers register
with the existing HTTPS gateway under the `gpu-4090` tag. Their inference data
path uses FRP TCP proxies rather than the gateway-to-agent WebSocket tunnel.

## Scope

- Add an explicit agent tunnel enable switch, enabled by default for backwards
  compatibility.
- Run eight agent/llama-swap containers plus one FRPC container in one Compose
  project on the worker host.
- Use one outbound FRP TCP proxy per worker, with gateway-side ports `2001`
  through `2008`.
- Configure the gateway container to reach those local FRPS ports without a
  public-address hairpin.
- Preserve the gateway's ownership of routing, concurrency, queues, retries,
  and model lifecycle policy.

This work does not add Redis, replace the gateway control plane, or change
model/tag policy semantics.

## Topology

```text
gateway container on FRPS host
  -> host.docker.internal:200N
  -> local FRPS TCP listener
  -> outbound FRPC tunnel
  -> worker-gpuN container:6006
  -> local llama-swap and its selected GPU N
```

`N` is `1` through `8` for FRP ports and `0` through `7` for CUDA GPU indexes.
The agent on each worker reports `http://host.docker.internal:200N` as its
`swap_url`. `host.docker.internal` is resolved only inside the gateway
container using Docker's `host-gateway` mapping; it is not a worker-host DNS
dependency.

## Agent Tunnel Behavior

The agent gains `LLMSWAP_TUNNEL_ENABLED`, defaulting to `1`.

- When enabled, behavior is unchanged: the agent starts its reverse WebSocket
  tunnel.
- When set to `0`, the agent does not create a tunnel but continues HTTP config
  polling, heartbeat reporting, artifact installation, local llama-swap state
  collection, and gateway-controlled restarts.
- The gateway needs no routing change. With no active tunnel, its existing
  direct `llama_swap_url` fallback selects the FRP endpoint.

## Worker Compose Project

The worker project lives under `/data0/images/llm-swap-8x4090`.

- `worker-gpu0` through `worker-gpu7` use the same built agent image and each
  set `NVIDIA_VISIBLE_DEVICES` to one distinct GPU index.
- All workers expose container port `6006` only on the private Compose network;
  no worker port is published on the worker host.
- Worker IDs use the supplied deployment ID with a stable `-gpu0` through
  `-gpu7` suffix.
- Every worker uses the existing `gpu-4090` tag and the gateway's existing
  agent and llama-swap credentials. Credentials live only in a host-local,
  mode-0600 environment file and never in Git or the Compose file.
- `/data0/images/llm-swap-8x4090/models` is shared between workers. The agent's
  artifact locks make shared installation safe, while each container keeps its
  rendered config, logs, and llama-swap state in a separate host directory.

## FRPC

FRPC is a service in the same Compose project. It connects to the supplied
FRPS endpoint using token authentication and declares eight TCP proxies:

| Service | GPU | Local target | FRPS port |
| --- | ---: | --- | ---: |
| worker-gpu0 | 0 | worker-gpu0:6006 | 2001 |
| worker-gpu1 | 1 | worker-gpu1:6006 | 2002 |
| worker-gpu2 | 2 | worker-gpu2:6006 | 2003 |
| worker-gpu3 | 3 | worker-gpu3:6006 | 2004 |
| worker-gpu4 | 4 | worker-gpu4:6006 | 2005 |
| worker-gpu5 | 5 | worker-gpu5:6006 | 2006 |
| worker-gpu6 | 6 | worker-gpu6:6006 | 2007 |
| worker-gpu7 | 7 | worker-gpu7:6006 | 2008 |

FRP credentials are supplied through the same host-local secret environment
file or an equally restricted generated FRPC configuration. They are not
stored in repository files.

## Gateway Host Configuration

The gateway Compose service receives:

```yaml
extra_hosts:
  - "host.docker.internal:host-gateway"
```

The gateway's process network therefore reaches the FRPS listeners through the
local Docker host. It must not use the public FRPS/gateway IP for its own
worker URLs, which could require public-address hairpin routing.

## Validation and Rollback

Validation proceeds in order:

1. Verify all eight physical GPUs and Docker GPU access before deployment.
2. Validate rendered Compose and FRPC configuration without secrets in output.
3. Verify all eight FRPC proxies are registered and each local FRPS port can
   reach the matching worker's llama-swap health endpoint from the gateway
   container.
4. Verify all eight agents heartbeat under `gpu-4090`, report no active tunnel,
   and advertise the expected direct URL.
5. Run a gateway-routed non-streaming and streaming request against an eligible
   model and confirm routing uses the advertised direct endpoint.

If any worker registration or request-path check fails, stop the Compose
project and remove the gateway `extra_hosts` change. Re-enabling the tunnel
switch restores the former agent transport behavior without changing model
configuration.
