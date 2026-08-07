package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"llm-swap/internal/config"

	"gopkg.in/yaml.v3"
)

const DefaultGatewayServiceNameArchivePath = "/opt/llmswap/state/service-name-promotions.json"

type serviceNameArchive struct {
	ArchiveID         string                      `json:"archive_id"`
	ServiceName       string                      `json:"service_name"`
	TargetModel       string                      `json:"target_model"`
	Model             config.Model                `json:"model"`
	ModelMaxLoadedSet bool                        `json:"model_max_loaded_set"`
	BeforeTagPolicies map[string]config.TagPolicy `json:"before_tag_policies"`
	AfterTagPolicies  map[string]config.TagPolicy `json:"after_tag_policies"`
	CreatedAt         time.Time                   `json:"created_at"`
	RolledBack        bool                        `json:"rolled_back,omitempty"`
}

type serviceNameArchiveStore interface {
	Put(context.Context, serviceNameArchive) error
	Get(context.Context, string) (serviceNameArchive, bool, error)
	Delete(context.Context, string) error
}

type memoryServiceNameArchiveStore struct {
	mu       sync.Mutex
	archives map[string]serviceNameArchive
}

func newMemoryServiceNameArchiveStore() *memoryServiceNameArchiveStore {
	return &memoryServiceNameArchiveStore{archives: map[string]serviceNameArchive{}}
}

func (s *memoryServiceNameArchiveStore) Put(_ context.Context, archive serviceNameArchive) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archives[archive.ArchiveID] = archive
	return nil
}

func (s *memoryServiceNameArchiveStore) Get(_ context.Context, id string) (serviceNameArchive, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	archive, ok := s.archives[id]
	return archive, ok, nil
}

func (s *memoryServiceNameArchiveStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.archives, id)
	return nil
}

type fileServiceNameArchiveStore struct {
	mu   sync.Mutex
	path string
}

type serviceNameArchiveDocument struct {
	Version  int                           `json:"version"`
	Archives map[string]serviceNameArchive `json:"archives"`
}

func newFileServiceNameArchiveStore(path string) *fileServiceNameArchiveStore {
	return &fileServiceNameArchiveStore{path: path}
}

func (s *fileServiceNameArchiveStore) Put(ctx context.Context, archive serviceNameArchive) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	doc.Archives[archive.ArchiveID] = archive
	return writeJSONFileAtomically(s.path, doc, 0o600)
}

