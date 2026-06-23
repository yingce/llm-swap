# Agent Notes

This repo implements a single-gateway, many-worker control plane for llama-swap
model serving. Read `docs/agents/project-map.md` before making non-trivial
changes.

## Current Architecture

- Gateway owns routing, request proxying, worker registry state, concurrency,
  queueing, retries, loaded-replica reconciliation, request logs, metrics, and
  the dashboard UI.
- Worker agents are intentionally thin and mostly stateless. An agent installs
  artifacts, renders local `llama-swap.yaml`, reports local llama-swap state,
  and restarts llama-swap only when the gateway allows it.
- llama-swap is still the per-worker runtime switcher. The gateway does not
  start model runtimes directly; it proxies to worker llama-swap URLs.

## Important Defaults

- Runtime root defaults to `/opt/llmswap`.
- Gateway config defaults to `/opt/llmswap/gateway.yaml`.
- Agent config defaults to `/opt/llmswap/agent.yaml`.
- Agent model root defaults to `/opt/llmswap/models`.
- Agent renders `/opt/llmswap/llama-swap.yaml`.
- Gateway listens on `:8080` by default.
- Worker llama-swap listens on `swap_port`, default `6006`.
- `tokens.llama_swap` and `agent.llama_swap_token` are optional; when omitted,
  they inherit the agent token.

## Safety Rules

- Do not put real tokens, host credentials, or production-only secrets in docs
  or examples.
- Do not move request concurrency, queues, retry policy, active counts, or model
  replica policy into the worker agent.
- Use tests first for behavior changes. Existing Go tests are the primary
  regression net.
- Preserve user changes in the working tree. Do not reset or checkout files
  unless explicitly asked.

## Common Commands

```bash
go test ./...
go test ./internal/gateway -count=1
go test ./internal/agent -count=1
go test ./internal/config -count=1
```

Worker install dry-run tests require a POSIX shell:

```bash
go test ./scripts -count=1
```

## Main Entry Points

- Gateway: `cmd/gateway/main.go`
- Agent: `cmd/agent/main.go`
- Worker installer: `scripts/install-worker.sh`
- Example gateway config: `examples/gateway.yaml`
- Example agent config: `examples/agent.yaml`

