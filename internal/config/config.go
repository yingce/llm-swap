package config

type GatewayConfig struct {
	OSS         OSSConfig            `yaml:"oss" json:"oss"`
	Tokens      TokenConfig          `yaml:"tokens" json:"tokens"`
	Models      map[string]Model     `yaml:"models" json:"models"`
	TagPolicies map[string]TagPolicy `yaml:"tag_policies" json:"tag_policies"`
}

type OSSConfig struct {
	BaseURL string `yaml:"base_url" json:"base_url"`
}

type TokenConfig struct {
	Client    string `yaml:"client" json:"client"`
	Agent     string `yaml:"agent" json:"agent"`
	LlamaSwap string `yaml:"llama_swap" json:"llama_swap"`
}

type Model struct {
	Priority       int      `yaml:"priority" json:"priority"`
	MinLoaded      int      `yaml:"min_loaded" json:"min_loaded"`
	MaxLoaded      int      `yaml:"max_loaded" json:"max_loaded"`
	MaxConcurrency int      `yaml:"max_concurrency" json:"max_concurrency"`
	MaxQueue       int      `yaml:"max_queue" json:"max_queue"`
	QueueTimeoutMS int      `yaml:"queue_timeout_ms" json:"queue_timeout_ms"`
	TTL            int      `yaml:"ttl" json:"ttl"`
	Artifact       Artifact `yaml:"artifact" json:"artifact"`
	Run            string   `yaml:"run" json:"run"`
	CmdStop        string   `yaml:"cmd_stop" json:"cmd_stop,omitempty"`
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
		LlamaSwapURL     string   `yaml:"llama_swap_url" json:"llama_swap_url"`
		GatewayURL       string   `yaml:"gateway_url" json:"gateway_url"`
		Token            string   `yaml:"token" json:"token"`
	} `yaml:"agent" json:"agent"`
}
