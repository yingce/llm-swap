package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadGatewayAppliesDefaultProxyAttempts(t *testing.T) {
	cfg, err := LoadGateway(stringsReader(validGatewayYAML("")))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.Gateway.ProxyAttempts != 3 {
		t.Fatalf("proxy_attempts = %d, want 3", cfg.Gateway.ProxyAttempts)
	}
}

func TestLoadGatewayRejectsInvalidProxyAttempts(t *testing.T) {
	_, err := LoadGateway(stringsReader(validGatewayYAML(`
gateway:
  proxy_attempts: -1
`)))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !contains(err.Error(), "proxy_attempts") {
		t.Fatalf("error = %v, want proxy_attempts", err)
	}
}

func TestLoadGatewayRuntimeUsesFileEnvAndCLI(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(validGatewayYAML(`
gateway:
  listen_addr: :9000
  proxy_attempts: 2
`)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SWAP_GATEWAY_TOKENS_CLIENT", "env-client-token")

	runtime, err := LoadGatewayRuntime(context.Background(), GatewayRuntimeOptions{
		ConfigPath: configPath,
		Args: []string{
			"--addr", ":8088",
			"--proxy-attempts", "4",
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayRuntime returned error: %v", err)
	}
	if runtime.ListenAddr != ":8088" {
		t.Fatalf("listen addr = %q, want CLI addr", runtime.ListenAddr)
	}
	if runtime.Config.Gateway.ProxyAttempts != 4 {
		t.Fatalf("proxy attempts = %d, want CLI value", runtime.Config.Gateway.ProxyAttempts)
	}
	if !runtime.Overrides.ListenAddr || !runtime.Overrides.ProxyAttempts || !runtime.Overrides.Tokens {
		t.Fatalf("overrides = %+v, want listen/proxy/tokens marked", runtime.Overrides)
	}
	if runtime.Config.Tokens.Client != "env-client-token" {
		t.Fatalf("client token = %q, want env override", runtime.Config.Tokens.Client)
	}
}

func TestLoadGatewayRuntimeDefaultsLlamaSwapTokenToEnvAgentToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(`
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: file-agent-token
models:
  qwen:
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SWAP_GATEWAY_TOKENS_AGENT", "env-agent-token")

	runtime, err := LoadGatewayRuntime(context.Background(), GatewayRuntimeOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatalf("LoadGatewayRuntime returned error: %v", err)
	}
	if runtime.Config.Tokens.LlamaSwap != "env-agent-token" {
		t.Fatalf("llama_swap token = %q, want env agent token", runtime.Config.Tokens.LlamaSwap)
	}
}

func TestLoadGatewayRuntimeAcceptsShortGatewayEnvNames(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(validGatewayYAML("")), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SWAP_GATEWAY_ADDR", ":7070")
	t.Setenv("LLM_SWAP_GATEWAY_PROXY_ATTEMPTS", "5")

	runtime, err := LoadGatewayRuntime(context.Background(), GatewayRuntimeOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatalf("LoadGatewayRuntime returned error: %v", err)
	}
	if runtime.ListenAddr != ":7070" {
		t.Fatalf("listen addr = %q, want short env addr", runtime.ListenAddr)
	}
	if runtime.Config.Gateway.ProxyAttempts != 5 {
		t.Fatalf("proxy attempts = %d, want short env value", runtime.Config.Gateway.ProxyAttempts)
	}
	if !runtime.Overrides.ListenAddr || !runtime.Overrides.ProxyAttempts {
		t.Fatalf("overrides = %+v, want short env overrides marked", runtime.Overrides)
	}
}

func TestLoadGatewayRuntimeUsesConfigPathFromEnv(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(configPath, []byte(validGatewayYAML(`
gateway:
  listen_addr: :9090
`)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SWAP_GATEWAY_CONFIG", configPath)

	runtime, err := LoadGatewayRuntime(context.Background(), GatewayRuntimeOptions{})
	if err != nil {
		t.Fatalf("LoadGatewayRuntime returned error: %v", err)
	}
	if runtime.ListenAddr != ":9090" {
		t.Fatalf("listen addr = %q, want env config file value", runtime.ListenAddr)
	}
}

func validGatewayYAML(prefix string) string {
	return prefix + `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
  llama_swap: worker-token
models:
  qwen:
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
}

func stringsReader(s string) *strings.Reader {
	return strings.NewReader(s)
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
