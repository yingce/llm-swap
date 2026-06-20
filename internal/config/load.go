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
	if cfg.Agent.ModelRoot == "" || cfg.Agent.LlamaSwapConfig == "" || cfg.Agent.GatewayURL == "" {
		return cfg, fmt.Errorf("agent model_root, llama_swap_config, and gateway_url are required")
	}
	return cfg, nil
}

func validateGateway(cfg GatewayConfig) error {
	if strings.TrimSpace(cfg.OSS.BaseURL) == "" {
		return fmt.Errorf("oss.base_url is required")
	}
	if cfg.Tokens.Agent == "" || cfg.Tokens.LlamaSwap == "" {
		return fmt.Errorf("tokens.agent and tokens.llama_swap are required")
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
		if strings.TrimSpace(model.Run) == "" {
			return fmt.Errorf("model %s run is required", name)
		}
		if model.MaxLoaded > 0 && model.MinLoaded > model.MaxLoaded {
			return fmt.Errorf("model %s min_loaded cannot exceed max_loaded", name)
		}
		if model.MaxConcurrency < 0 || model.MaxQueue < 0 {
			return fmt.Errorf("model %s concurrency and queue limits must be non-negative", name)
		}
	}
	for tag, policy := range cfg.TagPolicies {
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
