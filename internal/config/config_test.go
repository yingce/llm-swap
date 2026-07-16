package config

import (
	"strings"
	"testing"
)

func TestLoadGatewayConfigValidatesWarmModel(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
models:
  qwen:
    priority: 100
    min_loaded: 1
    max_loaded: 2
    max_concurrency: 4
    max_queue: 8
    queue_timeout_ms: 30000
    ttl: 900
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
  gpu-4090:
    max_concurrency: 8
    max_queue: 16
    worker_defaults:
      max_concurrency: 2
      max_queue: 4
    allowed_models: [qwen]
    warm_when_idle: missing
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "warm_when_idle") {
		t.Fatalf("error = %v, want warm_when_idle", err)
	}
}

func TestLoadGatewayConfigAcceptsMinimalValidConfig(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
models:
  qwen:
    priority: 100
    min_loaded: 1
    max_loaded: 2
    max_concurrency: 4
    max_queue: 8
    queue_timeout_ms: 30000
    ttl: 900
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
    check_endpoint: /model_info
tag_policies:
  gpu-4090:
    max_concurrency: 8
    max_queue: 16
    worker_defaults:
      max_concurrency: 2
      max_queue: 4
    allowed_models: [qwen]
    warm_when_idle: qwen
`
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.TagPolicies["gpu-4090"].WarmWhenIdle != "qwen" {
		t.Fatalf("warm_when_idle = %q", cfg.TagPolicies["gpu-4090"].WarmWhenIdle)
	}
	if cfg.Models["qwen"].CheckEndpoint != "/model_info" {
		t.Fatalf("models.qwen.check_endpoint = %q, want /model_info", cfg.Models["qwen"].CheckEndpoint)
	}
	if cfg.Tokens.LlamaSwap != "agent-token" {
		t.Fatalf("tokens.llama_swap = %q, want inherited agent token", cfg.Tokens.LlamaSwap)
	}
}

func TestLoadGatewayConfigAcceptsMetricsStoreConfig(t *testing.T) {
	raw := validGatewayYAML(`
metrics_store:
  enabled: true
  type: victoriametrics
  query_url: http://victoriametrics:8428
  default_range: 2h
  max_range: 14d
  timeout_ms: 2500
`)
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if !cfg.MetricsStore.Enabled {
		t.Fatal("metrics_store.enabled = false, want true")
	}
	if cfg.MetricsStore.Type != "victoriametrics" || cfg.MetricsStore.QueryURL != "http://victoriametrics:8428" {
		t.Fatalf("metrics store = %+v, want victoriametrics query URL", cfg.MetricsStore)
	}
	if cfg.MetricsStore.DefaultRange != "2h" || cfg.MetricsStore.MaxRange != "14d" || cfg.MetricsStore.TimeoutMS != 2500 {
		t.Fatalf("metrics store ranges = %+v", cfg.MetricsStore)
	}
}

func TestLoadGatewayConfigAcceptsBillingMode(t *testing.T) {
	raw := `
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
    billing:
      mode: per_token
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if got := cfg.Models["qwen"].Billing.Mode; got != "per_token" {
		t.Fatalf("billing.mode = %q, want per_token", got)
	}
}

