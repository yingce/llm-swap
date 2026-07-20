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
	Models             map[string]llamaSwapModel `yaml:"models"`
}

type llamaSwapPerformance struct {
	Enable bool   `yaml:"enable"`
	Every  string `yaml:"every"`
}

type llamaSwapModel struct {
	Cmd           literalString `yaml:"cmd"`
	CmdStop       string        `yaml:"cmdStop,omitempty"`
	CheckEndpoint string        `yaml:"checkEndpoint,omitempty"`
	TTL           int           `yaml:"ttl"`
}

type literalString string

func (s literalString) MarshalYAML() (any, error) {
	return &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!str",
		Value: string(s),
		Style: yaml.LiteralStyle,
	}, nil
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

	for _, modelName := range resp.TagPolicy.AllowedModels {
		model, ok := resp.Models[modelName]
		if !ok {
			return nil, fmt.Errorf("allowed model %q missing from config models", modelName)
		}
		modelPath := filepath.ToSlash(filepath.Join(modelRoot, config.ResolvedModelDir(modelName, model)))
		cmd, err := modelCommand(modelName, model, modelPath)
		if err != nil {
			return nil, err
		}
		rendered := llamaSwapModel{
			Cmd: literalString(loggedShellCommand(modelName, cmd)),
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

type modelCommandSpec struct {
	shell string
	argv  []string
}

func modelCommand(modelName string, model config.Model, modelPath string) (modelCommandSpec, error) {
	if strings.TrimSpace(model.Run) != "" {
		return modelCommandSpec{shell: strings.ReplaceAll(model.Run, "{{model_path}}", modelPath)}, nil
	}

	switch strings.ToLower(strings.TrimSpace(model.Runtime)) {
	case "vllm":
		return runtimeCommand("/opt/llmswap/bin/vllm.server", append([]string{
			modelPath,
			"--served-model-name", modelName,
		}, expandRuntimeArgs(model.RuntimeArgs)...)...), nil
	case "sglang":
		return runtimeCommand("/opt/llmswap/bin/sglang.server", append([]string{
			modelPath,
			"--served-model-name", modelName,
		}, expandRuntimeArgs(model.RuntimeArgs)...)...), nil
	case "llamacpp":
		return runtimeCommand("/opt/llmswap/bin/llamacpp.server", append([]string{
			llamaCppModelPath(modelPath, model.Artifact),
			"--alias", modelName,
		}, expandRuntimeArgs(model.RuntimeArgs)...)...), nil
	default:
		return modelCommandSpec{}, fmt.Errorf("allowed model %q has empty run command and unsupported runtime %q", modelName, model.Runtime)
	}
}

func expandRuntimeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		fields := splitRuntimeArg(arg)
		if len(fields) == 0 {
			continue
		}
		out = append(out, fields...)
	}
	return out
}

func splitRuntimeArg(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var out []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range value {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if current.Len() > 0 {
				out = append(out, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}

func runtimeCommand(binary string, args ...string) modelCommandSpec {
	argv := append([]string{binary}, args...)
	return modelCommandSpec{
		shell: "PORT=${PORT} " + shellJoin(argv...),
		argv:  argv,
	}
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

func loggedShellCommand(modelName string, cmd modelCommandSpec) string {
	if len(cmd.argv) > 0 {
		return loggedArgvCommand(modelName, cmd.argv)
	}
	return loggedRawShellCommand(modelName, cmd.shell)
}

func loggedRawShellCommand(modelName string, cmd string) string {
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

func loggedArgvCommand(modelName string, argv []string) string {
	script := fmt.Sprintf(
		"model_name=$1; shift; mkdir -p %s; LLMSWAP_MODEL_PORT=\"${PORT}\"; { printf \"===== start time=%%s model=%%s port=%%s =====\\n\" \"$(date -Is)\" \"$model_name\" \"$LLMSWAP_MODEL_PORT\"; printf \"cmd: PORT=%%s\" \"$LLMSWAP_MODEL_PORT\"; printf \" %%s\" \"$@\"; printf \"\\n\"; PORT=\"${PORT}\" \"$@\"; rc=$?; printf \"===== exit time=%%s model=%%s status=%%s =====\\n\" \"$(date -Is)\" \"$model_name\" \"$rc\"; exit \"$rc\"; } >> %s 2>&1",
		filepath.ToSlash(filepath.Dir(runtimeLogPath)),
		runtimeLogPath,
	)
	parts := []string{"/bin/sh", "-c", script, "llmswap-model", modelName}
	parts = append(parts, argv...)
	return shellJoin(parts...)
}

func shellJoin(args ...string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellArg(arg))
	}
	return strings.Join(parts, " ")
}

func shellArg(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
