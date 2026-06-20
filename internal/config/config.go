package config

type GatewayConfig struct {
	OSS         OSSConfig            `yaml:"oss"`
	Tokens      TokenConfig          `yaml:"tokens"`
	Models      map[string]Model     `yaml:"models"`
	TagPolicies map[string]TagPolicy `yaml:"tag_policies"`
}

type OSSConfig struct {
	BaseURL string `yaml:"base_url"`
}

type TokenConfig struct {
	Client    string `yaml:"client"`
	Agent     string `yaml:"agent"`
	LlamaSwap string `yaml:"llama_swap"`
}

type Model struct {
	Priority       int      `yaml:"priority"`
	MinLoaded      int      `yaml:"min_loaded"`
	MaxLoaded      int      `yaml:"max_loaded"`
	MaxConcurrency int      `yaml:"max_concurrency"`
	MaxQueue       int      `yaml:"max_queue"`
	QueueTimeoutMS int      `yaml:"queue_timeout_ms"`
	TTL            int      `yaml:"ttl"`
	Artifact       Artifact `yaml:"artifact"`
	Run            string   `yaml:"run"`
	CmdStop        string   `yaml:"cmd_stop"`
}

type Artifact struct {
	Object    string `yaml:"object"`
	Kind      string `yaml:"kind"`
	CRC64ECMA string `yaml:"crc64ecma"`
}

type TagPolicy struct {
	MaxConcurrency int            `yaml:"max_concurrency"`
	MaxQueue       int            `yaml:"max_queue"`
	WorkerDefaults WorkerDefaults `yaml:"worker_defaults"`
	AllowedModels  []string       `yaml:"allowed_models"`
	WarmWhenIdle   string         `yaml:"warm_when_idle"`
}

type WorkerDefaults struct {
	MaxConcurrency int `yaml:"max_concurrency"`
	MaxQueue       int `yaml:"max_queue"`
}

type AgentConfig struct {
	Agent struct {
		ID               string   `yaml:"id"`
		Tags             []string `yaml:"tags"`
		ModelRoot        string   `yaml:"model_root"`
		LlamaSwapConfig  string   `yaml:"llama_swap_config"`
		LlamaSwapService string   `yaml:"llama_swap_service"`
		LlamaSwapURL     string   `yaml:"llama_swap_url"`
		GatewayURL       string   `yaml:"gateway_url"`
		Token            string   `yaml:"token"`
	} `yaml:"agent"`
}
