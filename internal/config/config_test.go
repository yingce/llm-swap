package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadGatewayAcceptsModelDirectoryAndAlias(t *testing.T) {
	raw := strings.Replace(validGatewayYAML(""), "  qwen:\n", "  qwen:\n    model_dir: joyfox-model-20260720\n", 1)
	raw += "\nmodel_aliases:\n  joyfox-model-latest: qwen\n"

	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Models["qwen"].ModelDir; got != "joyfox-model-20260720" {
		t.Fatalf("model_dir = %q, want joyfox-model-20260720", got)
	}
	if got := cfg.ModelAliases["joyfox-model-latest"]; got != "qwen" {
		t.Fatalf("alias target = %q, want qwen", got)
	}
}

func TestLoadGatewayTransportFRPTCPValidation(t *testing.T) {
	validTransport := `
transport:
  type: frp_tcp
  frp:
    server_addr: frps.example.invalid
    server_port: 7000
    auth_token: transport-token
    port_start: 2000
    port_end: 2007
    lease_ttl_seconds: 180
`

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "valid", raw: validTransport},
		{name: "unsupported transport type", raw: strings.Replace(validTransport, "  type: frp_tcp", "  type: frp-tcp", 1), want: "transport.type"},
		{name: "near match transport type", raw: strings.Replace(validTransport, "  type: frp_tcp", "  type: frp_tcp_v2", 1), want: "transport.type"},
		{name: "missing address", raw: strings.Replace(validTransport, "    server_addr: frps.example.invalid\n", "", 1), want: "transport.frp.server_addr"},
		{name: "missing auth token", raw: strings.Replace(validTransport, "    auth_token: transport-token\n", "", 1), want: "transport.frp.auth_token"},
		{name: "invalid frps port", raw: strings.Replace(validTransport, "    server_port: 7000", "    server_port: 0", 1), want: "transport.frp.server_port"},
		{name: "frps port above range", raw: strings.Replace(validTransport, "    server_port: 7000", "    server_port: 65536", 1), want: "transport.frp.server_port"},
		{name: "reversed port range", raw: strings.Replace(validTransport, "    port_end: 2007", "    port_end: 1999", 1), want: "transport.frp.port_start"},
		{name: "port start below range", raw: strings.Replace(validTransport, "    port_start: 2000", "    port_start: 0", 1), want: "transport.frp.port_start"},
		{name: "out of range port", raw: strings.Replace(validTransport, "    port_start: 2000", "    port_start: 65536", 1), want: "transport.frp.port_start"},
		{name: "dial address is URL", raw: strings.Replace(validTransport, "    server_port: 7000", "    dial_addr: https://frps.example.invalid\n    server_port: 7000", 1), want: "transport.frp.dial_addr"},
		{name: "relative lease store", raw: strings.Replace(validTransport, "    lease_ttl_seconds: 180", "    lease_ttl_seconds: 180\n    lease_store_path: state/transport-leases.json", 1), want: "transport.frp.lease_store_path"},
		{name: "port end below range", raw: strings.Replace(validTransport, "    port_end: 2007", "    port_end: 0", 1), want: "transport.frp.port_end"},
		{name: "port end above range", raw: strings.Replace(validTransport, "    port_end: 2007", "    port_end: 65536", 1), want: "transport.frp.port_end"},
		{name: "zero ttl", raw: strings.Replace(validTransport, "    lease_ttl_seconds: 180", "    lease_ttl_seconds: 0", 1), want: "transport.frp.lease_ttl_seconds"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadGateway(strings.NewReader(validGatewayYAML(tt.raw)))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("LoadGateway returned error: %v", err)
				}
				if cfg.Transport.Type != "frp_tcp" {
					t.Fatalf("transport.type = %q, want frp_tcp", cfg.Transport.Type)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadGatewayAcceptsLegacyConfigWithoutTransport(t *testing.T) {
	cfg, err := LoadGateway(strings.NewReader(validGatewayYAML("")))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.Transport.Type != "" {
		t.Fatalf("transport.type = %q, want empty legacy mode", cfg.Transport.Type)
	}
	if cfg.Transport.FRP.LeaseTTLSeconds != 0 {
		t.Fatalf("legacy lease_ttl_seconds = %d, want 0", cfg.Transport.FRP.LeaseTTLSeconds)
	}
}

func TestLoadGatewayDefaultsFRPTCPLeaseTTLWhenOmitted(t *testing.T) {
	raw := `
transport:
  type: frp_tcp
  frp:
    server_addr: frps.example.invalid
    server_port: 7000
    auth_token: transport-token
    port_start: 2000
    port_end: 2007
`
	cfg, err := LoadGateway(strings.NewReader(validGatewayYAML(raw)))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.Transport.FRP.LeaseTTLSeconds != 180 {
		t.Fatalf("lease_ttl_seconds = %d, want 180", cfg.Transport.FRP.LeaseTTLSeconds)
	}
	if cfg.Transport.FRP.DialAddr != "frps.example.invalid" {
		t.Fatalf("dial_addr = %q, want server_addr compatibility default", cfg.Transport.FRP.DialAddr)
	}
	if cfg.Transport.FRP.LeaseStorePath != "" {
		t.Fatalf("lease_store_path = %q, want legacy config-directory default", cfg.Transport.FRP.LeaseStorePath)
	}
}

func TestLoadGatewayAcceptsExplicitFRPDialAndLeaseStorePaths(t *testing.T) {
	raw := `
transport:
  type: frp_tcp
  frp:
    server_addr: frps.example.invalid
    dial_addr: 127.0.0.1
    server_port: 7000
    auth_token: transport-token
    port_start: 2000
    port_end: 2007
    lease_store_path: /opt/llmswap/state/transport-leases.json
`
	cfg, err := LoadGateway(strings.NewReader(validGatewayYAML(raw)))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.Transport.FRP.DialAddr != "127.0.0.1" {
		t.Fatalf("dial_addr = %q, want loopback", cfg.Transport.FRP.DialAddr)
	}
	if cfg.Transport.FRP.LeaseStorePath != "/opt/llmswap/state/transport-leases.json" {
		t.Fatalf("lease_store_path = %q, want explicit durable path", cfg.Transport.FRP.LeaseStorePath)
	}
}

func TestApplyGatewayDefaultsPreservesNonzeroFRPTTL(t *testing.T) {
	base := GatewayConfig{Transport: TransportConfig{
		Type: "frp_tcp",
		FRP:  FRPTCPConfig{LeaseTTLSeconds: 90},
	}}

	tests := []struct {
		name string
		cfg  GatewayConfig
	}{
		{name: "programmatic", cfg: base},
		{name: "json round trip", cfg: jsonRoundTripGatewayConfig(t, base)},
		{name: "yaml round trip", cfg: yamlRoundTripGatewayConfig(t, base)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyGatewayDefaults(&tt.cfg)
			if tt.cfg.Transport.FRP.LeaseTTLSeconds != 90 {
				t.Fatalf("lease_ttl_seconds = %d, want 90", tt.cfg.Transport.FRP.LeaseTTLSeconds)
			}
		})
	}
}

