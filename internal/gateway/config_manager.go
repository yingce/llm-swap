package gateway

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"llm-swap/internal/config"

	"gopkg.in/yaml.v3"
)

type ConfigManager struct {
	mu          sync.RWMutex
	cfg         config.GatewayConfig
	fileCfg     config.GatewayConfig
	rawYAML     []byte
	version     int64
	configPath  string
	runtimePins runtimeConfigPins
}

type runtimeConfigPins struct {
	Tokens           config.TokenConfig
	ListenAddr       string
	ProxyAttempts    int
	PinProxyAttempts bool
}

type uiConfigResponse struct {
	Version int64                `json:"version"`
	Config  config.GatewayConfig `json:"config"`
	YAML    string               `json:"yaml"`
}

type uiConfigDryRunResponse struct {
	Valid                  bool             `json:"valid"`
	Version                int64            `json:"version"`
	Changes                []uiConfigChange `json:"changes"`
	Impacts                []uiConfigImpact `json:"impacts"`
	ApplyMode              string           `json:"apply_mode"`
	RequiresGatewayRestart bool             `json:"requires_gateway_restart"`
	Error                  string           `json:"error,omitempty"`
}

type uiConfigApplyResponse struct {
	Version                int64            `json:"version"`
	Changes                []uiConfigChange `json:"changes"`
	Impacts                []uiConfigImpact `json:"impacts"`
	ApplyMode              string           `json:"apply_mode"`
	RequiresGatewayRestart bool             `json:"requires_gateway_restart"`
}

type uiConfigChange struct {
	Path                   string `json:"path"`
	Type                   string `json:"type"`
	Model                  string `json:"model,omitempty"`
	RequiresWorkerRestart  bool   `json:"requires_worker_restart"`
	RequiresGatewayRestart bool   `json:"requires_gateway_restart"`
	Detail                 string `json:"detail,omitempty"`
}

type uiConfigImpact struct {
	Model                 string `json:"model"`
	WorkerID              string `json:"worker_id"`
	RunningState          string `json:"running_state,omitempty"`
	Loaded                bool   `json:"loaded"`
	RequiresWorkerRestart bool   `json:"requires_worker_restart"`
	Reason                string `json:"reason,omitempty"`
}

func NewConfigManager(cfg config.GatewayConfig, configPath string) *ConfigManager {
	return NewConfigManagerWithOverrides(cfg, configPath, config.GatewayRuntimeOverrides{})
}

func NewConfigManagerWithOverrides(cfg config.GatewayConfig, configPath string, overrides config.GatewayRuntimeOverrides) *ConfigManager {
	cfg = normalizeGatewayConfigForServer(cfg)
	fileCfg := cfg
	var rawYAML []byte
	if strings.TrimSpace(configPath) != "" {
		if raw, err := os.ReadFile(configPath); err == nil && len(raw) > 0 {
			rawYAML = append([]byte(nil), raw...)
			if loaded, err := config.LoadGateway(bytes.NewReader(raw)); err == nil {
				fileCfg = loaded
			}
		}
	}
	return &ConfigManager{
		cfg:        cloneGatewayConfig(cfg),
		fileCfg:    cloneGatewayConfig(fileCfg),
		rawYAML:    rawYAML,
		version:    1,
		configPath: configPath,
		runtimePins: runtimeConfigPins{
			Tokens:           cfg.Tokens,
			ListenAddr:       cfg.Gateway.ListenAddr,
			ProxyAttempts:    cfg.Gateway.ProxyAttempts,
			PinProxyAttempts: overrides.ProxyAttempts,
		},
	}
}

func normalizeGatewayConfigForServer(cfg config.GatewayConfig) config.GatewayConfig {
	if cfg.Gateway.ProxyAttempts == 0 {
		cfg.Gateway.ProxyAttempts = config.DefaultProxyAttempts
	}
	if cfg.Tokens.LlamaSwap == "" {
		cfg.Tokens.LlamaSwap = cfg.Tokens.Agent
	}
	if cfg.MetricsStore.Type == "" {
		cfg.MetricsStore.Type = "victoriametrics"
	}
	if cfg.MetricsStore.DefaultRange == "" {
		cfg.MetricsStore.DefaultRange = "1h"
	}
	if cfg.MetricsStore.MaxRange == "" {
		cfg.MetricsStore.MaxRange = "7d"
	}
	if cfg.MetricsStore.TimeoutMS <= 0 {
		cfg.MetricsStore.TimeoutMS = 3000
	}
	return cfg
}

