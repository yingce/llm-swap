# Production Compose Deployment Design

## Goal

Move the production gateway on `47.84.130.96` from supervisor-managed process
deployment to Docker Compose, while preserving `/opt/llmswap/gateway.yaml`,
request logs, worker event logs, and the optional VictoriaMetrics history store.

## Design

The repository will include a production gateway image and compose stack:

- `Dockerfile.gateway` builds a static `llm-swap-gateway` binary and runs it in
  a small Debian runtime image.
- `deploy/production/compose.yaml` runs:
  - `gateway`, exposing `8080`;
  - `victoriametrics`, exposing `8428` locally for query access;
  - `vmagent`, scraping gateway `/metrics` and remote-writing to
    VictoriaMetrics.
- `deploy/production/vmagent/promscrape.yml` targets `gateway:8080`.

Production keeps mutable state under `/opt/llmswap`:

- `/opt/llmswap/gateway.yaml` is mounted read-only into the gateway container.
- `/opt/llmswap/logs` is mounted read-write for request and worker event JSONL.
- `/opt/llmswap/data/victoriametrics` is mounted into VictoriaMetrics.

No real tokens or production secrets are committed. Existing production
`gateway.yaml` remains the source of truth.

## Migration

On the production host:

1. Install Docker Engine and the compose plugin if missing.
2. Upload the current source tree to `/opt/llmswap/deploy/current`.
3. Back up `/opt/llmswap/gateway.yaml`.
4. Enable `metrics_store` in the production config with
   `query_url: http://victoriametrics:8428`.
5. Stop supervisor `llmswap-gateway`.
6. Start `docker compose -f deploy/production/compose.yaml up -d --build`.
7. Verify `/ui/status`, `/metrics`, and compose service health.

Rollback is to stop the compose gateway and start supervisor `llmswap-gateway`
again, using the preserved binary and config.
