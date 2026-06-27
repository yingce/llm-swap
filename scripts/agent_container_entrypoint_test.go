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

func runAgentEntrypoint(t *testing.T, root string, extraEnv map[string]string) string {
	t.Helper()
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "agent-container-entrypoint.sh")
	cmd := exec.Command("bash", script, "bash", "-lc", "cat \"$LLMSWAP_ROOT/bin/llama-swap\"")
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
