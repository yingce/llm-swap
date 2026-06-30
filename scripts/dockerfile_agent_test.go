package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerfileAgentDoesNotPersistBuildOnlyEnvIntoRuntimeImage(t *testing.T) {
	repo := repoRoot(t)
	path := filepath.Join(repo, "Dockerfile.agent")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)

	unwanted := []string{
		"ENV UV_INDEX_URL=",
		"ENV UV_EXTRA_INDEX_URL=",
		"ENV PIP_INDEX_URL=",
		"ENV PIP_EXTRA_INDEX_URL=",
		"ENV LLMSWAP_UV_PYTHON_INSTALL_MIRROR=",
		"ENV LLMSWAP_TORCH_INDEX_URL=",
		"ENV LLMSWAP_TORCH_INDEX_URL_BASE=",
		"ENV LLMSWAP_VLLM_PACKAGE=",
		"ENV LLMSWAP_SGLANG_PACKAGE=",
		"ENV LLMSWAP_LLAMA_CPP_ARCH=",
		"ENV LLAMA_SWAP_REF=",
		"ENV LLAMA_SWAP_RELEASE_URL=",
	}
	for _, entry := range unwanted {
		if strings.Contains(text, entry) {
			t.Fatalf("Dockerfile.agent unexpectedly persists build-only runtime env %q", entry)
		}
	}
}

func TestDockerfileAgentCopiesAgentBinaryAfterHeavyInstallLayers(t *testing.T) {
	repo := repoRoot(t)
	path := filepath.Join(repo, "Dockerfile.agent")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)

	copyBinary := "COPY --from=agent-build /out/llm-swap-agent /tmp/llm-swap-agent"
	runtimeStage := "--only runtime"
	tailscaleStage := "--only tailscale"
	agentStage := "--only agent"
	supervisorStage := "--only supervisor"

	copyIdx := strings.Index(text, copyBinary)
	if copyIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", copyBinary)
	}
	runtimeIdx := strings.Index(text, runtimeStage)
	if runtimeIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", runtimeStage)
	}
	tailscaleIdx := strings.Index(text, tailscaleStage)
	if tailscaleIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", tailscaleStage)
	}
	agentIdx := strings.Index(text, agentStage)
	if agentIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", agentStage)
	}
	supervisorIdx := strings.Index(text, supervisorStage)
	if supervisorIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", supervisorStage)
	}

	if copyIdx <= runtimeIdx {
		t.Fatalf("agent binary copy should happen after runtime install layers to preserve cache")
	}
	if copyIdx <= tailscaleIdx {
		t.Fatalf("agent binary copy should happen after tailscale install layer to preserve cache")
	}
	if copyIdx >= agentIdx {
		t.Fatalf("agent binary copy should happen before the agent install stage")
	}
	if copyIdx >= supervisorIdx {
		t.Fatalf("agent binary copy should happen before the supervisor stage")
	}
}
