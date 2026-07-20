package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"

	"llm-swap/internal/config"
)

func resolveRequestedModel(cfg config.GatewayConfig, requested string) (string, config.Model, bool) {
	resolved := requested
	if target, ok := cfg.ModelAliases[requested]; ok {
		resolved = target
	}
	model, ok := cfg.Models[resolved]
	return resolved, model, ok
}

func rewriteRequestModel(body []byte, resolved string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode request body: %w", err)
	}
	payload["model"] = resolved
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode request body: %w", err)
	}
	return encoded, nil
}

func withRequestedModel(fields map[string]any, requested, resolved string) map[string]any {
	if requested != resolved {
		fields["requested_model"] = requested
	}
	return fields
}
