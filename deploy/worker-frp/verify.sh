#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
compose_file="$script_dir/compose.yaml"
env_file="${1:-$script_dir/.env.example}"

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

config_json="$(mktemp)"
gpu_list="$(mktemp)"
cleanup() {
  rm -f -- "$config_json" "$gpu_list"
}
trap cleanup EXIT

docker compose --env-file "$env_file" -f "$compose_file" config --format json >"$config_json"

python3 - "$config_json" <<'PY'
import json
import os
import sys

with open(sys.argv[1], "r", encoding="utf-8") as stream:
    config = json.load(stream)

expected_names = [f"worker-gpu{i}" for i in range(8)]
services = config.get("services", {})
if sorted(services) != expected_names:
    raise SystemExit(f"expected exactly {expected_names}; got {sorted(services)}")

images = set()
model_sources = set()
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
    if build.get("args") != {"LLMSWAP_INSTALL_TAILSCALE": "0", "LLMSWAP_RUNTIME": "all"}:
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
    if environment.get("LLMSWAP_AGENT_TAGS") != "gpu-4090":
        raise SystemExit(f"{name}: LLMSWAP_AGENT_TAGS must be gpu-4090")
    if environment.get("LLMSWAP_AGENT_TOKEN_FILE") != "/run/secrets/agent_token":
        raise SystemExit(f"{name}: agent token must come from the mounted secret")

    secrets = service.get("secrets", [])
    if len(secrets) != 1 or secrets[0].get("source") != "agent_token" or secrets[0].get("target") != "agent_token":
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
