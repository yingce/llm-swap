#!/usr/bin/env bash
set -euo pipefail

LLMSWAP_ROOT="${LLMSWAP_ROOT:-/opt/llmswap}"
LLMSWAP_BIN_DIR="${LLMSWAP_BIN_DIR:-$LLMSWAP_ROOT/bin}"
LLMSWAP_AGENT_BIN="${LLMSWAP_AGENT_BIN:-$LLMSWAP_BIN_DIR/llm-swap-agent}"
LLMSWAP_LLAMA_SWAP_BIN="${LLMSWAP_LLAMA_SWAP_BIN:-$LLMSWAP_BIN_DIR/llama-swap}"
LLMSWAP_BUNDLED_LLAMA_SWAP_BIN="${LLMSWAP_BUNDLED_LLAMA_SWAP_BIN:-$LLMSWAP_BIN_DIR/llama-swap.bundled}"
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
LLMSWAP_TAILSCALE_SOCKET="${LLMSWAP_TAILSCALE_SOCKET:-/run/tailscale/tailscaled.sock}"
LLMSWAP_TAILSCALE_PORT="${LLMSWAP_TAILSCALE_PORT:-41641}"
LLMSWAP_TAILSCALE_TUN="${LLMSWAP_TAILSCALE_TUN:-userspace-networking}"
LLMSWAP_TAILSCALE_WAIT_SECONDS="${LLMSWAP_TAILSCALE_WAIT_SECONDS:-60}"
LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL="${LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL:-}"
LLMSWAP_SUPERVISOR_CONF_DIR="${LLMSWAP_SUPERVISOR_CONF_DIR:-/etc/supervisor/conf.d}"
LLMSWAP_SUPERVISORD_CONFIG="${LLMSWAP_SUPERVISORD_CONFIG:-/etc/supervisor/supervisord.conf}"

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

write_agent_supervisor_wrapper() {
  local explicit_swap_url wait_for_tailscale tailscale_bin wrapper
  explicit_swap_url="$(first_non_empty "${LLMSWAP_SWAP_URL:-}" "${LLM_SWAP_AGENT_SWAP_URL:-}" "${SWAP_URL:-}" || true)"
  wait_for_tailscale=0
  if [[ -z "${explicit_swap_url// }" && ( "$LLMSWAP_ENABLE_TAILSCALE" == "1" || -n "$LLMSWAP_TAILSCALE_AUTHKEY" || -n "$LLMSWAP_TAILSCALE_HOSTNAME" ) ]]; then
    wait_for_tailscale=1
  fi
  tailscale_bin="$(command -v tailscale 2>/dev/null || printf 'tailscale')"
  wrapper="$LLMSWAP_BIN_DIR/agent-supervisor.sh"

  install -d "$LLMSWAP_BIN_DIR" "$LLMSWAP_LOG_DIR" "$LLMSWAP_SUPERVISOR_CONF_DIR"
  cat > "$wrapper" <<EOF
#!/usr/bin/env bash
set -euo pipefail

agent_bin="$LLMSWAP_AGENT_BIN"
agent_config="$LLMSWAP_AGENT_CONFIG"
tailscale_bin="$tailscale_bin"
tailscale_socket="$LLMSWAP_TAILSCALE_SOCKET"
wait_for_tailscale="$wait_for_tailscale"
wait_seconds="$LLMSWAP_TAILSCALE_WAIT_SECONDS"

config_has_explicit_swap_url() {
  [[ -f "\$agent_config" ]] && grep -Eq '^[[:space:]]*(swap_url|llama_swap_url):[[:space:]]*[^[:space:]#]' "\$agent_config"
}

if [[ "\$wait_for_tailscale" == "1" ]] && ! config_has_explicit_swap_url; then
  deadline=\$((SECONDS + wait_seconds))
  while true; do
    if ip="\$("\$tailscale_bin" --socket="\$tailscale_socket" ip -4 2>/dev/null | head -n1)" && [[ -n "\${ip// }" ]]; then
      printf 'tailscale IPv4 ready for agent: %s\n' "\$ip"
      break
    fi
    if (( SECONDS >= deadline )); then
      printf 'timed out waiting for tailscale IPv4 before starting agent\n' >&2
      exit 1
    fi
    sleep 1
  done
fi

exec "\$agent_bin" --config "\$agent_config"
EOF
  chmod 0755 "$wrapper"

  cat > "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-agent.conf" <<EOF
[program:llmswap-agent]
command=$wrapper
directory=$LLMSWAP_ROOT
autostart=true
autorestart=true
startsecs=5
priority=50
stdout_logfile=$LLMSWAP_LOG_DIR/agent.out.log
stderr_logfile=$LLMSWAP_LOG_DIR/agent.err.log
environment=LLM_SWAP_AGENT_CONFIG="$LLMSWAP_AGENT_CONFIG"
EOF
}

