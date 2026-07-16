package gateway

import "llm-swap/internal/config"

func activeGatewayConfig(cfg config.GatewayConfig) config.GatewayConfig {
	out := cloneGatewayConfig(cfg)
	if len(out.Models) == 0 {
		return out
	}

	for name, model := range out.Models {
		if model.Disabled {
			delete(out.Models, name)
		}
	}
	for tag, policy := range out.TagPolicies {
		if len(policy.AllowedModels) > 0 {
			allowed := make([]string, 0, len(policy.AllowedModels))
			for _, modelName := range policy.AllowedModels {
				if _, ok := out.Models[modelName]; ok {
					allowed = append(allowed, modelName)
				}
			}
			policy.AllowedModels = allowed
		}
		if policy.WarmWhenIdle != "" {
			if _, ok := out.Models[policy.WarmWhenIdle]; !ok {
				policy.WarmWhenIdle = ""
			}
		}
		out.TagPolicies[tag] = policy
	}
	return out
}
