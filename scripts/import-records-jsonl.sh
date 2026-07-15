#!/usr/bin/env sh
set -eu

ROOT="${LLMSWAP_DEPLOY_ROOT:-/opt/llmswap/deploy/current}"
REQUESTS="${LLMSWAP_REQUEST_LOG:-/opt/llmswap/logs/gateway-requests.jsonl}"
EVENTS="${LLMSWAP_WORKER_EVENT_LOG:-/opt/llmswap/logs/gateway-worker-events.jsonl}"
GO_IMAGE="${LLMSWAP_GO_IMAGE:-golang:1.23-bookworm}"
NETWORK="${LLMSWAP_COMPOSE_NETWORK:-llmswap_default}"

if [ $# -ge 1 ]; then
  REQUESTS="$1"
fi
if [ $# -ge 2 ]; then
  EVENTS="$2"
fi

if [ -z "${PG_DSN:-}" ] && [ -z "${LLMSWAP_RECORDS_STORE_DSN:-}" ]; then
  echo "PG_DSN or LLMSWAP_RECORDS_STORE_DSN is required" >&2
  exit 1
fi

if [ ! -d "$ROOT" ]; then
  echo "deploy root not found: $ROOT" >&2
  exit 1
fi

if command -v go >/dev/null 2>&1; then
  cd "$ROOT"
  exec go run ./cmd/import-records --requests "$REQUESTS" --events "$EVENTS"
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "neither go nor docker is available" >&2
  exit 1
fi

exec docker run --rm \
  --network "$NETWORK" \
  -e PG_DSN="${PG_DSN:-}" \
  -e LLMSWAP_RECORDS_STORE_DSN="${LLMSWAP_RECORDS_STORE_DSN:-}" \
  -e LLMSWAP_RECORDS_STORE_GATEWAY_ID="${LLMSWAP_RECORDS_STORE_GATEWAY_ID:-}" \
  -e GOPROXY="${GOPROXY:-https://goproxy.cn,direct}" \
  -e GOSUMDB="${GOSUMDB:-sum.golang.google.cn}" \
  -v "$ROOT:/src:ro" \
  -v "/opt/llmswap/logs:/opt/llmswap/logs:ro" \
  -w /src \
  "$GO_IMAGE" \
  go run ./cmd/import-records --requests "$REQUESTS" --events "$EVENTS"
