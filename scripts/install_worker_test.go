package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallWorkerDryRunUsesCuda124TorchIndexAndSupervisor(t *testing.T) {
	out := runInstallWorker(t, "12.4")

	assertContains(t, out, "uv venv /opt/llmswap/venvs/vllm --python 3.12 --managed-python --clear")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu124")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm --torch-backend=cu124")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm-omni[minicpmo]==0.24.0 --torch-backend=cu124")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python librosa soundfile torchcodec av")
	assertContains(t, out, "uv venv /opt/llmswap/venvs/sglang --python 3.12 --managed-python --clear")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu124")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python --prerelease=allow sglang --torch-backend=cu124")
	assertContains(t, out, "sglang_minicpmv46_patch=applied")
	assertContains(t, out, "/etc/supervisor/conf.d/llmswap-agent.conf")
	assertContains(t, out, "/etc/supervisor/conf.d/llmswap-llama-swap.conf")
	assertNotContains(t, out, "systemctl")
}

func TestDockerfileAgentInstallsJITBuildTools(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join(repoRoot(t), "Dockerfile.agent"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	assertContains(t, text, "build-essential")
	assertContains(t, text, "ninja-build")
	assertContains(t, text, "python3-jinja2")
}

func TestDockerfileAgentConfiguresUvNetworkRetries(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join(repoRoot(t), "Dockerfile.agent"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	for _, want := range []string{
		"ARG UV_HTTP_RETRIES=10",
		"ARG UV_HTTP_TIMEOUT=120",
		"ARG UV_HTTP_CONNECT_TIMEOUT=30",
		"UV_HTTP_RETRIES=\"${UV_HTTP_RETRIES}\"",
		"UV_HTTP_TIMEOUT=\"${UV_HTTP_TIMEOUT}\"",
		"UV_HTTP_CONNECT_TIMEOUT=\"${UV_HTTP_CONNECT_TIMEOUT}\"",
	} {
		assertContains(t, text, want)
	}
}

func TestDockerfileAgentScopesBuildProxyToBuildKitSecrets(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join(repoRoot(t), "Dockerfile.agent"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	if got := strings.Count(text, "--mount=type=secret,id=llmswap_runtime_proxy"); got < 4 {
		t.Fatalf("runtime proxy secret mounts = %d, want at least 4 proxied network stages", got)
	}
	assertContains(t, text, `runtime_proxy="$(cat /run/secrets/llmswap_runtime_proxy 2>/dev/null || true)"`)
	assertNotContains(t, text, "ARG LLMSWAP_RUNTIME_HTTP_PROXY")
}

func TestDockerfileGatewayScopesGoProxyToBuildKitSecret(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join(repoRoot(t), "Dockerfile.gateway"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	assertContains(t, text, "--mount=type=secret,id=llmswap_runtime_proxy")
	assertContains(t, text, `runtime_proxy="$(cat /run/secrets/llmswap_runtime_proxy 2>/dev/null || true)"`)
	assertNotContains(t, text, "ARG LLMSWAP_RUNTIME_HTTP_PROXY")
}

func TestDockerfileAgentInstallsAllRuntimesInOneHardlinkLayer(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "Dockerfile.agent"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(dockerfile)
	assertContains(t, text, "UV_LINK_MODE=hardlink")
	assertContains(t, text, `--runtime "${LLMSWAP_RUNTIME}"`)
	if got := strings.Count(text, "--only runtime"); got != 1 {
		t.Fatalf("Dockerfile --only runtime count = %d, want 1", got)
	}
	assertContains(t, text, `rm -rf "${LLMSWAP_ROOT}/cache" "${LLMSWAP_ROOT}/src"`)
}

func TestInstallWorkerExportsUvNetworkRetryDefaults(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--only", "base")

	assertContains(t, out, "INFO uv_http_retries=10 uv_http_timeout=120 uv_http_connect_timeout=30")
}

func TestInstallWorkerDryRunUsesSystemSupervisor(t *testing.T) {
	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "apt-get install -y ca-certificates curl gnupg procps python3 python3-venv python3-dev python3-pip python3-jinja2 supervisor git")
	assertContains(t, out, "apt-get install -y ca-certificates curl gnupg procps python3 python3-venv python3-dev python3-pip python3-jinja2 supervisor git build-essential ninja-build ffmpeg libavdevice58")
	assertContains(t, out, "WRITE /opt/llmswap/bin/llama-swap-supervisor.sh")
	assertNotContains(t, out, "while [[ ! -s \"$LLMSWAP_LLAMA_SWAP_CONFIG\" ]]; do")
	assertContains(t, out, "models: {}")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-llama-swap.conf")
	assertContains(t, out, "command=/opt/llmswap/bin/llama-swap-supervisor.sh")
	assertContains(t, out, "stdout_logfile=/opt/llmswap/logs/llama-swap.out.log")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-agent.conf")
	assertContains(t, out, "sh -c pgrep -x supervisord >/dev/null || supervisord -c /etc/supervisor/supervisord.conf")
	assertContains(t, out, "supervisorctl reread")
	assertNotContains(t, out, "/opt/llmswap/venvs/base")
	assertNotContains(t, out, "pip install supervisor")
}

func TestInstallWorkerDryRunSelectsCuda128AndCuda130Indexes(t *testing.T) {
	cuda128 := runInstallWorker(t, "12.8")
	assertContains(t, cuda128, "https://download.pytorch.org/whl/cu128")
	assertContains(t, cuda128, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python torch==2.11.0 torchaudio==2.11.0 torchvision==0.26.0 --index-url https://download.pytorch.org/whl/cu128")
	assertNotContains(t, cuda128, "vllm-0.24.0+cu128")
	assertContains(t, cuda128, "git clone --depth 1 --branch v0.22.0 https://github.com/vllm-project/vllm.git /opt/llmswap/src/vllm-v0.22.0")
	assertContains(t, cuda128, "grep -Ev '^(torch|torchaudio|torchvision|nvidia-cutlass-dsl|humming-kernels)(\\[|[<>=[:space:]]|$)' requirements/cuda.txt > requirements/cuda.llmswap-filtered.txt")
	assertContains(t, cuda128, "uv pip install --python '/opt/llmswap/venvs/vllm/bin/python' -r requirements/cuda.llmswap-filtered.txt --torch-backend='cu128'")
	assertContains(t, cuda128, "VLLM_FLASH_ATTN_VERSION=2")
	assertContains(t, cuda128, "uv build --python '/opt/llmswap/venvs/vllm/bin/python' --wheel --no-build-isolation")
	assertNotContains(t, cuda128, "--editable .")
	assertContains(t, cuda128, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm-omni[minicpmo]==0.22.0 --torch-backend=cu128")
	assertNotContains(t, cuda128, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm==0.22.0 --torch-backend=cu128")
	assertNotContains(t, cuda128, "uv venv /opt/llmswap/venvs/vllm-omni")
	assertNotContains(t, cuda128, "WRITE /opt/llmswap/bin/vllm-omni.server")
	assertContains(t, cuda128, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python torch==2.8.0 torchaudio==2.8.0 torchvision==0.23.0 --index-url https://download.pytorch.org/whl/cu128")
	assertContains(t, cuda128, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python torchao==0.9.0")
	assertContains(t, cuda128, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python --prerelease=allow sglang==0.5.5.post3 cuda-python<13")
	assertContains(t, cuda128, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python librosa soundfile torchcodec==0.5.0 av")
	assertNotContains(t, cuda128, "https://docs.sglang.ai/whl/cu128/")
	assertNotContains(t, cuda128, "sglang-kernel")

	cuda130 := runInstallWorker(t, "13.0")
	assertContains(t, cuda130, "https://download.pytorch.org/whl/cu130")
	assertContains(t, cuda130, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm --torch-backend=cu130")
	assertNotContains(t, cuda130, "vllm-0.24.0+cu130")
	assertContains(t, cuda130, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm-omni[minicpmo]==0.24.0 --torch-backend=cu130")
	assertNotContains(t, cuda130, "uv venv /opt/llmswap/venvs/vllm-omni")
	assertContains(t, cuda130, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python --prerelease=allow sglang --torch-backend=cu130")
	assertNotContains(t, cuda130, "sglang[all]")
}

func TestInstallWorkerDryRunSupportsRuntimeList(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "vllm,llamacpp")

	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm-omni[minicpmo]==0.22.0 --torch-backend=cu128")
	assertContains(t, out, "llamacpp-linux-cu128-sm89.tar.gz")
	assertNotContains(t, out, "/opt/llmswap/venvs/vllm-omni")
	assertNotContains(t, out, "/opt/llmswap/venvs/sglang")
}

func TestInstallWorkerVLLMRuntimeIncludesOmniInUnifiedVenv(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "vllm")

	assertContains(t, out, "uv venv /opt/llmswap/venvs/vllm --python 3.12 --managed-python --clear")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python torch==2.11.0 torchaudio==2.11.0 torchvision==0.26.0 --index-url https://download.pytorch.org/whl/cu128")
	assertContains(t, out, "git clone --depth 1 --branch v0.22.0 https://github.com/vllm-project/vllm.git /opt/llmswap/src/vllm-v0.22.0")
	assertContains(t, out, "uv pip install --python '/opt/llmswap/venvs/vllm/bin/python' -r requirements/cuda.llmswap-filtered.txt --torch-backend='cu128'")
	assertContains(t, out, "uv build --python '/opt/llmswap/venvs/vllm/bin/python' --wheel --no-build-isolation")
	assertContains(t, out, "uv pip install --python '/opt/llmswap/venvs/vllm/bin/python' --no-deps")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm-omni[minicpmo]==0.22.0 --torch-backend=cu128")
	assertContains(t, out, "WRITE /opt/llmswap/bin/vllm.server")
	assertContains(t, out, "--omni")
	assertNotContains(t, out, "/opt/llmswap/venvs/vllm-omni")
	assertNotContains(t, out, "WRITE /opt/llmswap/bin/vllm-omni.server")
	assertNotContains(t, out, "--editable .")
}

func TestInstallWorkerDryRunSupportsVLLMOmniRuntime(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "vllm-omni")

	assertContains(t, out, "INFO cuda_version=12.8 torch_backend=cu128 root=/opt/llmswap runtime=vllm-omni")
	assertContains(t, out, "uv venv /opt/llmswap/venvs/vllm --python 3.12 --managed-python --clear")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python torch==2.11.0 torchaudio==2.11.0 torchvision==0.26.0 --index-url https://download.pytorch.org/whl/cu128")
	assertContains(t, out, "git clone --depth 1 --branch v0.22.0 https://github.com/vllm-project/vllm.git /opt/llmswap/src/vllm-v0.22.0")
	assertContains(t, out, "uv pip install --python '/opt/llmswap/venvs/vllm/bin/python' -r requirements/cuda.llmswap-filtered.txt --torch-backend='cu128'")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm-omni[minicpmo]==0.22.0 --torch-backend=cu128")
	assertContains(t, out, "WRITE /opt/llmswap/bin/vllm.server")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm-omni --python")
	assertNotContains(t, out, "WRITE /opt/llmswap/bin/vllm-omni.server")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/sglang --python")
}

func TestInstallWorkerDryRunChecksSGLangResolvedCudaRuntime(t *testing.T) {
	out := runInstallWorker(t, "13.0", "--runtime", "sglang")

	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu130")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python --prerelease=allow sglang --torch-backend=cu130")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python librosa soundfile torchcodec av")
	assertContains(t, out, `kwargs.pop(\"hidden_size\", None)`)
	assertContains(t, out, `/opt/llmswap/venvs/sglang/bin/python - <<'PY'`)
	assertNotContains(t, out, "/opt/llmswap/venvs/sglang/bin/python -c from pathlib")
	assertContains(t, out, `/opt/llmswap/venvs/sglang/bin/python -c import torch, sglang; print('torch', torch.__version__); print('torch_cuda', torch.version.cuda); print('cuda_available', torch.cuda.is_available()); print('sglang', sglang.__version__)`)
}

func TestInstallWorkerDryRunDefaultsToPython312AndAllowsOverride(t *testing.T) {
	out := runInstallWorker(t, "12.8")
	assertContains(t, out, "uv venv /opt/llmswap/venvs/vllm --python 3.12 --managed-python --clear")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm-omni --python 3.12 --managed-python --clear")
	assertContains(t, out, "uv venv /opt/llmswap/venvs/sglang --python 3.12 --managed-python --clear")

	override := runInstallWorker(t, "12.8", "--python", "3.11")
	assertContains(t, override, "uv venv /opt/llmswap/venvs/vllm --python 3.11 --managed-python --clear")
	assertNotContains(t, override, "uv venv /opt/llmswap/venvs/vllm-omni --python 3.11 --managed-python --clear")
	assertContains(t, override, "uv venv /opt/llmswap/venvs/sglang --python 3.11 --managed-python --clear")
}

func TestInstallWorkerDryRunInstallsUvWithPipFallback(t *testing.T) {
	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "if [ -n \"${LLMSWAP_RUNTIME_HTTP_PROXY:-}\" ]; then export HTTP_PROXY=\"$LLMSWAP_RUNTIME_HTTP_PROXY\" HTTPS_PROXY=\"$LLMSWAP_RUNTIME_HTTP_PROXY\" http_proxy=\"$LLMSWAP_RUNTIME_HTTP_PROXY\" https_proxy=\"$LLMSWAP_RUNTIME_HTTP_PROXY\"; fi; timeout 120 sh -c 'curl -LsSf https://astral.sh/uv/install.sh | sh' || python3 -m pip install --upgrade uv")
}

func TestInstallWorkerDryRunScopesRuntimeProxyToUvInstaller(t *testing.T) {
	t.Setenv("LLMSWAP_RUNTIME_HTTP_PROXY", "http://proxy.example.invalid:6008")

	out := runInstallWorker(t, "12.8", "--only", "base")

	assertContains(t, out, "LLMSWAP_RUNTIME_HTTP_PROXY")
	assertNotContains(t, out, "http://proxy.example.invalid:6008")
}

func TestInstallWorkerDryRunConfiguresUvCacheUnderRoot(t *testing.T) {
	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "INFO uv_cache_dir=/opt/llmswap/cache/uv uv_python_install_dir=/opt/llmswap/python uv_link_mode=hardlink")
	assertContains(t, out, "RUN mkdir -p /opt/llmswap/bin /opt/llmswap/models /opt/llmswap/venvs /opt/llmswap/logs /opt/llmswap/cache/uv /opt/llmswap/python")
	assertNotContains(t, out, "/root/.cache/uv")
	assertNotContains(t, out, "/root/.local/share/uv/python")
}

func TestInstallWorkerDryRunAcceptsPythonInstallMirror(t *testing.T) {
	t.Setenv("LLMSWAP_UV_PYTHON_INSTALL_MIRROR", "https://python-standalone.org/mirror/astral-sh/python-build-standalone/")

	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "INFO uv_python_install_mirror=https://python-standalone.org/mirror/astral-sh/python-build-standalone/")
}

func TestInstallWorkerDryRunAcceptsTorchIndexOverride(t *testing.T) {
	t.Setenv("LLMSWAP_TORCH_INDEX_URL", "https://mirror.example.invalid/pytorch/cu128")

	out := runInstallWorker(t, "12.8", "--runtime", "vllm")

	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python torch==2.11.0 torchaudio==2.11.0 torchvision==0.26.0 --index-url https://mirror.example.invalid/pytorch/cu128")
	assertNotContains(t, out, "https://download.pytorch.org/whl/cu128")
}

func TestInstallWorkerCanSkipTailscaleAndSelectRuntime(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "vllm", "--skip-tailscale")

	assertContains(t, out, "uv venv /opt/llmswap/venvs/vllm --python 3.12 --managed-python --clear")
	assertContains(t, out, "WRITE /opt/llmswap/bin/vllm.server")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm-omni --python 3.12 --managed-python --clear")
	assertNotContains(t, out, "WRITE /opt/llmswap/bin/vllm-omni.server")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/sglang --python 3.12 --managed-python --clear")
	assertNotContains(t, out, "WRITE /opt/llmswap/bin/sglang.server")
	assertNotContains(t, out, "tailscale")
}

func TestInstallWorkerOnlyRuntimeInstallsSelectedRuntimeWithoutBaseAgentOrSupervisor(t *testing.T) {
	out := runInstallWorker(t, "13.0", "--only", "runtime", "--runtime", "sglang")

	assertContains(t, out, "INFO cuda_version=13.0 torch_backend=cu130 root=/opt/llmswap runtime=sglang only=runtime")
	assertContains(t, out, "RUN mkdir -p /opt/llmswap/bin /opt/llmswap/models /opt/llmswap/venvs /opt/llmswap/logs /opt/llmswap/cache/uv /opt/llmswap/python")
	assertContains(t, out, "uv venv /opt/llmswap/venvs/sglang --python 3.12 --managed-python --clear")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu130")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python --prerelease=allow sglang --torch-backend=cu130")
	assertContains(t, out, "WRITE /opt/llmswap/bin/sglang.server")
	assertNotContains(t, out, "apt-get update")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm")
	assertNotContains(t, out, "tailscale")
	assertNotContains(t, out, "WRITE /opt/llmswap/agent.yaml")
	assertNotContains(t, out, "go build -o /opt/llmswap/bin/llm-swap-agent")
	assertNotContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-agent.conf")
	assertNotContains(t, out, "supervisorctl")
}

func TestInstallWorkerOnlyAgentSkipsRuntimeAndSupervisor(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--only", "agent", "--agent-binary", "/tmp/llm-swap-agent")

	assertContains(t, out, "INFO cuda_version=12.8 torch_backend=cu128 root=/opt/llmswap runtime=all only=agent")
	assertContains(t, out, "RUN mkdir -p /opt/llmswap/bin /opt/llmswap/logs")
	assertContains(t, out, "WRITE /opt/llmswap/agent.yaml")
	assertContains(t, out, "install -m 0755 /tmp/llm-swap-agent /opt/llmswap/bin/llm-swap-agent")
	assertNotContains(t, out, "apt-get update")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm-omni")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/sglang")
	assertNotContains(t, out, "tailscale")
	assertNotContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-agent.conf")
}

func TestInstallWorkerOnlySupervisorSkipsRuntimeAndAgent(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--only", "supervisor")

	assertContains(t, out, "INFO cuda_version=12.8 torch_backend=cu128 root=/opt/llmswap runtime=all only=supervisor")
	assertContains(t, out, "RUN mkdir -p /opt/llmswap/bin /opt/llmswap/logs")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-llama-swap.conf")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-agent.conf")
	assertContains(t, out, "supervisorctl reread")
	assertNotContains(t, out, "apt-get update")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm-omni")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/sglang")
	assertNotContains(t, out, "WRITE /opt/llmswap/agent.yaml")
	assertNotContains(t, out, "go build -o /opt/llmswap/bin/llm-swap-agent")
	assertNotContains(t, out, "tailscale")
}

func TestInstallWorkerDryRunWritesAgentPrestartHook(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--only", "supervisor")

	assertContains(t, out, `prestart_script="${LLMSWAP_AGENT_PRESTART_SCRIPT:-/opt/llmswap/agent-prestart.sh}"`)
	assertContains(t, out, `source "$prestart_script"`)
	assertContains(t, out, `exec "$agent_bin" --config "$agent_config"`)
}

func TestInstallWorkerDryRunStartsTailscaleWhenAuthKeyProvidedAndConfiguresSupervisor(t *testing.T) {
	out := runInstallWorker(t, "12.8",
		"--runtime", "llamacpp",
		"--tailscale-authkey", "tskey-auth-test",
		"--tailscale-hostname", "worker-01",
	)

	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-tailscaled.conf")
	assertContains(t, out, "command=/usr/sbin/tailscaled --state=/opt/llmswap/tailscale/tailscaled.state --socket=/run/tailscale/tailscaled.sock --port=41641")
	assertContains(t, out, "stdout_logfile=/opt/llmswap/logs/tailscaled.out.log")
	assertContains(t, out, "WRITE /opt/llmswap/bin/tailscale-init.sh")
	assertContains(t, out, "tailscale --socket=\"/run/tailscale/tailscaled.sock\" login --auth-key \"tskey-auth-test\"")
	assertContains(t, out, "tailscale --socket=\"/run/tailscale/tailscaled.sock\" set --hostname \"worker-01\"")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-tailscale-init.conf")
	assertContains(t, out, "autorestart=false")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-llama-swap.conf")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-agent.conf")
	assertContains(t, out, "WRITE /opt/llmswap/bin/agent-supervisor.sh")
	assertContains(t, out, "wait_for_tailscale=\"1\"")
	assertContains(t, out, "tailscale_socket=\"/run/tailscale/tailscaled.sock\"")
	assertContains(t, out, "\"$tailscale_bin\" --socket=\"$tailscale_socket\" ip -4")
	assertContains(t, out, "command=/opt/llmswap/bin/agent-supervisor.sh")
	assertContains(t, out, "supervisorctl reread")
	assertContains(t, out, "supervisorctl update")
	assertNotContains(t, out, "tailscale up --auth-key")
}

