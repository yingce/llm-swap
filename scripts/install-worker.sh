#!/usr/bin/env bash
set -euo pipefail

LLMSWAP_ROOT="${LLMSWAP_ROOT:-/opt/llmswap}"
LLMSWAP_PYTHON="${LLMSWAP_PYTHON:-3.12}"
LLMSWAP_RUNTIME="${LLMSWAP_RUNTIME:-all}"
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
LLMSWAP_LLAMA_SWAP_TOKEN="${LLMSWAP_LLAMA_SWAP_TOKEN:-worker-token}"
LLMSWAP_SWAP_PORT="${LLMSWAP_SWAP_PORT:-8081}"
LLMSWAP_FORCE_CONFIG="${LLMSWAP_FORCE_CONFIG:-0}"
LLMSWAP_SIMULATE_EXISTING_AGENT_CONFIG="${LLMSWAP_SIMULATE_EXISTING_AGENT_CONFIG:-0}"
LLMSWAP_TAILSCALE_AUTHKEY="${LLMSWAP_TAILSCALE_AUTHKEY:-${TAILSCALE_AUTHKEY:-}}"
LLMSWAP_TAILSCALE_HOSTNAME="${LLMSWAP_TAILSCALE_HOSTNAME:-}"
LLMSWAP_UV_INSTALL_TIMEOUT="${LLMSWAP_UV_INSTALL_TIMEOUT:-120}"
LLMSWAP_UV_CACHE_DIR="${LLMSWAP_UV_CACHE_DIR:-$LLMSWAP_ROOT/cache/uv}"
LLMSWAP_UV_PYTHON_INSTALL_DIR="${LLMSWAP_UV_PYTHON_INSTALL_DIR:-$LLMSWAP_ROOT/python}"
LLMSWAP_UV_PYTHON_INSTALL_MIRROR="${LLMSWAP_UV_PYTHON_INSTALL_MIRROR:-}"
LLMSWAP_LLAMA_CPP_BASE_URL="${LLMSWAP_LLAMA_CPP_BASE_URL:-http://llmfs-bj.oss-cn-beijing.aliyuncs.com/models}"
LLMSWAP_LLAMA_CPP_CUDA="${LLMSWAP_LLAMA_CPP_CUDA:-auto}"
LLMSWAP_LLAMA_CPP_ARCH="${LLMSWAP_LLAMA_CPP_ARCH:-sm89}"
LLMSWAP_RUNTIME_CACHE_DIR="${LLMSWAP_RUNTIME_CACHE_DIR:-$LLMSWAP_ROOT/cache/runtimes}"
UV_LINK_MODE="${UV_LINK_MODE:-copy}"
export UV_CACHE_DIR="$LLMSWAP_UV_CACHE_DIR"
export UV_PYTHON_INSTALL_DIR="$LLMSWAP_UV_PYTHON_INSTALL_DIR"
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
  --runtime all|vllm|sglang|llamacpp
                            Runtime environments to install.
  --cuda-version X.Y        Override detected CUDA version, e.g. 12.4, 12.8, 13.0.
  --llamacpp-base-url URL   Base URL containing llamacpp-linux-<variant>.tar.gz.
  --llamacpp-cuda auto|cu124|cu128|cu130
                            llama.cpp CUDA package selector. Default: auto.
  --tailscale-authkey KEY   Run tailscale up with this auth key after install.
  --tailscale-hostname NAME Hostname passed to tailscale up when auth key is set.
  --skip-tailscale          Do not install or configure Tailscale.
  --skip-supervisor         Do not install or configure supervisor.
  --python PYTHON           Python version/executable used by uv venv. Default: 3.12.
  --agent-binary PATH       Existing llm-swap-agent binary to install.
  --agent-id ID             Agent ID written into /opt/llmswap/agent.yaml.
  --tags TAGS               Comma-separated tags written into agent config.
  --gateway-url URL         Gateway URL written into agent config.
  --agent-token TOKEN       Gateway agent token written into agent config.
  --llama-swap-token TOKEN  llama-swap token written into agent config.
  --swap-port PORT          llama-swap port used when swap_url is omitted.
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

install_base_packages() {
  printf 'INFO uv_cache_dir=%s uv_python_install_dir=%s uv_link_mode=%s\n' "$UV_CACHE_DIR" "$UV_PYTHON_INSTALL_DIR" "$UV_LINK_MODE"
  if [[ -n "${UV_PYTHON_INSTALL_MIRROR:-}" ]]; then
    printf 'INFO uv_python_install_mirror=%s\n' "$UV_PYTHON_INSTALL_MIRROR"
  fi
  run mkdir -p "$LLMSWAP_ROOT/bin" "$LLMSWAP_ROOT/models" "$LLMSWAP_ROOT/venvs" "$LLMSWAP_ROOT/logs" "$UV_CACHE_DIR" "$UV_PYTHON_INSTALL_DIR" "$LLMSWAP_RUNTIME_CACHE_DIR"
  if command -v apt-get >/dev/null 2>&1 || [[ "$LLMSWAP_DRY_RUN" == "1" ]]; then
    run apt-get update
    run apt-get install -y ca-certificates curl gnupg python3 python3-venv python3-dev python3-pip supervisor git
  else
    echo "apt-get not found; install ca-certificates, curl, python3, python3-venv, python3-dev, python3-pip, supervisor, git manually" >&2
  fi
}

