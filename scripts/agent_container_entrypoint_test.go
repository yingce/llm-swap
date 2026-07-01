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
	writeExecutable(t, filepath.Join(binDir, "tailscaled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "tailscale"), "#!/bin/sh\nexit 0\n")

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

func TestAgentContainerEntrypointRejectsLegacyAgentEnvWithoutConfigFile(t *testing.T) {
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

	out, err := runAgentEntrypointCommandResult(t, root, map[string]string{
		"PATH":                       binDir + ":/usr/bin:/bin",
		"LLM_SWAP_AGENT_ID":          "native-worker-01",
		"LLM_SWAP_AGENT_TAGS":        "gpu-4090,prod",
		"LLM_SWAP_AGENT_GATEWAY_URL": "https://gateway.example.invalid",
		"LLM_SWAP_AGENT_TOKEN":       "agent-token",
		"LLM_SWAP_AGENT_SWAP_URL":    "https://worker.example.invalid:8443",
	})
	if err == nil {
		t.Fatalf("entrypoint succeeded with legacy env aliases, output:\n%s", out)
	}
	if !strings.Contains(out, "missing required env LLMSWAP_GATEWAY_URL") {
		t.Fatalf("entrypoint output = %q, want missing LLMSWAP_GATEWAY_URL", out)
	}
}

func TestAgentContainerEntrypointDoesNotExportLegacyAgentDefaults(t *testing.T) {
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

	out := runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                 binDir + ":/usr/bin:/bin",
		"LLMSWAP_AGENT_ID":     "worker-01",
		"LLMSWAP_AGENT_TAGS":   "gpu-4090,prod",
		"LLMSWAP_GATEWAY_URL":  "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN":  "agent-token",
		"LLMSWAP_FORCE_CONFIG": "1",
		"LLMSWAP_SWAP_PORT":    "6006",
	}, "env")
	if strings.Contains(out, "LLM_SWAP_AGENT_") {
		t.Fatalf("env output contains legacy agent env:\n%s", out)
	}
	if !strings.Contains(out, "LLMSWAP_GATEWAY_URL=https://gateway.example.invalid") {
		t.Fatalf("env output missing standard gateway env:\n%s", out)
	}
}

func TestAgentContainerEntrypointStartsTailscaleAtRuntimeWhenRequested(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	confDir := filepath.Join(root, "supervisor", "conf.d")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\necho agent\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\necho bundled\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	writeExecutable(t, filepath.Join(binDir, "tailscaled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "tailscale"), "#!/bin/sh\nexit 0\n")
	out := runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                        binDir + ":/usr/bin:/bin",
		"LLMSWAP_GATEWAY_URL":         "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN":         "agent-token",
		"LLMSWAP_ENABLE_TAILSCALE":    "1",
		"LLMSWAP_TAILSCALE_AUTHKEY":   "tskey-test",
		"LLMSWAP_TAILSCALE_HOSTNAME":  "worker-ts",
		"LLMSWAP_TAILSCALE_SOCKET":    filepath.Join(root, "run", "tailscaled.sock"),
		"LLMSWAP_SUPERVISOR_CONF_DIR": confDir,
		"LLMSWAP_SUPERVISORD_CONFIG":  filepath.Join(root, "supervisor", "supervisord.conf"),
	})
	if strings.TrimSpace(out) != "supervisord-started" {
		t.Fatalf("entrypoint output = %q, want supervisord-started", out)
	}

	tailscaledConf, err := os.ReadFile(filepath.Join(confDir, "llmswap-tailscaled.conf"))
	if err != nil {
		t.Fatal(err)
	}
	tailscaledText := string(tailscaledConf)
	for _, want := range []string{
		"[program:llmswap-tailscaled]",
		"command=" + filepath.Join(binDir, "tailscaled") + " --state=",
		"--tun=userspace-networking",
		"--socket=" + filepath.Join(root, "run", "tailscaled.sock"),
		"autostart=true",
	} {
		if !strings.Contains(tailscaledText, want) {
			t.Fatalf("tailscaled conf missing %q:\n%s", want, tailscaledText)
		}
	}

	initConf, err := os.ReadFile(filepath.Join(confDir, "llmswap-tailscale-init.conf"))
	if err != nil {
		t.Fatal(err)
	}
	initConfText := string(initConf)
	for _, want := range []string{
		"[program:llmswap-tailscale-init]",
		"autorestart=false",
		"startretries=0",
	} {
		if !strings.Contains(initConfText, want) {
			t.Fatalf("tailscale init conf missing %q:\n%s", want, initConfText)
		}
	}

	initScript, err := os.ReadFile(filepath.Join(binDir, "tailscale-init.sh"))
	if err != nil {
		t.Fatal(err)
	}
	initScriptText := string(initScript)
	for _, want := range []string{
		filepath.Join(binDir, "tailscale") + "\" --socket=\"" + filepath.Join(root, "run", "tailscaled.sock") + "\" login --auth-key \"tskey-test\"",
		filepath.Join(binDir, "tailscale") + "\" --socket=\"" + filepath.Join(root, "run", "tailscaled.sock") + "\" set --hostname \"worker-ts\"",
	} {
		if !strings.Contains(initScriptText, want) {
			t.Fatalf("tailscale init script missing %q:\n%s", want, initScriptText)
		}
	}

	agentConf, err := os.ReadFile(filepath.Join(confDir, "llmswap-agent.conf"))
	if err != nil {
		t.Fatal(err)
	}
	agentConfText := string(agentConf)
	assertContains(t, agentConfText, "command="+filepath.Join(binDir, "agent-supervisor.sh"))

	agentWrapper, err := os.ReadFile(filepath.Join(binDir, "agent-supervisor.sh"))
	if err != nil {
		t.Fatal(err)
	}
	agentWrapperText := string(agentWrapper)
	for _, want := range []string{
		"wait_for_tailscale=\"1\"",
		"tailscale_bin=\"" + filepath.Join(binDir, "tailscale") + "\"",
		"tailscale_socket=\"" + filepath.Join(root, "run", "tailscaled.sock") + "\"",
		"\"$tailscale_bin\" --socket=\"$tailscale_socket\" ip -4",
		"agent_bin=\"" + filepath.Join(binDir, "llm-swap-agent") + "\"",
		"agent_config=\"" + filepath.Join(root, "agent.yaml") + "\"",
		"exec \"$agent_bin\" --config \"$agent_config\"",
	} {
		if !strings.Contains(agentWrapperText, want) {
			t.Fatalf("agent supervisor wrapper missing %q:\n%s", want, agentWrapperText)
		}
	}
}

