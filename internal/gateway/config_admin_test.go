package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestUIConfigDryRunReportsAddedModel(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/dry-run", strings.NewReader(testGatewayYAMLWithModels("qwen", "new-model")))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var resp uiConfigDryRunResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Valid || resp.Version != 1 {
		t.Fatalf("response = %+v, want valid current version 1", resp)
	}
	if !hasConfigChange(resp.Changes, "models.new-model", "added") {
		t.Fatalf("changes = %+v, want added model change", resp.Changes)
	}
	if resp.RequiresGatewayRestart {
		t.Fatalf("requires_gateway_restart = true, want false for model add")
	}
}

func TestUIConfigApplyUpdatesAgentConfigAndPersistsYAML(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	srv := NewServerWithGatewayConfigPath(testUIGatewayConfig(), configPath)
	nextYAML := testGatewayYAMLWithModels("qwen", "new-model")
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(nextYAML))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var applyResp uiConfigApplyResponse
	if err := json.NewDecoder(rr.Body).Decode(&applyResp); err != nil {
		t.Fatalf("decode apply response: %v", err)
	}
	if applyResp.Version != 2 {
		t.Fatalf("version = %d, want 2", applyResp.Version)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !bytes.Equal(data, []byte(nextYAML)) {
		t.Fatalf("persisted config = %q, want submitted yaml", string(data))
	}

	agentReq := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090", nil)
	agentReq.Header.Set("Authorization", "Bearer agent-secret")
	agentRR := httptest.NewRecorder()
	srv.ServeHTTP(agentRR, agentReq)
	if agentRR.Code != http.StatusOK {
		t.Fatalf("agent config status = %d, want 200: %s", agentRR.Code, agentRR.Body.String())
	}
	var agentResp protocol.AgentConfigResponse
	if err := json.NewDecoder(agentRR.Body).Decode(&agentResp); err != nil {
		t.Fatalf("decode agent config: %v", err)
	}
	if _, ok := agentResp.Models["new-model"]; !ok {
		t.Fatalf("agent models = %+v, want new-model after apply", agentResp.Models)
	}
}

func TestUIConfigApplyRejectsInvalidYAMLWithoutMutatingCurrentConfig(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader("models: {}\n"))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	agentReq := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090", nil)
	agentReq.Header.Set("Authorization", "Bearer agent-secret")
	agentRR := httptest.NewRecorder()
	srv.ServeHTTP(agentRR, agentReq)
	if agentRR.Code != http.StatusOK {
		t.Fatalf("agent config status = %d, want 200: %s", agentRR.Code, agentRR.Body.String())
	}
	var agentResp protocol.AgentConfigResponse
	if err := json.NewDecoder(agentRR.Body).Decode(&agentResp); err != nil {
		t.Fatalf("decode agent config: %v", err)
	}
	if _, ok := agentResp.Models["qwen"]; !ok {
		t.Fatalf("agent models = %+v, want existing qwen after failed apply", agentResp.Models)
	}
}

func TestUIConfigReturnsOriginalYAMLWithoutMaterializingAutomaticMaxLoaded(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithModels("qwen")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithGatewayConfigPath(cfg, configPath)
	req := httptest.NewRequest(http.MethodGet, "/ui/api/config", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp uiConfigResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.YAML != raw {
		t.Fatalf("yaml = %q, want original raw yaml", resp.YAML)
	}
	if strings.Contains(resp.YAML, "max_loaded") {
		t.Fatalf("yaml materialized max_loaded and would change auto semantics:\n%s", resp.YAML)
	}
}

func TestUIConfigApplyRoundTripPreservesAutomaticMaxLoaded(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithModels("qwen")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithGatewayConfigPath(cfg, configPath)

	configReq := httptest.NewRequest(http.MethodGet, "/ui/api/config", nil)
	configReq.Header.Set("Authorization", "Bearer agent-secret")
	configRR := httptest.NewRecorder()
	srv.ServeHTTP(configRR, configReq)
	if configRR.Code != http.StatusOK {
		t.Fatalf("config status = %d, want 200: %s", configRR.Code, configRR.Body.String())
	}
	var configResp uiConfigResponse
	if err := json.NewDecoder(configRR.Body).Decode(&configResp); err != nil {
		t.Fatalf("decode config response: %v", err)
	}

	applyReq := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(configResp.YAML))
	applyReq.Header.Set("Authorization", "Bearer agent-secret")
	applyRR := httptest.NewRecorder()
	srv.ServeHTTP(applyRR, applyReq)
	if applyRR.Code != http.StatusOK {
		t.Fatalf("apply status = %d, want 200: %s", applyRR.Code, applyRR.Body.String())
	}

	applied := srv.currentConfig()
	if applied.Models["qwen"].MaxLoadedSet {
		t.Fatal("MaxLoadedSet = true after round trip, want automatic max_loaded preserved")
	}
}

