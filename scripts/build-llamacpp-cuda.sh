#!/usr/bin/env bash
set -euo pipefail

LLMSWAP_ROOT="${LLMSWAP_ROOT:-/opt/llmswap}"
LLAMACPP_SRC="${LLAMACPP_SRC:-/root/autodl-tmp/llama.cpp}"
LLAMACPP_BUILD_ROOT="${LLAMACPP_BUILD_ROOT:-/root/autodl-tmp/llmswap-llamacpp-build}"
LLAMACPP_OUT="${LLAMACPP_OUT:-$LLMSWAP_ROOT/runtimes/llamacpp}"
LLAMACPP_CACHE="${LLAMACPP_CACHE:-$LLMSWAP_ROOT/cache/cuda}"
LLAMACPP_TMP="${LLAMACPP_TMP:-/root/autodl-tmp/tmp}"
LLAMACPP_ARCHS="${LLAMACPP_ARCHS:-89}"
LLAMACPP_VERSIONS="${LLAMACPP_VERSIONS:-cu130}"
LLAMACPP_JOBS="${LLAMACPP_JOBS:-$(nproc)}"
LLAMACPP_INSTALL_TOOLKITS="${LLAMACPP_INSTALL_TOOLKITS:-1}"
LLAMACPP_REF="${LLAMACPP_REF:-}"

usage() {
  cat <<'EOF'
Usage: build-llamacpp-cuda.sh [versions...]

Build llama.cpp CUDA runtime packages.

Examples:
  build-llamacpp-cuda.sh cu130
  LLAMACPP_ARCHS="80;86;89" build-llamacpp-cuda.sh cu124 cu128 cu130

Environment:
  LLAMACPP_SRC               llama.cpp source tree. Default: /root/autodl-tmp/llama.cpp
  LLAMACPP_OUT               output directory. Default: /opt/llmswap/runtimes/llamacpp
  LLAMACPP_ARCHS             CMAKE_CUDA_ARCHITECTURES. Default: 89
  LLAMACPP_INSTALL_TOOLKITS  install missing CUDA toolkits from NVIDIA runfiles. Default: 1
  LLAMACPP_JOBS              build parallelism. Default: nproc
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi
if [[ $# -gt 0 ]]; then
  LLAMACPP_VERSIONS="$*"
fi

log() {
  printf 'INFO %s\n' "$*"
}

die() {
  printf 'ERROR %s\n' "$*" >&2
  exit 1
}

cuda_path_for_version() {
  case "$1" in
    cu124) printf '%s\n' "${CUDA_124_HOME:-/root/autodl-tmp/cuda-12.4}" ;;
    cu128) printf '%s\n' "${CUDA_128_HOME:-/root/autodl-tmp/cuda-12.8}" ;;
    cu130) printf '%s\n' "${CUDA_130_HOME:-/usr/local/cuda-13.0}" ;;
    *) die "unsupported CUDA package '$1'; use cu124, cu128, or cu130" ;;
  esac
}

cuda_runfile_url() {
  case "$1" in
    cu124) printf '%s\n' "https://developer.download.nvidia.com/compute/cuda/12.4.1/local_installers/cuda_12.4.1_550.54.15_linux.run" ;;
    cu128) printf '%s\n' "https://developer.download.nvidia.com/compute/cuda/12.8.1/local_installers/cuda_12.8.1_570.124.06_linux.run" ;;
    cu130) printf '%s\n' "https://developer.download.nvidia.com/compute/cuda/13.0.0/local_installers/cuda_13.0.0_580.65.06_linux.run" ;;
  esac
}

ensure_source() {
  if [[ ! -d "$LLAMACPP_SRC/.git" ]]; then
    die "llama.cpp source tree not found at $LLAMACPP_SRC"
  fi
  if [[ -n "$LLAMACPP_REF" ]]; then
    git -C "$LLAMACPP_SRC" fetch --tags --depth 1 origin "$LLAMACPP_REF"
    git -C "$LLAMACPP_SRC" checkout FETCH_HEAD
  fi
}

install_toolkit() {
  local version="$1"
  local cuda_home="$2"
  if [[ -x "$cuda_home/bin/nvcc" ]]; then
    return 0
  fi
  if [[ "$LLAMACPP_INSTALL_TOOLKITS" != "1" ]]; then
    die "$version toolkit missing at $cuda_home and LLAMACPP_INSTALL_TOOLKITS=0"
  fi

  local url
  url="$(cuda_runfile_url "$version")"
  local runfile="$LLAMACPP_CACHE/$(basename "$url")"
  mkdir -p "$LLAMACPP_CACHE" "$cuda_home"
  if [[ ! -s "$runfile" ]]; then
    log "download $version toolkit runfile to $runfile"
    curl -fL --retry 5 --retry-delay 5 -o "$runfile" "$url"
  fi

  log "install $version toolkit to $cuda_home"
  sh "$runfile" --silent --toolkit --toolkitpath="$cuda_home" --override
}

