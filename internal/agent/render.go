package agent

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

const runtimeLogPath = "/opt/llmswap/logs/model-runtime.log"

type llamaSwapConfig struct {
	HealthCheckTimeout int                       `yaml:"healthCheckTimeout"`
	StartPort          int                       `yaml:"startPort"`
	GlobalTTL          int                       `yaml:"globalTTL"`
	APIKeys            []string                  `yaml:"apiKeys,omitempty"`
	Performance        llamaSwapPerformance      `yaml:"performance"`
	Hooks              map[string]any            `yaml:"hooks,omitempty"`
	Models             map[string]llamaSwapModel `yaml:"models"`
}

type llamaSwapPerformance struct {
	Enable bool   `yaml:"enable"`
	Every  string `yaml:"every"`
}

type llamaSwapModel struct {
	Cmd           string `yaml:"cmd"`
	CmdStop       string `yaml:"cmdStop,omitempty"`
	CheckEndpoint string `yaml:"checkEndpoint,omitempty"`
	TTL           int    `yaml:"ttl"`
}

func RenderLlamaSwapConfig(resp protocol.AgentConfigResponse, modelRoot string, token string) ([]byte, error) {
	out := llamaSwapConfig{
		HealthCheckTimeout: 300,
		StartPort:          10001,
		GlobalTTL:          0,
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
		modelPath := filepath.ToSlash(filepath.Join(modelRoot, modelName))
		cmd, err := modelCommand(modelName, model, modelPath)
		if err != nil {
			return nil, err
		}
		rendered := llamaSwapModel{
			Cmd: loggedShellCommand(modelName, cmd),
			TTL: model.TTL,
		}
		if model.CmdStop != "" {
			rendered.CmdStop = shellCommand(model.CmdStop)
		}
		if model.CheckEndpoint != "" {
			rendered.CheckEndpoint = model.CheckEndpoint
		} else if defaultEndpoint := defaultRuntimeCheckEndpoint(model.Runtime); defaultEndpoint != "" {
			rendered.CheckEndpoint = defaultEndpoint
		}
		out.Models[modelName] = rendered
	}

	return yaml.Marshal(out)
}

func defaultRuntimeCheckEndpoint(runtime string) string {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "sglang":
		return "/model_info"
	default:
		return ""
	}
}

func modelCommand(modelName string, model config.Model, modelPath string) (string, error) {
	if strings.TrimSpace(model.Run) != "" {
		return strings.ReplaceAll(model.Run, "{{model_path}}", modelPath), nil
	}

	switch strings.ToLower(strings.TrimSpace(model.Runtime)) {
	case "vllm":
		return runtimeCommand("/opt/llmswap/bin/vllm.server", append([]string{
			modelPath,
			"--served-model-name", modelName,
		}, model.RuntimeArgs...)...), nil
	case "sglang":
		return runtimeCommand("/opt/llmswap/bin/sglang.server", append([]string{
			modelPath,
			"--served-model-name", modelName,
		}, model.RuntimeArgs...)...), nil
	case "llamacpp":
		return runtimeCommand("/opt/llmswap/bin/llamacpp.server", append([]string{
			llamaCppModelPath(modelPath, model.Artifact),
			"--alias", modelName,
		}, model.RuntimeArgs...)...), nil
	default:
		return "", fmt.Errorf("allowed model %q has empty run command and unsupported runtime %q", modelName, model.Runtime)
	}
}

func runtimeCommand(binary string, args ...string) string {
	parts := []string{"PORT=${PORT}", shellArg(binary)}
	for _, arg := range args {
		parts = append(parts, shellArg(arg))
	}
	return strings.Join(parts, " ")
}

func llamaCppModelPath(modelPath string, artifact config.Artifact) string {
	if artifact.Kind != "file" {
		return modelPath
	}
	base := path.Base(strings.TrimSpace(artifact.Object))
	if base == "." || base == "/" || base == "" {
		return modelPath
	}
	return filepath.ToSlash(filepath.Join(modelPath, base))
}

func shellCommand(cmd string) string {
	return "/bin/sh -c '" + strings.ReplaceAll(cmd, "'", "'\"'\"'") + "'"
}

func loggedShellCommand(modelName string, cmd string) string {
	logDir := filepath.ToSlash(filepath.Dir(runtimeLogPath))
	modelArg := shellArg(modelName)
	cmdArg := shellArg(cmd)
	wrapped := fmt.Sprintf(
		"mkdir -p %s; LLMSWAP_MODEL_CMD=%s; LLMSWAP_MODEL_PORT=\"${PORT:-}\"; if [ -z \"$LLMSWAP_MODEL_PORT\" ]; then LLMSWAP_MODEL_PORT=$(printf \"%%s\\n\" \"$LLMSWAP_MODEL_CMD\" | sed -n 's/.*PORT=\\([0-9][0-9]*\\).*/\\1/p' | head -n1); fi; { printf \"===== start time=%%s model=%%s port=%%s =====\\n\" \"$(date -Is)\" %s \"$LLMSWAP_MODEL_PORT\"; printf \"cmd: %%s\\n\" \"$LLMSWAP_MODEL_CMD\"; %s; rc=$?; printf \"===== exit time=%%s model=%%s status=%%s =====\\n\" \"$(date -Is)\" %s \"$rc\"; exit \"$rc\"; } >> %s 2>&1",
		logDir,
		cmdArg,
		modelArg,
		cmd,
		modelArg,
		runtimeLogPath,
	)
	return shellCommand(wrapped)
}

func shellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
