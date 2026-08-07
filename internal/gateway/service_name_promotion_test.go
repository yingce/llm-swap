package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"

	"gopkg.in/yaml.v3"
)

func TestPromoteServiceNameAtomicallyArchivesDisabledIdleCanonicalAndRollbackRestoresIt(t *testing.T) {
	srv, configPath := newPromotionTestServer(t, 1)
	srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID: "gpu-ready", LlamaSwapURL: "http://worker", Tags: []string{"gpu"},
		Artifacts:     map[string]string{"A-Pro-0808": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "A-Pro-0808", State: "ready"}},
	}, time.Now())

	promoted := postPromotion(t, srv, "/ui/api/service-names/promote", map[string]any{
		"service_name": "A-Pro", "target_model": "A-Pro-0808",
	})
	if promoted.Action != "promote" || promoted.ServiceName != "A-Pro" || promoted.TargetModel != "A-Pro-0808" || promoted.ArchiveID == "" {
		t.Fatalf("promotion response = %+v", promoted)
	}
	cfg := srv.currentConfig()
	if _, exists := cfg.Models["A-Pro"]; exists {
		t.Fatal("old canonical remains in active model namespace")
	}
	if got := cfg.ModelAliases["A-Pro"]; got != "A-Pro-0808" {
		t.Fatalf("service alias target = %q", got)
	}
	if containsString(cfg.TagPolicies["gpu"].AllowedModels, "A-Pro") {
		t.Fatalf("promoted canonical remains in tag policy: %+v", cfg.TagPolicies["gpu"])
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "A-Pro:\n") || !strings.Contains(string(persisted), "A-Pro: A-Pro-0808") {
		t.Fatalf("persisted promotion is incomplete:\n%s", persisted)
	}
	if events := srv.recentAgentEvents(); len(events) == 0 || events[len(events)-1].Event != "gateway_service_name_promoted" {
		t.Fatalf("operator events = %+v", events)
	}

	rolledBack := postPromotion(t, srv, "/ui/api/service-names/rollback", map[string]any{
		"service_name": "A-Pro", "target_model": "A-Pro-0808", "archive_id": promoted.ArchiveID,
	})
	if rolledBack.Action != "rollback" {
		t.Fatalf("rollback response = %+v", rolledBack)
	}
	cfg = srv.currentConfig()
	if model, exists := cfg.Models["A-Pro"]; !exists || !model.Disabled {
		t.Fatalf("restored canonical = %+v, exists=%v", model, exists)
	}
	if _, exists := cfg.ModelAliases["A-Pro"]; exists {
		t.Fatal("service alias remains after rollback")
	}
	if !containsString(cfg.TagPolicies["gpu"].AllowedModels, "A-Pro") {
		t.Fatalf("tag policy was not restored: %+v", cfg.TagPolicies["gpu"])
	}
}

func TestPromoteServiceNameRejectsUnsafeOldOrTargetStateWithoutPartialConfig(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Server)
		readyFloor int
		want       string
	}{
		{name: "enabled old canonical", readyFloor: 1, mutate: func(s *Server) {
			cfg, _ := s.configManager.Snapshot()
			m := cfg.Models["A-Pro"]
			m.Disabled = false
			cfg.Models["A-Pro"] = m
			s.configManager.cfg = cfg
			s.configManager.fileCfg = cloneGatewayConfig(cfg)
		}, want: "disabled"},
		{name: "old canonical running", readyFloor: 1, mutate: func(s *Server) {
			s.workers.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "old", LlamaSwapURL: "http://old", Artifacts: map[string]string{"A-Pro": "ready"}, RunningModels: []protocol.RunningModel{{Model: "A-Pro", State: "ready"}}}, time.Now())
		}, want: "running"},
		{name: "old canonical active request", readyFloor: 1, mutate: func(s *Server) { _ = s.accounting.Acquire("request", "A-Pro", "gpu", "new") }, want: "active"},
		{name: "old canonical installing", readyFloor: 1, mutate: func(s *Server) {
			s.workers.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "old", LlamaSwapURL: "http://old", Artifacts: map[string]string{"A-Pro": "installing"}}, time.Now())
		}, want: "installing"},
		{name: "target below ready floor", readyFloor: 2, mutate: func(s *Server) {}, want: "ready floor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, configPath := newPromotionTestServer(t, tt.readyFloor)
			srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "new", Tags: []string{"gpu"}, LlamaSwapURL: "http://new", Artifacts: map[string]string{"A-Pro-0808": "ready"}, RunningModels: []protocol.RunningModel{{Model: "A-Pro-0808", State: "ready"}}}, time.Now())
			tt.mutate(srv)
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			rr := postPromotionRaw(srv, "/ui/api/service-names/promote", map[string]any{"service_name": "A-Pro", "target_model": "A-Pro-0808"})
			if rr.Code != http.StatusConflict || !strings.Contains(strings.ToLower(rr.Body.String()), tt.want) {
				t.Fatalf("status=%d body=%s, want conflict containing %q", rr.Code, rr.Body.String(), tt.want)
			}
			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed promotion changed persisted config")
			}
			cfg := srv.currentConfig()
			if _, exists := cfg.Models["A-Pro"]; !exists || cfg.ModelAliases["A-Pro"] != "" {
				t.Fatalf("failed promotion changed runtime config: %+v", cfg)
			}
		})
	}
}