func (m *ConfigManager) Snapshot() (config.GatewayConfig, int64) {
	if m == nil {
		return config.GatewayConfig{}, 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneGatewayConfig(m.cfg), m.version
}

func (m *ConfigManager) YAML() ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.RLock()
	if len(m.rawYAML) > 0 {
		out := append([]byte(nil), m.rawYAML...)
		m.mu.RUnlock()
		return out, nil
	}
	m.mu.RUnlock()
	cfg, _ := m.Snapshot()
	return yaml.Marshal(cfg)
}

func (m *ConfigManager) DryRun(raw []byte) (uiConfigDryRunResponse, config.GatewayConfig) {
	resp, runtimeCfg, _ := m.dryRun(raw)
	return resp, runtimeCfg
}

func (m *ConfigManager) dryRun(raw []byte) (uiConfigDryRunResponse, config.GatewayConfig, config.GatewayConfig) {
	current, version := m.Snapshot()
	fileCfg, err := config.LoadGateway(bytes.NewReader(raw))
	if err != nil {
		return uiConfigDryRunResponse{Valid: false, Version: version, Error: err.Error()}, current, current
	}
	runtimeCfg := m.applyRuntimePins(fileCfg)
	changes := diffGatewayConfig(current, runtimeCfg)
	changes = append(changes, m.processFieldChanges(fileCfg)...)
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Type < changes[j].Type
		}
		return changes[i].Path < changes[j].Path
	})
	return uiConfigDryRunResponse{
		Valid:                  true,
		Version:                version,
		Changes:                changes,
		ApplyMode:              applyModeForChanges(changes),
		RequiresGatewayRestart: changesRequireGatewayRestart(changes),
	}, runtimeCfg, fileCfg
}

func (m *ConfigManager) Apply(raw []byte) (uiConfigApplyResponse, error) {
	dryRun, runtimeCfg, fileCfg := m.dryRun(raw)
	if !dryRun.Valid {
		return uiConfigApplyResponse{}, errInvalidConfig{message: dryRun.Error}
	}
	if strings.TrimSpace(m.configPath) != "" {
		if err := os.MkdirAll(filepath.Dir(m.configPath), 0o755); err != nil {
			return uiConfigApplyResponse{}, err
		}
		if err := os.WriteFile(m.configPath, raw, 0o644); err != nil {
			return uiConfigApplyResponse{}, err
		}
	}
	if dryRun.RequiresGatewayRestart {
		m.mu.Lock()
		m.rawYAML = append([]byte(nil), raw...)
		m.fileCfg = cloneGatewayConfig(fileCfg)
		version := m.version
		m.mu.Unlock()
		return uiConfigApplyResponse{
			Version:                version,
			Changes:                dryRun.Changes,
			ApplyMode:              dryRun.ApplyMode,
			RequiresGatewayRestart: true,
		}, nil
	}
	m.mu.Lock()
	m.cfg = cloneGatewayConfig(runtimeCfg)
	m.fileCfg = cloneGatewayConfig(fileCfg)
	m.rawYAML = append([]byte(nil), raw...)
	m.version++
	version := m.version
	m.mu.Unlock()
	return uiConfigApplyResponse{
		Version:                version,
		Changes:                dryRun.Changes,
		ApplyMode:              dryRun.ApplyMode,
		RequiresGatewayRestart: dryRun.RequiresGatewayRestart,
	}, nil
}

func (m *ConfigManager) applyRuntimePins(cfg config.GatewayConfig) config.GatewayConfig {
	cfg.Tokens = m.runtimePins.Tokens
	cfg.Gateway.ListenAddr = m.runtimePins.ListenAddr
	if m.runtimePins.PinProxyAttempts {
		cfg.Gateway.ProxyAttempts = m.runtimePins.ProxyAttempts
	}
	return cfg
}

func (m *ConfigManager) processFieldChanges(candidateFileCfg config.GatewayConfig) []uiConfigChange {
	m.mu.RLock()
	baseFileCfg := cloneGatewayConfig(m.fileCfg)
	m.mu.RUnlock()
	changes := []uiConfigChange{}
	if baseFileCfg.Tokens != candidateFileCfg.Tokens {
		changes = append(changes, uiConfigChange{Path: "tokens", Type: "changed", RequiresGatewayRestart: true})
	}
	if baseFileCfg.Gateway.ListenAddr != candidateFileCfg.Gateway.ListenAddr {
		changes = append(changes, uiConfigChange{Path: "gateway.listen_addr", Type: "changed", RequiresGatewayRestart: true})
	}
	if m.runtimePins.PinProxyAttempts && baseFileCfg.Gateway.ProxyAttempts != candidateFileCfg.Gateway.ProxyAttempts {
		changes = append(changes, uiConfigChange{Path: "gateway.proxy_attempts", Type: "changed", RequiresGatewayRestart: true})
	}
	return changes
}

