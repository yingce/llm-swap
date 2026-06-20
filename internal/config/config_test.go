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
  llama_swap: worker-token
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
  llama_swap: worker-token
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
    warm_when_idle: qwen
`
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.TagPolicies["gpu-4090"].WarmWhenIdle != "qwen" {
		t.Fatalf("warm_when_idle = %q", cfg.TagPolicies["gpu-4090"].WarmWhenIdle)
	}
}