func TestInstallWorkerDryRunDoesNotStartTailscaleWithoutAuthKey(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "llamacpp")

	assertContains(t, out, "INFO no tailscale auth key or hostname provided; not configuring supervisor-managed tailscale init.")
	assertNotContains(t, out, "tailscale up --auth-key")
	assertNotContains(t, out, "llmswap-tailscaled.conf")
}

func TestInstallWorkerDryRunInstallsLlamaCppRuntimeAndBinWrappers(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "llamacpp")

	assertContains(t, out, "INFO llamacpp_variant=cu128-sm89")
	assertContains(t, out, "curl -fL --retry 5 --retry-delay 5 -o /opt/llmswap/cache/runtimes/llamacpp-linux-cu128-sm89.tar.gz http://llmfs-bj.oss-cn-beijing.aliyuncs.com/models/llamacpp-linux-cu128-sm89.tar.gz")
	assertContains(t, out, "tar -C /opt/llmswap/runtimes/llamacpp/cu128-sm89 -xzf /opt/llmswap/cache/runtimes/llamacpp-linux-cu128-sm89.tar.gz")
	assertContains(t, out, "WRITE /opt/llmswap/bin/llamacpp.server")
	assertContains(t, out, "LLAMACPP_BIN=\"/opt/llmswap/runtimes/llamacpp/cu128-sm89/bin\"")
	assertContains(t, out, "for dir in \"$LLAMACPP_BIN\" \"$LLAMACPP_BIN/../lib\" /usr/local/cuda/lib64 /usr/local/cuda-*/lib64 /usr/lib/x86_64-linux-gnu; do")
	assertContains(t, out, "LLAMACPP_LIBS=\"${LLAMACPP_LIBS:+$LLAMACPP_LIBS:}$dir\"")
	assertContains(t, out, "LLMSWAP_LLAMACPP_EXTRA_LD_LIBRARY_PATH")
	assertContains(t, out, "exec \"$LLAMACPP_BIN/llama-server\" -m \"$MODEL_PATH\" \"$@\" \"${SERVER_ARGS[@]}\"")
	assertContains(t, out, "exec \"$LLAMACPP_BIN/llama-server\" \"$@\" \"${SERVER_ARGS[@]}\"")
	assertContains(t, out, "WRITE /opt/llmswap/bin/llama-server")
	assertContains(t, out, "exec \"$LLAMACPP_BIN/llama-server\" \"$@\"")
	assertContains(t, out, "test -x /opt/llmswap/bin/llamacpp.server")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm-omni")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/sglang")
}

