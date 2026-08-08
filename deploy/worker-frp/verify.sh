#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="$script_dir/compose.yaml"
env_file="${1:-$script_dir/.env}"

command -v docker >/dev/null 2>&1 || {
  printf '%s\n' 'docker is required' >&2
  exit 1
}
command -v python3 >/dev/null 2>&1 || {
  printf '%s\n' 'python3 is required for offline Compose validation' >&2
  exit 1
}
[[ -r "$env_file" ]] || {
  printf 'environment file is not readable: %s\n' "$env_file" >&2
  exit 1
}

umask 077
config_json="$(mktemp)"
gpu_list="$(mktemp)"
image_mode_file="$(mktemp)"
image_ref_file="$(mktemp)"
cleanup() {
  rm -f -- "$config_json" "$gpu_list" "$image_mode_file" "$image_ref_file"
}
trap cleanup EXIT

docker compose --env-file "$env_file" -f "$compose_file" config --format json >"$config_json"

python3 - "$config_json" "$env_file" "$image_mode_file" "$image_ref_file" <<'PY'
import json
import os
import re
import stat
import sys

with open(sys.argv[1], "r", encoding="utf-8") as stream:
    config = json.load(stream)

def load_required_env(path):
    values = {}
    try:
        with open(path, "r", encoding="utf-8-sig") as stream:
            lines = stream.readlines()
    except OSError:
        raise SystemExit("deployment environment file could not be read")
    for raw_line in lines:
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].lstrip()
        if "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
            value = value[1:-1]
        values[key] = value
    required = {
        "WORKER_IMAGE",
        "LLMSWAP_BUILD_VERSION",
        "LLMSWAP_BUILD_COMMIT",
        "LLMSWAP_GATEWAY_URL",
        "WORKER_STATE_ROOT",
        "MODEL_ROOT",
        "AGENT_TOKEN_FILE",
    }
    if not required.issubset(values):
        raise SystemExit("deployment environment file is missing required variables")
    return values

deployment_env = load_required_env(sys.argv[2])
if deployment_env["LLMSWAP_BUILD_VERSION"] == deployment_env["LLMSWAP_BUILD_COMMIT"]:
    raise SystemExit("Agent release version and source commit must be distinct")

expected_names = [f"worker-gpu{i}" for i in range(8)]
services = config.get("services", {})
if sorted(services) != expected_names:
    raise SystemExit(f"expected exactly {expected_names}; got {sorted(services)}")

images = set()
model_sources = set()
log_sources = {}
gateway_urls = set()
for index, name in enumerate(expected_names):
    service = services[name]
    if service.get("hostname") != name:
        raise SystemExit(f"{name}: hostname must equal service name")

    image = service.get("image", "")
    if not image:
        raise SystemExit(f"{name}: image is required")
    images.add(image)
    build = service.get("build", {})
    if os.path.basename(build.get("dockerfile", "")) != "Dockerfile.agent":
        raise SystemExit(f"{name}: must build Dockerfile.agent")
    if build.get("args") != {
            "LLMSWAP_RUNTIME": "all",
            "LLMSWAP_BUILD_VERSION": deployment_env["LLMSWAP_BUILD_VERSION"],
            "LLMSWAP_BUILD_COMMIT": deployment_env["LLMSWAP_BUILD_COMMIT"],
    }:
        raise SystemExit(f"{name}: unexpected build args")

    environment = service.get("environment", {})
    if set(environment) != {
        "LLMSWAP_GATEWAY_URL",
        "LLMSWAP_AGENT_TAGS",
        "LLMSWAP_AGENT_TOKEN_FILE",
    }:
        raise SystemExit(f"{name}: runtime environment contains unexpected keys")
    if not environment.get("LLMSWAP_GATEWAY_URL"):
        raise SystemExit(f"{name}: LLMSWAP_GATEWAY_URL is empty")
    gateway_urls.add(environment["LLMSWAP_GATEWAY_URL"])
    if environment.get("LLMSWAP_AGENT_TAGS") != "gpu-4090":
        raise SystemExit(f"{name}: LLMSWAP_AGENT_TAGS must be gpu-4090")
    if environment.get("LLMSWAP_AGENT_TOKEN_FILE") != "/run/secrets/agent_token":
        raise SystemExit(f"{name}: agent token must come from the mounted secret")

    secrets = service.get("secrets", [])
    if (len(secrets) != 1 or secrets[0].get("source") != "agent_token" or
            secrets[0].get("target") != "agent_token" or secrets[0].get("mode") != "0400"):
        raise SystemExit(f"{name}: expected exactly one agent_token secret")
    if service.get("ports") or service.get("expose"):
        raise SystemExit(f"{name}: worker ports/expose must not be published")
    if service.get("runtime"):
        raise SystemExit(f"{name}: legacy runtime field must not be set")
    if set(service.get("networks", {})) != {"worker-private"}:
        raise SystemExit(f"{name}: expected only worker-private network")

    devices = (
        service.get("deploy", {})
        .get("resources", {})
        .get("reservations", {})
        .get("devices", [])
    )
    expected_device = str(index)
    if len(devices) != 1:
        raise SystemExit(f"{name}: expected one GPU reservation")
    device = devices[0]
    if device.get("driver") != "nvidia" or device.get("device_ids") != [expected_device] or device.get("capabilities") != ["gpu"]:
        raise SystemExit(f"{name}: expected physical GPU {expected_device}")

    mounts = {mount.get("target"): mount for mount in service.get("volumes", [])}
    if "/opt/llmswap" in mounts:
        raise SystemExit(f"{name}: root bind would hide the image runtime")
    if set(mounts) != {"/opt/llmswap/logs", "/opt/llmswap/models"}:
        raise SystemExit(f"{name}: expected per-worker logs plus shared models mounts")
    logs = mounts["/opt/llmswap/logs"]
    expected_logs_suffix = "/" + name + "/logs"
    if logs.get("type") != "bind" or not logs.get("source", "").replace("\\", "/").endswith(expected_logs_suffix) or logs.get("read_only"):
        raise SystemExit(f"{name}: invalid per-worker logs bind")
    log_sources[name] = logs.get("source")
    models = mounts["/opt/llmswap/models"]
    if models.get("type") != "bind" or models.get("read_only"):
        raise SystemExit(f"{name}: invalid shared model bind")
    model_sources.add(models.get("source"))

