#!/usr/bin/env bash
set -euo pipefail

LLMSWAP_ROOT="${LLMSWAP_ROOT:-/opt/llmswap}"
LLMSWAP_PYTHON="${LLMSWAP_PYTHON:-3.12}"
LLMSWAP_RUNTIME="${LLMSWAP_RUNTIME:-all}"
LLMSWAP_ONLY="${LLMSWAP_ONLY:-all}"
LLMSWAP_INSTALL_TAILSCALE="${LLMSWAP_INSTALL_TAILSCALE:-1}"
LLMSWAP_INSTALL_SUPERVISOR="${LLMSWAP_INSTALL_SUPERVISOR:-1}"
LLMSWAP_DRY_RUN="${LLMSWAP_DRY_RUN:-0}"
LLMSWAP_CUDA_VERSION="${LLMSWAP_CUDA_VERSION:-auto}"
LLMSWAP_AGENT_BIN="${LLMSWAP_AGENT_BIN:-$LLMSWAP_ROOT/bin/llm-swap-agent}"
LLMSWAP_AGENT_CONFIG="${LLMSWAP_AGENT_CONFIG:-$LLMSWAP_ROOT/agent.yaml}"
LLMSWAP_AGENT_BINARY_SOURCE="${LLMSWAP_AGENT_BINARY_SOURCE:-}"
LLMSWAP_AGENT_ID="${LLMSWAP_AGENT_ID:-$(hostname 2>/dev/null || printf worker-01)}"
LLMSWAP_AGENT_TAGS="${LLMSWAP_AGENT_TAGS:-gpu}"
LLMSWAP_GATEWAY_URL="${LLMSWAP_GATEWAY_URL:-http://gateway.example.local:8080}"
LLMSWAP_AGENT_TOKEN="${LLMSWAP_AGENT_TOKEN:-agent-token}"
LLMSWAP_LLAMA_SWAP_TOKEN="${LLMSWAP_LLAMA_SWAP_TOKEN:-$LLMSWAP_AGENT_TOKEN}"
LLMSWAP_SWAP_PORT="${LLMSWAP_SWAP_PORT:-6006}"
LLMSWAP_FORCE_CONFIG="${LLMSWAP_FORCE_CONFIG:-0}"
LLMSWAP_SIMULATE_EXISTING_AGENT_CONFIG="${LLMSWAP_SIMULATE_EXISTING_AGENT_CONFIG:-0}"
LLMSWAP_TAILSCALE_AUTHKEY="${LLMSWAP_TAILSCALE_AUTHKEY:-}"
LLMSWAP_TAILSCALE_HOSTNAME="${LLMSWAP_TAILSCALE_HOSTNAME:-}"
LLMSWAP_TAILSCALE_WAIT_SECONDS="${LLMSWAP_TAILSCALE_WAIT_SECONDS:-60}"
LLMSWAP_UV_INSTALL_TIMEOUT="${LLMSWAP_UV_INSTALL_TIMEOUT:-120}"
LLMSWAP_UV_CACHE_DIR="${LLMSWAP_UV_CACHE_DIR:-$LLMSWAP_ROOT/cache/uv}"
LLMSWAP_UV_PYTHON_INSTALL_DIR="${LLMSWAP_UV_PYTHON_INSTALL_DIR:-$LLMSWAP_ROOT/python}"
LLMSWAP_UV_PYTHON_INSTALL_MIRROR="${LLMSWAP_UV_PYTHON_INSTALL_MIRROR:-}"
LLMSWAP_TORCH_INDEX_URL="${LLMSWAP_TORCH_INDEX_URL:-}"
LLMSWAP_TORCH_INDEX_URL_BASE="${LLMSWAP_TORCH_INDEX_URL_BASE:-https://download.pytorch.org/whl}"
UV_HTTP_RETRIES="${UV_HTTP_RETRIES:-10}"
UV_HTTP_TIMEOUT="${UV_HTTP_TIMEOUT:-120}"
UV_HTTP_CONNECT_TIMEOUT="${UV_HTTP_CONNECT_TIMEOUT:-30}"
LLMSWAP_LLAMA_CPP_BASE_URL="${LLMSWAP_LLAMA_CPP_BASE_URL:-http://llmfs-bj.oss-cn-beijing.aliyuncs.com/models}"
LLMSWAP_LLAMA_CPP_CUDA="${LLMSWAP_LLAMA_CPP_CUDA:-auto}"
LLMSWAP_LLAMA_CPP_ARCH="${LLMSWAP_LLAMA_CPP_ARCH:-sm89}"
LLMSWAP_RUNTIME_CACHE_DIR="${LLMSWAP_RUNTIME_CACHE_DIR:-$LLMSWAP_ROOT/cache/runtimes}"
LLMSWAP_VLLM_PACKAGE="${LLMSWAP_VLLM_PACKAGE:-vllm[audio]}"
LLMSWAP_SGLANG_PACKAGE="${LLMSWAP_SGLANG_PACKAGE:-sglang}"
UV_LINK_MODE="${UV_LINK_MODE:-copy}"
export UV_CACHE_DIR="$LLMSWAP_UV_CACHE_DIR"
export UV_PYTHON_INSTALL_DIR="$LLMSWAP_UV_PYTHON_INSTALL_DIR"
export UV_HTTP_RETRIES
export UV_HTTP_TIMEOUT
export UV_HTTP_CONNECT_TIMEOUT
if [[ -n "$LLMSWAP_UV_PYTHON_INSTALL_MIRROR" ]]; then
  export UV_PYTHON_INSTALL_MIRROR="$LLMSWAP_UV_PYTHON_INSTALL_MIRROR"
fi
export UV_LINK_MODE