install_uv() {
  if command -v uv >/dev/null 2>&1; then
    printf 'uv already installed: %s\n' "$(command -v uv)"
    return 0
  fi
  run sh -c "timeout $LLMSWAP_UV_INSTALL_TIMEOUT sh -c 'curl -LsSf https://astral.sh/uv/install.sh | sh' || python3 -m pip install --upgrade uv"
  export PATH="$HOME/.local/bin:$PATH"
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
  if [[ -z "$LLMSWAP_TAILSCALE_AUTHKEY" ]]; then
    printf 'INFO TAILSCALE_AUTHKEY not set; not running tailscale up.\n'
    return 0
  fi

  local args=(tailscale up --auth-key "$LLMSWAP_TAILSCALE_AUTHKEY")
  if [[ -n "$LLMSWAP_TAILSCALE_HOSTNAME" ]]; then
    args+=(--hostname "$LLMSWAP_TAILSCALE_HOSTNAME")
  fi
  run "${args[@]}"
}

install_torch() {
  local python="$1"
  local backend="$2"
  local index_url="https://download.pytorch.org/whl/$backend"
  run uv pip install --python "$python" torch torchvision torchaudio --index-url "$index_url"
}

install_vllm() {
  local backend="$1"
  local venv="$LLMSWAP_ROOT/venvs/vllm"
  run uv venv "$venv" --python "$LLMSWAP_PYTHON" --clear
  install_torch "$venv/bin/python" "$backend"
  run uv pip install --python "$venv/bin/python" vllm --torch-backend=auto
  write_runtime_wrapper "$LLMSWAP_ROOT/bin/vllm.server" "$venv/bin/python" vllm
  run ln -sfn "$venv/bin/python" "$LLMSWAP_ROOT/bin/vllm-python"
}

install_sglang() {
  local backend="$1"
  local venv="$LLMSWAP_ROOT/venvs/sglang"
  local check_script="import torch, sglang; print('torch', torch.__version__); print('torch_cuda', torch.version.cuda); print('cuda_available', torch.cuda.is_available()); print('sglang', sglang.__version__)"
  run uv venv "$venv" --python "$LLMSWAP_PYTHON" --clear
  run uv pip install --python "$venv/bin/python" sglang
  printf 'INFO sglang_resolved_runtime backend_hint=%s\n' "$backend"
  run "$venv/bin/python" -c "$check_script"
  write_runtime_wrapper "$LLMSWAP_ROOT/bin/sglang.server" "$venv/bin/python" sglang
  run ln -sfn "$venv/bin/python" "$LLMSWAP_ROOT/bin/sglang-python"
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
  run "$LLMSWAP_ROOT/bin/llamacpp.server" --version
}

write_llamacpp_bin_wrapper() {
  local path="$1"
  local bin_dir="$2"
  local executable="$3"
  local content
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
export LD_LIBRARY_PATH=\"\$LLAMACPP_BIN:\${LD_LIBRARY_PATH:-}\"
if [[ -n \"\$MODEL_PATH\" ]]; then
  exec \"\$LLAMACPP_BIN/llama-server\" -m \"\$MODEL_PATH\" --host \"\$HOST\" --port \"\$PORT\" \"\$@\"
fi
exec \"\$LLAMACPP_BIN/llama-server\" \"\$@\""
  else
    content="#!/usr/bin/env bash
set -euo pipefail
LLAMACPP_BIN=\"$bin_dir\"
export LD_LIBRARY_PATH=\"\$LLAMACPP_BIN:\${LD_LIBRARY_PATH:-}\"
exec \"\$LLAMACPP_BIN/$executable\" \"\$@\""
  fi
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
exec $python_bin -m sglang.launch_server --model-path \"\$MODEL_PATH\" --host \"\$HOST\" --port \"\$PORT\" \"\$@\""
      ;;
    *)
      echo "unsupported runtime wrapper '$runtime'" >&2
      exit 1
      ;;
  esac

  write_file "$path" "$content"
  run chmod 0755 "$path"
}

configure_supervisor() {
  if [[ "$LLMSWAP_INSTALL_SUPERVISOR" != "1" ]]; then
    return 0
  fi
  local conf
  conf="[program:llmswap-agent]
command=$LLMSWAP_AGENT_BIN --config $LLMSWAP_AGENT_CONFIG
directory=$LLMSWAP_ROOT
autostart=true
autorestart=true
startsecs=5
stdout_logfile=$LLMSWAP_ROOT/logs/agent.out.log
stderr_logfile=$LLMSWAP_ROOT/logs/agent.err.log
environment=LLM_SWAP_AGENT_CONFIG=\"$LLMSWAP_AGENT_CONFIG\""
  write_file /etc/supervisor/conf.d/llmswap-agent.conf "$conf"
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
    if [[ -f "$LLMSWAP_AGENT_CONFIG" || "$LLMSWAP_SIMULATE_EXISTING_AGENT_CONFIG" == "1" ]]; then
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
  printf 'INFO cuda_version=%s torch_backend=%s root=%s runtime=%s\n' "$cuda" "$backend" "$LLMSWAP_ROOT" "$LLMSWAP_RUNTIME"

  install_base_packages
  install_uv
  install_tailscale
  if runtime_enabled vllm; then
    install_vllm "$backend"
  fi
  if runtime_enabled sglang; then
    install_sglang "$backend"
  fi
  if runtime_enabled llamacpp; then
    install_llamacpp "$cuda"
  fi
  initialize_agent_config
  install_agent_binary
  configure_supervisor
  printf 'INFO worker install complete\n'
}

main "$@"