if len(images) != 1:
    raise SystemExit("all workers must reuse one image tag")
if len(model_sources) != 1 or None in model_sources:
    raise SystemExit("all workers must share one model root")
if "worker-private" not in config.get("networks", {}):
    raise SystemExit("worker-private network is missing")
if "agent_token" not in config.get("secrets", {}):
    raise SystemExit("file-backed agent_token secret is missing")

def is_placeholder(value):
    normalized = str(value).strip().lower()
    return (
        not normalized or
        "/path/to" in normalized or
        "replace" in normalized or
        ".example" in normalized or
        "example." in normalized
    )

def immutable_image(image):
    if re.fullmatch(r".+@sha256:[0-9a-fA-F]{64}", image):
        return True
    leaf = image.rsplit("/", 1)[-1]
    if ":" not in leaf:
        return False
    tag = leaf.rsplit(":", 1)[1]
    if tag.lower() == "latest":
        return False
    return re.search(r"[0-9a-fA-F]{7,}$", tag) is not None

image = next(iter(images))
if is_placeholder(image) or not immutable_image(image):
    raise SystemExit("WORKER_IMAGE must use a tag ending in at least 7 Git hex characters or a sha256 digest")
if len(gateway_urls) != 1 or is_placeholder(next(iter(gateway_urls))):
    raise SystemExit("LLMSWAP_GATEWAY_URL must be a non-placeholder deployment URL")
if deployment_env["WORKER_IMAGE"] != image:
    raise SystemExit("WORKER_IMAGE must be set directly in the deployment environment file")
if deployment_env["LLMSWAP_GATEWAY_URL"] != next(iter(gateway_urls)):
    raise SystemExit("LLMSWAP_GATEWAY_URL must be set directly in the deployment environment file")
for variable in ("WORKER_STATE_ROOT", "MODEL_ROOT", "AGENT_TOKEN_FILE"):
    value = deployment_env[variable]
    if is_placeholder(value) or not os.path.isabs(value):
        raise SystemExit(f"{variable} must be an absolute non-placeholder path in the deployment environment file")

def validate_directory(path, label):
    if is_placeholder(path) or not os.path.isabs(path):
        raise SystemExit(f"{label} must be an absolute non-placeholder path")
    if os.path.islink(path):
        raise SystemExit(f"{label} must not be a symlink")
    try:
        metadata = os.stat(path)
    except OSError:
        raise SystemExit(f"{label} must exist and be accessible")
    if not stat.S_ISDIR(metadata.st_mode):
        raise SystemExit(f"{label} must be a directory")

model_root = next(iter(model_sources))
validate_directory(model_root, "MODEL_ROOT")

state_roots = set()
for name, logs_path in log_sources.items():
    validate_directory(logs_path, f"{name} logs directory")
    worker_root = os.path.dirname(logs_path)
    if os.path.basename(worker_root) != name:
        raise SystemExit(f"{name} logs directory has an invalid layout")
    state_roots.add(os.path.dirname(worker_root))
if len(state_roots) != 1:
    raise SystemExit("workers must share exactly one WORKER_STATE_ROOT")
state_root = next(iter(state_roots))
validate_directory(state_root, "WORKER_STATE_ROOT")