func TestRollbackServiceNameSurvivesRestartAndRefusesToOverwriteLaterAliasChange(t *testing.T) {
	srv, configPath := newPromotionTestServer(t, 1)
	srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "new", Tags: []string{"gpu"}, LlamaSwapURL: "http://new", Artifacts: map[string]string{"A-Pro-0808": "ready"}, RunningModels: []protocol.RunningModel{{Model: "A-Pro-0808", State: "ready"}}}, time.Now())
	promoted := postPromotion(t, srv, "/ui/api/service-names/promote", map[string]any{"service_name": "A-Pro", "target_model": "A-Pro-0808"})

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(bytes.NewReader(persisted))
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewServerWithGatewayConfigPath(cfg, configPath)
	restarted.configManager.cfg.ModelAliases["A-Pro"] = "other-version"
	restarted.configManager.fileCfg.ModelAliases["A-Pro"] = "other-version"
	rr := postPromotionRaw(restarted, "/ui/api/service-names/rollback", map[string]any{"service_name": "A-Pro", "target_model": "A-Pro-0808", "archive_id": promoted.ArchiveID})
	if rr.Code != http.StatusConflict || !strings.Contains(strings.ToLower(rr.Body.String()), "changed") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if _, exists := restarted.currentConfig().Models["A-Pro"]; exists {
		t.Fatal("unsafe rollback restored archived model")
	}
}

func TestPromoteServiceNameRevisionOrArchiveFailureDoesNotPublishConfig(t *testing.T) {
	tests := []struct {
		name string
		fail func(*Server)
		want string
	}{
		{name: "revision allocation", fail: func(s *Server) {
			s.configManager.revisionStore = &failAfterStartupRevisionStore{calls: 1, err: errors.New("revision unavailable")}
		}, want: "revision unavailable"},
		{name: "archive persistence", fail: func(s *Server) {
			s.configManager.promotionStore = failingServiceNameArchiveStore{err: errors.New("archive unavailable")}
		}, want: "archive unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, configPath := newPromotionTestServer(t, 1)
			srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "new", Tags: []string{"gpu"}, LlamaSwapURL: "http://new", Artifacts: map[string]string{"A-Pro-0808": "ready"}, RunningModels: []protocol.RunningModel{{Model: "A-Pro-0808", State: "ready"}}}, time.Now())
			before, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			tt.fail(srv)
			rr := postPromotionRaw(srv, "/ui/api/service-names/promote", map[string]any{"service_name": "A-Pro", "target_model": "A-Pro-0808"})
			if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), tt.want) {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			after, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed transaction changed persisted config")
			}
			if _, exists := srv.currentConfig().Models["A-Pro"]; !exists {
				t.Fatal("failed transaction changed runtime namespace")
			}
		})
	}
}

func TestPromoteServiceNameConfigWriteFailureRemovesPreparedArchive(t *testing.T) {
	srv, _ := newPromotionTestServer(t, 1)
	srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "new", Tags: []string{"gpu"}, LlamaSwapURL: "http://new", Artifacts: map[string]string{"A-Pro-0808": "ready"}, RunningModels: []protocol.RunningModel{{Model: "A-Pro-0808", State: "ready"}}}, time.Now())
	blockedParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedParent, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.configManager.configPath = filepath.Join(blockedParent, "gateway.yaml")
	archives := newMemoryServiceNameArchiveStore()
	srv.configManager.promotionStore = archives

	rr := postPromotionRaw(srv, "/ui/api/service-names/promote", map[string]any{"service_name": "A-Pro", "target_model": "A-Pro-0808"})
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "persist promoted config") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(archives.archives) != 0 {
		t.Fatalf("orphaned prepared archives = %+v", archives.archives)
	}
	if _, exists := srv.currentConfig().Models["A-Pro"]; !exists {
		t.Fatal("failed config write changed runtime namespace")
	}
}

func newPromotionTestServer(t *testing.T, targetMinLoaded int) (*Server, string) {
	t.Helper()
	cfg := testUIGatewayConfig()
	cfg.Models = map[string]config.Model{
		"A-Pro":         {Disabled: true, Artifact: config.Artifact{Object: "old.tar.gz", Kind: "tar_gz", CRC64ECMA: "1"}, Run: "old"},
		"A-Pro-0808":    {MinLoaded: targetMinLoaded, Artifact: config.Artifact{Object: "new.tar.gz", Kind: "tar_gz", CRC64ECMA: "2"}, Run: "new"},
		"other-version": {Artifact: config.Artifact{Object: "other.tar.gz", Kind: "tar_gz", CRC64ECMA: "3"}, Run: "other"},
	}
	cfg.ModelAliases = map[string]string{}
	cfg.TagPolicies = map[string]config.TagPolicy{"gpu": {AllowedModels: []string{"A-Pro", "A-Pro-0808", "other-version"}}}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return NewServerWithGatewayConfigPath(cfg, configPath), configPath
}

func postPromotion(t *testing.T, srv *Server, path string, body map[string]any) uiServiceNameTransactionResponse {
	t.Helper()
	rr := postPromotionRaw(srv, path, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp uiServiceNameTransactionResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func postPromotionRaw(srv *Server, path string, body map[string]any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type failingServiceNameArchiveStore struct{ err error }

func (s failingServiceNameArchiveStore) Put(context.Context, serviceNameArchive) error { return s.err }
func (s failingServiceNameArchiveStore) Get(context.Context, string) (serviceNameArchive, bool, error) {
	return serviceNameArchive{}, false, s.err
}
func (s failingServiceNameArchiveStore) Delete(context.Context, string) error { return s.err }