build_one() {
  local version="$1"
  local cuda_home
  cuda_home="$(cuda_path_for_version "$version")"
  install_toolkit "$version" "$cuda_home"

  local build_dir="$LLAMACPP_BUILD_ROOT/$version-sm${LLAMACPP_ARCHS//;/_}"
  local package_dir="$LLAMACPP_OUT/$version-sm${LLAMACPP_ARCHS//;/_}"
  local tarball="$LLAMACPP_OUT/llamacpp-linux-$version-sm${LLAMACPP_ARCHS//;/_}.tar.gz"

  log "configure $version cuda_home=$cuda_home archs=$LLAMACPP_ARCHS"
  rm -rf "$build_dir" "$package_dir"
  cmake -S "$LLAMACPP_SRC" -B "$build_dir" \
    -DGGML_CUDA=ON \
    -DGGML_NATIVE=OFF \
    -DGGML_CUDA_FA_ALL_QUANTS=ON \
    -DLLAMA_CURL=ON \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_CUDA_COMPILER="$cuda_home/bin/nvcc" \
    -DCUDAToolkit_ROOT="$cuda_home" \
    -DCMAKE_CUDA_ARCHITECTURES="$LLAMACPP_ARCHS"

  log "build $version"
  cmake --build "$build_dir" --config Release -j "$LLAMACPP_JOBS" --target llama-server llama-cli llama-bench

  log "package $version to $tarball"
  mkdir -p "$package_dir/bin"
  cp -a "$build_dir/bin/." "$package_dir/bin/"
  copy_cuda_runtime_libs "$cuda_home" "$package_dir/bin"
  patch_package_runpaths "$package_dir/bin"
  cat > "$package_dir/bin/llamacpp.server" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
MODEL_PATH="${MODEL_PATH:-}"
if [[ -z "$MODEL_PATH" && $# -gt 0 && "$1" != -* ]]; then
  MODEL_PATH="$1"
  shift
fi
HOST="${HOST:-0.0.0.0}"
PORT="${PORT:-8080}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export LD_LIBRARY_PATH="$SCRIPT_DIR:${LD_LIBRARY_PATH:-}"
if [[ -n "$MODEL_PATH" ]]; then
  exec "$SCRIPT_DIR/llama-server" -m "$MODEL_PATH" --host "$HOST" --port "$PORT" "$@"
fi
exec "$SCRIPT_DIR/llama-server" "$@"
EOF
  chmod 0755 "$package_dir/bin/llamacpp.server"
  {
    printf 'version=%s\n' "$version"
    printf 'cuda_home=%s\n' "$cuda_home"
    printf 'archs=%s\n' "$LLAMACPP_ARCHS"
    printf 'source=%s\n' "$LLAMACPP_SRC"
    printf 'commit=%s\n' "$(git -C "$LLAMACPP_SRC" rev-parse HEAD)"
    printf 'built_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  } > "$package_dir/BUILDINFO"
  tar -C "$package_dir" -czf "$tarball" .
  sha256sum "$tarball" > "$tarball.sha256"
  log "built $tarball"
}

copy_cuda_runtime_libs() {
  local cuda_home="$1"
  local dest="$2"
  local lib_dir="$cuda_home/lib64"
  for pattern in libcudart.so* libcublas.so* libcublasLt.so*; do
    compgen -G "$lib_dir/$pattern" >/dev/null || die "missing CUDA runtime library pattern $lib_dir/$pattern"
    cp -a "$lib_dir"/$pattern "$dest/"
  done
}

patch_package_runpaths() {
  local bin_dir="$1"
  if ! command -v patchelf >/dev/null 2>&1; then
    die "patchelf is required to set package RUNPATH to \$ORIGIN"
  fi
  while IFS= read -r -d '' file; do
    if [[ -L "$file" ]]; then
      continue
    fi
    if file "$file" | grep -q 'ELF'; then
      patchelf --set-rpath '$ORIGIN' "$file"
    fi
  done < <(find "$bin_dir" -maxdepth 1 -type f -print0)
}

ensure_source
mkdir -p "$LLAMACPP_BUILD_ROOT" "$LLAMACPP_OUT" "$LLAMACPP_TMP"
export TMPDIR="$LLAMACPP_TMP"
for version in $LLAMACPP_VERSIONS; do
  build_one "$version"
done
