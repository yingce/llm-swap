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
LLMSWAP_AGENT_PRESTART_SCRIPT="${LLMSWAP_AGENT_PRESTART_SCRIPT:-$LLMSWAP_ROOT/agent-prestart.sh}"
LLMSWAP_AGENT_ID="${LLMSWAP_AGENT_ID:-$(hostname 2>/dev/null || printf worker-01)}"
LLMSWAP_AGENT_TAGS="${LLMSWAP_AGENT_TAGS:-gpu}"
LLMSWAP_SWAP_PORT="${LLMSWAP_SWAP_PORT:-6006}"
LLMSWAP_GATEWAY_URL="${LLMSWAP_GATEWAY_URL:-}"
LLMSWAP_AGENT_TOKEN="${LLMSWAP_AGENT_TOKEN:-}"
LLMSWAP_AGENT_TOKEN_FILE="${LLMSWAP_AGENT_TOKEN_FILE:-}"
LLMSWAP_LLAMA_SWAP_TOKEN="${LLMSWAP_LLAMA_SWAP_TOKEN:-$LLMSWAP_AGENT_TOKEN}"
LLMSWAP_FORCE_CONFIG="${LLMSWAP_FORCE_CONFIG:-0}"
LLMSWAP_ENABLE_TAILSCALE="${LLMSWAP_ENABLE_TAILSCALE:-0}"
LLMSWAP_TAILSCALE_AUTHKEY="${LLMSWAP_TAILSCALE_AUTHKEY:-}"
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
	local mode="$1"
	local agent_token="$2"
  local swap_url
  swap_url="$(first_non_empty "${LLMSWAP_SWAP_URL:-}" || true)"
  local tags_yaml
  tags_yaml="$(render_tags_yaml "$LLMSWAP_AGENT_TAGS")"

  install -d "$(dirname "$LLMSWAP_AGENT_CONFIG")" "$LLMSWAP_MODEL_ROOT" "$LLMSWAP_LOG_DIR"
  {
    printf 'agent:\n'
    if [[ "$mode" == "legacy" ]]; then
      printf '  id: %s\n' "$LLMSWAP_AGENT_ID"
    fi
    printf '  tags:\n%s\n' "$tags_yaml"
    printf '  model_root: %s\n' "$LLMSWAP_MODEL_ROOT"
    printf '  llama_swap_config: %s\n' "$LLMSWAP_LLAMA_SWAP_CONFIG"
    printf '  llama_swap_service: supervisor\n'
    printf '  restart_command: supervisorctl restart llmswap-llama-swap\n'
    printf '  swap_port: %s\n' "$LLMSWAP_SWAP_PORT"
    if [[ "$mode" == "legacy" && -n "${swap_url// }" ]]; then
      printf '  swap_url: %s\n' "$swap_url"
    fi
    printf '  gateway_url: %s\n' "$LLMSWAP_GATEWAY_URL"
    if [[ "$mode" == "frp" ]]; then
      local escaped_token="${agent_token//\'/\'\'}"
      printf "  token: '%s'\n" "$escaped_token"
    else
      printf '  token: %s\n' "$agent_token"
      printf '  llama_swap_token: %s\n' "$LLMSWAP_LLAMA_SWAP_TOKEN"
    fi
  } > "$LLMSWAP_AGENT_CONFIG"
  chmod 0600 "$LLMSWAP_AGENT_CONFIG"
}

