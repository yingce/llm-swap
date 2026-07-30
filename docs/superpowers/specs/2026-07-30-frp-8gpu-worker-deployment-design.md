# FRP 8-GPU Worker Deployment Design

## Goal

Deploy one eight-GPU NVIDIA RTX 4090 host as eight independent llm-swap
workers, with one worker container pinned to each GPU. The workers register
with the existing HTTPS gateway under the `gpu-4090` tag. Their inference data
path uses embedded FRPC TCP proxies. The gateway-to-agent WebSocket tunnel is
removed rather than retained as a fallback.

## Scope

- Remove WebSocket tunnel transport from the gateway and agent.
- Add gateway-owned FRP transport configuration and TCP slot leases.
- Start a version-matched FRPC Go client inside each agent process.
- Run eight agent/llama-swap containers in one Compose project on the worker
  host; each agent owns its embedded FRPC client.
- Use one outbound FRP TCP proxy per worker, with a gateway-assigned remote
  port in a configured range.
- Run a test gateway on the worker host first. It validates the complete
  path through the currently external FRPS address before migration to the
  real gateway host.
- Preserve the gateway's ownership of routing, concurrency, queues, retries,
  and model lifecycle policy.

This work does not add Redis, replace the gateway control plane, or change
model/tag policy semantics. The first version targets one gateway. A later
multi-gateway deployment must move the transport lease store to shared durable
coordination before accepting agents from more than one gateway instance.

## Topology

```text
test gateway on worker host
  -> external-FRPS-address:remote_port
  -> outbound FRPC tunnel
  -> worker-gpuN container:6006
  -> local llama-swap and its selected GPU N
```

For the initial simulation, the test gateway uses the public FRPS address.
For a later real-gateway migration, FRPS may join the gateway Compose network
and the worker URL can use its private `frps:remote_port` service address.

Each agent is identified by `<instance-id, gpu-index>`. `gpu-index` is local
to the host; the gateway assigns a separate globally reusable `transport_slot`.
With `port_start: 2000`, a lease for slot `n` has
`remote_port = 2000 + n`.

## Direct FRP Transport

The new agent transport has no gateway-held data-plane connection:

- Gateway proxying, metrics scraping, and model lifecycle actions use the
  worker's advertised direct FRP URL only.
- The agent continues HTTPS config polling, heartbeat reporting, artifact
  installation, local llama-swap state collection, and gateway-controlled
  restarts.
- A small adapter around a pinned FRP Go client version creates one FRPC client
  per single-GPU agent. FRPC runs under the same agent context and is canceled
  on agent shutdown.
- FRPC is not installed as a standalone binary or Compose sidecar.
- Tailscale is not built into the FRP worker image. Existing non-FRP images are
  not changed by this deployment-specific build argument.

The gateway and agent remove their tunnel route, registry, request-response
multiplexing, WebSocket ping/pong, and tunnel-specific test coverage. The
gateway retains its existing HTTP worker URL proxy behavior.

## Gateway-Controlled FRP Configuration

Workers do not carry FRP server details, port ranges, or FRP credentials in
their static local configuration. The authenticated gateway config response
supplies the effective transport configuration:

```yaml
transport:
  type: frp_tcp
  frp:
    server_addr: frps.example.invalid
    server_port: 7000
    auth_token: <secret>
    port_start: 2000
    port_end: 3000
    lease_ttl_seconds: 180
```

The real token is a gateway secret and is never committed, logged, or returned
by the admin UI.

The agent uses its existing agent-token-authenticated HTTPS channel to obtain
the transport payload. The sensitive FRP fields are envelope-encrypted with an
AES-GCM key derived by HKDF from the agent token, the agent ID, and the gateway
configuration generation. This is defense in depth for transient config
storage and accidental logging; HTTPS remains the transport protection.

The agent decrypts the payload only in memory. It never writes the FRP token to
the local agent YAML, Compose file, or normal logs.

## Transport Slot Lease

The gateway owns a small persistent transport lease store. It maps a stable
agent identity (`instance-id` plus local GPU index) to a slot and lease
generation.

1. The agent reads the encrypted FRP endpoint and configured port range.
2. It requests or renews a transport lease from the gateway.
3. The gateway returns `transport_slot`, `remote_port`, `lease_id`,
   `generation`, and `expires_at`.
4. The agent starts embedded FRPC only for that lease and waits for it to be
   ready before advertising its llama-swap URL in heartbeat.
5. Heartbeats renew the lease. Graceful shutdown releases it.

The gateway marks a worker unavailable with the normal short heartbeat stale
threshold, but does not release its transport slot until a longer lease TTL
expires. This prevents temporary network loss from immediately reassigning the
same FRP port. An old agent that reconnects after its lease was reassigned must
stop its obsolete FRPC client and acquire a fresh generation before it can
heartbeat as available.

The first version persists this mapping on the single gateway host. If a stale
FRPC connection still owns a port after a lease expires, FRPC registration
fails and the agent requests another slot; it cannot serve traffic until a
matching active lease and FRPC registration exist.

## Worker Compose Project

The worker project lives under `/data0/images/llm-swap-8x4090`.

- `worker-gpu0` through `worker-gpu7` use the same locally built agent image
  and each set `NVIDIA_VISIBLE_DEVICES` to one distinct GPU index.
- The agent image is built on this GPU worker host from `Dockerfile.agent` with
  the required runtimes and `LLMSWAP_INSTALL_TAILSCALE=0`. The eight services
  reuse that image.
- All workers expose container port `6006` only on the private Compose network;
  no worker port is published on the worker host and no FRPC sidecar exists.
- Worker IDs use the supplied deployment ID with a stable `-gpu0` through
  `-gpu7` suffix.
- Every worker uses the existing `gpu-4090` tag and its existing agent
  credential. The static credential lives only in a host-local, mode-0600
  environment file and never in Git or the Compose file. FRP credentials come
  from the encrypted gateway transport payload.
- `/data0/images/llm-swap-8x4090/models` is shared between workers. The agent's
  artifact locks make shared installation safe, while each container keeps its
  rendered config, logs, and llama-swap state in a separate host directory.

## Validation and Rollback

Validation proceeds in order:

1. Verify all eight physical GPUs and Docker GPU access before deployment.
2. Validate rendered test-gateway and worker Compose configuration without
   secrets in output.
3. Verify all eight embedded FRPC clients register their leased ports and each
   FRPS port reaches the matching worker's llama-swap health endpoint.
4. Verify all eight agents heartbeat under `gpu-4090`, advertise direct URLs,
   and have valid current lease generations.
5. Run a gateway-routed non-streaming and streaming request against an eligible
   model and confirm routing uses the advertised FRP endpoint.

If any worker registration or request-path check fails, stop the worker Compose
project, revoke its leases, and restore the prior gateway/agent release. This
hard-cut transport does not retain a tunnel compatibility path.
