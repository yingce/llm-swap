# Phase 1 Gap Closure Design

Date: 2026-06-21

## Goal

Close the missing Phase 1 runtime gaps in the llama-swap cluster gateway and agent without expanding into Redis, KV-cache affinity, or multi-gateway operation.

## Scope

- Gateway enforces model, tag, and worker concurrency and queue limits before dispatching user requests.
- Gateway owns proxy retry count through `gateway.proxy_attempts`.
- Agent reports llama-swap `/running` models in heartbeat payloads.
- Gateway runs a conservative loaded-replica reconciler that unloads excess idle replicas through llama-swap.
- Gateway metrics scraper also counts unique `/api/performance` samples.
- Agent runtime configuration supports config file, environment variables, and command-line flags through Viper and pflag.
- Agent defaults local paths under `/opt/llmswap`.
- Agent derives `swap_url` from Tailscale IPv4 plus `swap_port` when possible, falling back to local IPv4 plus `swap_port`; explicit `swap_url` wins.

## Non-Goals

- No Redis or distributed state.
- No active runtime warm request generation. `warm_when_idle` remains startup preload only.
- No blind concatenation of worker Prometheus text.
- No cache-affinity routing.

## Design

Add a gateway `QueueLimiter` with keyed model, tag, and worker gates. Each gate admits immediately when active count is below the configured limit, queues up to the configured queue length, and returns distinct queue-full or timeout errors. Requests release every acquired gate exactly once when proxy dispatch finishes or fails.

The gateway runtime loader uses `/opt/llmswap/gateway.yaml` by default and supports environment and command-line overrides through Viper and pflag. Proxy retry count defaults to 3 and is configured only in gateway config.

Add a llama-swap state client in the agent. Each reconcile cycle pulls `{llama_swap_url}/running`, tolerates transient errors by recording `last_error`, and includes parsed `running_models` in the heartbeat. Artifact and config reconcile continue even when `/running` fails.

Add a gateway `Reconciler` for `max_loaded`. It snapshots healthy idle workers, counts ready running model replicas, and unloads extra replicas only when `model_loaded_count > max_loaded`, `max_loaded > 0`, and the worker has no active gateway-owned requests. The reconciler starts as a periodic background loop from `cmd/gateway`.

Extend metrics scraping with `/api/performance` sample dedupe. The gateway exports a counter for unique performance samples and a scrape-error counter for failures.

Keep worker agents control-plane stateless. Agents do not own active request counts, queues, retry policy, replica counts, or scheduling decisions. Gateway tag policy determines installable/runnable models and all concurrency or quantity controls.

## Testing

Tests cover queue full and timeout behavior, proxy integration with limits, heartbeat running model reporting, idle-only unload reconciliation, and performance scrape dedupe. Verification command is:

```bash
docker compose run --rm dev go test ./...
```
