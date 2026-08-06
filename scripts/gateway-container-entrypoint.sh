#!/usr/bin/env bash
set -euo pipefail

LLMSWAP_ROOT="${LLMSWAP_ROOT:-/opt/llmswap}"
LLMSWAP_BIN_DIR="${LLMSWAP_BIN_DIR:-$LLMSWAP_ROOT/bin}"
LLMSWAP_GATEWAY_BIN="${LLMSWAP_GATEWAY_BIN:-/usr/local/bin/llm-swap-gateway}"
LLMSWAP_GATEWAY_CONFIG="${LLMSWAP_GATEWAY_CONFIG:-$LLMSWAP_ROOT/gateway.yaml}"
LLMSWAP_LOG_DIR="${LLMSWAP_LOG_DIR:-$LLMSWAP_ROOT/logs}"
LLMSWAP_SUPERVISOR_CONF_DIR="${LLMSWAP_SUPERVISOR_CONF_DIR:-/etc/supervisor/conf.d}"
LLMSWAP_SUPERVISORD_CONFIG="${LLMSWAP_SUPERVISORD_CONFIG:-/etc/supervisor/supervisord.conf}"

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
  if [[ $# -gt 0 ]]; then
    exec "$@"
  fi
  exec supervisord -n -c "$LLMSWAP_SUPERVISORD_CONFIG"
}

main "$@"