usage() {
  cat <<'EOF'
Usage: install-worker.sh [options]

Options:
  --dry-run                 Print commands without executing them.
  --root PATH               Installation root. Default: /opt/llmswap.
  --only all|base|runtime|agent|supervisor|tailscale
                            Run only one install stage. Default: all.
  --runtime all|vllm|sglang|llamacpp
                            Runtime environments to install.
  --cuda-version X.Y        Override detected CUDA version, e.g. 12.4, 12.8, 13.0.
  --llamacpp-base-url URL   Base URL containing llamacpp-linux-<variant>.tar.gz.
  --llamacpp-cuda auto|cu124|cu128|cu130
                            llama.cpp CUDA package selector. Default: auto.
  --tailscale-authkey KEY   Configure supervisor-managed tailscale login with this auth key.
  --tailscale-hostname NAME Hostname applied by supervisor-managed tailscale init.
  --skip-tailscale          Do not install or configure Tailscale.
  --skip-supervisor         Do not install or configure supervisor.
  --python PYTHON           Python version/executable used by uv venv. Default: 3.12.
  --agent-binary PATH       Existing llm-swap-agent binary to install.
  --agent-id ID             Agent ID written into /opt/llmswap/agent.yaml.
  --tags TAGS               Comma-separated tags written into agent config.
  --gateway-url URL         Gateway URL written into agent config.
  --agent-token TOKEN       Gateway agent token written into agent config.
  --llama-swap-token TOKEN  llama-swap token written into agent config. Default: agent token.
  --swap-port PORT          llama-swap port used when swap_url is omitted.
  --torch-index-url URL     Override the full torch wheel index URL.
  --torch-index-base URL    Override the torch wheel index base URL.
  --force-config            Overwrite an existing agent config.
  -h, --help                Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      LLMSWAP_DRY_RUN=1
      shift
      ;;
    --root)
      LLMSWAP_ROOT="$2"
      LLMSWAP_AGENT_BIN="$LLMSWAP_ROOT/bin/llm-swap-agent"
      LLMSWAP_AGENT_CONFIG="$LLMSWAP_ROOT/agent.yaml"
      shift 2
      ;;
    --runtime)
      LLMSWAP_RUNTIME="$2"
      shift 2
      ;;
    --only)
      LLMSWAP_ONLY="$2"
      shift 2
      ;;
    --cuda-version)
      LLMSWAP_CUDA_VERSION="$2"
      shift 2
      ;;
    --llamacpp-base-url)
      LLMSWAP_LLAMA_CPP_BASE_URL="$2"
      shift 2
      ;;
    --llamacpp-cuda)
      LLMSWAP_LLAMA_CPP_CUDA="$2"
      shift 2
      ;;
    --skip-tailscale)
      LLMSWAP_INSTALL_TAILSCALE=0
      shift
      ;;
    --tailscale-authkey)
      LLMSWAP_TAILSCALE_AUTHKEY="$2"
      shift 2
      ;;
    --tailscale-hostname)
      LLMSWAP_TAILSCALE_HOSTNAME="$2"
      shift 2
      ;;
    --skip-supervisor)
      LLMSWAP_INSTALL_SUPERVISOR=0
      shift
      ;;
    --python)
      LLMSWAP_PYTHON="$2"
      shift 2
      ;;
    --agent-binary)
      LLMSWAP_AGENT_BINARY_SOURCE="$2"
      shift 2
      ;;
    --agent-id)
      LLMSWAP_AGENT_ID="$2"
      shift 2
      ;;
    --tags)
      LLMSWAP_AGENT_TAGS="$2"
      shift 2
      ;;
    --gateway-url)
      LLMSWAP_GATEWAY_URL="$2"
      shift 2
      ;;
    --agent-token)
      LLMSWAP_AGENT_TOKEN="$2"
      shift 2
      ;;
    --llama-swap-token)
      LLMSWAP_LLAMA_SWAP_TOKEN="$2"
      shift 2
      ;;
    --swap-port)
      LLMSWAP_SWAP_PORT="$2"
      shift 2
      ;;
    --torch-index-url)
      LLMSWAP_TORCH_INDEX_URL="$2"
      shift 2
      ;;
    --torch-index-base)
      LLMSWAP_TORCH_INDEX_URL_BASE="$2"
      shift 2
      ;;
    --force-config)
      LLMSWAP_FORCE_CONFIG=1
      shift
      ;;
    --simulate-existing-agent-config)
      LLMSWAP_SIMULATE_EXISTING_AGENT_CONFIG=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

run() {
  if [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
    printf 'RUN %s\n' "$*"
    return 0
  fi
  "$@"
}

run_with_retry() {
  local attempts="$1"
  local sleep_seconds="$2"
  shift 2

  if [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
    printf 'RUN %s\n' "$*"
    return 0
  fi

  local attempt=1
  while true; do
    if "$@"; then
      return 0
    fi
    if [[ "$attempt" -ge "$attempts" ]]; then
      return 1
    fi
    printf 'WARN command failed (attempt %s/%s): %s\n' "$attempt" "$attempts" "$*" >&2
    sleep "$sleep_seconds"
    attempt=$((attempt + 1))
  done
}

write_file() {
  local path="$1"
  local content="$2"
  if [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
    printf 'WRITE %s\n%s\n' "$path" "$content"
    return 0
  fi
  install -d "$(dirname "$path")"
  printf '%s\n' "$content" > "$path"
}

require_root_unless_dry_run() {
  if [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
    return 0
  fi
  if [[ "$(id -u)" != "0" ]]; then
    echo "run as root or use --dry-run" >&2
    exit 1
  fi
}

detect_cuda_version() {
  if [[ "$LLMSWAP_CUDA_VERSION" != "auto" && -n "$LLMSWAP_CUDA_VERSION" ]]; then
    printf '%s\n' "$LLMSWAP_CUDA_VERSION"
    return 0
  fi
  if command -v nvidia-smi >/dev/null 2>&1; then
    nvidia-smi 2>/dev/null | sed -n 's/.*CUDA Version: \([0-9][0-9]*\.[0-9]\).*/\1/p' | head -n1
    return 0
  fi
  if command -v nvcc >/dev/null 2>&1; then
    nvcc --version 2>/dev/null | sed -n 's/.*release \([0-9][0-9]*\.[0-9]\).*/\1/p' | head -n1
    return 0
  fi
  printf 'cpu\n'
}

torch_backend_for_cuda() {
  local cuda="$1"
  case "$cuda" in
    12.4|12.5) printf 'cu124\n' ;;
    12.6|12.7) printf 'cu126\n' ;;
    12.8|12.9) printf 'cu128\n' ;;
    13.*) printf 'cu130\n' ;;
    cpu|"") printf 'cpu\n' ;;
    *)
      echo "unsupported CUDA version '$cuda'; set --cuda-version to 12.4, 12.8, 13.0, or cpu" >&2
      exit 1
      ;;
  esac
}

