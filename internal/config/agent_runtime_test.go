package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSwapURLPrefersExplicitValue(t *testing.T) {
	got, err := ResolveSwapURL(context.Background(), "http://custom:9000", 8081, func(context.Context) (string, bool) {
		return "100.64.0.1", true
	}, func() (string, error) {
		return "10.0.0.1", nil
	})
	if err != nil {
		t.Fatalf("ResolveSwapURL returned error: %v", err)
	}
	if got != "http://custom:9000" {
		t.Fatalf("swap url = %q, want explicit url", got)
	}
}

func TestResolveSwapURLUsesTailscaleIPBeforeLocalIP(t *testing.T) {
	got, err := ResolveSwapURL(context.Background(), "", 8081, func(context.Context) (string, bool) {
		return "100.64.0.10", true
	}, func() (string, error) {
		return "10.0.0.10", nil
	})
	if err != nil {
		t.Fatalf("ResolveSwapURL returned error: %v", err)
	}
	if got != "http://100.64.0.10:8081" {
		t.Fatalf("swap url = %q, want tailscale URL", got)
	}
}

func TestResolveSwapURLFallsBackToLocalIP(t *testing.T) {
	got, err := ResolveSwapURL(context.Background(), "", 8081, func(context.Context) (string, bool) {
		return "", false
	}, func() (string, error) {
		return "10.0.0.20", nil
	})
	if err != nil {
		t.Fatalf("ResolveSwapURL returned error: %v", err)
	}
	if got != "http://10.0.0.20:8081" {
		t.Fatalf("swap url = %q, want local IP URL", got)
	}
}

func TestLoadAgentRuntimeAppliesOptDefaultsAndDerivedSwapURL(t *testing.T) {
	cfg, err := LoadAgentRuntime(context.Background(), AgentRuntimeOptions{
		Args: []string{
			"--id", "gpu-01",
			"--tags", "gpu-4090,gpu-a100",
			"--gateway-url", "http://gateway",
			"--token", "agent-token",
			"--llama-swap-token", "worker-token",
		},
		TailscaleIP: func(context.Context) (string, bool) {
			return "100.64.0.30", true
		},
		LocalIP: func() (string, error) {
			return "10.0.0.30", nil
		},
	})
	if err != nil {
		t.Fatalf("LoadAgentRuntime returned error: %v", err)
	}
	if cfg.Agent.ModelRoot != "/opt/llmswap/models" {
		t.Fatalf("model_root = %q, want /opt/llmswap/models", cfg.Agent.ModelRoot)
	}
	if cfg.Agent.LlamaSwapConfig != "/opt/llmswap/llama-swap.yaml" {
		t.Fatalf("llama_swap_config = %q, want /opt/llmswap/llama-swap.yaml", cfg.Agent.LlamaSwapConfig)
	}
	if cfg.Agent.SwapPort != 8081 {
		t.Fatalf("swap_port = %d, want 8081", cfg.Agent.SwapPort)
	}
	if cfg.Agent.LlamaSwapURL != "http://100.64.0.30:8081" {
		t.Fatalf("llama_swap_url = %q, want derived tailscale URL", cfg.Agent.LlamaSwapURL)
	}
	if len(cfg.Agent.Tags) != 2 || cfg.Agent.Tags[0] != "gpu-4090" || cfg.Agent.Tags[1] != "gpu-a100" {
		t.Fatalf("tags = %v, want parsed CLI tags", cfg.Agent.Tags)
	}
}

func TestLoadAgentRuntimePriorityCLIEnvConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(configPath, []byte(`
agent:
  id: config-id
  tags: [config-tag]
  gateway_url: http://config-gateway
  token: config-token
  llama_swap_token: config-worker-token
  swap_url: http://config-worker:8081
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SWAP_AGENT_ID", "env-id")
	t.Setenv("LLM_SWAP_AGENT_GATEWAY_URL", "http://env-gateway")
	t.Setenv("SWAP_URL", "http://env-worker:8081")

	cfg, err := LoadAgentRuntime(context.Background(), AgentRuntimeOptions{
		ConfigPath: configPath,
		Args: []string{
			"--id", "cli-id",
			"--gateway-url", "http://cli-gateway",
		},
		TailscaleIP: func(context.Context) (string, bool) {
			return "100.64.0.40", true
		},
		LocalIP: func() (string, error) {
			return "10.0.0.40", nil
		},
	})
	if err != nil {
		t.Fatalf("LoadAgentRuntime returned error: %v", err)
	}
	if cfg.Agent.ID != "cli-id" {
		t.Fatalf("id = %q, want CLI value", cfg.Agent.ID)
	}
	if cfg.Agent.GatewayURL != "http://cli-gateway" {
		t.Fatalf("gateway_url = %q, want CLI value", cfg.Agent.GatewayURL)
	}
	if cfg.Agent.LlamaSwapURL != "http://env-worker:8081" {
		t.Fatalf("llama_swap_url = %q, want SWAP_URL env override", cfg.Agent.LlamaSwapURL)
	}
	if cfg.Agent.Token != "config-token" || cfg.Agent.LlamaSwapToken != "config-worker-token" {
		t.Fatalf("tokens = %q/%q, want config tokens", cfg.Agent.Token, cfg.Agent.LlamaSwapToken)
	}
}

func TestLoadAgentRuntimeUsesConfigPathFromEnv(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(configPath, []byte(`
agent:
  id: env-config-id
  tags: [gpu-4090]
  gateway_url: http://gateway
  token: agent-token
  llama_swap_token: worker-token
  swap_url: http://worker:8081
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_SWAP_AGENT_CONFIG", configPath)

	cfg, err := LoadAgentRuntime(context.Background(), AgentRuntimeOptions{})
	if err != nil {
		t.Fatalf("LoadAgentRuntime returned error: %v", err)
	}
	if cfg.Agent.ID != "env-config-id" {
		t.Fatalf("id = %q, want env config file value", cfg.Agent.ID)
	}
}