read_agent_token_file() {
  local token_file="$1"
  if [[ ! -f "$token_file" || -L "$token_file" || ! -r "$token_file" ]]; then
    printf 'invalid agent token file\n' >&2
    return 1
  fi
  local token_hex
  if ! token_hex="$(LC_ALL=C od -An -v -tx1 -- "$token_file" 2>/dev/null)"; then
    printf 'invalid agent token file\n' >&2
    return 1
  fi
  token_hex="${token_hex//$'\n'/ }"
  local -a token_bytes=()
  read -r -a token_bytes <<< "$token_hex"
  unset token_hex
  if (( ${#token_bytes[@]} < 1 || ${#token_bytes[@]} > 16384 )); then
    printf 'invalid agent token file\n' >&2
    return 1
  fi
  local byte
  for byte in "${token_bytes[@]}"; do
    if [[ ! "$byte" =~ ^[0-9a-f]{2}$ || "$byte" == "00" ]]; then
      printf 'invalid agent token file\n' >&2
      return 1
    fi
  done
  local last_index
  last_index=$((${#token_bytes[@]} - 1))
  if [[ "${token_bytes[$last_index],,}" == "0a" ]]; then
    unset "token_bytes[$last_index]"
    last_index=$((last_index - 1))
    if (( last_index >= 0 )) && [[ "${token_bytes[$last_index],,}" == "0d" ]]; then
      unset "token_bytes[$last_index]"
    fi
  fi
  local escaped_token=""
  for byte in "${token_bytes[@]}"; do
    byte="${byte,,}"
    if [[ "$byte" == "0a" || "$byte" == "0d" ]]; then
      printf 'invalid agent token file\n' >&2
      return 1
    fi
    escaped_token+="\\x$byte"
  done
  local token
  printf -v token '%b' "$escaped_token"
  unset escaped_token token_bytes
  token="$(trim "$token")"
  if [[ -z "$token" ]]; then
    printf 'invalid agent token file\n' >&2
    return 1
  fi
  printf '%s' "$token"
}

write_frp_agent_supervisor() {
  install -d "$LLMSWAP_LOG_DIR" "$LLMSWAP_SUPERVISOR_CONF_DIR"
  rm -f "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-tailscaled.conf" \
    "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-tailscale-init.conf" \
    "$LLMSWAP_BIN_DIR/tailscale-init.sh" "$LLMSWAP_BIN_DIR/agent-supervisor.sh"
  cat > "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-agent.conf" <<EOF
[program:llmswap-agent]
command=$LLMSWAP_AGENT_BIN --config $LLMSWAP_AGENT_CONFIG
directory=$LLMSWAP_ROOT
autostart=true
autorestart=true
startsecs=5
priority=50
stdout_logfile=$LLMSWAP_LOG_DIR/agent.out.log
stderr_logfile=$LLMSWAP_LOG_DIR/agent.err.log
EOF
}

write_agent_supervisor_wrapper() {
  local explicit_swap_url wait_for_tailscale tailscale_bin wrapper
  explicit_swap_url="$(first_non_empty "${LLMSWAP_SWAP_URL:-}" || true)"
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
prestart_script="\${LLMSWAP_AGENT_PRESTART_SCRIPT:-$LLMSWAP_AGENT_PRESTART_SCRIPT}"
tailscale_bin="$tailscale_bin"
tailscale_socket="$LLMSWAP_TAILSCALE_SOCKET"
wait_for_tailscale="$wait_for_tailscale"
wait_seconds="$LLMSWAP_TAILSCALE_WAIT_SECONDS"

if [[ -f "\$prestart_script" ]]; then
  # shellcheck source=/dev/null
  source "\$prestart_script"
fi

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
environment=LLMSWAP_AGENT_CONFIG="$LLMSWAP_AGENT_CONFIG"
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
  runtime_download_url="$(first_non_empty "${LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL:-}" || true)"

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

  local bootstrap_mode="legacy"
  if [[ -n "${LLMSWAP_AGENT_TOKEN_FILE// }" ]]; then
    bootstrap_mode="frp"
  fi

  if [[ "$LLMSWAP_FORCE_CONFIG" == "1" || ! -f "$LLMSWAP_AGENT_CONFIG" ]]; then
    require_env_when_bootstrapping LLMSWAP_GATEWAY_URL "$LLMSWAP_GATEWAY_URL"
    if [[ "$bootstrap_mode" == "frp" ]]; then
      if [[ -n "${LLMSWAP_AGENT_TOKEN// }" ]]; then
        printf 'ambiguous agent token input\n' >&2
        exit 1
      fi
      local file_agent_token
      file_agent_token="$(read_agent_token_file "$LLMSWAP_AGENT_TOKEN_FILE")"
      write_agent_config frp "$file_agent_token"
      unset file_agent_token
    else
      require_env_when_bootstrapping LLMSWAP_AGENT_TOKEN "$LLMSWAP_AGENT_TOKEN"
      write_agent_config legacy "$LLMSWAP_AGENT_TOKEN"
    fi
  fi

  if [[ "$bootstrap_mode" == "frp" ]]; then
    write_frp_agent_supervisor
  else
    write_agent_supervisor_wrapper
    start_tailscale_if_requested
  fi

  if [[ $# -gt 0 ]]; then
    exec "$@"
  fi
  exec supervisord -n -c "$LLMSWAP_SUPERVISORD_CONFIG"
}

main "$@"
