# Worker Runtime Install Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a worker install script that initializes Python runtime environments for vLLM and SGLang with CUDA-aware package selection.

**Architecture:** Keep the installer as a single Bash entrypoint with dry-run support so behavior can be tested without root or network access. Use uv for vLLM/SGLang Python environment and package management, system-level supervisor for process management, and Tailscale as an optional host dependency. Default uv venv creation to Python 3.12 instead of host/conda Python. Leave llama.cpp CUDA binaries out of this slice.

**Tech Stack:** Bash, uv, apt, supervisor, Tailscale, PyTorch CUDA wheel indexes, vLLM, SGLang, Go test harness.

---

### Task 1: Dry-Run Tests

**Files:**
- Create: `scripts/install_worker_test.go`

- [x] Write tests that execute `scripts/install-worker.sh --dry-run`.
- [x] Assert CUDA 12.4 maps to PyTorch `cu124`.
- [x] Assert CUDA 12.8 maps to PyTorch `cu128` and SGLang cu128 wheel indexes.
- [x] Assert CUDA 13.0 maps to PyTorch `cu130` for vLLM while SGLang resolves its own torch/CUDA package set.
- [x] Assert SGLang installs through `uv pip install sglang` without preinstalling torch, `sglang[all]`, extra indexes, or explicit kernel wheels.
- [x] Assert SGLang prints the resolved torch CUDA runtime after installation.
- [x] Assert supervisor is used and systemd is not referenced.
- [x] Assert supervisor is installed as a system package, not inside `/opt/llmswap/venvs/base`.
- [x] Assert no-systemd hosts start `supervisord` directly before `supervisorctl`.
- [x] Assert vLLM and SGLang venvs are initialized with `uv venv --python 3.12 --clear`.
- [x] Assert the Python version can still be overridden with `--python`.
- [x] Assert uv cache, managed Python install directory, link mode, and optional Python install mirror are exposed.
- [x] Assert `--runtime vllm --skip-tailscale` excludes SGLang and Tailscale.
- [x] Assert vLLM and SGLang server wrappers are written into `/opt/llmswap/bin`.
- [x] Assert dry-run initializes agent config from CLI values.
- [x] Assert dry-run builds agent from local source when no binary is provided.
- [x] Assert an existing agent config is preserved unless `--force-config` is set.

### Task 2: Worker Installer

**Files:**
- Create: `scripts/install-worker.sh`

- [x] Add `--dry-run`, `--root`, `--runtime`, `--cuda-version`, `--skip-tailscale`, `--skip-supervisor`, and `--python`.
- [x] Create `/opt/llmswap` directory layout by default.
- [x] Install base packages with apt when available.
- [x] Install uv through the official installer when uv is missing.
- [x] Detect CUDA from override, `nvidia-smi`, or `nvcc`.
- [x] Map CUDA 12.4/12.5 to `cu124`, 12.6/12.7 to `cu126`, 12.8/12.9 to `cu128`, and 13.x to `cu130`.
- [x] Create separate uv venvs for vLLM and SGLang with `uv venv --python 3.12 --clear` by default.
- [x] Keep uv cache and managed Python installs under `/opt/llmswap` by default.
- [x] Support `LLMSWAP_UV_PYTHON_INSTALL_MIRROR` for managed Python downloads on hosts that cannot reach the default release source.
- [x] Install torch/torchvision/torchaudio from the selected PyTorch CUDA wheel index.
- [x] Install vLLM with uv and `--torch-backend=auto`.
- [x] Install SGLang directly with `uv pip install sglang` and let SGLang resolve torch, kernels, FlashInfer, and CUDA runtime wheels.
- [x] Print the resolved SGLang runtime versions after install: torch version, torch CUDA runtime, CUDA availability, and SGLang version.
- [x] Write `/opt/llmswap/bin/vllm.server`, which accepts `MODEL_PATH` or the first positional argument and starts the vLLM OpenAI-compatible server.
- [x] Write `/opt/llmswap/bin/sglang.server`, which accepts `MODEL_PATH` or the first positional argument and starts the SGLang server via `python -m sglang.launch_server`.
- [x] Link runtime Python binaries to `/opt/llmswap/bin/vllm-python` and `/opt/llmswap/bin/sglang-python`.
- [x] Generate `/etc/supervisor/conf.d/llmswap-agent.conf`.
- [x] Start system `supervisord` directly when it is not already running.
- [x] Generate `/opt/llmswap/agent.yaml` from CLI/env values on first install.
- [x] Preserve an existing agent config unless `--force-config` is set.
- [x] Install an existing agent binary from `--agent-binary` when provided.
- [x] Build `./cmd/agent` into `/opt/llmswap/bin/llm-swap-agent` when running from the source repo and Go is available.

### Task 3: Verification

**Files:**
- Modify only if verification finds a defect.

- [x] Run `docker compose run --rm dev go test ./scripts -run TestInstallWorker -count=1`.
- [x] Run `docker compose run --rm dev go test ./...`.
