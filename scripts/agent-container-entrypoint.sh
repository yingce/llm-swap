#!/usr/bin/env bash
set -euo pipefail

LLMSWAP_ROOT="${LLMSWAP_ROOT:-/opt/llmswap}"
LLMSWAP_BIN_DIR="${LLMSWAP_BIN_DIR:-$LLMSWAP_ROOT/bin}"
LLMSWAP_AGENT_BIN="${LLMSWAP_AGENT_BIN:-$LLMSWAP_BIN_DIR/llm-swap-agent}"
LLMSWAP_LLAMA_SWAP_BIN="${LLMSWAP_LLAMA_SWAP_BIN:-$LLMSWAP_BIN_DIR/llama-swap}"
LLMSWAP_AGENT_CONFIG="${LLMSWAP_AGENT_CONFIG:-$LLMSWAP_ROOT/agent.yaml}"
LLMSWAP_MODEL_ROOT="${LLMSWAP_MODEL_ROOT:-$LLMSWAP_ROOT/models}"
LLMSWAP_LLAMA_SWAP_CONFIG="${LLMSWAP_LLAMA_SWAP_CONFIG:-$LLMSWAP_ROOT/llama-swap.yaml}"
LLMSWAP_LOG_DIR="${LLMSWAP_LOG_DIR:-$LLMSWAP_ROOT/logs}"
LLMSWAP_AGENT_ID="${LLMSWAP_AGENT_ID:-$(hostname 2>/dev/null || printf worker-01)}"
LLMSWAP_AGENT_TAGS="${LLMSWAP_AGENT_TAGS:-gpu}"
LLMSWAP_SWAP_PORT="${LLMSWAP_SWAP_PORT:-6006}"
LLMSWAP_GATEWAY_URL="${LLMSWAP_GATEWAY_URL:-}"
LLMSWAP_AGENT_TOKEN="${LLMSWAP_AGENT_TOKEN:-}"
LLMSWAP_LLAMA_SWAP_TOKEN="${LLMSWAP_LLAMA_SWAP_TOKEN:-$LLMSWAP_AGENT_TOKEN}"
LLMSWAP_FORCE_CONFIG="${LLMSWAP_FORCE_CONFIG:-0}"
LLMSWAP_ENABLE_TAILSCALE="${LLMSWAP_ENABLE_TAILSCALE:-0}"
LLMSWAP_TAILSCALE_AUTHKEY="${LLMSWAP_TAILSCALE_AUTHKEY:-${TAILSCALE_AUTHKEY:-}}"
LLMSWAP_TAILSCALE_HOSTNAME="${LLMSWAP_TAILSCALE_HOSTNAME:-}"

first_non_empty() {
  local value
  for value in "$@"; do
    if [[ -n "${value// }" ]]; then
      printf '%s\n' "$value"
      return 0
    fi
  done
  return 1
}

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

require_env_when_bootstrapping() {
  local name="$1"
  local value="$2"
  if [[ -z "${value// }" ]]; then
    printf 'missing required env %s because %s does not exist\n' "$name" "$LLMSWAP_AGENT_CONFIG" >&2
    exit 1
  fi
}

render_tags_yaml() {
  local tags="$1"
  local part
  local rendered=""
  IFS=',' read -r -a parts <<< "$tags"
  for part in "${parts[@]}"; do
    part="$(trim "$part")"
    if [[ -z "$part" ]]; then
      continue
    fi
    rendered="${rendered}    - ${part}"$'\n'
  done
  if [[ -z "$rendered" ]]; then
    rendered="    - gpu"$'\n'
  fi
  printf '%s' "$rendered"
}

