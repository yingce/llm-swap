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

func TestDockerfileAgentUsesStableRuntimeBaseBeforeAgentBuild(t *testing.T) {
	repo := repoRoot(t)
	path := filepath.Join(repo, "Dockerfile.agent")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)

	runtimeBase := "FROM ${BASE_IMAGE} AS runtime-base"
	finalStage := "FROM runtime-base AS final"
	agentBuild := "FROM golang:1.23-bookworm AS agent-build"
	agentSourceCopy := "COPY cmd ./cmd"
	runtimeInstall := "--only runtime"
	finalAgentInstall := "--only agent"

	runtimeBaseIdx := strings.Index(text, runtimeBase)
	if runtimeBaseIdx == -1 {
		t.Fatalf("Dockerfile.agent should name the heavy dependency stage %q", runtimeBase)
	}
	finalStageIdx := strings.Index(text, finalStage)
	if finalStageIdx == -1 {
		t.Fatalf("Dockerfile.agent should derive the final image from %q", finalStage)
	}
	agentBuildIdx := strings.Index(text, agentBuild)
	if agentBuildIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", agentBuild)
	}
	agentSourceIdx := strings.Index(text, agentSourceCopy)
	if agentSourceIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", agentSourceCopy)
	}
	runtimeInstallIdx := strings.Index(text, runtimeInstall)
	if runtimeInstallIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", runtimeInstall)
	}
	finalAgentInstallIdx := strings.LastIndex(text, finalAgentInstall)
	if finalAgentInstallIdx == -1 {
		t.Fatalf("Dockerfile.agent missing %q", finalAgentInstall)
	}

	if runtimeBaseIdx >= agentBuildIdx {
		t.Fatalf("runtime-base should be declared before agent-build so heavy runtime layers do not depend on agent source")
	}
	if agentSourceIdx <= runtimeInstallIdx {
		t.Fatalf("agent source copies should happen after runtime install layers")
	}
	if finalStageIdx <= runtimeInstallIdx {
		t.Fatalf("final stage should start after heavy runtime install layers")
	}
	if finalAgentInstallIdx <= finalStageIdx {
		t.Fatalf("agent install should happen in the final stage")
	}
}
