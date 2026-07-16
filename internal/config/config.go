package config

import "gopkg.in/yaml.v3"

type GatewayConfig struct {
	Gateway      GatewaySettings      `yaml:"gateway" json:"gateway"`
	OSS          OSSConfig            `yaml:"oss" json:"oss"`
	Tokens       TokenConfig          `yaml:"tokens" json:"tokens"`
	MetricsStore MetricsStoreConfig   `yaml:"metrics_store" json:"metrics_store"`
	RecordsStore RecordsStoreConfig   `yaml:"records_store" json:"records_store"`
	Models       map[string]Model     `yaml:"models" json:"models"`
	TagPolicies  map[string]TagPolicy `yaml:"tag_policies" json:"tag_policies"`
}

type GatewaySettings struct {
	ListenAddr    string `yaml:"listen_addr" json:"listen_addr"`
	ProxyAttempts int    `yaml:"proxy_attempts" json:"proxy_attempts"`
}

type OSSConfig struct {
	BaseURL string `yaml:"base_url" json:"base_url"`
}

type TokenConfig struct {
	Client    string `yaml:"client" json:"client"`
	Agent     string `yaml:"agent" json:"agent"`
	LlamaSwap string `yaml:"llama_swap" json:"llama_swap"`
}

type MetricsStoreConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	Type         string `yaml:"type" json:"type"`
	QueryURL     string `yaml:"query_url" json:"query_url"`
	DefaultRange string `yaml:"default_range" json:"default_range"`
	MaxRange     string `yaml:"max_range" json:"max_range"`
	TimeoutMS    int    `yaml:"timeout_ms" json:"timeout_ms"`
}

type RecordsStoreConfig struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	Type        string `yaml:"type" json:"type"`
	DSN         string `yaml:"dsn" json:"dsn"`
	GatewayID   string `yaml:"gateway_id" json:"gateway_id"`
	AutoMigrate bool   `yaml:"auto_migrate" json:"auto_migrate"`
	TimeoutMS   int    `yaml:"timeout_ms" json:"timeout_ms"`
}

type Model struct {
	Priority       int          `yaml:"priority" json:"priority"`
	MinLoaded      int          `yaml:"min_loaded" json:"min_loaded"`
	MaxLoaded      int          `yaml:"max_loaded" json:"max_loaded"`
	MaxLoadedSet   bool         `yaml:"-" json:"-"`
	MaxConcurrency int          `yaml:"max_concurrency" json:"max_concurrency"`
	MaxQueue       int          `yaml:"max_queue" json:"max_queue"`
	QueueTimeoutMS int          `yaml:"queue_timeout_ms" json:"queue_timeout_ms"`
	TTL            int          `yaml:"ttl" json:"ttl"`
	Artifact       Artifact     `yaml:"artifact" json:"artifact"`
	Run            string       `yaml:"run" json:"run"`
	Runtime        string       `yaml:"runtime" json:"runtime,omitempty"`
	RuntimeArgs    []string     `yaml:"runtime_args" json:"runtime_args,omitempty"`
	CmdStop        string       `yaml:"cmd_stop" json:"cmd_stop,omitempty"`
	CheckEndpoint  string       `yaml:"check_endpoint" json:"check_endpoint,omitempty"`
	Billing        ModelBilling `yaml:"billing" json:"billing,omitempty"`
}

type ModelBilling struct {
	PerRequestUSD            float64 `yaml:"per_request_usd" json:"per_request_usd,omitempty"`
	InputPerMillionUSD       float64 `yaml:"input_per_million_usd" json:"input_per_million_usd,omitempty"`
	OutputPerMillionUSD      float64 `yaml:"output_per_million_usd" json:"output_per_million_usd,omitempty"`
	CachedInputPerMillionUSD float64 `yaml:"cached_input_per_million_usd" json:"cached_input_per_million_usd,omitempty"`
}

func (m *Model) UnmarshalYAML(value *yaml.Node) error {
	type rawModel Model
	var raw rawModel
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*m = Model(raw)
	m.MaxLoadedSet = yamlMappingHasKey(value, "max_loaded")
	return nil
}

func (m Model) EffectiveMaxLoaded() int {
	if m.MaxLoadedSet || m.MaxLoaded > 0 {
		return m.MaxLoaded
	}
	return 0
}

func (m Model) HardMaxLoaded() int {
	if m.MaxLoadedSet || m.MaxLoaded > 0 {
		return m.MaxLoaded
	}
	return 0
}

func (m Model) MaxLoadedIsAuto() bool {
	return !m.MaxLoadedSet && m.MaxLoaded == 0
}

func yamlMappingHasKey(node *yaml.Node, key string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return true
		}
	}
	return false
}

type Artifact struct {
	Object    string `yaml:"object" json:"object"`
	Kind      string `yaml:"kind" json:"kind"`
	CRC64ECMA string `yaml:"crc64ecma" json:"crc64ecma"`
}

type TagPolicy struct {
	MaxConcurrency int            `yaml:"max_concurrency" json:"max_concurrency"`
	MaxQueue       int            `yaml:"max_queue" json:"max_queue"`
	WorkerDefaults WorkerDefaults `yaml:"worker_defaults" json:"worker_defaults"`
	AllowedModels  []string       `yaml:"allowed_models" json:"allowed_models"`
	WarmWhenIdle   string         `yaml:"warm_when_idle" json:"warm_when_idle"`
}

type WorkerDefaults struct {
	MaxConcurrency int `yaml:"max_concurrency" json:"max_concurrency"`
	MaxQueue       int `yaml:"max_queue" json:"max_queue"`
}

type AgentConfig struct {
	Agent struct {
		ID               string   `yaml:"id" json:"id"`
		Tags             []string `yaml:"tags" json:"tags"`
		ModelRoot        string   `yaml:"model_root" json:"model_root"`
		LlamaSwapConfig  string   `yaml:"llama_swap_config" json:"llama_swap_config"`
		LlamaSwapService string   `yaml:"llama_swap_service" json:"llama_swap_service"`
		RestartCommand   string   `yaml:"restart_command" json:"restart_command"`
		SwapURL          string   `yaml:"swap_url" json:"swap_url"`
		SwapPort         int      `yaml:"swap_port" json:"swap_port"`
		LlamaSwapURL     string   `yaml:"llama_swap_url" json:"llama_swap_url"`
		GatewayURL       string   `yaml:"gateway_url" json:"gateway_url"`
		Token            string   `yaml:"token" json:"token"`
		LlamaSwapToken   string   `yaml:"llama_swap_token" json:"llama_swap_token"`
	} `yaml:"agent" json:"agent"`
}
