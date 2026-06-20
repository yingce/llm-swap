package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"llm-swap/internal/protocol"
)

type llamaSwapConfig struct {
	StartPort   int                       `yaml:"startPort"`
	GlobalTTL   int                       `yaml:"globalTTL"`
	APIKeys     []string                  `yaml:"apiKeys,omitempty"`
	Performance llamaSwapPerformance      `yaml:"performance"`
	Hooks       map[string]any            `yaml:"hooks,omitempty"`
	Models      map[string]llamaSwapModel `yaml:"models"`
}

type llamaSwapPerformance struct {
	Enable bool   `yaml:"enable"`
	Every  string `yaml:"every"`
}

type llamaSwapModel struct {
	Cmd              string `yaml:"cmd"`
	CmdStop          string `yaml:"cmdStop,omitempty"`
	TTL              int    `yaml:"ttl"`
	ConcurrencyLimit int    `yaml:"concurrencyLimit,omitempty"`
}

func RenderLlamaSwapConfig(resp protocol.AgentConfigResponse, modelRoot string, token string) ([]byte, error) {
	out := llamaSwapConfig{
		StartPort: 10001,
		GlobalTTL: 0,
		Performance: llamaSwapPerformance{
			Enable: true,
			Every:  "5s",
		},
		Models: make(map[string]llamaSwapModel, len(resp.TagPolicy.AllowedModels)),
	}
	if token != "" {
		out.APIKeys = []string{token}
	}
	if resp.TagPolicy.WarmWhenIdle != "" {
		out.Hooks = map[string]any{
			"on_startup": map[string]any{
				"preload": []string{resp.TagPolicy.WarmWhenIdle},
			},
		}
	}

	for _, modelName := range resp.TagPolicy.AllowedModels {
		model, ok := resp.Models[modelName]
		if !ok {
			return nil, fmt.Errorf("allowed model %q missing from config models", modelName)
		}
		if strings.TrimSpace(model.Run) == "" {
			return nil, fmt.Errorf("allowed model %q has empty run command", modelName)
		}

		modelPath := filepath.ToSlash(filepath.Join(modelRoot, modelName))
		rendered := llamaSwapModel{
			Cmd: strings.ReplaceAll(model.Run, "{{model_path}}", modelPath),
			TTL: model.TTL,
		}
		if model.CmdStop != "" {
			rendered.CmdStop = model.CmdStop
		}
		if resp.TagPolicy.WorkerDefaults.MaxConcurrency != 0 {
			rendered.ConcurrencyLimit = resp.TagPolicy.WorkerDefaults.MaxConcurrency
		}

		out.Models[modelName] = rendered
	}

	return yaml.Marshal(out)
}