runtime_enabled() {
  local runtime="$1"
  case "$LLMSWAP_RUNTIME" in
    all) return 0 ;;
    "$runtime") return 0 ;;
    vllm|sglang|llamacpp) return 1 ;;
    *)
      echo "unsupported runtime '$LLMSWAP_RUNTIME'; use all, vllm, sglang, or llamacpp" >&2
      exit 1
      ;;
  esac
}

stage_enabled() {
  local stage="$1"
  case "$LLMSWAP_ONLY" in
    all) return 0 ;;
    "$stage") return 0 ;;
    base|runtime|agent|supervisor|tailscale) return 1 ;;
    *)
      echo "unsupported --only '$LLMSWAP_ONLY'; use all, base, runtime, agent, supervisor, or tailscale" >&2
      exit 1
      ;;
  esac
}

print_uv_info() {
  printf 'INFO uv_cache_dir=%s uv_python_install_dir=%s uv_link_mode=%s\n' "$UV_CACHE_DIR" "$UV_PYTHON_INSTALL_DIR" "$UV_LINK_MODE"
  if [[ -n "${UV_PYTHON_INSTALL_MIRROR:-}" ]]; then
    printf 'INFO uv_python_install_mirror=%s\n' "$UV_PYTHON_INSTALL_MIRROR"
  fi
  printf 'INFO uv_http_retries=%s uv_http_timeout=%s uv_http_connect_timeout=%s\n' "$UV_HTTP_RETRIES" "$UV_HTTP_TIMEOUT" "$UV_HTTP_CONNECT_TIMEOUT"
}

ensure_runtime_dirs() {
  run mkdir -p "$LLMSWAP_ROOT/bin" "$LLMSWAP_ROOT/models" "$LLMSWAP_ROOT/venvs" "$LLMSWAP_ROOT/logs" "$UV_CACHE_DIR" "$UV_PYTHON_INSTALL_DIR" "$LLMSWAP_RUNTIME_CACHE_DIR"
}

ensure_agent_dirs() {
  run mkdir -p "$LLMSWAP_ROOT/bin" "$LLMSWAP_ROOT/logs"
}

ensure_supervisor_dirs() {
  run mkdir -p "$LLMSWAP_ROOT/bin" "$LLMSWAP_ROOT/logs"
}

