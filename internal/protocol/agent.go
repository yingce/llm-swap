package protocol

import "llm-swap/internal/config"

type AgentConfigResponse struct {
	OSS       config.OSSConfig        `yaml:"oss" json:"oss"`
	Models    map[string]config.Model `yaml:"models" json:"models"`
	TagPolicy AgentTagPolicy          `yaml:"tag_policy" json:"tag_policy"`
}

type AgentTagPolicy struct {
	Tag            string                `yaml:"tag" json:"tag"`
	AllowedModels  []string              `yaml:"allowed_models" json:"allowed_models"`
	WarmWhenIdle   string                `yaml:"warm_when_idle" json:"warm_when_idle"`
	WorkerDefaults config.WorkerDefaults `yaml:"worker_defaults" json:"worker_defaults"`
}

type RunningModel struct {
	Model string `json:"model"`
	State string `json:"state"`
}

type HeartbeatRequest struct {
	AgentID       string                `json:"agent_id"`
	Tags          []string              `json:"tags"`
	LlamaSwapURL  string                `json:"llama_swap_url"`
	RunningModels []RunningModel        `json:"running_models"`
	Artifacts     map[string]string     `json:"artifacts"`
	Capacity      config.WorkerDefaults `json:"capacity"`
	NeedsRestart  bool                  `json:"needs_restart"`
	LastError     string                `json:"last_error"`
}

type HeartbeatResponse struct {
	WorkerState    string `json:"worker_state"`
	RestartAllowed bool   `json:"restart_allowed"`
}