secret = config.get("secrets", {}).get("agent_token", {})
token_path = secret.get("file", "")
if is_placeholder(token_path) or not os.path.isabs(token_path):
    raise SystemExit("AGENT_TOKEN_FILE must be an absolute non-placeholder path")
if os.path.islink(token_path):
    raise SystemExit("AGENT_TOKEN_FILE must not be a symlink")
try:
    token_metadata = os.stat(token_path)
except OSError:
    raise SystemExit("AGENT_TOKEN_FILE must exist and be accessible")
if not stat.S_ISREG(token_metadata.st_mode):
    raise SystemExit("AGENT_TOKEN_FILE must be a regular file")
if token_metadata.st_size < 1 or token_metadata.st_size > 16384:
    raise SystemExit("AGENT_TOKEN_FILE must contain between 1 and 16384 bytes")
if stat.S_IMODE(token_metadata.st_mode) & 0o077:
    raise SystemExit("AGENT_TOKEN_FILE must grant no permissions to group or other")
if token_metadata.st_uid not in {0, os.geteuid()}:
    raise SystemExit("AGENT_TOKEN_FILE must be owned by root or the current user")
if not os.access(token_path, os.R_OK):
    raise SystemExit("AGENT_TOKEN_FILE must be readable by the current user")
try:
    with open(token_path, "rb") as stream:
        token_content = stream.read(16385)
except OSError:
    raise SystemExit("AGENT_TOKEN_FILE must be readable by the current user")
if b"\x00" in token_content:
    raise SystemExit("AGENT_TOKEN_FILE contains invalid token data")
if token_content.endswith(b"\n"):
    token_content = token_content[:-1]
    if token_content.endswith(b"\r"):
        token_content = token_content[:-1]
if b"\n" in token_content or b"\r" in token_content or not token_content.strip():
    raise SystemExit("AGENT_TOKEN_FILE contains invalid token data")

image_mode = "digest" if "@sha256:" in image else "commit_tag"
try:
    with open(sys.argv[3], "w", encoding="ascii") as stream:
        stream.write(image_mode)
    with open(sys.argv[4], "w", encoding="utf-8") as stream:
        stream.write(image)
except OSError:
    raise SystemExit("could not record verified image mode")

for name, service in services.items():
    if name.lower().startswith("frpc"):
        raise SystemExit("FRPC must be embedded; sidecar service found")
    for key in service.get("environment", {}):
        normalized = key.upper()
        if normalized in {"NVIDIA_VISIBLE_DEVICES", "LLMSWAP_AGENT_ID"}:
            raise SystemExit(f"{name}: forbidden environment {key}")
        if "TAILSCALE" in normalized or "FRP" in normalized or normalized in {
            "LLMSWAP_SWAP_URL",
            "LLMSWAP_LLAMA_SWAP_TOKEN",
        }:
            raise SystemExit(f"{name}: forbidden runtime transport environment {key}")

print("Compose validation passed: 8 workers, physical GPUs 0..7, no published worker ports.")
PY

image_mode="$(<"$image_mode_file")"
if [[ "$image_mode" == "digest" ]]; then
  image_ref="$(<"$image_ref_file")"
  if ! docker image inspect "$image_ref" >/dev/null 2>&1; then
    printf '%s\n' 'digest image is not pulled or preloaded on this host' >&2
    exit 1
  fi
  unset image_ref
  printf '%s\n' 'Image mode: preloaded digest deployment path.'
elif [[ "$image_mode" == "commit_tag" ]]; then
  printf '%s\n' 'Image mode: commit-tag build path.'
else
  printf '%s\n' 'invalid verified image mode' >&2
  exit 1
fi
unset image_mode

if command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L >"$gpu_list" 2>/dev/null; then
  gpu_count="$(grep -c '^GPU [0-9][0-9]*:' "$gpu_list" || true)"
  if [[ "$gpu_count" != "8" ]]; then
    if [[ "${LLMSWAP_VERIFY_REQUIRE_8_GPUS:-0}" == "1" ]]; then
      printf 'host GPU check failed: expected 8 GPUs, detected %s\n' "$gpu_count" >&2
      exit 1
    fi
    printf 'Host GPU check skipped: expected 8 GPUs, detected %s; set LLMSWAP_VERIFY_REQUIRE_8_GPUS=1 on the deployment host.\n' "$gpu_count"
  else
    printf '%s\n' 'Host GPU check passed: 8 GPUs detected.'
  fi
else
  if [[ "${LLMSWAP_VERIFY_REQUIRE_8_GPUS:-0}" == "1" ]]; then
    printf '%s\n' 'host GPU check failed: nvidia-smi is unavailable or the driver is not active' >&2
    exit 1
  fi
  printf '%s\n' 'Host GPU check skipped: nvidia-smi is unavailable or the driver is not active; set LLMSWAP_VERIFY_REQUIRE_8_GPUS=1 on the deployment host.'
fi