func jsonRoundTripGatewayConfig(t *testing.T, cfg GatewayConfig) GatewayConfig {
	t.Helper()
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out GatewayConfig
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func yamlRoundTripGatewayConfig(t *testing.T, cfg GatewayConfig) GatewayConfig {
	t.Helper()
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out GatewayConfig
	if err := yaml.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLoadGatewayRejectsInvalidModelIdentity(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "missing alias target",
			raw:  validGatewayYAML("") + "\nmodel_aliases:\n  joyfox-model-latest: missing\n",
			want: "not defined",
		},
		{
			name: "alias and model collision",
			raw:  validGatewayYAML("") + "\nmodel_aliases:\n  qwen: qwen\n",
			want: "collides",
		},
		{
			name: "blank alias",
			raw:  validGatewayYAML("") + "\nmodel_aliases:\n  \"\": qwen\n",
			want: "non-empty trimmed alias and target",
		},
		{
			name: "nested directory",
			raw:  strings.Replace(validGatewayYAML(""), "  qwen:\n", "  qwen:\n    model_dir: nested/qwen\n", 1),
			want: "safe relative directory name",
		},
		{
			name: "traversal directory",
			raw:  strings.Replace(validGatewayYAML(""), "  qwen:\n", "  qwen:\n    model_dir: ..\n", 1),
			want: "safe relative directory name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadGateway(strings.NewReader(tt.raw))
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadGatewayRejectsTagReferenceToUndefinedModel(t *testing.T) {
	raw := strings.Replace(validGatewayYAML(""), "allowed_models: [qwen]", "allowed_models: [missing]", 1)

	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil || !strings.Contains(err.Error(), "tag gpu-4090 allowed model missing is not defined") {
		t.Fatalf("error = %v, want undefined allowed model", err)
	}
}

func TestLoadGatewayRejectsDuplicateModelDirectories(t *testing.T) {
	raw := strings.Replace(validGatewayYAML(""), "  qwen:\n", "  qwen:\n    model_dir: shared-model\n", 1)
	raw = strings.Replace(raw, "tag_policies:\n", `  llama:
    model_dir: shared-model
    artifact:
      object: llama.tar.gz
      kind: tar_gz
      crc64ecma: "456"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
`, 1)

	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "duplicate model_dir shared-model") {
		t.Fatalf("error = %v, want duplicate model_dir", err)
	}
}

func TestLoadGatewayRejectsOmittedModelDirectoriesThatCleanToSamePath(t *testing.T) {
	raw := strings.Replace(validGatewayYAML(""), "  qwen:\n", "  family/tmp/../v1:\n", 1)
	raw = strings.Replace(raw, "tag_policies:\n", `  family/v1:
    artifact:
      object: family-v1.tar.gz
      kind: tar_gz
      crc64ecma: "456"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
`, 1)
	raw = strings.Replace(raw, "allowed_models: [qwen]", "allowed_models: [family/tmp/../v1, family/v1]", 1)

	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "duplicate model_dir family/v1") {
		t.Fatalf("error = %v, want cleaned duplicate model_dir family/v1", err)
	}
}

func TestLoadGatewayRejectsReservedModelDirectory(t *testing.T) {
	raw := strings.Replace(validGatewayYAML(""), "  qwen:\n", "  qwen:\n    model_dir: .locks\n", 1)

	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("error = %v, want reserved model_dir error", err)
	}
}

