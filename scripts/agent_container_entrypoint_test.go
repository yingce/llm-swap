package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAgentContainerEntrypointUsesBundledLlamaSwapWhenNoOverrideProvided(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\necho agent\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\necho bundled\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap"), "#!/bin/sh\necho stale\n")
	if err := os.WriteFile(filepath.Join(root, "agent.yaml"), []byte("agent:\n  id: existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runAgentEntrypoint(t, root, nil)
	if strings.TrimSpace(out) != "#!/bin/sh\necho bundled" {
		t.Fatalf("llama-swap content = %q, want bundled binary", out)
	}
}

func TestAgentContainerEntrypointOverridesLlamaSwapWhenRuntimeURLProvided(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	fakeBinDir := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\necho agent\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\necho bundled\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap"), "#!/bin/sh\necho stale\n")
	writeExecutable(t, filepath.Join(fakeBinDir, "curl"), `#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    out="$2"
    shift 2
    continue
  fi
  shift
done
printf '%s\n' "$FAKE_CURL_CONTENT" > "$out"
`)
	if err := os.WriteFile(filepath.Join(root, "agent.yaml"), []byte("agent:\n  id: existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runAgentEntrypoint(t, root, map[string]string{
		"PATH":                            fakeBinDir + ":/usr/bin:/bin",
		"LLMSWAP_LLAMA_SWAP_DOWNLOAD_URL": "https://example.invalid/llama-swap",
		"FAKE_CURL_CONTENT":               "#!/bin/sh\necho override",
	})
	if strings.TrimSpace(out) != "#!/bin/sh\necho override" {
		t.Fatalf("llama-swap content = %q, want runtime override binary", out)
	}
}

func TestAgentContainerEntrypointKeepsMountedLlamaSwapWhenBundledMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\necho agent\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap"), "#!/bin/sh\necho mounted\n")
	if err := os.WriteFile(filepath.Join(root, "agent.yaml"), []byte("agent:\n  id: existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := runAgentEntrypoint(t, root, nil)
	if strings.TrimSpace(out) != "#!/bin/sh\necho mounted" {
		t.Fatalf("llama-swap content = %q, want mounted binary to remain active", out)
	}
}

func TestAgentContainerEntrypointAllowsInteractiveShellWithoutBootstrap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}

	root := t.TempDir()
	out := runAgentEntrypointCommand(t, root, nil, "bash", "-lc", "printf shell-ok")
	if strings.TrimSpace(out) != "shell-ok" {
		t.Fatalf("shell output = %q, want shell-ok", out)
	}
}

func TestAgentContainerEntrypointBootstrapsConfigFromRuntimeEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\necho agent\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\necho bundled\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")

	out := runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                     binDir + ":/usr/bin:/bin",
		"LLMSWAP_AGENT_ID":         "worker-runtime-01",
		"LLMSWAP_AGENT_TAGS":       "gpu-4090,prod",
		"LLMSWAP_GATEWAY_URL":      "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN":      "agent-token",
		"LLMSWAP_LLAMA_SWAP_TOKEN": "llama-token",
		"LLMSWAP_SWAP_URL":         "https://worker.example.invalid:8443",
	})
	if strings.TrimSpace(out) != "supervisord-started" {
		t.Fatalf("entrypoint output = %q, want supervisord-started", out)
	}

	config, err := os.ReadFile(filepath.Join(root, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(config)
	for _, want := range []string{
		"id: worker-runtime-01",
		"- gpu-4090",
		"- prod",
		"swap_url: https://worker.example.invalid:8443",
		"gateway_url: https://gateway.example.invalid",
		"token: agent-token",
		"llama_swap_token: llama-token",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent config missing %q:\n%s", want, text)
		}
	}
}

func TestAgentContainerEntrypointStartsTailscaleAtRuntimeWhenRequested(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	runDir := filepath.Join(root, "run")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\necho agent\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\necho bundled\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	writeExecutable(t, filepath.Join(binDir, "tailscaled"), `#!/bin/sh
set -eu
socket=""
log_file="${FAKE_TAILSCALE_LOG:?}"
for arg in "$@"; do
  case "$arg" in
    --socket=*)
      socket="${arg#--socket=}"
      ;;
  esac
done
printf 'tailscaled %s\n' "$*" >> "$log_file"
if [ -z "$socket" ]; then
  exit 1
fi
SOCKET_PATH="$socket" python3 - <<'PY'
import os
import socket
import time

path = os.environ["SOCKET_PATH"]
os.makedirs(os.path.dirname(path), exist_ok=True)
try:
    os.unlink(path)
except FileNotFoundError:
    pass
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.bind(path)
time.sleep(2)
PY
`)
	writeExecutable(t, filepath.Join(binDir, "tailscale"), `#!/bin/sh
set -eu
printf 'tailscale %s\n' "$*" >> "${FAKE_TAILSCALE_LOG:?}"
`)

	logPath := filepath.Join(root, "tailscale.log")
	out := runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                      binDir + ":/usr/bin:/bin",
		"LLMSWAP_GATEWAY_URL":       "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN":       "agent-token",
		"LLMSWAP_ENABLE_TAILSCALE":  "1",
		"LLMSWAP_TAILSCALE_AUTHKEY": "tskey-test",
		"LLMSWAP_TAILSCALE_HOSTNAME": "worker-ts",
		"FAKE_TAILSCALE_LOG":        logPath,
		"LLMSWAP_TAILSCALE_SOCKET":  filepath.Join(runDir, "tailscaled.sock"),
	})
	if strings.TrimSpace(out) != "supervisord-started" {
		t.Fatalf("entrypoint output = %q, want supervisord-started", out)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	for _, want := range []string{
		"tailscaled --state=",
		"--socket=" + filepath.Join(runDir, "tailscaled.sock"),
		"tailscale --socket=" + filepath.Join(runDir, "tailscaled.sock") + " up --auth-key tskey-test --hostname worker-ts",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("tailscale log missing %q:\n%s", want, logText)
		}
	}
}

func runAgentEntrypoint(t *testing.T, root string, extraEnv map[string]string) string {
	t.Helper()
	return runAgentEntrypointCommand(t, root, extraEnv, "cat", filepath.Join(root, "bin", "llama-swap"))
}

func runAgentEntrypointCommand(t *testing.T, root string, extraEnv map[string]string, args ...string) string {
	t.Helper()
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "agent-container-entrypoint.sh")
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = repo

	envMap := map[string]string{
		"HOME":                 t.TempDir(),
		"PATH":                 "/usr/bin:/bin",
		"LLMSWAP_ROOT":         root,
		"LLMSWAP_BIN_DIR":      filepath.Join(root, "bin"),
		"LLMSWAP_AGENT_CONFIG": filepath.Join(root, "agent.yaml"),
		"LLMSWAP_LOG_DIR":      filepath.Join(root, "logs"),
		"LLMSWAP_MODEL_ROOT":   filepath.Join(root, "models"),
	}
	for key, value := range extraEnv {
		envMap[key] = value
	}

	cmd.Env = append(os.Environ(), flattenEnv(envMap)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent-container-entrypoint.sh failed: %v\n%s", err, string(out))
	}
	return string(out)
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func flattenEnv(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}
