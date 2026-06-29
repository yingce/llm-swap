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
