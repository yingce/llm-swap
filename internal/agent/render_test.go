package agent

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestRenderLlamaSwapConfigRendersAllowedModels(t *testing.T) {
	resp := protocol.AgentConfigResponse{
		Models: map[string]config.Model{
			"qwen": {
				Run:     "llama-server -m {{model_path}}/model.gguf --port ${PORT} --alias {{model_path}}",
				CmdStop: "pkill -f qwen",
				TTL:     30,
			},
			"extra": {
				Run: "llama-server -m {{model_path}}/extra.gguf --port ${PORT}",
			},
		},
		TagPolicy: protocol.AgentTagPolicy{
			AllowedModels: []string{"qwen"},
			WarmWhenIdle:  "qwen",
			WorkerDefaults: config.WorkerDefaults{
				MaxConcurrency: 2,
			},
		},
	}

	out, err := RenderLlamaSwapConfig(resp, "/models", "llama-token")
	if err != nil {
		t.Fatalf("RenderLlamaSwapConfig() error = %v", err)
	}

	doc := parseYAML(t, out)
	if got := doc["startPort"]; got != 10001 {
		t.Fatalf("startPort = %#v, want 10001", got)
	}
	if got := doc["globalTTL"]; got != 0 {
		t.Fatalf("globalTTL = %#v, want 0", got)
	}

	apiKeys := doc["apiKeys"].([]any)
	if len(apiKeys) != 1 || apiKeys[0] != "llama-token" {
		t.Fatalf("apiKeys = %#v, want [llama-token]", apiKeys)
	}

	performance := doc["performance"].(map[string]any)
	if performance["enable"] != true {
		t.Fatalf("performance.enable = %#v, want true", performance["enable"])
	}
	if performance["every"] != "5s" {
		t.Fatalf("performance.every = %#v, want 5s", performance["every"])
	}

	hooks := doc["hooks"].(map[string]any)
	onStartup := hooks["on_startup"].(map[string]any)
	preload := onStartup["preload"].([]any)
	if len(preload) != 1 || preload[0] != "qwen" {
		t.Fatalf("hooks.on_startup.preload = %#v, want [qwen]", preload)
	}

	models := doc["models"].(map[string]any)
	if len(models) != 1 {
		t.Fatalf("models length = %d, want 1: %#v", len(models), models)
	}
	if _, ok := models["extra"]; ok {
		t.Fatalf("models rendered disallowed entry: %#v", models)
	}

	qwen := models["qwen"].(map[string]any)
	wantCmd := "llama-server -m /models/qwen/model.gguf --port ${PORT} --alias /models/qwen"
	if qwen["cmd"] != wantCmd {
		t.Fatalf("qwen cmd = %q, want %q", qwen["cmd"], wantCmd)
	}
	if strings.Contains(qwen["cmd"].(string), "{{model_path}}") {
		t.Fatalf("qwen cmd still contains model_path placeholder: %q", qwen["cmd"])
	}
	if !strings.Contains(qwen["cmd"].(string), "${PORT}") {
		t.Fatalf("qwen cmd did not preserve ${PORT}: %q", qwen["cmd"])
	}
	if qwen["ttl"] != 30 {
		t.Fatalf("qwen ttl = %#v, want 30", qwen["ttl"])
	}
	if qwen["cmdStop"] != "pkill -f qwen" {
		t.Fatalf("qwen cmdStop = %#v, want pkill -f qwen", qwen["cmdStop"])
	}
	if qwen["concurrencyLimit"] != 2 {
		t.Fatalf("qwen concurrencyLimit = %#v, want 2", qwen["concurrencyLimit"])
	}
}

func TestRenderLlamaSwapConfigOmitsAPIKeysWhenTokenEmpty(t *testing.T) {
	resp := protocol.AgentConfigResponse{
		Models: map[string]config.Model{
			"qwen": {Run: "llama-server -m {{model_path}}/model.gguf --port ${PORT}"},
		},
		TagPolicy: protocol.AgentTagPolicy{
			AllowedModels: []string{"qwen"},
		},
	}

	out, err := RenderLlamaSwapConfig(resp, "/models", "")
	if err != nil {
		t.Fatalf("RenderLlamaSwapConfig() error = %v", err)
	}

	doc := parseYAML(t, out)
	if _, ok := doc["apiKeys"]; ok {
		t.Fatalf("apiKeys rendered for empty token: %#v", doc["apiKeys"])
	}
}

func TestRenderLlamaSwapConfigMissingAllowedModelReturnsError(t *testing.T) {
	resp := protocol.AgentConfigResponse{
		Models: map[string]config.Model{
			"other": {Run: "llama-server -m {{model_path}}/model.gguf --port ${PORT}"},
		},
		TagPolicy: protocol.AgentTagPolicy{
			AllowedModels: []string{"qwen"},
		},
	}

	_, err := RenderLlamaSwapConfig(resp, "/models", "")
	if err == nil {
		t.Fatalf("RenderLlamaSwapConfig() error = nil, want missing model error")
	}
	if !strings.Contains(err.Error(), "qwen") {
		t.Fatalf("RenderLlamaSwapConfig() error = %q, want model name", err)
	}
}

func TestRenderLlamaSwapConfigEmptyAllowedModelCommandReturnsError(t *testing.T) {
	resp := protocol.AgentConfigResponse{
		Models: map[string]config.Model{
			"qwen": {},
		},
		TagPolicy: protocol.AgentTagPolicy{
			AllowedModels: []string{"qwen"},
		},
	}

	_, err := RenderLlamaSwapConfig(resp, "/models", "")
	if err == nil {
		t.Fatalf("RenderLlamaSwapConfig() error = nil, want empty command error")
	}
	if !strings.Contains(err.Error(), "empty run command") {
		t.Fatalf("RenderLlamaSwapConfig() error = %q, want empty command", err)
	}
}

func TestRenderLlamaSwapConfigQwen36GGUFPath(t *testing.T) {
	resp := protocol.AgentConfigResponse{
		Models: map[string]config.Model{
			"qwen3.6": {
				Run: "llama-server -m {{model_path}}/Qwen3.6-35B-A3B-RP-NSFW-q4_K_M.gguf --port ${PORT}",
			},
		},
		TagPolicy: protocol.AgentTagPolicy{
			AllowedModels: []string{"qwen3.6"},
		},
	}

	out, err := RenderLlamaSwapConfig(resp, "/opt/llmfly/models", "")
	if err != nil {
		t.Fatalf("RenderLlamaSwapConfig() error = %v", err)
	}

	models := parseYAML(t, out)["models"].(map[string]any)
	qwen := models["qwen3.6"].(map[string]any)
	wantCmd := "llama-server -m /opt/llmfly/models/qwen3.6/Qwen3.6-35B-A3B-RP-NSFW-q4_K_M.gguf --port ${PORT}"
	if qwen["cmd"] != wantCmd {
		t.Fatalf("qwen3.6 cmd = %q, want %q", qwen["cmd"], wantCmd)
	}
}

func parseYAML(t *testing.T, data []byte) map[string]any {
	t.Helper()

	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v\n%s", err, data)
	}
	return out
}