start_tailscale_if_requested() {
  if [[ "$LLMSWAP_ENABLE_TAILSCALE" != "1" && -z "$LLMSWAP_TAILSCALE_AUTHKEY" ]]; then
    rm -f "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-tailscaled.conf" "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-tailscale-init.conf" "$LLMSWAP_BIN_DIR/tailscale-init.sh"
    return 0
  fi
  if ! command -v tailscaled >/dev/null 2>&1 || ! command -v tailscale >/dev/null 2>&1; then
    printf 'tailscale requested but tailscaled/tailscale is not installed\n' >&2
    exit 1
  fi

  install -d "$LLMSWAP_ROOT/tailscale" "$(dirname "$LLMSWAP_TAILSCALE_SOCKET")" "$LLMSWAP_LOG_DIR" "$LLMSWAP_SUPERVISOR_CONF_DIR"

  local tailscaled_bin tailscale_bin init_script
  tailscaled_bin="$(command -v tailscaled)"
  tailscale_bin="$(command -v tailscale)"
  init_script="$LLMSWAP_BIN_DIR/tailscale-init.sh"

  cat > "$init_script" <<EOF
#!/usr/bin/env bash
set -euo pipefail

socket="${LLMSWAP_TAILSCALE_SOCKET}"
tries=0
until [[ -S "\$socket" ]]; do
  tries=\$((tries + 1))
  if [[ "\$tries" -ge 30 ]]; then
    printf 'tailscaled did not create %s in time\n' "\$socket" >&2
    exit 1
  fi
  sleep 1
done
EOF
  if [[ -n "$LLMSWAP_TAILSCALE_AUTHKEY" ]]; then
    cat >> "$init_script" <<EOF
"$tailscale_bin" --socket="$LLMSWAP_TAILSCALE_SOCKET" login --auth-key "$LLMSWAP_TAILSCALE_AUTHKEY"
EOF
  fi
  if [[ -n "$LLMSWAP_TAILSCALE_HOSTNAME" ]]; then
    cat >> "$init_script" <<EOF
"$tailscale_bin" --socket="$LLMSWAP_TAILSCALE_SOCKET" set --hostname "$LLMSWAP_TAILSCALE_HOSTNAME"
EOF
  fi
  chmod 0755 "$init_script"

  cat > "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-tailscaled.conf" <<EOF
[program:llmswap-tailscaled]
command=$tailscaled_bin --state=$LLMSWAP_ROOT/tailscale/tailscaled.state --socket=$LLMSWAP_TAILSCALE_SOCKET --port=$LLMSWAP_TAILSCALE_PORT --tun=$LLMSWAP_TAILSCALE_TUN
directory=$LLMSWAP_ROOT
autostart=true
autorestart=true
startsecs=3
stopasgroup=true
killasgroup=true
stdout_logfile=$LLMSWAP_LOG_DIR/tailscaled.out.log
stderr_logfile=$LLMSWAP_LOG_DIR/tailscaled.err.log
EOF

  cat > "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-tailscale-init.conf" <<EOF
[program:llmswap-tailscale-init]
command=$init_script
directory=$LLMSWAP_ROOT
autostart=true
autorestart=false
startsecs=0
startretries=0
priority=20
stdout_logfile=$LLMSWAP_LOG_DIR/tailscale-init.out.log
stderr_logfile=$LLMSWAP_LOG_DIR/tailscale-init.err.log
EOF
}

prepare_llama_swap_binary() {
  local runtime_download_url
  runtime_download_url="$(first_non_empty "${LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL:-}" "${LLAMA_SWAP_DOWNLOAD_URL:-}" || true)"

  if [[ -n "${runtime_download_url// }" ]]; then
    local tmp_bin
    tmp_bin="$(mktemp "$LLMSWAP_BIN_DIR/llama-swap.runtime.XXXXXX")"
    curl -fL --retry 5 --retry-delay 5 -o "$tmp_bin" "$runtime_download_url"
    chmod 0755 "$tmp_bin"
    install -m 0755 "$tmp_bin" "$LLMSWAP_LLAMA_SWAP_BIN"
    rm -f "$tmp_bin"
    return 0
  fi

  if [[ -x "$LLMSWAP_BUNDLED_LLAMA_SWAP_BIN" ]]; then
    install -m 0755 "$LLMSWAP_BUNDLED_LLAMA_SWAP_BIN" "$LLMSWAP_LLAMA_SWAP_BIN"
    return 0
  fi

  if [[ -x "$LLMSWAP_LLAMA_SWAP_BIN" ]]; then
    return 0
  fi

  printf 'missing llama-swap binary: %s\n' "$LLMSWAP_LLAMA_SWAP_BIN" >&2
  printf 'provide LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL, build with LLAMA_SWAP_DOWNLOAD_URL, or mount a llama-swap binary at runtime\n' >&2
  exit 1
}

should_passthrough_shell() {
  if [[ $# -eq 0 ]]; then
    return 1
  fi
  case "${1##*/}" in
    bash|sh)
      return 0
      ;;
  esac
  return 1
}

main() {
  if should_passthrough_shell "$@"; then
    exec "$@"
  fi

  install -d "$LLMSWAP_BIN_DIR" "$LLMSWAP_MODEL_ROOT" "$LLMSWAP_LOG_DIR"

  if [[ ! -x "$LLMSWAP_AGENT_BIN" ]]; then
    printf 'missing agent binary: %s\n' "$LLMSWAP_AGENT_BIN" >&2
    exit 1
  fi
  prepare_llama_swap_binary
  if [[ ! -x "$LLMSWAP_LLAMA_SWAP_BIN" ]]; then
    printf 'missing llama-swap binary after preparation: %s\n' "$LLMSWAP_LLAMA_SWAP_BIN" >&2
    exit 1
  fi

  if [[ "$LLMSWAP_FORCE_CONFIG" == "1" || ! -f "$LLMSWAP_AGENT_CONFIG" ]]; then
    require_env_when_bootstrapping LLMSWAP_GATEWAY_URL "$LLMSWAP_GATEWAY_URL"
    require_env_when_bootstrapping LLMSWAP_AGENT_TOKEN "$LLMSWAP_AGENT_TOKEN"
    write_agent_config
  fi

  write_agent_supervisor_wrapper
  start_tailscale_if_requested

  if [[ $# -gt 0 ]]; then
    exec "$@"
  fi
  exec supervisord -n -c "$LLMSWAP_SUPERVISORD_CONFIG"
}

main "$@"