func TestInstallWorkerDryRunDefaultsCuda13LlamaCppToCuda130AndAllowsOverride(t *testing.T) {
	out := runInstallWorker(t, "13.0", "--runtime", "llamacpp")
	assertContains(t, out, "INFO llamacpp_variant=cu130-sm89")

	override := runInstallWorker(t, "13.0", "--runtime", "llamacpp", "--llamacpp-cuda", "cu128")
	assertContains(t, override, "INFO llamacpp_variant=cu128-sm89")
}

func TestInstallWorkerDryRunWritesRuntimeServerWrappers(t *testing.T) {
	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "WRITE /opt/llmswap/bin/vllm.server")
	assertContains(t, out, "MODEL_PATH=\"${MODEL_PATH:-}\"")
	assertContains(t, out, "MODEL_PATH=\"$1\"")
	assertContains(t, out, "VLLM_MODE_ARGS=()")
	assertContains(t, out, "HAS_DEPLOY_CONFIG=0")
	assertContains(t, out, "VLLM_MODE_ARGS=(--omni)")
	assertContains(t, out, "exec /opt/llmswap/venvs/vllm/bin/vllm serve \"$MODEL_PATH\" \"${VLLM_MODE_ARGS[@]}\" --host \"$HOST\" --port \"$PORT\" \"$@\"")
	assertContains(t, out, "WRITE /opt/llmswap/bin/sglang.server")
	assertContains(t, out, "exec /opt/llmswap/venvs/sglang/bin/python -m sglang.launch_server --model-path \"$MODEL_PATH\" --host \"$HOST\" --port \"$PORT\"")
	assertContains(t, out, "WRITE /opt/llmswap/bin/sglang-python")
	assertContains(t, out, "WRITE /opt/llmswap/bin/vllm-python")
	assertNotContains(t, out, "WRITE /opt/llmswap/bin/vllm-omni-python")
	assertContains(t, out, "LLMSWAP_CUDA_LIBS=\"${LLMSWAP_CUDA_LIBS:+$LLMSWAP_CUDA_LIBS:}$dir\"")
}