func TestLoadGatewayConfigRejectsInvalidBillingMode(t *testing.T) {
	raw := `
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
    billing:
      mode: hourly
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "billing.mode") {
		t.Fatalf("error = %v, want billing.mode", err)
	}
}

func TestLoadGatewayConfigDefaultsMetricsStore(t *testing.T) {
	cfg, err := LoadGateway(strings.NewReader(validGatewayYAML("")))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.MetricsStore.Enabled {
		t.Fatal("metrics_store.enabled = true, want false")
	}
	if cfg.MetricsStore.Type != "victoriametrics" {
		t.Fatalf("metrics_store.type = %q, want victoriametrics", cfg.MetricsStore.Type)
	}
	if cfg.MetricsStore.DefaultRange != "1h" || cfg.MetricsStore.MaxRange != "7d" || cfg.MetricsStore.TimeoutMS != 3000 {
		t.Fatalf("metrics store defaults = %+v", cfg.MetricsStore)
	}
}

func TestLoadGatewayConfigTreatsMissingMaxLoadedAsAutomatic(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
  llama_swap: worker-token
models:
  qwen:
    min_loaded: 1
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.Models["qwen"].MaxLoadedSet {
		t.Fatalf("MaxLoadedSet = true, want false")
	}
	if got := cfg.Models["qwen"].HardMaxLoaded(); got != 0 {
		t.Fatalf("HardMaxLoaded = %d, want 0 for automatic", got)
	}
}

func TestLoadGatewayConfigAcceptsRuntimeWithoutRun(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
  llama_swap: worker-token
models:
  qwen:
    min_loaded: 1
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    runtime: sglang
    runtime_args:
      - --trust-remote-code
      - --dtype
      - bfloat16
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.Models["qwen"].Runtime != "sglang" {
		t.Fatalf("runtime = %q, want sglang", cfg.Models["qwen"].Runtime)
	}
	if got := cfg.Models["qwen"].RuntimeArgs; len(got) != 3 || got[0] != "--trust-remote-code" || got[2] != "bfloat16" {
		t.Fatalf("runtime_args = %#v, want parsed args", got)
	}
}

func TestLoadGatewayConfigRejectsModelWithoutRunOrRuntime(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
models:
  qwen:
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "run or runtime") {
		t.Fatalf("error = %v, want run/runtime", err)
	}
}

func TestLoadGatewayConfigRejectsUnsupportedRuntime(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
models:
  qwen:
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    runtime: unknown
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "runtime must be vllm, sglang, or llamacpp") {
		t.Fatalf("error = %v, want runtime validation", err)
	}
}

func TestLoadGatewayConfigRejectsExplicitMaxLoadedBelowMinLoaded(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
  llama_swap: worker-token
models:
  qwen:
    min_loaded: 1
    max_loaded: 0
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "min_loaded cannot exceed max_loaded") {
		t.Fatalf("error = %v, want min_loaded/max_loaded", err)
	}
}

func TestLoadGatewayConfigRequiresClientToken(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
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
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "tokens.client") {
		t.Fatalf("error = %v, want tokens.client", err)
	}
}

func TestLoadGatewayConfigRejectsNegativeTagLimits(t *testing.T) {
	raw := `
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
    max_concurrency: -1
    worker_defaults:
      max_queue: -1
    allowed_models: [qwen]
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "tag gpu-4090") {
		t.Fatalf("error = %v, want tag gpu-4090", err)
	}
}

func TestLoadGatewayConfigRejectsNegativeQueueTimeout(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
  llama_swap: worker-token
models:
  qwen:
    queue_timeout_ms: -1
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
  gpu-4090:
    allowed_models: [qwen]
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "queue_timeout_ms") {
		t.Fatalf("error = %v, want queue_timeout_ms", err)
	}
}

func TestLoadAgentRequiresRuntimeFields(t *testing.T) {
	raw := `
agent:
  id: gpu-01
  tags: [gpu-4090]
  model_root: /data/models
  llama_swap_config: /etc/llama-swap/config.yaml
  gateway_url: http://gateway
`
	_, err := LoadAgent(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "swap_url") {
		t.Fatalf("error = %v, want swap_url", err)
	}
}

func TestLoadAgentAcceptsSeparateAgentAndLlamaSwapTokens(t *testing.T) {
	raw := `
agent:
  id: gpu-01
  tags: [gpu-4090]
  model_root: /data/models
  llama_swap_config: /etc/llama-swap/config.yaml
  swap_url: http://worker
  gateway_url: http://gateway
  token: agent-token
  llama_swap_token: worker-token
`
	cfg, err := LoadAgent(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.Agent.Token != "agent-token" {
		t.Fatalf("agent.token = %q, want agent-token", cfg.Agent.Token)
	}
	if cfg.Agent.LlamaSwapToken != "worker-token" {
		t.Fatalf("agent.llama_swap_token = %q, want worker-token", cfg.Agent.LlamaSwapToken)
	}
	if cfg.Agent.LlamaSwapURL != "http://worker" {
		t.Fatalf("agent.llama_swap_url = %q, want swap_url alias", cfg.Agent.LlamaSwapURL)
	}
}

func TestLoadAgentAcceptsRestartCommand(t *testing.T) {
	raw := `
agent:
  id: gpu-01
  tags: [gpu-4090]
  model_root: /data/models
  llama_swap_config: /etc/llama-swap/config.yaml
  restart_command: docker restart llama-swap
  llama_swap_url: http://worker
  gateway_url: http://gateway
  token: agent-token
  llama_swap_token: worker-token
`
	cfg, err := LoadAgent(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.Agent.RestartCommand != "docker restart llama-swap" {
		t.Fatalf("agent.restart_command = %q, want docker restart llama-swap", cfg.Agent.RestartCommand)
	}
}

func TestLoadAgentDefaultsLlamaSwapTokenToAgentToken(t *testing.T) {
	raw := `
agent:
  id: gpu-01
  tags: [gpu-4090]
  model_root: /data/models
  llama_swap_config: /etc/llama-swap/config.yaml
  llama_swap_url: http://worker
  gateway_url: http://gateway
  token: agent-token
`
	cfg, err := LoadAgent(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.Agent.LlamaSwapToken != "agent-token" {
		t.Fatalf("agent.llama_swap_token = %q, want inherited agent token", cfg.Agent.LlamaSwapToken)
	}
}
