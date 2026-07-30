package scripts_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"llm-swap/internal/agent"
	"llm-swap/internal/config"
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

func TestAgentContainerEntrypointBootstrapsGatewayConfigInOneRenameAndRestartsFromYAMLMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	fakeBinDir := filepath.Join(root, "fake-bin")
	confDir := filepath.Join(root, "supervisor", "conf.d")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\necho agent\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\necho bundled\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	mvLog := filepath.Join(root, "mv-calls")
	writeExecutable(t, filepath.Join(fakeBinDir, "mv"), `#!/bin/sh
set -eu
printf 'call\n' >> "$FAKE_MV_LOG"
exec /usr/bin/mv "$@"
`)
	tokenPath := filepath.Join(root, "agent-token")
	if err := os.WriteFile(tokenPath, []byte("file-agent-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                     fakeBinDir + ":" + binDir + ":/usr/bin:/bin",
		"LLMSWAP_GATEWAY_URL":      "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN_FILE": tokenPath,
		"LLMSWAP_AGENT_TAGS":       "gpu-4090",
		"FAKE_MV_LOG":              mvLog,
	})
	if strings.Contains(out, "file-agent-token") {
		t.Fatalf("entrypoint output leaked token: %q", out)
	}
	configPath := filepath.Join(root, "agent.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "# llmswap-bootstrap: gateway\nagent:\n") {
		t.Fatalf("gateway config missing authoritative YAML marker:\n%s", text)
	}
	if _, err := config.LoadAgentRuntime(context.Background(), config.AgentRuntimeOptions{ConfigPath: configPath, Root: root}); err != nil {
		t.Fatalf("generated gateway config does not load through the agent runtime schema: %v", err)
	}
	for _, want := range []string{"gateway_url: https://gateway.example.invalid", "token: 'file-agent-token'", "- gpu-4090", "swap_port: 6006"} {
		if !strings.Contains(text, want) {
			t.Fatalf("FRP config missing %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"  id:", "swap_url:", "llama_swap_url:", "llama_swap_token:", "frp"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("FRP config contains %q:\n%s", forbidden, text)
		}
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("agent config mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(root, ".agent-bootstrap-mode")); !os.IsNotExist(err) {
		t.Fatalf("gateway bootstrap created obsolete sidecar marker: %v", err)
	}
	calls, err := os.ReadFile(mvLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(calls) != "call\n" {
		t.Fatalf("gateway config rename calls = %q, want exactly one", calls)
	}
	for _, path := range []string{
		filepath.Join(confDir, "llmswap-tailscaled.conf"),
		filepath.Join(confDir, "llmswap-tailscale-init.conf"),
		filepath.Join(binDir, "agent-supervisor.sh"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("FRP bootstrap created legacy Tailscale artifact %s: %v", path, err)
		}
	}
	agentConf, err := os.ReadFile(filepath.Join(confDir, "llmswap-agent.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agentConf), "command="+filepath.Join(binDir, "llm-swap-agent")+" --config "+configPath) {
		t.Fatalf("FRP agent supervisor config = %s", agentConf)
	}

	runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":        fakeBinDir + ":" + binDir + ":/usr/bin:/bin",
		"FAKE_MV_LOG": mvLog,
	})
	if _, err := os.Stat(filepath.Join(binDir, "agent-supervisor.sh")); !os.IsNotExist(err) {
		t.Fatalf("gateway YAML marker restart created legacy wrapper: %v", err)
	}
	calls, err = os.ReadFile(mvLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(calls) != "call\n" {
		t.Fatalf("restart rewrote committed gateway config: %q", calls)
	}
}

func TestAgentContainerEntrypointWritesConfigAtomicallyAndKeepsOldTargetOnFailure(t *testing.T) {
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
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\nexit 0\n")
	modeLog := filepath.Join(root, "mv-mode")
	writeExecutable(t, filepath.Join(fakeBinDir, "mv"), `#!/bin/sh
set -eu
/usr/bin/stat -c '%a' "$1" > "$FAKE_MV_MODE_LOG"
exit 1
`)
	configPath := filepath.Join(root, "agent.yaml")
	oldConfig := "# llmswap-bootstrap: legacy\nagent:\n  id: old-config\n"
	if err := os.WriteFile(configPath, []byte(oldConfig), 0o640); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(root, "agent-token")
	if err := os.WriteFile(tokenPath, []byte("replacement-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runAgentEntrypointCommandResult(t, root, map[string]string{
		"PATH":                     fakeBinDir + ":" + binDir + ":/usr/bin:/bin",
		"LLMSWAP_FORCE_CONFIG":     "1",
		"LLMSWAP_GATEWAY_URL":      "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN_FILE": tokenPath,
		"FAKE_MV_MODE_LOG":         modeLog,
	})
	if err == nil {
		t.Fatalf("entrypoint succeeded despite atomic rename failure: %s", out)
	}
	data, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != oldConfig {
		t.Fatalf("failed config replacement changed target:\n%s", data)
	}
	info, statErr := os.Stat(configPath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("failed config replacement changed target mode to %o", info.Mode().Perm())
	}
	mode, readErr := os.ReadFile(modeLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.TrimSpace(string(mode)) != "600" {
		t.Fatalf("temporary config mode = %q, want 600 before rename", mode)
	}
}

func TestAgentContainerEntrypointExistingLegacyConfigIgnoresExtraTokenMount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	confDir := filepath.Join(root, "supervisor", "conf.d")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	writeExecutable(t, filepath.Join(binDir, "tailscaled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "tailscale"), "#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(root, "agent.yaml"), []byte("agent:\n  id: legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".agent-bootstrap-mode"), []byte("frp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(root, "extra-token")
	if err := os.WriteFile(tokenPath, []byte("unused-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                        binDir + ":/usr/bin:/bin",
		"LLMSWAP_AGENT_TOKEN_FILE":    tokenPath,
		"LLMSWAP_ENABLE_TAILSCALE":    "1",
		"LLMSWAP_SUPERVISOR_CONF_DIR": confDir,
	})
	for _, path := range []string{
		filepath.Join(binDir, "agent-supervisor.sh"),
		filepath.Join(confDir, "llmswap-tailscaled.conf"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("legacy config with extra secret did not preserve legacy startup %s: %v", path, err)
		}
	}
}

func TestAgentContainerEntrypointForceGatewayAndLegacyCommitModeInYAML(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	configPath := filepath.Join(root, "agent.yaml")
	if err := os.WriteFile(configPath, []byte("# llmswap-bootstrap: legacy\nagent:\n  id: legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(root, "agent-token")
	if err := os.WriteFile(tokenPath, []byte("gateway-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                     binDir + ":/usr/bin:/bin",
		"LLMSWAP_FORCE_CONFIG":     "1",
		"LLMSWAP_GATEWAY_URL":      "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN_FILE": tokenPath,
	})
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# llmswap-bootstrap: gateway\nagent:\n") {
		t.Fatalf("force gateway did not commit gateway mode in YAML:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(binDir, "agent-supervisor.sh")); !os.IsNotExist(err) {
		t.Fatalf("force gateway created legacy wrapper: %v", err)
	}

	runAgentEntrypointCommand(t, root, map[string]string{"PATH": binDir + ":/usr/bin:/bin"})
	if _, err := os.Stat(filepath.Join(binDir, "agent-supervisor.sh")); !os.IsNotExist(err) {
		t.Fatalf("gateway YAML marker restart created legacy wrapper: %v", err)
	}

	runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                 binDir + ":/usr/bin:/bin",
		"LLMSWAP_FORCE_CONFIG": "1",
		"LLMSWAP_GATEWAY_URL":  "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN":  "legacy-token",
	})
	data, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# llmswap-bootstrap: legacy\nagent:\n") {
		t.Fatalf("force legacy did not commit legacy mode in YAML:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(binDir, "agent-supervisor.sh")); err != nil {
		t.Fatalf("force legacy did not restore legacy wrapper: %v", err)
	}
}

func TestAgentContainerEntrypointAcceptsCRLFTerminatedFRPTokenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	tokenPath := filepath.Join(root, "agent-token")
	if err := os.WriteFile(tokenPath, []byte("crlf-token\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out := runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                     binDir + ":/usr/bin:/bin",
		"LLMSWAP_GATEWAY_URL":      "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN_FILE": tokenPath,
		"LLMSWAP_AGENT_TAGS":       "gpu-4090",
	})
	if strings.Contains(out, "crlf-token") {
		t.Fatalf("entrypoint output leaked token: %q", out)
	}
	data, err := os.ReadFile(filepath.Join(root, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "token: 'crlf-token'") {
		t.Fatalf("CRLF token was not normalized:\n%s", data)
	}
}

func TestAgentContainerEntrypointRejectsAmbiguousOrInvalidFRPTokenFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}
	tests := []struct {
		name       string
		file       string
		envToken   string
		wantOutput string
	}{
		{name: "ambiguous", file: "file-token\n", envToken: "env-token", wantOutput: "ambiguous agent token input"},
		{name: "empty", file: " \n", wantOutput: "invalid agent token file"},
		{name: "multiline", file: "first\nsecond\n", wantOutput: "invalid agent token file"},
		{name: "bare_carriage_return", file: "first\r", wantOutput: "invalid agent token file"},
		{name: "internal_carriage_return", file: "fir\rst\n", wantOutput: "invalid agent token file"},
		{name: "multiple_crlf_lines", file: "first\r\n\r\n", wantOutput: "invalid agent token file"},
		{name: "nul", file: "first\x00second", wantOutput: "invalid agent token file"},
		{name: "trailing_nul", file: "first\x00", wantOutput: "invalid agent token file"},
		{name: "oversized", file: strings.Repeat("x", 16385), wantOutput: "invalid agent token file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0o755); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\nexit 0\n")
			writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\nexit 0\n")
			tokenPath := filepath.Join(root, "agent-token")
			if err := os.WriteFile(tokenPath, []byte(tt.file), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := runAgentEntrypointCommandResult(t, root, map[string]string{
				"PATH":                     binDir + ":/usr/bin:/bin",
				"LLMSWAP_GATEWAY_URL":      "https://gateway.example.invalid",
				"LLMSWAP_AGENT_TOKEN_FILE": tokenPath,
				"LLMSWAP_AGENT_TOKEN":      tt.envToken,
			})
			if err == nil || !strings.Contains(out, tt.wantOutput) {
				t.Fatalf("result err=%v output=%q, want %q", err, out, tt.wantOutput)
			}
			for _, secret := range []string{"file-token", "env-token", "first", "second"} {
				if strings.Contains(out, secret) {
					t.Fatalf("error output leaked token material: %q", out)
				}
			}
		})
	}
}

func TestAgentContainerEntrypointDoesNotReadTokenFileForExistingConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agent-container-entrypoint.sh tests require a POSIX shell")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\necho bundled\n")
	if err := os.WriteFile(filepath.Join(root, "agent.yaml"), []byte("agent:\n  id: existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runAgentEntrypoint(t, root, map[string]string{
		"LLMSWAP_AGENT_TOKEN_FILE": filepath.Join(root, "does-not-exist"),
	})
	if strings.TrimSpace(out) != "#!/bin/sh\necho bundled" {
		t.Fatalf("entrypoint output = %q, want existing config startup", out)
	}
}

