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

	assertContains(t, out, "uv venv /opt/llmswap/venvs/vllm --python 3.12 --clear")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python torch torchvision torchaudio --index-url https://download.pytorch.org/whl/cu124")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/vllm/bin/python vllm --torch-backend=auto")
	assertContains(t, out, "uv venv /opt/llmswap/venvs/sglang --python 3.12 --clear")
	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python sglang")
	assertNotContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python torch torchvision torchaudio")
	assertContains(t, out, "/etc/supervisor/conf.d/llmswap-agent.conf")
	assertNotContains(t, out, "systemctl")
}

func TestInstallWorkerDryRunUsesSystemSupervisor(t *testing.T) {
	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "apt-get install -y ca-certificates curl gnupg python3 python3-venv python3-dev python3-pip supervisor git")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-agent.conf")
	assertContains(t, out, "sh -c pgrep -x supervisord >/dev/null || supervisord -c /etc/supervisor/supervisord.conf")
	assertContains(t, out, "supervisorctl reread")
	assertNotContains(t, out, "/opt/llmswap/venvs/base")
	assertNotContains(t, out, "pip install supervisor")
}

func TestInstallWorkerDryRunSelectsCuda128AndCuda130Indexes(t *testing.T) {
	cuda128 := runInstallWorker(t, "12.8")
	assertContains(t, cuda128, "https://download.pytorch.org/whl/cu128")
	assertContains(t, cuda128, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python sglang")
	assertNotContains(t, cuda128, "https://docs.sglang.ai/whl/cu128/")
	assertNotContains(t, cuda128, "sglang-kernel")

	cuda130 := runInstallWorker(t, "13.0")
	assertContains(t, cuda130, "https://download.pytorch.org/whl/cu130")
	assertContains(t, cuda130, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python sglang")
	assertNotContains(t, cuda130, "sglang[all]")
}

func TestInstallWorkerDryRunChecksSGLangResolvedCudaRuntime(t *testing.T) {
	out := runInstallWorker(t, "13.0", "--runtime", "sglang")

	assertContains(t, out, "uv pip install --python /opt/llmswap/venvs/sglang/bin/python sglang")
	assertContains(t, out, `/opt/llmswap/venvs/sglang/bin/python -c import torch, sglang; print('torch', torch.__version__); print('torch_cuda', torch.version.cuda); print('cuda_available', torch.cuda.is_available()); print('sglang', sglang.__version__)`)
}

func TestInstallWorkerDryRunDefaultsToPython312AndAllowsOverride(t *testing.T) {
	out := runInstallWorker(t, "12.8")
	assertContains(t, out, "uv venv /opt/llmswap/venvs/vllm --python 3.12 --clear")
	assertContains(t, out, "uv venv /opt/llmswap/venvs/sglang --python 3.12 --clear")

	override := runInstallWorker(t, "12.8", "--python", "3.11")
	assertContains(t, override, "uv venv /opt/llmswap/venvs/vllm --python 3.11 --clear")
	assertContains(t, override, "uv venv /opt/llmswap/venvs/sglang --python 3.11 --clear")
}

func TestInstallWorkerDryRunInstallsUvWithPipFallback(t *testing.T) {
	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "timeout 120 sh -c 'curl -LsSf https://astral.sh/uv/install.sh | sh' || python3 -m pip install --upgrade uv")
}

func TestInstallWorkerDryRunConfiguresUvCacheUnderRoot(t *testing.T) {
	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "INFO uv_cache_dir=/opt/llmswap/cache/uv uv_python_install_dir=/opt/llmswap/python uv_link_mode=copy")
	assertContains(t, out, "RUN mkdir -p /opt/llmswap/bin /opt/llmswap/models /opt/llmswap/venvs /opt/llmswap/logs /opt/llmswap/cache/uv /opt/llmswap/python")
	assertNotContains(t, out, "/root/.cache/uv")
	assertNotContains(t, out, "/root/.local/share/uv/python")
}

func TestInstallWorkerDryRunAcceptsPythonInstallMirror(t *testing.T) {
	t.Setenv("LLMSWAP_UV_PYTHON_INSTALL_MIRROR", "https://python-standalone.org/mirror/astral-sh/python-build-standalone/")

	out := runInstallWorker(t, "12.8")

	assertContains(t, out, "INFO uv_python_install_mirror=https://python-standalone.org/mirror/astral-sh/python-build-standalone/")
}

func TestInstallWorkerCanSkipTailscaleAndSelectRuntime(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "vllm", "--skip-tailscale")

	assertContains(t, out, "uv venv /opt/llmswap/venvs/vllm --python 3.12 --clear")
	assertContains(t, out, "WRITE /opt/llmswap/bin/vllm.server")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/sglang --python 3.12 --clear")
	assertNotContains(t, out, "WRITE /opt/llmswap/bin/sglang.server")
	assertNotContains(t, out, "tailscale")
}