func TestAgentContainerEntrypointDoesNotWaitForTailscaleWhenSwapURLExplicit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	confDir := filepath.Join(root, "supervisor", "conf.d")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\necho agent\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\necho bundled\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	writeExecutable(t, filepath.Join(binDir, "tailscaled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "tailscale"), "#!/bin/sh\nexit 0\n")

	runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                        binDir + ":/usr/bin:/bin",
		"LLMSWAP_GATEWAY_URL":         "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN":         "agent-token",
		"LLMSWAP_SWAP_URL":            "https://worker.example.invalid:8443",
		"LLMSWAP_ENABLE_TAILSCALE":    "1",
		"LLMSWAP_TAILSCALE_AUTHKEY":   "tskey-test",
		"LLMSWAP_SUPERVISOR_CONF_DIR": confDir,
	})

	agentWrapper, err := os.ReadFile(filepath.Join(binDir, "agent-supervisor.sh"))
	if err != nil {
		t.Fatal(err)
	}
	agentWrapperText := string(agentWrapper)
	assertContains(t, agentWrapperText, "wait_for_tailscale=\"0\"")
}

func runAgentEntrypoint(t *testing.T, root string, extraEnv map[string]string) string {
	t.Helper()
	return runAgentEntrypointCommand(t, root, extraEnv, "cat", filepath.Join(root, "bin", "llama-swap"))
}

func runAgentEntrypointCommand(t *testing.T, root string, extraEnv map[string]string, args ...string) string {
	t.Helper()
	out, err := runAgentEntrypointCommandResult(t, root, extraEnv, args...)
	if err != nil {
		t.Fatalf("agent-container-entrypoint.sh failed: %v\n%s", err, out)
	}
	return out
}

func runAgentEntrypointCommandResult(t *testing.T, root string, extraEnv map[string]string, args ...string) (string, error) {
	t.Helper()
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "agent-container-entrypoint.sh")
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Dir = repo

	envMap := map[string]string{
		"HOME":                        t.TempDir(),
		"PATH":                        "/usr/bin:/bin",
		"LLMSWAP_ROOT":                root,
		"LLMSWAP_BIN_DIR":             filepath.Join(root, "bin"),
		"LLMSWAP_AGENT_CONFIG":        filepath.Join(root, "agent.yaml"),
		"LLMSWAP_LOG_DIR":             filepath.Join(root, "logs"),
		"LLMSWAP_MODEL_ROOT":          filepath.Join(root, "models"),
		"LLMSWAP_SUPERVISOR_CONF_DIR": filepath.Join(root, "supervisor", "conf.d"),
		"LLMSWAP_SUPERVISORD_CONFIG":  filepath.Join(root, "supervisor", "supervisord.conf"),
	}
	for key, value := range extraEnv {
		envMap[key] = value
	}

	cmd.Env = append(os.Environ(), flattenEnv(envMap)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
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
