package gateway

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"llm-swap/internal/config"

	"gopkg.in/yaml.v3"
)

const uiConfigRedactedSecret = "__LLMSWAP_REDACTED__"

type ConfigManager struct {
	applyMu        sync.Mutex
	mu             sync.RWMutex
	cfg            config.GatewayConfig
	fileCfg        config.GatewayConfig
	rawYAML        []byte
	version        int64
	revisionStore  ConfigRevisionStore
	promotionStore serviceNameArchiveStore
	configPath     string
	runtimePins    runtimeConfigPins
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
	manager, err := NewConfigManagerWithOverridesAndRevisionStore(context.Background(), cfg, configPath, overrides, NewMemoryConfigRevisionStore())
	if err != nil {
		panic(fmt.Sprintf("initialize in-memory configuration revision: %v", err))
	}
	return manager
}

func NewConfigManagerWithRevisionStore(ctx context.Context, cfg config.GatewayConfig, configPath string, store ConfigRevisionStore) (*ConfigManager, error) {
	return NewConfigManagerWithOverridesAndRevisionStore(ctx, cfg, configPath, config.GatewayRuntimeOverrides{}, store)
}

func NewConfigManagerWithOverridesAndRevisionStore(ctx context.Context, cfg config.GatewayConfig, configPath string, overrides config.GatewayRuntimeOverrides, store ConfigRevisionStore) (*ConfigManager, error) {
	if store == nil {
		return nil, fmt.Errorf("configuration revision store is required")
	}
	revision, err := store.Allocate(ctx)
	if err != nil {
		return nil, fmt.Errorf("allocate startup configuration revision: %w", err)
	}
	if revision <= 0 {
		return nil, fmt.Errorf("allocate startup configuration revision: store returned non-positive revision %d", revision)
	}
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
		cfg:            cloneGatewayConfig(cfg),
		fileCfg:        cloneGatewayConfig(fileCfg),
		rawYAML:        rawYAML,
		version:        revision,
		revisionStore:  store,
		promotionStore: promotionStoreForConfigPath(configPath),
		configPath:     configPath,
		runtimePins: runtimeConfigPins{
			Tokens:           cfg.Tokens,
			ListenAddr:       cfg.Gateway.ListenAddr,
			ProxyAttempts:    cfg.Gateway.ProxyAttempts,
			PinProxyAttempts: overrides.ProxyAttempts,
		},
	}, nil
}