func TestUIConfigApplyRestartRequiredChangePersistsWithoutReplacingRuntimeSnapshot(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithModels("qwen")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithGatewayConfigPath(cfg, configPath)
	nextRaw := strings.Replace(raw, "agent: agent-secret", "agent: next-agent-secret", 1)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(nextRaw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp uiConfigApplyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.RequiresGatewayRestart {
		t.Fatalf("response = %+v, want requires gateway restart", resp)
	}
	if got := srv.currentConfig().Tokens.Agent; got != "agent-secret" {
		t.Fatalf("runtime agent token = %q, want unchanged old token", got)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != nextRaw {
		t.Fatalf("persisted config = %q, want restart-required raw config", string(data))
	}
}

func TestUIConfigApplyKeepsRuntimeTokenOverrideForHotModelChange(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithModels("qwen")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tokens.Agent = "env-agent-secret"
	cfg.Tokens.LlamaSwap = "env-agent-secret"
	srv := NewServerWithGatewayConfigPath(cfg, configPath)
	nextRaw := testGatewayYAMLWithModels("qwen", "new-model")
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(nextRaw))
	req.Header.Set("Authorization", "Bearer env-agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp uiConfigApplyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RequiresGatewayRestart {
		t.Fatalf("response = %+v, model-only change with runtime token override should remain hot", resp)
	}
	current := srv.currentConfig()
	if current.Tokens.Agent != "env-agent-secret" {
		t.Fatalf("runtime agent token = %q, want env override retained", current.Tokens.Agent)
	}
	if _, ok := current.Models["new-model"]; !ok {
		t.Fatalf("models = %+v, want new-model hot applied", current.Models)
	}
}

func TestUIConfigApplyKeepsRuntimeProxyAttemptsOverride(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithModels("qwen")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Gateway.ProxyAttempts = 7
	srv := NewServerWithGatewayConfigPathAndOverrides(cfg, configPath, config.GatewayRuntimeOverrides{ProxyAttempts: true})
	nextRaw := strings.Replace(raw, "proxy_attempts: 2", "proxy_attempts: 9", 1)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(nextRaw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp uiConfigApplyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.RequiresGatewayRestart || resp.ApplyMode != "save_requires_gateway_restart" {
		t.Fatalf("response = %+v, want save_requires_gateway_restart", resp)
	}
	if got := srv.currentConfig().Gateway.ProxyAttempts; got != 7 {
		t.Fatalf("runtime proxy attempts = %d, want override retained", got)
	}
}

func TestUIConfigDryRunReportsLoadedWorkerImpactForRuntimeChange(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	})
	nextRaw := strings.Replace(testGatewayYAMLWithModels("qwen"), "run: llama-swap run qwen", "run: llama-swap run qwen --new-arg", 1)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/dry-run", strings.NewReader(nextRaw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp uiConfigDryRunResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ApplyMode != "hot_apply" {
		t.Fatalf("apply_mode = %q, want hot_apply", resp.ApplyMode)
	}
	impact, ok := findConfigImpact(resp.Impacts, "qwen", "gpu-01")
	if !ok {
		t.Fatalf("impacts = %+v, want qwen on gpu-01", resp.Impacts)
	}
	if !impact.Loaded || !impact.RequiresWorkerRestart || impact.RunningState != "ready" {
		t.Fatalf("impact = %+v, want loaded ready worker restart impact", impact)
	}
	if change, ok := findConfigChange(resp.Changes, "models.qwen", "changed"); !ok || !change.RequiresWorkerRestart {
		t.Fatalf("changes = %+v, want model change requiring worker restart", resp.Changes)
	}
}

func TestUIConfigDryRunRuntimeChangeWithoutLoadedModelDoesNotRequireWorkerRestart(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Artifacts:    map[string]string{"qwen": "ready"},
	})
	nextRaw := strings.Replace(testGatewayYAMLWithModels("qwen"), "run: llama-swap run qwen", "run: llama-swap run qwen --new-arg", 1)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/dry-run", strings.NewReader(nextRaw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp uiConfigDryRunResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Impacts) != 0 {
		t.Fatalf("impacts = %+v, want none for unloaded model", resp.Impacts)
	}
	if change, ok := findConfigChange(resp.Changes, "models.qwen", "changed"); !ok || change.RequiresWorkerRestart {
		t.Fatalf("changes = %+v, want model change without worker restart", resp.Changes)
	}
}

func TestUIConfigDryRunGatewayRestartChangeReportsSaveOnlyMode(t *testing.T) {
	raw := testGatewayYAMLWithModels("qwen")
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithGatewayConfigPath(cfg, "")
	nextRaw := strings.Replace(raw, "agent: agent-secret", "agent: next-agent-secret", 1)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/dry-run", strings.NewReader(nextRaw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp uiConfigDryRunResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ApplyMode != "save_requires_gateway_restart" || !resp.RequiresGatewayRestart {
		t.Fatalf("response = %+v, want save_requires_gateway_restart", resp)
	}
}

func hasConfigChange(changes []uiConfigChange, path string, changeType string) bool {
	_, ok := findConfigChange(changes, path, changeType)
	return ok
}

func findConfigChange(changes []uiConfigChange, path string, changeType string) (uiConfigChange, bool) {
	for _, change := range changes {
		if change.Path == path && change.Type == changeType {
			return change, true
		}
	}
	return uiConfigChange{}, false
}

func findConfigImpact(impacts []uiConfigImpact, model string, workerID string) (uiConfigImpact, bool) {
	for _, impact := range impacts {
		if impact.Model == model && impact.WorkerID == workerID {
			return impact, true
		}
	}
	return uiConfigImpact{}, false
}

func testGatewayYAMLWithModels(models ...string) string {
	var b strings.Builder
	b.WriteString(`gateway:
  proxy_attempts: 2
oss:
  base_url: https://oss.example.com
tokens:
  client: client-secret
  agent: agent-secret
models:
`)
	for _, model := range models {
		b.WriteString("  " + model + ":\n")
		b.WriteString("    artifact:\n")
		b.WriteString("      object: " + model + ".tar.gz\n")
		b.WriteString("      kind: tar_gz\n")
		b.WriteString("      crc64ecma: \"123\"\n")
		b.WriteString("    run: llama-swap run " + model + "\n")
	}
	b.WriteString("tag_policies:\n")
	b.WriteString("  gpu-4090:\n")
	b.WriteString("    allowed_models:\n")
	for _, model := range models {
		b.WriteString("      - " + model + "\n")
	}
	b.WriteString("    worker_defaults:\n")
	b.WriteString("      max_concurrency: 2\n")
	b.WriteString("      max_queue: 4\n")
	return b.String()
}
