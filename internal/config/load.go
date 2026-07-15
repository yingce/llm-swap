package config

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadGateway(r io.Reader) (GatewayConfig, error) {
	var cfg GatewayConfig
	if err := yaml.NewDecoder(r).Decode(&cfg); err != nil {
		return cfg, err
	}
	applyGatewayDefaults(&cfg)
	applyGatewayTokenDefaults(&cfg)
	return cfg, validateGateway(cfg)
}

func LoadAgent(r io.Reader) (AgentConfig, error) {
	var cfg AgentConfig
	if err := yaml.NewDecoder(r).Decode(&cfg); err != nil {
		return cfg, err
	}
	if cfg.Agent.ID == "" {
		return cfg, fmt.Errorf("agent.id is required")
	}
	if len(cfg.Agent.Tags) == 0 {
		return cfg, fmt.Errorf("agent.tags is required")
	}
	if cfg.Agent.LlamaSwapURL == "" && cfg.Agent.SwapURL != "" {
		cfg.Agent.LlamaSwapURL = cfg.Agent.SwapURL
	}
	if cfg.Agent.ModelRoot == "" || cfg.Agent.LlamaSwapConfig == "" || cfg.Agent.LlamaSwapURL == "" || cfg.Agent.GatewayURL == "" {
		return cfg, fmt.Errorf("agent model_root, llama_swap_config, swap_url, and gateway_url are required")
	}
	if cfg.Agent.Token == "" {
		return cfg, fmt.Errorf("agent.token is required")
	}
	if cfg.Agent.LlamaSwapToken == "" {
		cfg.Agent.LlamaSwapToken = cfg.Agent.Token
	}
	return cfg, nil
}

func validateGateway(cfg GatewayConfig) error {
	if cfg.Gateway.ProxyAttempts < 0 {
		return fmt.Errorf("gateway.proxy_attempts must be non-negative")
	}
	if cfg.MetricsStore.Enabled && cfg.MetricsStore.Type != "victoriametrics" {
		return fmt.Errorf("metrics_store.type must be victoriametrics")
	}
	if cfg.RecordsStore.Enabled {
		if cfg.RecordsStore.Type != "postgres" {
			return fmt.Errorf("records_store.type must be postgres")
		}
		if strings.TrimSpace(cfg.RecordsStore.DSN) == "" {
			return fmt.Errorf("records_store.dsn is required when records_store.enabled is true")
		}
	}
	if strings.TrimSpace(cfg.OSS.BaseURL) == "" {
		return fmt.Errorf("oss.base_url is required")
	}
	if cfg.Tokens.Client == "" || cfg.Tokens.Agent == "" {
		return fmt.Errorf("tokens.client and tokens.agent are required")
	}
	if len(cfg.Models) == 0 {
		return fmt.Errorf("models is required")
	}
	for name, model := range cfg.Models {
		if model.Artifact.Object == "" {
			return fmt.Errorf("model %s artifact.object is required", name)
		}
		if model.Artifact.Kind != "file" && model.Artifact.Kind != "tar_gz" {
			return fmt.Errorf("model %s artifact.kind must be file or tar_gz", name)
		}
		if model.Artifact.CRC64ECMA == "" {
			return fmt.Errorf("model %s artifact.crc64ecma is required", name)
		}
		if strings.TrimSpace(model.Run) == "" && strings.TrimSpace(model.Runtime) == "" {
			return fmt.Errorf("model %s run or runtime is required", name)
		}
		if strings.TrimSpace(model.Runtime) != "" && !validModelRuntime(model.Runtime) {
			return fmt.Errorf("model %s runtime must be vllm, sglang, or llamacpp", name)
		}
		if model.MaxLoadedSet && model.MinLoaded > model.MaxLoaded {
			return fmt.Errorf("model %s min_loaded cannot exceed max_loaded", name)
		}
		if model.MaxConcurrency < 0 || model.MaxQueue < 0 {
			return fmt.Errorf("model %s concurrency and queue limits must be non-negative", name)
		}
		if model.QueueTimeoutMS < 0 {
			return fmt.Errorf("model %s queue_timeout_ms must be non-negative", name)
		}
	}
	for tag, policy := range cfg.TagPolicies {
		if policy.MaxConcurrency < 0 || policy.MaxQueue < 0 || policy.WorkerDefaults.MaxConcurrency < 0 || policy.WorkerDefaults.MaxQueue < 0 {
			return fmt.Errorf("tag %s concurrency and queue limits must be non-negative", tag)
		}
		for _, model := range policy.AllowedModels {
			if _, ok := cfg.Models[model]; !ok {
				return fmt.Errorf("tag %s allowed model %s is not defined", tag, model)
			}
		}
		if policy.WarmWhenIdle != "" && !slices.Contains(policy.AllowedModels, policy.WarmWhenIdle) {
			return fmt.Errorf("tag %s warm_when_idle %s must be in allowed_models", tag, policy.WarmWhenIdle)
		}
	}
	return nil
}

func validModelRuntime(runtime string) bool {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "vllm", "sglang", "llamacpp":
		return true
	default:
		return false
	}
}

func applyGatewayDefaults(cfg *GatewayConfig) {
	if cfg.Gateway.ProxyAttempts == 0 {
		cfg.Gateway.ProxyAttempts = DefaultProxyAttempts
	}
	if cfg.MetricsStore.Type == "" {
		cfg.MetricsStore.Type = "victoriametrics"
	}
	if cfg.MetricsStore.DefaultRange == "" {
		cfg.MetricsStore.DefaultRange = "1h"
	}
	if cfg.MetricsStore.MaxRange == "" {
		cfg.MetricsStore.MaxRange = "7d"
	}
	if cfg.MetricsStore.TimeoutMS <= 0 {
		cfg.MetricsStore.TimeoutMS = 3000
	}
	if cfg.RecordsStore.Type == "" {
		cfg.RecordsStore.Type = "postgres"
	}
	if cfg.RecordsStore.TimeoutMS <= 0 {
		cfg.RecordsStore.TimeoutMS = 3000
	}
}

func applyGatewayTokenDefaults(cfg *GatewayConfig) {
	if cfg.Tokens.LlamaSwap == "" {
		cfg.Tokens.LlamaSwap = cfg.Tokens.Agent
	}
}