func promotionStoreForConfigPath(configPath string) serviceNameArchiveStore {
	if strings.TrimSpace(configPath) == "" {
		return newMemoryServiceNameArchiveStore()
	}
	return newFileServiceNameArchiveStore(filepath.Join(filepath.Dir(configPath), "service-name-promotions.json"))
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

func (m *ConfigManager) UIConfig() (uiConfigResponse, error) {
	if m == nil {
		return uiConfigResponse{}, nil
	}
	m.mu.RLock()
	cfg := cloneGatewayConfig(m.cfg)
	version := m.version
	raw := append([]byte(nil), m.rawYAML...)
	m.mu.RUnlock()

	if len(raw) == 0 {
		var err error
		raw, err = yaml.Marshal(cfg)
		if err != nil {
			return uiConfigResponse{}, err
		}
	}
	redactedYAML, err := replaceFRPAuthTokenYAML(raw, "", uiConfigRedactedSecret)
	if err != nil {
		return uiConfigResponse{}, err
	}
	if cfg.Transport.FRP.AuthToken != "" {
		cfg.Transport.FRP.AuthToken = uiConfigRedactedSecret
	}
	return uiConfigResponse{Version: version, Config: cfg, YAML: string(redactedYAML)}, nil
}

func (m *ConfigManager) DryRun(raw []byte) (uiConfigDryRunResponse, config.GatewayConfig) {
	resp, runtimeCfg, _, _ := m.dryRun(raw)
	return resp, runtimeCfg
}

func (m *ConfigManager) dryRun(raw []byte) (uiConfigDryRunResponse, config.GatewayConfig, config.GatewayConfig, []byte) {
	current, version := m.Snapshot()
	preparedRaw, err := m.restoreRedactedFRPAuthToken(raw)
	if err != nil {
		return uiConfigDryRunResponse{Valid: false, Version: version, Error: err.Error()}, current, current, nil
	}
	fileCfg, err := config.LoadGateway(bytes.NewReader(preparedRaw))
	if err != nil {
		return uiConfigDryRunResponse{Valid: false, Version: version, Error: err.Error()}, current, current, nil
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
	}, runtimeCfg, fileCfg, preparedRaw
}

func (m *ConfigManager) Apply(raw []byte) (uiConfigApplyResponse, error) {
	return m.ApplyContext(context.Background(), raw)
}

func (m *ConfigManager) ApplyContext(ctx context.Context, raw []byte) (uiConfigApplyResponse, error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	dryRun, runtimeCfg, fileCfg, preparedRaw := m.dryRun(raw)
	if !dryRun.Valid {
		return uiConfigApplyResponse{}, errInvalidConfig{message: dryRun.Error}
	}
	var revision int64
	if !dryRun.RequiresGatewayRestart {
		var err error
		revision, err = m.revisionStore.Allocate(ctx)
		if err != nil {
			return uiConfigApplyResponse{}, fmt.Errorf("allocate hot-apply configuration revision: %w", err)
		}
		if revision <= dryRun.Version {
			return uiConfigApplyResponse{}, fmt.Errorf("allocate hot-apply configuration revision: store returned %d after %d", revision, dryRun.Version)
		}
	}
	if strings.TrimSpace(m.configPath) != "" {
		if err := os.MkdirAll(filepath.Dir(m.configPath), 0o755); err != nil {
			return uiConfigApplyResponse{}, err
		}
		if err := os.WriteFile(m.configPath, preparedRaw, 0o644); err != nil {
			return uiConfigApplyResponse{}, err
		}
	}
	if dryRun.RequiresGatewayRestart {
		m.mu.Lock()
		m.rawYAML = append([]byte(nil), preparedRaw...)
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
	m.rawYAML = append([]byte(nil), preparedRaw...)
	m.version = revision
	version := revision
	m.mu.Unlock()
	return uiConfigApplyResponse{
		Version:                version,
		Changes:                dryRun.Changes,
		ApplyMode:              dryRun.ApplyMode,
		RequiresGatewayRestart: dryRun.RequiresGatewayRestart,
	}, nil
}

func (m *ConfigManager) restoreRedactedFRPAuthToken(raw []byte) ([]byte, error) {
	m.mu.RLock()
	secret := m.fileCfg.Transport.FRP.AuthToken
	m.mu.RUnlock()
	return replaceFRPAuthTokenYAML(raw, uiConfigRedactedSecret, secret)
}

func replaceFRPAuthTokenYAML(raw []byte, match string, replacement string) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	authTokens := yamlPathContributors(&document, "transport", "frp", "auth_token")
	if len(authTokens) == 0 {
		return append([]byte(nil), raw...), nil
	}
	changed := false
	for _, authToken := range authTokens {
		if authToken == nil || authToken.Value == "" || (match != "" && authToken.Value != match) {
			continue
		}
		if match == uiConfigRedactedSecret && (replacement == "" || replacement == uiConfigRedactedSecret) {
			return nil, fmt.Errorf("transport.frp.auth_token uses the redaction marker but there is no current secret to preserve")
		}
		authToken.Kind = yaml.ScalarNode
		authToken.Tag = "!!str"
		authToken.Value = replacement
		authToken.Style = 0
		changed = true
	}
	if !changed {
		return append([]byte(nil), raw...), nil
	}
	return yaml.Marshal(&document)
}

func yamlMappingValue(node *yaml.Node, path ...string) *yaml.Node {
	for _, key := range path {
		node = yamlLookupMappingKey(node, key, make(map[*yaml.Node]bool))
		if node == nil {
			return nil
		}
	}
	return yamlResolveAlias(node)
}

func yamlLookupMappingKey(node *yaml.Node, key string, visiting map[*yaml.Node]bool) *yaml.Node {
	node = yamlDocumentRoot(node)
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode {
		if node.Alias == nil || visiting[node] {
			return nil
		}
		visiting[node] = true
		defer delete(visiting, node)
		return yamlLookupMappingKey(node.Alias, key, visiting)
	}
	if node.Kind != yaml.MappingNode || visiting[node] {
		return nil
	}
	visiting[node] = true
	defer delete(visiting, node)

	// YAML merge keys only supply defaults. An explicit key on the mapping
	// always wins, regardless of where the merge key appears.
	for i := 0; i+1 < len(node.Content); i += 2 {
		if isYAMLMergeKey(node.Content[i]) {
			continue
		}
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if !isYAMLMergeKey(node.Content[i]) {
			continue
		}
		if value := yamlLookupMergedKey(node.Content[i+1], key, visiting); value != nil {
			return value
		}
	}
	return nil
}

func yamlLookupMergedKey(node *yaml.Node, key string, visiting map[*yaml.Node]bool) *yaml.Node {
	node = yamlDocumentRoot(node)
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.AliasNode, yaml.MappingNode:
		return yamlLookupMappingKey(node, key, visiting)
	case yaml.SequenceNode:
		if visiting[node] {
			return nil
		}
		visiting[node] = true
		defer delete(visiting, node)
		for _, candidate := range node.Content {
			if value := yamlLookupMergedKey(candidate, key, visiting); value != nil {
				return value
			}
		}
	}
	return nil
}

func yamlMappingContributors(node *yaml.Node, key string) []*yaml.Node {
	values := make([]*yaml.Node, 0, 1)
	yamlCollectMappingContributors(node, key, make(map[*yaml.Node]bool), make(map[*yaml.Node]bool), &values)
	return values
}

func yamlPathContributors(node *yaml.Node, path ...string) []*yaml.Node {
	current := []*yaml.Node{yamlDocumentRoot(node)}
	for _, key := range path {
		next := make([]*yaml.Node, 0, len(current))
		seen := make(map[*yaml.Node]bool)
		for _, parent := range current {
			for _, value := range yamlMappingContributors(parent, key) {
				if value == nil || seen[value] {
					continue
				}
				seen[value] = true
				next = append(next, value)
			}
		}
		if len(next) == 0 {
			return nil
		}
		current = next
	}
	return current
}