write_llama_swap_supervisor_wrapper() {
  local wrapper_path="$LLMSWAP_ROOT/bin/llama-swap-supervisor.sh"
  local content
  content="#!/usr/bin/env bash
set -euo pipefail

LLMSWAP_ROOT=\"\${LLMSWAP_ROOT:-$LLMSWAP_ROOT}\"
LLMSWAP_LLAMA_SWAP_BIN=\"\${LLMSWAP_LLAMA_SWAP_BIN:-\$LLMSWAP_ROOT/bin/llama-swap}\"
LLMSWAP_LLAMA_SWAP_CONFIG=\"\${LLMSWAP_LLAMA_SWAP_CONFIG:-\$LLMSWAP_ROOT/llama-swap.yaml}\"
LLMSWAP_SWAP_PORT=\"\${LLMSWAP_SWAP_PORT:-$LLMSWAP_SWAP_PORT}\"
LLMSWAP_LLAMA_SWAP_TOKEN=\"\${LLMSWAP_LLAMA_SWAP_TOKEN:-\${LLMSWAP_AGENT_TOKEN:-}}\"

if [[ ! -s \"\$LLMSWAP_LLAMA_SWAP_CONFIG\" ]]; then
  mkdir -p \"\$(dirname \"\$LLMSWAP_LLAMA_SWAP_CONFIG\")\"
  {
    printf 'healthCheckTimeout: 300\n'
    printf 'startPort: 10001\n'
    printf 'globalTTL: 0\n'
    if [[ -n \"\${LLMSWAP_LLAMA_SWAP_TOKEN// }\" ]]; then
      printf 'apiKeys:\n'
      printf '    - %s\n' \"\$LLMSWAP_LLAMA_SWAP_TOKEN\"
    fi
    printf 'performance:\n'
    printf '    enable: true\n'
    printf '    every: 5s\n'
    printf 'models: {}\n'
  } > \"\$LLMSWAP_LLAMA_SWAP_CONFIG\"
fi

exec \"\$LLMSWAP_LLAMA_SWAP_BIN\" -config \"\$LLMSWAP_LLAMA_SWAP_CONFIG\" -listen \":\$LLMSWAP_SWAP_PORT\" -watch-config"
  write_file "$wrapper_path" "$content"
  run chmod 0755 "$wrapper_path"
}

write_agent_supervisor_wrapper() {
  local wrapper_path="$LLMSWAP_ROOT/bin/agent-supervisor.sh"
  local wait_for_tailscale="0"
  if [[ -n "$LLMSWAP_TAILSCALE_AUTHKEY" || -n "$LLMSWAP_TAILSCALE_HOSTNAME" ]]; then
    wait_for_tailscale="1"
  fi
  local content
  if [[ "$wait_for_tailscale" != "1" ]]; then
    content="#!/usr/bin/env bash
set -euo pipefail

exec \"$LLMSWAP_AGENT_BIN\" --config \"$LLMSWAP_AGENT_CONFIG\""
    write_file "$wrapper_path" "$content"
    run chmod 0755 "$wrapper_path"
    return 0
  fi

  content="#!/usr/bin/env bash
set -euo pipefail

agent_bin=\"$LLMSWAP_AGENT_BIN\"
agent_config=\"$LLMSWAP_AGENT_CONFIG\"
tailscale_bin=\"\${LLMSWAP_TAILSCALE_BIN:-tailscale}\"
tailscale_socket=\"/run/tailscale/tailscaled.sock\"
wait_for_tailscale=\"1\"
wait_seconds=\"\${LLMSWAP_TAILSCALE_WAIT_SECONDS:-$LLMSWAP_TAILSCALE_WAIT_SECONDS}\"

config_has_explicit_swap_url() {
  [[ -f \"\$agent_config\" ]] && grep -Eq '^[[:space:]]*(swap_url|llama_swap_url):[[:space:]]*[^[:space:]#]' \"\$agent_config\"
}

if [[ \"\$wait_for_tailscale\" == \"1\" ]] && ! config_has_explicit_swap_url; then
  deadline=\$((SECONDS + wait_seconds))
  while true; do
    if ip=\"\$(\"\$tailscale_bin\" --socket=\"\$tailscale_socket\" ip -4 2>/dev/null | head -n1)\" && [[ -n \"\${ip// }\" ]]; then
      printf 'tailscale IPv4 ready for agent: %s\n' \"\$ip\"
      break
    fi
    if (( SECONDS >= deadline )); then
      printf 'timed out waiting for tailscale IPv4 before starting agent\n' >&2
      exit 1
    fi
    sleep 1
  done
fi

exec \"\$agent_bin\" --config \"\$agent_config\""
  write_file "$wrapper_path" "$content"
  run chmod 0755 "$wrapper_path"
}

write_tailscale_init_script() {
  local script_path="$LLMSWAP_ROOT/bin/tailscale-init.sh"
  local content
  content="#!/usr/bin/env bash
set -euo pipefail

socket=\"/run/tailscale/tailscaled.sock\"
tries=0
until [[ -S \"\$socket\" ]]; do
  tries=\$((tries + 1))
  if [[ \"\$tries\" -ge 30 ]]; then
    printf 'tailscaled did not create %s in time\n' \"\$socket\" >&2
    exit 1
  fi
  sleep 1
done"
  if [[ -n "$LLMSWAP_TAILSCALE_AUTHKEY" ]]; then
    content="${content}
tailscale --socket=\"/run/tailscale/tailscaled.sock\" login --auth-key \"$LLMSWAP_TAILSCALE_AUTHKEY\""
  fi
  if [[ -n "$LLMSWAP_TAILSCALE_HOSTNAME" ]]; then
    content="${content}
tailscale --socket=\"/run/tailscale/tailscaled.sock\" set --hostname \"$LLMSWAP_TAILSCALE_HOSTNAME\""
  fi
  write_file "$script_path" "$content"
  run chmod 0755 "$script_path"
}

install_base_packages() {
  print_uv_info
  ensure_runtime_dirs
  if command -v apt-get >/dev/null 2>&1 || [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
    local packages=(ca-certificates curl gnupg procps python3 python3-venv python3-dev python3-pip supervisor git build-essential ninja-build ffmpeg)
    if [[ "$LLMSWAP_DRY_RUN" == "1" ]] || apt-cache show libavdevice58 >/dev/null 2>&1; then
      packages+=(libavdevice58)
    fi
    run_with_retry 3 5 apt-get update
    run_with_retry 3 5 apt-get install -y "${packages[@]}"
  else
    echo "apt-get not found; install ca-certificates, curl, python3, python3-venv, python3-dev, python3-pip, supervisor, git, and ffmpeg manually" >&2
  fi
}

install_uv() {
  local candidate
  if command -v uv >/dev/null 2>&1; then
    printf 'uv already installed: %s\n' "$(command -v uv)"
    return 0
  fi
  for candidate in "${HOME:-}/.local/bin/uv" /usr/local/bin/uv /usr/bin/uv; do
    if [[ -x "$candidate" ]]; then
      export PATH="$(dirname "$candidate"):$PATH"
      printf 'uv already installed: %s\n' "$candidate"
      return 0
    fi
  done
  run sh -c "timeout $LLMSWAP_UV_INSTALL_TIMEOUT sh -c 'curl -LsSf https://astral.sh/uv/install.sh | sh' || python3 -m pip install --upgrade uv"
  if [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
    return 0
  fi
  for candidate in "${HOME:-}/.local/bin/uv" /usr/local/bin/uv /usr/bin/uv; do
    if [[ -x "$candidate" ]]; then
      export PATH="$(dirname "$candidate"):$PATH"
      printf 'uv installed: %s\n' "$candidate"
      return 0
    fi
  done
  echo "uv installation succeeded but no uv binary was found in HOME/.local/bin, /usr/local/bin, or /usr/bin" >&2
  exit 1
}

install_tailscale() {
  if [[ "$LLMSWAP_INSTALL_TAILSCALE" != "1" ]]; then
    return 0
  fi
  if command -v tailscale >/dev/null 2>&1; then
    printf 'tailscale already installed: %s\n' "$(command -v tailscale)"
  else
    run sh -c "curl -fsSL https://tailscale.com/install.sh | sh"
  fi
  if [[ -z "$LLMSWAP_TAILSCALE_AUTHKEY" && -z "$LLMSWAP_TAILSCALE_HOSTNAME" ]]; then
    printf 'INFO no tailscale auth key or hostname provided; not configuring supervisor-managed tailscale init.\n'
    return 0
  fi

  configure_tailscale_supervisor
  ensure_supervisord
  run supervisorctl reread
  run supervisorctl update
}

configure_tailscale_supervisor() {
  local tailscaled_bin
  tailscaled_bin="$(command -v tailscaled 2>/dev/null || true)"
  if [[ -z "$tailscaled_bin" ]]; then
    tailscaled_bin="/usr/sbin/tailscaled"
  fi
  run mkdir -p "$LLMSWAP_ROOT/tailscale" "$LLMSWAP_ROOT/logs" /run/tailscale "$LLMSWAP_ROOT/bin"
  write_tailscale_init_script
  local conf init_conf
  conf="[program:llmswap-tailscaled]
command=$tailscaled_bin --state=$LLMSWAP_ROOT/tailscale/tailscaled.state --socket=/run/tailscale/tailscaled.sock --port=41641
directory=$LLMSWAP_ROOT
autostart=true
autorestart=true
startsecs=3
stopasgroup=true
killasgroup=true
stdout_logfile=$LLMSWAP_ROOT/logs/tailscaled.out.log
stderr_logfile=$LLMSWAP_ROOT/logs/tailscaled.err.log"
  init_conf="[program:llmswap-tailscale-init]
command=$LLMSWAP_ROOT/bin/tailscale-init.sh
directory=$LLMSWAP_ROOT
autostart=true
autorestart=false
startsecs=0
startretries=0
priority=20
stdout_logfile=$LLMSWAP_ROOT/logs/tailscale-init.out.log
stderr_logfile=$LLMSWAP_ROOT/logs/tailscale-init.err.log"
  write_file /etc/supervisor/conf.d/llmswap-tailscaled.conf "$conf"
  write_file /etc/supervisor/conf.d/llmswap-tailscale-init.conf "$init_conf"
}

install_torch() {
  local python="$1"
  local backend="$2"
  local index_url
  index_url="$(torch_index_url_for_backend "$backend")"
  run uv pip install --python "$python" torch torchvision torchaudio --index-url "$index_url"
}

torch_index_url_for_backend() {
  local backend="$1"
  printf '%s\n' "${LLMSWAP_TORCH_INDEX_URL:-$LLMSWAP_TORCH_INDEX_URL_BASE/$backend}"
}

install_multimodal_audio_deps() {
  local python="$1"
  run uv pip install --python "$python" librosa soundfile torchcodec av
}

install_vllm() {
  local backend="$1"
  local venv="$LLMSWAP_ROOT/venvs/vllm"
  run uv venv "$venv" --python "$LLMSWAP_PYTHON" --managed-python --clear
  install_torch "$venv/bin/python" "$backend"
  run uv pip install --python "$venv/bin/python" "$LLMSWAP_VLLM_PACKAGE"
  install_multimodal_audio_deps "$venv/bin/python"
  write_runtime_wrapper "$LLMSWAP_ROOT/bin/vllm.server" "$venv/bin/python" vllm
  write_python_wrapper "$LLMSWAP_ROOT/bin/vllm-python" "$venv/bin/python"
}

install_sglang() {
  local backend="$1"
  local venv="$LLMSWAP_ROOT/venvs/sglang"
  local check_script="import torch, sglang; print('torch', torch.__version__); print('torch_cuda', torch.version.cuda); print('cuda_available', torch.cuda.is_available()); print('sglang', sglang.__version__)"
  run uv venv "$venv" --python "$LLMSWAP_PYTHON" --managed-python --clear
  run uv pip install --python "$venv/bin/python" --prerelease=allow "$LLMSWAP_SGLANG_PACKAGE"
  install_multimodal_audio_deps "$venv/bin/python"
  patch_sglang_minicpmv46_config "$venv/bin/python"
  printf 'INFO sglang_resolved_runtime backend_hint=%s\n' "$backend"
  run "$venv/bin/python" -c "$check_script"
  write_runtime_wrapper "$LLMSWAP_ROOT/bin/sglang.server" "$venv/bin/python" sglang
  write_python_wrapper "$LLMSWAP_ROOT/bin/sglang-python" "$venv/bin/python"
}

patch_sglang_minicpmv46_config() {
  local python="$1"
  if [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
    cat <<EOF
RUN $python - <<'PY'
from pathlib import Path
import py_compile
import sglang

path = Path(sglang.__file__).parent / "srt" / "configs" / "minicpmv4_6.py"
if not path.exists():
    print("INFO sglang_minicpmv46_patch=skip_missing")
    raise SystemExit(0)
s = path.read_text()
old = "        super().__init__(tie_word_embeddings=tie_word_embeddings, **kwargs)\n"
new = "        kwargs.pop(\"hidden_size\", None)\n        kwargs.pop(\"vocab_size\", None)\n        super().__init__(tie_word_embeddings=tie_word_embeddings, **kwargs)\n"
if new in s:
    print("INFO sglang_minicpmv46_patch=already_applied")
elif old in s:
    path.write_text(s.replace(old, new, 1))
    print("INFO sglang_minicpmv46_patch=applied")
else:
    raise SystemExit(f"unsupported SGLang MiniCPMV4_6Config layout: {path}")
py_compile.compile(str(path), doraise=True)
PY
EOF
    return 0
  fi

  "$python" - <<'PY'
from pathlib import Path
import py_compile
import sglang

path = Path(sglang.__file__).parent / "srt" / "configs" / "minicpmv4_6.py"
if not path.exists():
    print("INFO sglang_minicpmv46_patch=skip_missing")
    raise SystemExit(0)
s = path.read_text()
old = "        super().__init__(tie_word_embeddings=tie_word_embeddings, **kwargs)\n"
new = "        kwargs.pop(\"hidden_size\", None)\n        kwargs.pop(\"vocab_size\", None)\n        super().__init__(tie_word_embeddings=tie_word_embeddings, **kwargs)\n"
if new in s:
    print("INFO sglang_minicpmv46_patch=already_applied")
elif old in s:
    path.write_text(s.replace(old, new, 1))
    print("INFO sglang_minicpmv46_patch=applied")
else:
    raise SystemExit(f"unsupported SGLang MiniCPMV4_6Config layout: {path}")
py_compile.compile(str(path), doraise=True)
PY
}

llamacpp_cuda_for_cuda() {
  local cuda="$1"
  if [[ "$LLMSWAP_LLAMA_CPP_CUDA" != "auto" && -n "$LLMSWAP_LLAMA_CPP_CUDA" ]]; then
    case "$LLMSWAP_LLAMA_CPP_CUDA" in
      cu124|cu128|cu130) printf '%s\n' "$LLMSWAP_LLAMA_CPP_CUDA" ;;
      *)
        echo "unsupported llama.cpp CUDA selector '$LLMSWAP_LLAMA_CPP_CUDA'; use auto, cu124, cu128, or cu130" >&2
        exit 1
        ;;
    esac
    return 0
  fi

  case "$cuda" in
    12.4|12.5) printf 'cu124\n' ;;
    12.6|12.7|12.8|12.9) printf 'cu128\n' ;;
    13.*) printf 'cu130\n' ;;
    *)
      echo "unsupported CUDA version '$cuda' for llama.cpp; set --llamacpp-cuda to cu124, cu128, or cu130" >&2
      exit 1
      ;;
  esac
}

join_url() {
  local base="${1%/}"
  local path="${2#/}"
  printf '%s/%s\n' "$base" "$path"
}

install_llamacpp() {
  local cuda="$1"
  local cuda_pkg
  cuda_pkg="$(llamacpp_cuda_for_cuda "$cuda")"
  local variant="${cuda_pkg}-${LLMSWAP_LLAMA_CPP_ARCH}"
  local archive="llamacpp-linux-${variant}.tar.gz"
  local url
  url="$(join_url "$LLMSWAP_LLAMA_CPP_BASE_URL" "$archive")"
  local cached="$LLMSWAP_RUNTIME_CACHE_DIR/$archive"
  local dest="$LLMSWAP_ROOT/runtimes/llamacpp/$variant"

  printf 'INFO llamacpp_variant=%s\n' "$variant"
  run mkdir -p "$LLMSWAP_RUNTIME_CACHE_DIR" "$dest" "$LLMSWAP_ROOT/bin"
  if [[ "$LLMSWAP_DRY_RUN" == "1" || ! -s "$cached" ]]; then
    run curl -fL --retry 5 --retry-delay 5 -o "$cached" "$url"
  fi
  run rm -rf "$dest"
  run mkdir -p "$dest"
  run tar -C "$dest" -xzf "$cached"
  write_llamacpp_bin_wrapper "$LLMSWAP_ROOT/bin/llamacpp.server" "$dest/bin" "llamacpp.server"
  write_llamacpp_bin_wrapper "$LLMSWAP_ROOT/bin/llama-server" "$dest/bin" "llama-server"
  write_llamacpp_bin_wrapper "$LLMSWAP_ROOT/bin/llama-cli" "$dest/bin" "llama-cli"
  write_llamacpp_bin_wrapper "$LLMSWAP_ROOT/bin/llama-bench" "$dest/bin" "llama-bench"
  run test -x "$LLMSWAP_ROOT/bin/llamacpp.server"
}

write_llamacpp_bin_wrapper() {
  local path="$1"
  local bin_dir="$2"
  local executable="$3"
  local content
  local ld_setup
  ld_setup='LLAMACPP_LIBS=""
for dir in "$LLAMACPP_BIN" "$LLAMACPP_BIN/../lib" /usr/local/cuda/lib64 /usr/local/cuda-*/lib64 /usr/lib/x86_64-linux-gnu; do
  if [[ -d "$dir" ]]; then
    LLAMACPP_LIBS="${LLAMACPP_LIBS:+$LLAMACPP_LIBS:}$dir"
  fi
done
if [[ -n "${LLMSWAP_LLAMACPP_EXTRA_LD_LIBRARY_PATH:-}" ]]; then
  LLAMACPP_LIBS="${LLAMACPP_LIBS:+$LLAMACPP_LIBS:}$LLMSWAP_LLAMACPP_EXTRA_LD_LIBRARY_PATH"
fi
export LD_LIBRARY_PATH="$LLAMACPP_LIBS:${LD_LIBRARY_PATH:-}"'
  if [[ "$executable" == "llamacpp.server" ]]; then
    content="#!/usr/bin/env bash
set -euo pipefail
LLAMACPP_BIN=\"$bin_dir\"
MODEL_PATH=\"\${MODEL_PATH:-}\"
if [[ -z \"\$MODEL_PATH\" && \$# -gt 0 && \"\$1\" != -* ]]; then
  MODEL_PATH=\"\$1\"
  shift
fi
HOST=\"\${HOST:-0.0.0.0}\"
PORT=\"\${PORT:-8080}\"
$ld_setup
SERVER_ARGS=()
has_host=0
has_port=0
for arg in \"\$@\"; do
  case \"\$arg\" in
    --host|--host=*|--host-ip|--host-ip=*) has_host=1 ;;
    --port|--port=*|-p) has_port=1 ;;
  esac
done
if [[ \"\$has_host\" == \"0\" ]]; then
  SERVER_ARGS+=(--host \"\$HOST\")
fi
if [[ \"\$has_port\" == \"0\" ]]; then
  SERVER_ARGS+=(--port \"\$PORT\")
fi
if [[ -n \"\$MODEL_PATH\" ]]; then
  exec \"\$LLAMACPP_BIN/llama-server\" -m \"\$MODEL_PATH\" \"\$@\" \"\${SERVER_ARGS[@]}\"
fi
exec \"\$LLAMACPP_BIN/llama-server\" \"\$@\" \"\${SERVER_ARGS[@]}\""
  else
    content="#!/usr/bin/env bash
set -euo pipefail
LLAMACPP_BIN=\"$bin_dir\"
$ld_setup
exec \"\$LLAMACPP_BIN/$executable\" \"\$@\""
  fi
  run rm -f "$path"
  write_file "$path" "$content"
  run chmod 0755 "$path"
}

write_python_wrapper() {
  local path="$1"
  local python_bin="$2"
  local content
  content="#!/usr/bin/env bash
set -euo pipefail
PYTHON_BIN=\"$python_bin\"
VENV_DIR=\"\$(cd \"\$(dirname \"\$PYTHON_BIN\")/..\" && pwd)\"
LLMSWAP_CUDA_LIBS=\"\"
for dir in \"\$VENV_DIR\"/lib/python*/site-packages/nvidia/*/lib; do
  if [[ -d \"\$dir\" ]]; then
    LLMSWAP_CUDA_LIBS=\"\${LLMSWAP_CUDA_LIBS:+\$LLMSWAP_CUDA_LIBS:}\$dir\"
  fi
done
if [[ -n \"\$LLMSWAP_CUDA_LIBS\" ]]; then
  export LD_LIBRARY_PATH=\"\$LLMSWAP_CUDA_LIBS:\${LD_LIBRARY_PATH:-}\"
fi
exec \"\$PYTHON_BIN\" \"\$@\""
  run rm -f "$path"
  write_file "$path" "$content"
  run chmod 0755 "$path"
}

write_runtime_wrapper() {
  local path="$1"
  local python_bin="$2"
  local runtime="$3"
  local content

  case "$runtime" in
    vllm)
      content="#!/usr/bin/env bash
set -euo pipefail
MODEL_PATH=\"\${MODEL_PATH:-}\"
if [[ -z \"\$MODEL_PATH\" && \$# -gt 0 ]]; then
  MODEL_PATH=\"\$1\"
  shift
fi
if [[ -z \"\$MODEL_PATH\" ]]; then
  echo \"usage: vllm.server MODEL_PATH [vllm args...]\" >&2
  exit 2
fi
HOST=\"\${HOST:-0.0.0.0}\"
PORT=\"\${PORT:-8000}\"
PYTHON_BIN=\"$python_bin\"
VENV_DIR=\"\$(cd \"\$(dirname \"\$PYTHON_BIN\")/..\" && pwd)\"
LLMSWAP_CUDA_LIBS=\"\"
for dir in \"\$VENV_DIR\"/lib/python*/site-packages/nvidia/*/lib; do
  if [[ -d \"\$dir\" ]]; then
    LLMSWAP_CUDA_LIBS=\"\${LLMSWAP_CUDA_LIBS:+\$LLMSWAP_CUDA_LIBS:}\$dir\"
  fi
done
if [[ -n \"\$LLMSWAP_CUDA_LIBS\" ]]; then
  export LD_LIBRARY_PATH=\"\$LLMSWAP_CUDA_LIBS:\${LD_LIBRARY_PATH:-}\"
fi
exec $(dirname "$python_bin")/vllm serve \"\$MODEL_PATH\" --host \"\$HOST\" --port \"\$PORT\" \"\$@\""
      ;;
    sglang)
      content="#!/usr/bin/env bash
set -euo pipefail
MODEL_PATH=\"\${MODEL_PATH:-}\"
if [[ -z \"\$MODEL_PATH\" && \$# -gt 0 ]]; then
  MODEL_PATH=\"\$1\"
  shift
fi
if [[ -z \"\$MODEL_PATH\" ]]; then
  echo \"usage: sglang.server MODEL_PATH [sglang args...]\" >&2
  exit 2
fi
HOST=\"\${HOST:-0.0.0.0}\"
PORT=\"\${PORT:-30000}\"
PYTHON_BIN=\"$python_bin\"
VENV_DIR=\"\$(cd \"\$(dirname \"\$PYTHON_BIN\")/..\" && pwd)\"
LLMSWAP_CUDA_LIBS=\"\"
for dir in \"\$VENV_DIR\"/lib/python*/site-packages/nvidia/*/lib; do
  if [[ -d \"\$dir\" ]]; then
    LLMSWAP_CUDA_LIBS=\"\${LLMSWAP_CUDA_LIBS:+\$LLMSWAP_CUDA_LIBS:}\$dir\"
  fi
done
if [[ -n \"\$LLMSWAP_CUDA_LIBS\" ]]; then
  export LD_LIBRARY_PATH=\"\$LLMSWAP_CUDA_LIBS:\${LD_LIBRARY_PATH:-}\"
fi
exec $python_bin -m sglang.launch_server --model-path \"\$MODEL_PATH\" --host \"\$HOST\" --port \"\$PORT\" \"\$@\""
      ;;
    *)
      echo "unsupported runtime wrapper '$runtime'" >&2
      exit 1
      ;;
  esac

  run rm -f "$path"
  write_file "$path" "$content"
  run chmod 0755 "$path"
}

configure_supervisor() {
  if [[ "$LLMSWAP_INSTALL_SUPERVISOR" != "1" ]]; then
    return 0
  fi
  local llama_conf agent_conf
  write_llama_swap_supervisor_wrapper
  write_agent_supervisor_wrapper
  llama_conf="[program:llmswap-llama-swap]
command=$LLMSWAP_ROOT/bin/llama-swap-supervisor.sh
directory=$LLMSWAP_ROOT
autostart=true
autorestart=true
startsecs=3
stopasgroup=true
killasgroup=true
stdout_logfile=$LLMSWAP_ROOT/logs/llama-swap.out.log
stderr_logfile=$LLMSWAP_ROOT/logs/llama-swap.err.log"
  agent_conf="[program:llmswap-agent]
command=$LLMSWAP_ROOT/bin/agent-supervisor.sh
directory=$LLMSWAP_ROOT
autostart=true
autorestart=true
startsecs=5
priority=50
stdout_logfile=$LLMSWAP_ROOT/logs/agent.out.log
stderr_logfile=$LLMSWAP_ROOT/logs/agent.err.log
environment=LLMSWAP_AGENT_CONFIG=\"$LLMSWAP_AGENT_CONFIG\""
  write_file /etc/supervisor/conf.d/llmswap-llama-swap.conf "$llama_conf"
  write_file /etc/supervisor/conf.d/llmswap-agent.conf "$agent_conf"
  ensure_supervisord
  run supervisorctl reread
  run supervisorctl update
}

ensure_supervisord() {
  run sh -c "pgrep -x supervisord >/dev/null || supervisord -c /etc/supervisor/supervisord.conf"
}

format_yaml_tags() {
  local tags="$1"
  local out=""
  IFS=',' read -r -a parts <<< "$tags"
  for part in "${parts[@]}"; do
    part="${part#"${part%%[![:space:]]*}"}"
    part="${part%"${part##*[![:space:]]}"}"
    if [[ -z "$part" ]]; then
      continue
    fi
    if [[ -n "$out" ]]; then
      out="$out, "
    fi
    out="$out$part"
  done
  printf '[%s]\n' "$out"
}

initialize_agent_config() {
  if [[ "$LLMSWAP_FORCE_CONFIG" != "1" ]]; then
    if [[ "$LLMSWAP_SIMULATE_EXISTING_AGENT_CONFIG" == "1" || ( "$LLMSWAP_DRY_RUN" != "1" && -f "$LLMSWAP_AGENT_CONFIG" ) ]]; then
      printf 'INFO %s exists; keeping it\n' "$LLMSWAP_AGENT_CONFIG"
      return 0
    fi
  fi

  local tags_yaml
  tags_yaml="$(format_yaml_tags "$LLMSWAP_AGENT_TAGS")"
  local content
  content="agent:
  id: $LLMSWAP_AGENT_ID
  tags: $tags_yaml
  model_root: $LLMSWAP_ROOT/models
  llama_swap_config: $LLMSWAP_ROOT/llama-swap.yaml
  llama_swap_service: supervisor
  restart_command: supervisorctl restart llmswap-llama-swap
  swap_port: $LLMSWAP_SWAP_PORT
  gateway_url: $LLMSWAP_GATEWAY_URL
  token: $LLMSWAP_AGENT_TOKEN
  llama_swap_token: $LLMSWAP_LLAMA_SWAP_TOKEN"
  write_file "$LLMSWAP_AGENT_CONFIG" "$content"
}

install_agent_binary() {
  if [[ -n "$LLMSWAP_AGENT_BINARY_SOURCE" ]]; then
    run install -m 0755 "$LLMSWAP_AGENT_BINARY_SOURCE" "$LLMSWAP_AGENT_BIN"
    return 0
  fi
  if [[ -d cmd/agent ]]; then
    if command -v go >/dev/null 2>&1 || [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
      run go build -o "$LLMSWAP_AGENT_BIN" ./cmd/agent
      return 0
    fi
  fi
  printf 'WARN llm-swap-agent binary not installed; pass --agent-binary or run go build -o %s ./cmd/agent\n' "$LLMSWAP_AGENT_BIN"
}

main() {
  require_root_unless_dry_run
  local cuda
  cuda="$(detect_cuda_version)"
  local backend
  backend="$(torch_backend_for_cuda "$cuda")"
  printf 'INFO cuda_version=%s torch_backend=%s root=%s runtime=%s only=%s\n' "$cuda" "$backend" "$LLMSWAP_ROOT" "$LLMSWAP_RUNTIME" "$LLMSWAP_ONLY"

  if stage_enabled base; then
    install_base_packages
    install_uv
  fi

  if stage_enabled tailscale; then
    install_tailscale
  fi

  if stage_enabled runtime; then
    if ! stage_enabled base; then
      print_uv_info
      ensure_runtime_dirs
      install_uv
    fi
    if runtime_enabled vllm; then
      install_vllm "$backend"
    fi
    if runtime_enabled sglang; then
      install_sglang "$backend"
    fi
    if runtime_enabled llamacpp; then
      install_llamacpp "$cuda"
    fi
  fi

  if stage_enabled agent; then
    ensure_agent_dirs
    initialize_agent_config
    install_agent_binary
  fi

  if stage_enabled supervisor; then
    ensure_supervisor_dirs
    configure_supervisor
  fi

  printf 'INFO worker install complete\n'
}

main "$@"