func TestInstallWorkerDryRunStartsTailscaleWhenAuthKeyProvidedAndConfiguresSupervisor(t *testing.T) {
	out := runInstallWorker(t, "12.8",
		"--runtime", "llamacpp",
		"--tailscale-authkey", "tskey-auth-test",
		"--tailscale-hostname", "worker-01",
	)

	assertContains(t, out, "tailscale up --auth-key tskey-auth-test --hostname worker-01")
	assertContains(t, out, "WRITE /etc/supervisor/conf.d/llmswap-agent.conf")
	assertContains(t, out, "supervisorctl reread")
	assertContains(t, out, "supervisorctl update")
}

func TestInstallWorkerDryRunDoesNotStartTailscaleWithoutAuthKey(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "llamacpp")

	assertContains(t, out, "INFO TAILSCALE_AUTHKEY not set; not running tailscale up.")
	assertNotContains(t, out, "tailscale up --auth-key")
}

func TestInstallWorkerDryRunInstallsLlamaCppRuntimeAndBinWrappers(t *testing.T) {
	out := runInstallWorker(t, "12.8", "--runtime", "llamacpp")

	assertContains(t, out, "INFO llamacpp_variant=cu128-sm89")
	assertContains(t, out, "curl -fL --retry 5 --retry-delay 5 -o /opt/llmswap/cache/runtimes/llamacpp-linux-cu128-sm89.tar.gz http://llmfs-bj.oss-cn-beijing.aliyuncs.com/models/llamacpp-linux-cu128-sm89.tar.gz")
	assertContains(t, out, "tar -C /opt/llmswap/runtimes/llamacpp/cu128-sm89 -xzf /opt/llmswap/cache/runtimes/llamacpp-linux-cu128-sm89.tar.gz")
	assertContains(t, out, "WRITE /opt/llmswap/bin/llamacpp.server")
	assertContains(t, out, "LLAMACPP_BIN=\"/opt/llmswap/runtimes/llamacpp/cu128-sm89/bin\"")
	assertContains(t, out, "exec \"$LLAMACPP_BIN/llama-server\" -m \"$MODEL_PATH\" --host \"$HOST\" --port \"$PORT\" \"$@\"")
	assertContains(t, out, "exec \"$LLAMACPP_BIN/llama-server\" \"$@\"")
	assertContains(t, out, "WRITE /opt/llmswap/bin/llama-server")
	assertContains(t, out, "exec \"$LLAMACPP_BIN/llama-server\" \"$@\"")
	assertContains(t, out, "/opt/llmswap/bin/llamacpp.server --version")
	assertNotContains(t, out, "uv venv /opt/llmswap/venvs/vllm")
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
	assertContains(t, out, "exec /opt/llmswap/venvs/vllm/bin/vllm serve \"$MODEL_PATH\" --host \"$HOST\" --port \"$PORT\"")
	assertContains(t, out, "WRITE /opt/llmswap/bin/sglang.server")
	assertContains(t, out, "exec /opt/llmswap/venvs/sglang/bin/python -m sglang.launch_server --model-path \"$MODEL_PATH\" --host \"$HOST\" --port \"$PORT\"")
	assertContains(t, out, "ln -sfn /opt/llmswap/venvs/sglang/bin/python /opt/llmswap/bin/sglang-python")
	assertContains(t, out, "ln -sfn /opt/llmswap/venvs/vllm/bin/python /opt/llmswap/bin/vllm-python")
}

func TestInstallWorkerDryRunInitializesAgentConfigAndBuildsAgent(t *testing.T) {
	out := runInstallWorker(t, "12.8",
		"--agent-id", "gpu-01",
		"--tags", "gpu-4090,gpu-a100",
		"--gateway-url", "http://gateway:8080",
		"--agent-token", "agent-token",
		"--llama-swap-token", "worker-token",
	)

	assertContains(t, out, "go build -o /opt/llmswap/bin/llm-swap-agent ./cmd/agent")
	assertContains(t, out, "WRITE /opt/llmswap/agent.yaml")
	assertContains(t, out, "id: gpu-01")
	assertContains(t, out, "tags: [gpu-4090, gpu-a100]")
	assertContains(t, out, "gateway_url: http://gateway:8080")
	assertContains(t, out, "token: agent-token")
	assertContains(t, out, "llama_swap_token: worker-token")
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
	cmd.Env = append(os.Environ(), "LLMSWAP_ASSUME_YES=1")
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