func TestAgentContainerEntrypointReadsFRPTokenFileContentOnce(t *testing.T) {
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
	writeExecutable(t, filepath.Join(binDir, "llm-swap-agent"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "llama-swap.bundled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	readerPath := filepath.Join(fakeBinDir, "token-reader")
	writeExecutable(t, readerPath, `#!/bin/sh
set -eu
printf 'call\n' >> "$FAKE_READER_LOG"
printf '736166652d746f6b656e\n'
printf 'malicious\nsecond-line\n' > "$2"
`)
	tokenPath := filepath.Join(root, "agent-token")
	if err := os.WriteFile(tokenPath, []byte("safe-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "reader-calls")

	out := runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                       fakeBinDir + ":" + binDir + ":/usr/bin:/bin",
		"LLMSWAP_GATEWAY_URL":        "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN_FILE":   tokenPath,
		"LLMSWAP_AGENT_TOKEN_READER": readerPath,
		"LLMSWAP_AGENT_TAGS":         "gpu-4090",
		"FAKE_READER_LOG":            logPath,
	})
	if strings.Contains(out, "safe-token") || strings.Contains(out, "malicious") {
		t.Fatalf("entrypoint output leaked token material: %q", out)
	}
	data, err := os.ReadFile(filepath.Join(root, "agent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "token: 'safe-token'") || strings.Contains(text, "malicious") {
		t.Fatalf("config did not use the single safe snapshot:\n%s", text)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(calls) != "call\n" {
		t.Fatalf("token reader calls = %q, want exactly one", calls)
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

func TestAgentContainerEntrypointWritesAgentPrestartHook(t *testing.T) {
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

	runAgentEntrypointCommand(t, root, map[string]string{
		"PATH":                        binDir + ":/usr/bin:/bin",
		"LLMSWAP_GATEWAY_URL":         "https://gateway.example.invalid",
		"LLMSWAP_AGENT_TOKEN":         "agent-token",
		"LLMSWAP_SUPERVISOR_CONF_DIR": confDir,
	})

	agentWrapper, err := os.ReadFile(filepath.Join(binDir, "agent-supervisor.sh"))
	if err != nil {
		t.Fatal(err)
	}
	agentWrapperText := string(agentWrapper)
	assertContains(t, agentWrapperText, `prestart_script="${LLMSWAP_AGENT_PRESTART_SCRIPT:-`+filepath.Join(root, "agent-prestart.sh")+`}"`)
	assertContains(t, agentWrapperText, `source "$prestart_script"`)
	assertContains(t, agentWrapperText, `exec "$agent_bin" --config "$agent_config"`)
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
	if envMap["LLMSWAP_AGENT_TOKEN_FILE"] != "" && envMap["LLMSWAP_AGENT_TOKEN_READER"] == "" {
		testBinary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		readerPath := filepath.Join(t.TempDir(), "token-reader")
		writeExecutable(t, readerPath, fmt.Sprintf(`#!/bin/sh
export LLMSWAP_TEST_TOKEN_READER_HELPER=1
export LLMSWAP_TEST_TOKEN_PATH="$2"
exec %q -test.run '^TestAgentTokenReaderHelperProcess$'
`, testBinary))
		envMap["LLMSWAP_AGENT_TOKEN_READER"] = readerPath
	}

	cmd.Env = append(os.Environ(), flattenEnv(envMap)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAgentTokenReaderHelperProcess(t *testing.T) {
	if os.Getenv("LLMSWAP_TEST_TOKEN_READER_HELPER") != "1" {
		return
	}
	tokenHex, err := agent.ReadAgentTokenFileHex(os.Getenv("LLMSWAP_TEST_TOKEN_PATH"))
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "invalid agent token file")
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, tokenHex)
	os.Exit(0)
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