func TestInstallWorkerDryRunInitializesAgentConfigAndBuildsAgent(t *testing.T) {
	out := runInstallWorker(t, "12.8",
		"--agent-id", "gpu-01",
		"--tags", "gpu-4090,gpu-a100",
		"--gateway-url", "http://gateway:8080",
		"--agent-token", "agent-token",
	)

	assertContains(t, out, "go build -ldflags")
	assertContains(t, out, "llm-swap/internal/buildinfo.Commit=")
	assertContains(t, out, "-o /opt/llmswap/bin/llm-swap-agent ./cmd/agent")
	assertContains(t, out, "WRITE /opt/llmswap/agent.yaml")
	assertContains(t, out, "id: gpu-01")
	assertContains(t, out, "tags: [gpu-4090, gpu-a100]")
	assertContains(t, out, "gateway_url: http://gateway:8080")
	assertContains(t, out, "restart_command: supervisorctl restart llmswap-llama-swap")
	assertContains(t, out, "token: agent-token")
	assertContains(t, out, "llama_swap_token: agent-token")
}

func TestInstallWorkerCanUseExistingAgentBinary(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--agent-binary", "/tmp/llm-swap-agent")

	assertContains(t, out, "install -m 0755 /tmp/llm-swap-agent /opt/llmswap/bin/llm-swap-agent")
	assertNotContains(t, out, "go build -o /opt/llmswap/bin/llm-swap-agent ./cmd/agent")
}

func TestInstallWorkerDoesNotOverwriteExistingAgentConfigUnlessForced(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--simulate-existing-agent-config")
	assertContains(t, out, "INFO /opt/llmswap/agent.yaml exists; keeping it")
	assertNotContains(t, out, "WRITE /opt/llmswap/agent.yaml")

	forced := runInstallWorker(t, "12.8", "--simulate-existing-agent-config", "--force-config")
	assertContains(t, forced, "WRITE /opt/llmswap/agent.yaml")
}

func runInstallWorker(t *testing.T, cuda string, args ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install-worker.sh dry-run tests require a POSIX shell")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "install-worker.sh")
	allArgs := append([]string{"--dry-run", "--cuda-version", cuda}, args...)
	cmd := exec.Command("bash", append([]string{script}, allArgs...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"PATH=/usr/bin:/bin",
		"LLMSWAP_ASSUME_YES=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install-worker.sh failed: %v\n%s", err, string(out))
	}
	return string(out)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}

func assertContains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("output missing %q:\n%s", want, text)
	}
}

func assertNotContains(t *testing.T, text, unwanted string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Fatalf("output contains %q:\n%s", unwanted, text)
	}
}