func TestLoadGatewayRejectsArtifactSourceCacheCollision(t *testing.T) {
	raw := strings.Replace(validGatewayYAML(""), "  qwen:\n", "  qwen:\n    model_dir: model.gguf\n", 1)
	raw = strings.Replace(raw, "object: qwen.tar.gz", "object: releases/model.gguf", 1)
	raw = strings.Replace(raw, "kind: tar_gz", "kind: file", 1)

	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "artifact source") {
		t.Fatalf("error = %v, want artifact source cache collision", err)
	}
}

func TestLoadGatewayRejectsCrossModelArtifactSourceCacheCollision(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
models:
  a:
    model_dir: shared.gguf
    artifact:
      object: releases/a.gguf
      kind: file
      crc64ecma: "123"
    run: "serve {{model_path}}"
  b:
    model_dir: b-model
    artifact:
      object: releases/shared.gguf
      kind: file
      crc64ecma: "456"
    run: "serve {{model_path}}"
tag_policies:
  gpu-4090:
    allowed_models: [a, b]
`

	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "shared.gguf") || !strings.Contains(err.Error(), "artifact source") {
		t.Fatalf("error = %v, want cross-model artifact source cache collision", err)
	}
}

func TestLoadGatewayRejectsShellUnsafeModelDirectoryForRawRun(t *testing.T) {
	raw := strings.Replace(validGatewayYAML(""), "  qwen:\n", "  qwen:\n    model_dir: \"joyfox model;touch-pwned\"\n", 1)

	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "safe relative directory name") {
		t.Fatalf("error = %v, want shell-safe model_dir validation", err)
	}
}

func TestResolvedModelDirUsesConfiguredDirectoryOrModelName(t *testing.T) {
	if got := ResolvedModelDir("qwen", Model{}); got != "qwen" {
		t.Fatalf("ResolvedModelDir without override = %q, want qwen", got)
	}
	if got := ResolvedModelDir("qwen", Model{ModelDir: "joyfox-model-20260720"}); got != "joyfox-model-20260720" {
		t.Fatalf("ResolvedModelDir with override = %q, want joyfox-model-20260720", got)
	}
}

func TestModelDirectoryJSONNameAndOmitEmpty(t *testing.T) {
	omitted, err := json.Marshal(Model{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(omitted), "model_dir") {
		t.Fatalf("empty model_dir was not omitted: %s", omitted)
	}

	included, err := json.Marshal(Model{ModelDir: "joyfox-model-20260720"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(included), `"model_dir":"joyfox-model-20260720"`) {
		t.Fatalf("model_dir JSON field missing or misnamed: %s", included)
	}
}

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
	if !strings.Contains(err.Error(), "runtime must be vllm, vllm-omni, sglang, or llamacpp") {
		t.Fatalf("error = %v, want runtime validation", err)
	}
}

func TestLoadGatewayConfigAcceptsVLLMOmniRuntime(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
models:
  paw:
    artifact:
      object: paw.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    runtime: vllm-omni
tag_policies:
  gpu-4090:
    allowed_models: [paw]
`
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway() error = %v", err)
	}
	if got := cfg.Models["paw"].Runtime; got != "vllm-omni" {
		t.Fatalf("runtime = %q, want vllm-omni", got)
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
	if !strings.Contains(err.Error(), "agent.token") {
		t.Fatalf("error = %v, want agent.token", err)
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

func TestLoadAgentWithoutExternalURLDoesNotDefaultLlamaSwapToken(t *testing.T) {
	raw := `
agent:
  id: gpu-01
  tags: [gpu-4090]
  model_root: /data/models
  llama_swap_config: /etc/llama-swap/config.yaml
  gateway_url: http://gateway
  token: agent-token
`
	cfg, err := LoadAgent(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadAgent returned error: %v", err)
	}
	if cfg.Agent.LlamaSwapURL != "" {
		t.Fatalf("agent.llama_swap_url = %q, want empty", cfg.Agent.LlamaSwapURL)
	}
	if cfg.Agent.LlamaSwapToken != "" {
		t.Fatalf("agent.llama_swap_token = %q, want empty without legacy URL", cfg.Agent.LlamaSwapToken)
	}
}