func (s *fileServiceNameArchiveStore) Get(ctx context.Context, id string) (serviceNameArchive, bool, error) {
	if err := ctx.Err(); err != nil {
		return serviceNameArchive{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return serviceNameArchive{}, false, err
	}
	archive, ok := doc.Archives[id]
	return archive, ok, nil
}

func (s *fileServiceNameArchiveStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	delete(doc.Archives, id)
	return writeJSONFileAtomically(s.path, doc, 0o600)
}

func (s *fileServiceNameArchiveStore) load() (serviceNameArchiveDocument, error) {
	doc := serviceNameArchiveDocument{Version: 1, Archives: map[string]serviceNameArchive{}}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return doc, fmt.Errorf("read service-name archive: %w", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("decode service-name archive: %w", err)
	}
	if doc.Version != 1 {
		return doc, fmt.Errorf("unsupported service-name archive version %d", doc.Version)
	}
	if doc.Archives == nil {
		doc.Archives = map[string]serviceNameArchive{}
	}
	return doc, nil
}

func writeJSONFileAtomically(path string, value any, mode os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("persistent path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".service-name-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

type serviceNamePromotionRequest struct {
	ServiceName string `json:"service_name"`
	TargetModel string `json:"target_model"`
	ArchiveID   string `json:"archive_id,omitempty"`
}

type uiServiceNameTransactionResponse struct {
	Action      string `json:"action"`
	ServiceName string `json:"service_name"`
	TargetModel string `json:"target_model"`
	ArchiveID   string `json:"archive_id"`
	Version     int64  `json:"version"`
}

type serviceNameConflict struct{ message string }

func (e serviceNameConflict) Error() string { return e.message }

func (m *ConfigManager) PromoteServiceName(ctx context.Context, req serviceNamePromotionRequest, validate func(config.GatewayConfig) error) (uiServiceNameTransactionResponse, error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	current, fileCurrent, version := m.serviceNameConfigSnapshot()
	if !sameServiceNameNamespace(current, fileCurrent) {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "persisted model namespace has pending restart changes; restart gateway before promotion"}
	}
	serviceName, target := strings.TrimSpace(req.ServiceName), strings.TrimSpace(req.TargetModel)
	if serviceName == "" || target == "" {
		return uiServiceNameTransactionResponse{}, errInvalidConfig{message: "service_name and target_model are required"}
	}
	old, exists := current.Models[serviceName]
	if !exists {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "service name must be an existing canonical model"}
	}
	if !old.Disabled {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "old canonical model must be disabled"}
	}
	if _, exists := current.ModelAliases[serviceName]; exists {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "service name already exists as an alias"}
	}
	if serviceName == target {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "target model must differ from the service name"}
	}
	if targetModel, exists := current.Models[target]; !exists {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "target canonical model is not defined"}
	} else if targetModel.Disabled {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "target canonical model is disabled"}
	}
	if validate != nil {
		if err := validate(current); err != nil {
			return uiServiceNameTransactionResponse{}, err
		}
	}

	next := cloneGatewayConfig(current)
	delete(next.Models, serviceName)
	if next.ModelAliases == nil {
		next.ModelAliases = map[string]string{}
	}
	next.ModelAliases[serviceName] = target
	beforePolicies, afterPolicies := map[string]config.TagPolicy{}, map[string]config.TagPolicy{}
	for tag, policy := range next.TagPolicies {
		changed := false
		filtered := make([]string, 0, len(policy.AllowedModels))
		for _, name := range policy.AllowedModels {
			if name == serviceName {
				changed = true
				continue
			}
			filtered = append(filtered, name)
		}
		policy.AllowedModels = filtered
		if policy.WarmWhenIdle == serviceName {
			policy.WarmWhenIdle = ""
			changed = true
		}
		if changed {
			beforePolicies[tag] = current.TagPolicies[tag]
			afterPolicies[tag] = policy
			next.TagPolicies[tag] = policy
		}
	}
	fileNext := cloneGatewayConfig(fileCurrent)
	namespace := cloneGatewayConfig(next)
	fileNext.Models, fileNext.ModelAliases, fileNext.TagPolicies = namespace.Models, namespace.ModelAliases, namespace.TagPolicies
	prepared, fileCfg, err := m.prepareTransactionConfig(fileNext)
	if err != nil {
		return uiServiceNameTransactionResponse{}, err
	}
	revision, err := m.allocateTransactionRevision(ctx, version)
	if err != nil {
		return uiServiceNameTransactionResponse{}, err
	}
	id, err := randomArchiveID()
	if err != nil {
		return uiServiceNameTransactionResponse{}, err
	}
	archive := serviceNameArchive{ArchiveID: id, ServiceName: serviceName, TargetModel: target, Model: old, ModelMaxLoadedSet: old.MaxLoadedSet, BeforeTagPolicies: beforePolicies, AfterTagPolicies: afterPolicies, CreatedAt: time.Now().UTC()}
	if err := m.promotionStore.Put(ctx, archive); err != nil {
		return uiServiceNameTransactionResponse{}, fmt.Errorf("persist service-name archive: %w", err)
	}
	if err := m.persistTransactionConfig(prepared); err != nil {
		_ = m.promotionStore.Delete(context.Background(), archive.ArchiveID)
		return uiServiceNameTransactionResponse{}, fmt.Errorf("persist promoted config: %w", err)
	}
	m.commitTransactionConfig(next, fileCfg, prepared, revision)
	return uiServiceNameTransactionResponse{Action: "promote", ServiceName: serviceName, TargetModel: target, ArchiveID: id, Version: revision}, nil
}