write_agent_config() {
  local swap_url
  swap_url="$(first_non_empty "${LLMSWAP_SWAP_URL:-}" "${LLM_SWAP_AGENT_SWAP_URL:-}" "${SWAP_URL:-}" || true)"
  local tags_yaml
  tags_yaml="$(render_tags_yaml "$LLMSWAP_AGENT_TAGS")"

  install -d "$(dirname "$LLMSWAP_AGENT_CONFIG")" "$LLMSWAP_MODEL_ROOT" "$LLMSWAP_LOG_DIR"
  {
    printf 'agent:\n'
    printf '  id: %s\n' "$LLMSWAP_AGENT_ID"
    printf '  tags:\n%s\n' "$tags_yaml"
    printf '  model_root: %s\n' "$LLMSWAP_MODEL_ROOT"
    printf '  llama_swap_config: %s\n' "$LLMSWAP_LLAMA_SWAP_CONFIG"
    printf '  llama_swap_service: supervisor\n'
    printf '  restart_command: supervisorctl restart llmswap-llama-swap\n'
    printf '  swap_port: %s\n' "$LLMSWAP_SWAP_PORT"
    if [[ -n "${swap_url// }" ]]; then
      printf '  swap_url: %s\n' "$swap_url"
    fi
    printf '  gateway_url: %s\n' "$LLMSWAP_GATEWAY_URL"
    printf '  token: %s\n' "$LLMSWAP_AGENT_TOKEN"
    printf '  llama_swap_token: %s\n' "$LLMSWAP_LLAMA_SWAP_TOKEN"
  } > "$LLMSWAP_AGENT_CONFIG"
}

start_tailscale_if_requested() {
  if [[ "$LLMSWAP_ENABLE_TAILSCALE" != "1" && -z "$LLMSWAP_TAILSCALE_AUTHKEY" ]]; then
    return 0
  fi
  if ! command -v tailscaled >/dev/null 2>&1 || ! command -v tailscale >/dev/null 2>&1; then
    printf 'tailscale requested but tailscaled/tailscale is not installed\n' >&2
    exit 1
  fi

  install -d "$LLMSWAP_ROOT/tailscale" /run/tailscale "$LLMSWAP_LOG_DIR"
  if ! pgrep -x tailscaled >/dev/null 2>&1; then
    tailscaled \
      --state="$LLMSWAP_ROOT/tailscale/tailscaled.state" \
      --socket=/run/tailscale/tailscaled.sock \
      --port=41641 \
      >>"$LLMSWAP_LOG_DIR/tailscaled.out.log" \
      2>>"$LLMSWAP_LOG_DIR/tailscaled.err.log" &
  fi

  local tries=0
  until [[ -S /run/tailscale/tailscaled.sock ]]; do
    tries=$((tries + 1))
    if [[ "$tries" -ge 30 ]]; then
      printf 'tailscaled did not create /run/tailscale/tailscaled.sock in time\n' >&2
      exit 1
    fi
    sleep 1
  done

  if [[ -n "$LLMSWAP_TAILSCALE_AUTHKEY" ]]; then
    local args=(tailscale up --auth-key "$LLMSWAP_TAILSCALE_AUTHKEY")
    if [[ -n "$LLMSWAP_TAILSCALE_HOSTNAME" ]]; then
      args+=(--hostname "$LLMSWAP_TAILSCALE_HOSTNAME")
    fi
    "${args[@]}"
  fi
}

main() {
  install -d "$LLMSWAP_BIN_DIR" "$LLMSWAP_MODEL_ROOT" "$LLMSWAP_LOG_DIR"

  if [[ ! -x "$LLMSWAP_AGENT_BIN" ]]; then
    printf 'missing agent binary: %s\n' "$LLMSWAP_AGENT_BIN" >&2
    exit 1
  fi
  if [[ ! -x "$LLMSWAP_LLAMA_SWAP_BIN" ]]; then
    printf 'missing llama-swap binary: %s\n' "$LLMSWAP_LLAMA_SWAP_BIN" >&2
    printf 'set LLAMA_SWAP_DOWNLOAD_URL at build time or mount the binary at runtime\n' >&2
    exit 1
  fi

  if [[ "$LLMSWAP_FORCE_CONFIG" == "1" || ! -f "$LLMSWAP_AGENT_CONFIG" ]]; then
    require_env_when_bootstrapping LLMSWAP_GATEWAY_URL "$LLMSWAP_GATEWAY_URL"
    require_env_when_bootstrapping LLMSWAP_AGENT_TOKEN "$LLMSWAP_AGENT_TOKEN"
    write_agent_config
  fi

  start_tailscale_if_requested

  if [[ $# -gt 0 ]]; then
    exec "$@"
  fi
  exec supervisord -n -c /etc/supervisor/supervisord.conf
}

main "$@"
