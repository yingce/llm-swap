package protocol

import (
	"time"

	"llm-swap/internal/config"
)

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

type AgentEvent struct {
	Time            time.Time `json:"time"`
	Event           string    `json:"event"`
	Model           string    `json:"model,omitempty"`
	Object          string    `json:"object,omitempty"`
	Kind            string    `json:"kind,omitempty"`
	CRC64ECMA       string    `json:"crc64ecma,omitempty"`
	DownloadedBytes int64     `json:"downloaded_bytes,omitempty"`
	TotalBytes      int64     `json:"total_bytes,omitempty"`
	Percent         float64   `json:"percent,omitempty"`
	DurationMS      int64     `json:"duration_ms,omitempty"`
	Error           string    `json:"error,omitempty"`
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
	Events        []AgentEvent          `json:"events,omitempty"`
}

type HeartbeatResponse struct {
	WorkerState    string `json:"worker_state"`
	RestartAllowed bool   `json:"restart_allowed"`
}