func (m *ConfigManager) RollbackServiceName(ctx context.Context, req serviceNamePromotionRequest) (uiServiceNameTransactionResponse, error) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	current, fileCurrent, version := m.serviceNameConfigSnapshot()
	if !sameServiceNameNamespace(current, fileCurrent) {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "persisted model namespace has pending restart changes; restart gateway before rollback"}
	}
	archive, ok, err := m.promotionStore.Get(ctx, strings.TrimSpace(req.ArchiveID))
	if err != nil {
		return uiServiceNameTransactionResponse{}, fmt.Errorf("read service-name archive: %w", err)
	}
	if !ok || archive.RolledBack {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "promotion archive is not available"}
	}
	if archive.ServiceName != strings.TrimSpace(req.ServiceName) || archive.TargetModel != strings.TrimSpace(req.TargetModel) {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "alias, target, and archive do not match"}
	}
	if _, exists := current.Models[archive.ServiceName]; exists {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "active model namespace changed after promotion"}
	}
	if current.ModelAliases[archive.ServiceName] != archive.TargetModel {
		return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "service alias changed after promotion"}
	}
	for tag, expected := range archive.AfterTagPolicies {
		if !reflect.DeepEqual(current.TagPolicies[tag], expected) {
			return uiServiceNameTransactionResponse{}, serviceNameConflict{message: "tag policy changed after promotion"}
		}
	}

	next := cloneGatewayConfig(current)
	delete(next.ModelAliases, archive.ServiceName)
	next.Models[archive.ServiceName] = archive.Model
	restored := next.Models[archive.ServiceName]
	restored.MaxLoadedSet = archive.ModelMaxLoadedSet
	next.Models[archive.ServiceName] = restored
	for tag, policy := range archive.BeforeTagPolicies {
		next.TagPolicies[tag] = policy
	}
	fileNext := cloneGatewayConfig(fileCurrent)
	namespace := cloneGatewayConfig(next)
	fileNext.Models, fileNext.ModelAliases, fileNext.TagPolicies = namespace.Models, namespace.ModelAliases, namespace.TagPolicies
	prepared, fileCfg, err := m.prepareTransactionConfig(fileNext)
	if err != nil {
		return uiServiceNameTransactionResponse{}, err
	}
	revision, err := m.allocateTransactionRevision(ctx, version)
	if err != nil {
		return uiServiceNameTransactionResponse{}, err
	}
	if err := m.persistTransactionConfig(prepared); err != nil {
		return uiServiceNameTransactionResponse{}, fmt.Errorf("persist rollback config: %w", err)
	}
	m.commitTransactionConfig(next, fileCfg, prepared, revision)
	archive.RolledBack = true
	_ = m.promotionStore.Put(context.Background(), archive)
	return uiServiceNameTransactionResponse{Action: "rollback", ServiceName: archive.ServiceName, TargetModel: archive.TargetModel, ArchiveID: archive.ArchiveID, Version: revision}, nil
}

func (m *ConfigManager) serviceNameConfigSnapshot() (config.GatewayConfig, config.GatewayConfig, int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneGatewayConfig(m.cfg), cloneGatewayConfig(m.fileCfg), m.version
}

func sameServiceNameNamespace(runtimeCfg, fileCfg config.GatewayConfig) bool {
	return reflect.DeepEqual(runtimeCfg.Models, fileCfg.Models) &&
		reflect.DeepEqual(runtimeCfg.ModelAliases, fileCfg.ModelAliases) &&
		reflect.DeepEqual(runtimeCfg.TagPolicies, fileCfg.TagPolicies)
}

func (m *ConfigManager) prepareTransactionConfig(next config.GatewayConfig) ([]byte, config.GatewayConfig, error) {
	raw, err := marshalGatewayConfigPreservingAutoMax(next)
	if err != nil {
		return nil, config.GatewayConfig{}, err
	}
	fileCfg, err := config.LoadGateway(strings.NewReader(string(raw)))
	if err != nil {
		return nil, config.GatewayConfig{}, errInvalidConfig{message: err.Error()}
	}
	return raw, fileCfg, nil
}

func marshalGatewayConfigPreservingAutoMax(cfg config.GatewayConfig) ([]byte, error) {
	var document yaml.Node
	if err := document.Encode(cfg); err != nil {
		return nil, err
	}
	models := yamlMappingValue(&document, "models")
	if models != nil && models.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(models.Content); i += 2 {
			name, modelNode := models.Content[i].Value, models.Content[i+1]
			model, ok := cfg.Models[name]
			if !ok || model.MaxLoadedSet || model.MaxLoaded > 0 || modelNode.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(modelNode.Content); j += 2 {
				if modelNode.Content[j].Value == "max_loaded" {
					modelNode.Content = append(modelNode.Content[:j], modelNode.Content[j+2:]...)
					break
				}
			}
		}
	}
	return yaml.Marshal(&document)
}

func (m *ConfigManager) allocateTransactionRevision(ctx context.Context, current int64) (int64, error) {
	revision, err := m.revisionStore.Allocate(ctx)
	if err != nil {
		return 0, fmt.Errorf("allocate service-name transaction revision: %w", err)
	}
	if revision <= current {
		return 0, fmt.Errorf("allocate service-name transaction revision: store returned %d after %d", revision, current)
	}
	return revision, nil
}

func (m *ConfigManager) persistTransactionConfig(raw []byte) error {
	if strings.TrimSpace(m.configPath) == "" {
		return nil
	}
	dir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gateway-promotion-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, m.configPath)
}

func (m *ConfigManager) commitTransactionConfig(runtimeCfg, fileCfg config.GatewayConfig, raw []byte, revision int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg, m.fileCfg, m.rawYAML, m.version = cloneGatewayConfig(runtimeCfg), cloneGatewayConfig(fileCfg), append([]byte(nil), raw...), revision
}

func randomArchiveID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create archive id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
