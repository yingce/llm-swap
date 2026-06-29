package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGatewayContainerEntrypointStartsGatewayUnderSupervisor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gateway-container-entrypoint.sh tests require a POSIX shell")
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
	writeExecutable(t, filepath.Join(binDir, "llm-swap-gateway"), "#!/bin/sh\necho gateway\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")

	out := runGatewayEntrypointCommand(t, root, nil)
	if strings.TrimSpace(out) != "supervisord-started" {
		t.Fatalf("entrypoint output = %q, want supervisord-started", out)
	}

	gatewayConf, err := os.ReadFile(filepath.Join(confDir, "llmswap-gateway.conf"))
	if err != nil {
		t.Fatal(err)
	}
	gatewayText := string(gatewayConf)
	for _, want := range []string{
		"[program:llmswap-gateway]",
		"command=" + filepath.Join(binDir, "llm-swap-gateway") + " --config " + filepath.Join(root, "gateway.yaml"),
		"autostart=true",
		"autorestart=true",
	} {
		if !strings.Contains(gatewayText, want) {
			t.Fatalf("gateway conf missing %q:\n%s", want, gatewayText)
		}
	}
}

func TestGatewayContainerEntrypointStartsTailscaleAtRuntimeWhenRequested(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gateway-container-entrypoint.sh tests require a POSIX shell")
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
	writeExecutable(t, filepath.Join(binDir, "llm-swap-gateway"), "#!/bin/sh\necho gateway\n")
	writeExecutable(t, filepath.Join(binDir, "supervisord"), "#!/bin/sh\nprintf supervisord-started\n")
	writeExecutable(t, filepath.Join(binDir, "tailscaled"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "tailscale"), "#!/bin/sh\nexit 0\n")

	out := runGatewayEntrypointCommand(t, root, map[string]string{
		"PATH":                        binDir + ":/usr/bin:/bin",
		"LLMSWAP_ENABLE_TAILSCALE":    "1",
		"LLMSWAP_TAILSCALE_AUTHKEY":   "tskey-test",
		"LLMSWAP_TAILSCALE_HOSTNAME":  "gateway-ts",
		"LLMSWAP_TAILSCALE_SOCKET":    filepath.Join(root, "run", "tailscaled.sock"),
		"LLMSWAP_TAILSCALE_TUN":       "tun",
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
		"--socket=" + filepath.Join(root, "run", "tailscaled.sock"),
		"--tun=tun",
	} {
		if !strings.Contains(tailscaledText, want) {
			t.Fatalf("tailscaled conf missing %q:\n%s", want, tailscaledText)
		}
	}

	initScript, err := os.ReadFile(filepath.Join(binDir, "tailscale-init.sh"))
	if err != nil {
		t.Fatal(err)
	}
	initScriptText := string(initScript)
	for _, want := range []string{
		filepath.Join(binDir, "tailscale") + "\" --socket=\"" + filepath.Join(root, "run", "tailscaled.sock") + "\" login --auth-key \"tskey-test\"",
		filepath.Join(binDir, "tailscale") + "\" --socket=\"" + filepath.Join(root, "run", "tailscaled.sock") + "\" set --hostname \"gateway-ts\"",
	} {
		if !strings.Contains(initScriptText, want) {
			t.Fatalf("tailscale init script missing %q:\n%s", want, initScriptText)
		}
	}
}

func runGatewayEntrypointCommand(t *testing.T, root string, extraEnv map[string]string) string {
	t.Helper()
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "gateway-container-entrypoint.sh")
	cmd := exec.Command("bash", script)
	cmd.Dir = repo

	envMap := map[string]string{
		"HOME":                        t.TempDir(),
		"PATH":                        filepath.Join(root, "bin") + ":/usr/bin:/bin",
		"LLMSWAP_ROOT":                root,
		"LLMSWAP_BIN_DIR":             filepath.Join(root, "bin"),
		"LLMSWAP_GATEWAY_BIN":         filepath.Join(root, "bin", "llm-swap-gateway"),
		"LLMSWAP_GATEWAY_CONFIG":      filepath.Join(root, "gateway.yaml"),
		"LLMSWAP_LOG_DIR":             filepath.Join(root, "logs"),
		"LLMSWAP_SUPERVISOR_CONF_DIR": filepath.Join(root, "supervisor", "conf.d"),
		"LLMSWAP_SUPERVISORD_CONFIG":  filepath.Join(root, "supervisor", "supervisord.conf"),
	}
	for key, value := range extraEnv {
		envMap[key] = value
	}

	cmd.Env = append(os.Environ(), flattenEnv(envMap)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gateway-container-entrypoint.sh failed: %v\n%s", err, string(out))
	}
	return string(out)
}