type errInvalidConfig struct {
	message string
}

func (e errInvalidConfig) Error() string {
	if e.message == "" {
		return "invalid config"
	}
	return e.message
}

func diffGatewayConfig(oldCfg config.GatewayConfig, newCfg config.GatewayConfig) []uiConfigChange {
	changes := []uiConfigChange{}
	for _, name := range sortedModelNames(newCfg.Models) {
		newModel := newCfg.Models[name]
		oldModel, ok := oldCfg.Models[name]
		if !ok {
			changes = append(changes, uiConfigChange{Path: "models." + name, Type: "added", Model: name})
			continue
		}
		if modelRuntimeFieldsChanged(oldModel, newModel) {
			changes = append(changes, uiConfigChange{
				Path:                  "models." + name,
				Type:                  "changed",
				Model:                 name,
				RequiresWorkerRestart: true,
				Detail:                "runtime command or artifact changed",
			})
			continue
		}
		if !reflect.DeepEqual(oldModel, newModel) {
			changes = append(changes, uiConfigChange{Path: "models." + name, Type: "changed", Model: name})
		}
	}
	for _, name := range sortedModelNames(oldCfg.Models) {
		if _, ok := newCfg.Models[name]; !ok {
			changes = append(changes, uiConfigChange{Path: "models." + name, Type: "removed", Model: name, RequiresWorkerRestart: true})
		}
	}
	if !reflect.DeepEqual(oldCfg.TagPolicies, newCfg.TagPolicies) {
		changes = append(changes, uiConfigChange{Path: "tag_policies", Type: "changed"})
	}
	if !reflect.DeepEqual(oldCfg.OSS, newCfg.OSS) {
		changes = append(changes, uiConfigChange{Path: "oss", Type: "changed"})
	}
	if oldCfg.Gateway.ListenAddr != newCfg.Gateway.ListenAddr {
		changes = append(changes, uiConfigChange{Path: "gateway.listen_addr", Type: "changed", RequiresGatewayRestart: true})
	}
	if oldCfg.Tokens != newCfg.Tokens {
		changes = append(changes, uiConfigChange{Path: "tokens", Type: "changed", RequiresGatewayRestart: true})
	}
	if !reflect.DeepEqual(oldCfg.MetricsStore, newCfg.MetricsStore) {
		changes = append(changes, uiConfigChange{Path: "metrics_store", Type: "changed", RequiresGatewayRestart: true})
	}
	if oldCfg.Gateway.ProxyAttempts != newCfg.Gateway.ProxyAttempts {
		changes = append(changes, uiConfigChange{Path: "gateway.proxy_attempts", Type: "changed"})
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Type < changes[j].Type
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

func changesRequireGatewayRestart(changes []uiConfigChange) bool {
	for _, change := range changes {
		if change.RequiresGatewayRestart {
			return true
		}
	}
	return false
}

func applyModeForChanges(changes []uiConfigChange) string {
	if changesRequireGatewayRestart(changes) {
		return "save_requires_gateway_restart"
	}
	return "hot_apply"
}

func modelRuntimeFieldsChanged(a config.Model, b config.Model) bool {
	return a.Run != b.Run ||
		a.Runtime != b.Runtime ||
		!reflect.DeepEqual(a.RuntimeArgs, b.RuntimeArgs) ||
		a.CmdStop != b.CmdStop ||
		a.CheckEndpoint != b.CheckEndpoint ||
		a.Artifact != b.Artifact
}

func sortedModelNames(models map[string]config.Model) []string {
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func cloneGatewayConfig(cfg config.GatewayConfig) config.GatewayConfig {
	out := cfg
	out.Models = make(map[string]config.Model, len(cfg.Models))
	for name, model := range cfg.Models {
		model.RuntimeArgs = append([]string(nil), model.RuntimeArgs...)
		out.Models[name] = model
	}
	out.TagPolicies = make(map[string]config.TagPolicy, len(cfg.TagPolicies))
	for tag, policy := range cfg.TagPolicies {
		policy.AllowedModels = append([]string(nil), policy.AllowedModels...)
		out.TagPolicies[tag] = policy
	}
	return out
}
