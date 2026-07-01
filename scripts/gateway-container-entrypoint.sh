#!/usr/bin/env bash
set -euo pipefail

LLMSWAP_ROOT="${LLMSWAP_ROOT:-/opt/llmswap}"
LLMSWAP_BIN_DIR="${LLMSWAP_BIN_DIR:-$LLMSWAP_ROOT/bin}"
LLMSWAP_GATEWAY_BIN="${LLMSWAP_GATEWAY_BIN:-/usr/local/bin/llm-swap-gateway}"
LLMSWAP_GATEWAY_CONFIG="${LLMSWAP_GATEWAY_CONFIG:-$LLMSWAP_ROOT/gateway.yaml}"
LLMSWAP_LOG_DIR="${LLMSWAP_LOG_DIR:-$LLMSWAP_ROOT/logs}"
LLMSWAP_SUPERVISOR_CONF_DIR="${LLMSWAP_SUPERVISOR_CONF_DIR:-/etc/supervisor/conf.d}"
LLMSWAP_SUPERVISORD_CONFIG="${LLMSWAP_SUPERVISORD_CONFIG:-/etc/supervisor/supervisord.conf}"
LLMSWAP_TAILSCALE_STATE_DIR="${LLMSWAP_TAILSCALE_STATE_DIR:-$LLMSWAP_ROOT/tailscale}"
LLMSWAP_ENABLE_TAILSCALE="${LLMSWAP_ENABLE_TAILSCALE:-0}"
LLMSWAP_TAILSCALE_AUTHKEY="${LLMSWAP_TAILSCALE_AUTHKEY:-}"
LLMSWAP_TAILSCALE_HOSTNAME="${LLMSWAP_TAILSCALE_HOSTNAME:-}"
LLMSWAP_TAILSCALE_SOCKET="${LLMSWAP_TAILSCALE_SOCKET:-/run/tailscale/tailscaled.sock}"
LLMSWAP_TAILSCALE_PORT="${LLMSWAP_TAILSCALE_PORT:-41641}"
LLMSWAP_TAILSCALE_TUN="${LLMSWAP_TAILSCALE_TUN:-tun}"

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

ensure_gateway_supervisor_program() {
  install -d "$LLMSWAP_SUPERVISOR_CONF_DIR" "$LLMSWAP_LOG_DIR"
  cat > "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-gateway.conf" <<EOF
[program:llmswap-gateway]
command=$LLMSWAP_GATEWAY_BIN --config $LLMSWAP_GATEWAY_CONFIG
directory=$LLMSWAP_ROOT
autostart=true
autorestart=true
startsecs=3
stopasgroup=true
killasgroup=true
stdout_logfile=$LLMSWAP_LOG_DIR/gateway.out.log
stderr_logfile=$LLMSWAP_LOG_DIR/gateway.err.log
EOF
}

start_tailscale_if_requested() {
  if [[ "$LLMSWAP_ENABLE_TAILSCALE" != "1" && -z "$LLMSWAP_TAILSCALE_AUTHKEY" && -z "$LLMSWAP_TAILSCALE_HOSTNAME" ]]; then
    rm -f "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-tailscaled.conf" \
      "$LLMSWAP_SUPERVISOR_CONF_DIR/llmswap-tailscale-init.conf" \
      "$LLMSWAP_BIN_DIR/tailscale-init.sh"
    return 0
  fi
  if ! command -v tailscaled >/dev/null 2>&1 || ! command -v tailscale >/dev/null 2>&1; then
    printf 'tailscale requested but tailscaled/tailscale is not installed\n' >&2
    exit 1
  fi

  install -d "$LLMSWAP_TAILSCALE_STATE_DIR" "$(dirname "$LLMSWAP_TAILSCALE_SOCKET")" "$LLMSWAP_LOG_DIR" "$LLMSWAP_SUPERVISOR_CONF_DIR" "$LLMSWAP_BIN_DIR"

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
command=$tailscaled_bin --state=$LLMSWAP_TAILSCALE_STATE_DIR/tailscaled.state --socket=$LLMSWAP_TAILSCALE_SOCKET --port=$LLMSWAP_TAILSCALE_PORT --tun=$LLMSWAP_TAILSCALE_TUN
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

main() {
  if should_passthrough_shell "$@"; then
    exec "$@"
  fi

  install -d "$LLMSWAP_BIN_DIR" "$LLMSWAP_LOG_DIR" "$LLMSWAP_ROOT"

  if [[ ! -x "$LLMSWAP_GATEWAY_BIN" ]]; then
    printf 'missing gateway binary: %s\n' "$LLMSWAP_GATEWAY_BIN" >&2
    exit 1
  fi

  ensure_gateway_supervisor_program
  start_tailscale_if_requested

  if [[ $# -gt 0 ]]; then
    exec "$@"
  fi
  exec supervisord -n -c "$LLMSWAP_SUPERVISORD_CONFIG"
}

main "$@"