func yamlCollectMappingContributors(node *yaml.Node, key string, visiting map[*yaml.Node]bool, seen map[*yaml.Node]bool, values *[]*yaml.Node) {
	node = yamlDocumentRoot(node)
	if node == nil {
		return
	}
	if node.Kind == yaml.AliasNode {
		if node.Alias == nil || visiting[node] {
			return
		}
		visiting[node] = true
		defer delete(visiting, node)
		yamlCollectMappingContributors(node.Alias, key, visiting, seen, values)
		return
	}
	if node.Kind != yaml.MappingNode || visiting[node] {
		return
	}
	visiting[node] = true
	defer delete(visiting, node)

	for i := 0; i+1 < len(node.Content); i += 2 {
		if isYAMLMergeKey(node.Content[i]) || node.Content[i].Value != key {
			continue
		}
		value := yamlResolveAlias(node.Content[i+1])
		if value != nil && !seen[value] {
			seen[value] = true
			*values = append(*values, value)
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if isYAMLMergeKey(node.Content[i]) {
			yamlCollectMergedContributors(node.Content[i+1], key, visiting, seen, values)
		}
	}
}

func yamlCollectMergedContributors(node *yaml.Node, key string, visiting map[*yaml.Node]bool, seen map[*yaml.Node]bool, values *[]*yaml.Node) {
	node = yamlDocumentRoot(node)
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.AliasNode, yaml.MappingNode:
		yamlCollectMappingContributors(node, key, visiting, seen, values)
	case yaml.SequenceNode:
		if visiting[node] {
			return
		}
		visiting[node] = true
		defer delete(visiting, node)
		for _, candidate := range node.Content {
			yamlCollectMergedContributors(candidate, key, visiting, seen, values)
		}
	}
}

func yamlDocumentRoot(node *yaml.Node) *yaml.Node {
	for node != nil && node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil
		}
		node = node.Content[0]
	}
	return node
}

func yamlResolveAlias(node *yaml.Node) *yaml.Node {
	visited := make(map[*yaml.Node]bool)
	node = yamlDocumentRoot(node)
	for node != nil && node.Kind == yaml.AliasNode {
		if node.Alias == nil || visited[node] {
			return nil
		}
		visited[node] = true
		node = yamlDocumentRoot(node.Alias)
	}
	return node
}

func isYAMLMergeKey(node *yaml.Node) bool {
	return node != nil && (node.Tag == "!!merge" || node.Value == "<<")
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
	for _, alias := range sortedAliasNames(oldCfg.ModelAliases, newCfg.ModelAliases) {
		oldTarget, oldOK := oldCfg.ModelAliases[alias]
		newTarget, newOK := newCfg.ModelAliases[alias]
		switch {
		case !oldOK:
			changes = append(changes, uiConfigChange{Path: "model_aliases." + alias, Type: "added"})
		case !newOK:
			changes = append(changes, uiConfigChange{Path: "model_aliases." + alias, Type: "removed"})
		case oldTarget != newTarget:
			changes = append(changes, uiConfigChange{Path: "model_aliases." + alias, Type: "changed"})
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
	if oldCfg.Transport.FRP.LeaseStorePath != newCfg.Transport.FRP.LeaseStorePath {
		changes = append(changes, uiConfigChange{Path: "transport.frp.lease_store_path", Type: "changed", RequiresGatewayRestart: true})
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
	return a.ModelDir != b.ModelDir ||
		a.Run != b.Run ||
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

func sortedAliasNames(aliasMaps ...map[string]string) []string {
	set := map[string]struct{}{}
	for _, aliases := range aliasMaps {
		for alias := range aliases {
			set[alias] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for alias := range set {
		names = append(names, alias)
	}
	sort.Strings(names)
	return names
}

func cloneGatewayConfig(cfg config.GatewayConfig) config.GatewayConfig {
	out := cfg
	out.Models = make(map[string]config.Model, len(cfg.Models))
	for name, model := range cfg.Models {
		model.RuntimeArgs = append([]string(nil), model.RuntimeArgs...)
		model.TagCapacity = cloneWorkerDefaultsMap(model.TagCapacity)
		out.Models[name] = model
	}
	out.ModelAliases = make(map[string]string, len(cfg.ModelAliases))
	for alias, target := range cfg.ModelAliases {
		out.ModelAliases[alias] = target
	}
	out.TagPolicies = make(map[string]config.TagPolicy, len(cfg.TagPolicies))
	for tag, policy := range cfg.TagPolicies {
		policy.AllowedModels = append([]string(nil), policy.AllowedModels...)
		out.TagPolicies[tag] = policy
	}
	return out
}

func cloneWorkerDefaultsMap(values map[string]config.WorkerDefaults) map[string]config.WorkerDefaults {
	if values == nil {
		return nil
	}
	out := make(map[string]config.WorkerDefaults, len(values))
	for tag, capacity := range values {
		out[tag] = capacity
	}
	return out
}
