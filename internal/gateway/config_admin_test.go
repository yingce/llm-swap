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

	"gopkg.in/yaml.v3"
)

func TestUIConfigRedactsFRPAuthTokenFromStructuredConfigAndYAML(t *testing.T) {
	const secret = "frp-super-secret"
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithFRPSecret(secret, "qwen")
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
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatalf("config response leaked FRP auth token: %s", rr.Body.String())
	}
	var resp uiConfigResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if got := resp.Config.Transport.FRP.AuthToken; got != uiConfigRedactedSecret {
		t.Fatalf("structured auth token = %q, want redaction marker", got)
	}
	if !strings.Contains(resp.YAML, "auth_token: "+uiConfigRedactedSecret) {
		t.Fatalf("YAML does not contain redaction marker:\n%s", resp.YAML)
	}
}

func TestUIConfigApplyRedactedFRPAuthTokenPreservesExistingSecret(t *testing.T) {
	const secret = "frp-super-secret"
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithFRPSecret(secret, "qwen")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithGatewayConfigPath(cfg, configPath)
	redacted := strings.Replace(raw, secret, uiConfigRedactedSecret, 1)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(redacted))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), secret) {
		t.Fatalf("apply response leaked FRP auth token: %s", rr.Body.String())
	}
	if got := srv.currentConfig().Transport.FRP.AuthToken; got != secret {
		t.Fatalf("runtime auth token = %q, want existing secret preserved", got)
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), secret) || strings.Contains(string(persisted), uiConfigRedactedSecret) {
		t.Fatalf("persisted config did not restore existing secret:\n%s", persisted)
	}
}

func TestUIConfigApplyRealFRPAuthTokenReplacesExistingSecret(t *testing.T) {
	const oldSecret = "frp-old-secret"
	const newSecret = "frp-new-secret"
	configPath := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithFRPSecret(oldSecret, "qwen")
	if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServerWithGatewayConfigPath(cfg, configPath)
	nextRaw := strings.Replace(raw, oldSecret, newSecret, 1)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(nextRaw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), oldSecret) || strings.Contains(rr.Body.String(), newSecret) {
		t.Fatalf("apply response leaked an FRP auth token: %s", rr.Body.String())
	}
	if got := srv.currentConfig().Transport.FRP.AuthToken; got != newSecret {
		t.Fatalf("runtime auth token = %q, want replacement", got)
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), newSecret) || strings.Contains(string(persisted), oldSecret) {
		t.Fatalf("persisted config did not replace secret:\n%s", persisted)
	}
}

func TestUIConfigApplyRedactedFRPAuthTokenWithoutExistingSecretFails(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	raw := testGatewayYAMLWithFRPSecret(uiConfigRedactedSecret, "qwen")
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(raw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no current secret") {
		t.Fatalf("error = %s, want clear missing backing secret error", rr.Body.String())
	}
	if got := srv.currentConfig().Transport.FRP.AuthToken; got != "" {
		t.Fatalf("runtime auth token = %q, want unchanged empty token", got)
	}
}

func TestUIConfigDryRunAndValidateRedactedFRPAuthTokenDoNotLeak(t *testing.T) {
	const secret = "frp-super-secret"
	raw := testGatewayYAMLWithFRPSecret(secret, "qwen")
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg)
	redacted := strings.Replace(raw, secret, uiConfigRedactedSecret, 1)

	for _, endpoint := range []string{"/ui/api/config/dry-run", "/ui/api/config/validate"} {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(redacted))
			req.Header.Set("Authorization", "Bearer agent-secret")
			rr := httptest.NewRecorder()

			srv.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), secret) {
				t.Fatalf("response leaked FRP auth token: %s", rr.Body.String())
			}
			var resp uiConfigDryRunResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if !resp.Valid {
				t.Fatalf("response = %+v, want valid redacted round trip", resp)
			}
		})
	}
	_, candidate := srv.configManager.DryRun([]byte(redacted))
	if got := candidate.Transport.FRP.AuthToken; got != secret {
		t.Fatalf("dry-run candidate auth token = %q, want existing secret restored", got)
	}
}

func TestUIConfigRedactsFRPAuthTokenThroughYAMLAliasesAndMerges(t *testing.T) {
	const secret = "frp-aliased-secret"
	for _, shape := range []string{"transport_alias", "frp_alias", "scalar_alias", "merge_map", "merge_sequence", "merge_direct_override"} {
		t.Run(shape, func(t *testing.T) {
			raw := testGatewayYAMLWithFRPShape(secret, shape, "qwen")
			cfg, err := config.LoadGateway(strings.NewReader(raw))
			if err != nil {
				t.Fatalf("load fixture: %v\n%s", err, raw)
			}
			configPath := filepath.Join(t.TempDir(), "gateway.yaml")
			if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
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
			for _, credential := range []string{secret, "inactive-default-secret"} {
				if strings.Contains(rr.Body.String(), credential) {
					t.Fatalf("config response leaked FRP auth token contributor %q: %s", credential, rr.Body.String())
				}
			}
			var resp uiConfigResponse
			if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if got := resp.Config.Transport.FRP.AuthToken; got != uiConfigRedactedSecret {
				t.Fatalf("structured auth token = %q, want marker", got)
			}
			if !strings.Contains(resp.YAML, uiConfigRedactedSecret) {
				t.Fatalf("redacted YAML does not contain marker:\n%s", resp.YAML)
			}
			if shape == "merge_direct_override" && !strings.Contains(resp.YAML, "unrelated-auth-token") {
				t.Fatalf("redaction changed an unrelated auth_token path:\n%s", resp.YAML)
			}
		})
	}
}

func TestUIConfigApplyRoundTripRestoresFRPAuthTokenThroughYAMLAliasesAndMerges(t *testing.T) {
	const secret = "frp-aliased-secret"
	for _, shape := range []string{"transport_alias", "frp_alias", "scalar_alias", "merge_map", "merge_sequence", "merge_direct_override"} {
		t.Run(shape, func(t *testing.T) {
			raw := testGatewayYAMLWithFRPShape(secret, shape, "qwen")
			cfg, err := config.LoadGateway(strings.NewReader(raw))
			if err != nil {
				t.Fatalf("load fixture: %v\n%s", err, raw)
			}
			configPath := filepath.Join(t.TempDir(), "gateway.yaml")
			if err := os.WriteFile(configPath, []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
			srv := NewServerWithGatewayConfigPath(cfg, configPath)

			getReq := httptest.NewRequest(http.MethodGet, "/ui/api/config", nil)
			getReq.Header.Set("Authorization", "Bearer agent-secret")
			getRR := httptest.NewRecorder()
			srv.ServeHTTP(getRR, getReq)
			if getRR.Code != http.StatusOK {
				t.Fatalf("GET status = %d, want 200: %s", getRR.Code, getRR.Body.String())
			}
			var configResp uiConfigResponse
			if err := json.NewDecoder(getRR.Body).Decode(&configResp); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(configResp.YAML, secret) || strings.Contains(configResp.YAML, "inactive-default-secret") || !strings.Contains(configResp.YAML, uiConfigRedactedSecret) {
				t.Fatalf("GET YAML is not safely redacted:\n%s", configResp.YAML)
			}

			applyReq := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(configResp.YAML))
			applyReq.Header.Set("Authorization", "Bearer agent-secret")
			applyRR := httptest.NewRecorder()
			srv.ServeHTTP(applyRR, applyReq)
			if applyRR.Code != http.StatusOK {
				t.Fatalf("apply status = %d, want 200: %s", applyRR.Code, applyRR.Body.String())
			}
			if strings.Contains(applyRR.Body.String(), secret) || strings.Contains(applyRR.Body.String(), "inactive-default-secret") {
				t.Fatalf("apply response leaked FRP auth token: %s", applyRR.Body.String())
			}
			persisted, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(persisted), uiConfigRedactedSecret) {
				t.Fatalf("persisted YAML still contains marker:\n%s", persisted)
			}
			if strings.Contains(string(persisted), "inactive-default-secret") {
				t.Fatalf("persisted YAML retained a shadowed credential:\n%s", persisted)
			}
			if shape == "merge_direct_override" && !strings.Contains(string(persisted), "unrelated-auth-token") {
				t.Fatalf("persisted YAML changed an unrelated auth_token path:\n%s", persisted)
			}
			persistedCfg, err := config.LoadGateway(bytes.NewReader(persisted))
			if err != nil {
				t.Fatalf("load persisted config: %v\n%s", err, persisted)
			}
			if got := persistedCfg.Transport.FRP.AuthToken; got != secret {
				t.Fatalf("persisted auth token = %q, want restored secret", got)
			}
			if got := srv.currentConfig().Transport.FRP.AuthToken; got != secret {
				t.Fatalf("runtime auth token = %q, want effective current secret", got)
			}
		})
	}
}

func TestYAMLMappingValueFollowsMergeAliasesWithoutLoopingOnCycles(t *testing.T) {
	mapA := &yaml.Node{Kind: yaml.MappingNode}
	mapB := &yaml.Node{Kind: yaml.MappingNode}
	aliasA := &yaml.Node{Kind: yaml.AliasNode, Alias: mapA}
	aliasB := &yaml.Node{Kind: yaml.AliasNode, Alias: mapB}
	mergeKeyA := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"}
	mergeKeyB := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!merge", Value: "<<"}
	secret := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "secret"}
	mapA.Content = []*yaml.Node{mergeKeyA, aliasB}
	mapB.Content = []*yaml.Node{
		mergeKeyB, aliasA,
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "auth_token"}, secret,
	}

	if got := yamlMappingValue(mapA, "auth_token"); got != secret {
		t.Fatalf("auth_token node = %p, want direct value reachable through cyclic merge %p", got, secret)
	}
	if got := yamlMappingValue(mapA, "missing"); got != nil {
		t.Fatalf("missing node = %+v, want nil without looping", got)
	}
}

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

func TestUIConfigDryRunReportsAliasHotApply(t *testing.T) {
	raw := strings.Replace(
		testGatewayYAMLWithModels("v1", "v2"),
		"tag_policies:\n",
		"model_aliases:\n  latest: v1\ntag_policies:\n",
		1,
	)
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg)
	nextRaw := strings.Replace(raw, "latest: v1", "latest: v2", 1)
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
	change, ok := findConfigChange(resp.Changes, "model_aliases.latest", "changed")
	if !ok {
		t.Fatalf("changes = %+v, want changed latest alias", resp.Changes)
	}
	if change.RequiresGatewayRestart || change.RequiresWorkerRestart {
		t.Fatalf("change = %+v, want alias hot apply without restart", change)
	}
	if resp.ApplyMode != "hot_apply" || resp.RequiresGatewayRestart {
		t.Fatalf("response = %+v, want hot_apply without gateway restart", resp)
	}
}

func TestUIConfigDryRunReportsAliasAddedAndRemovedAsHotApply(t *testing.T) {
	baseRaw := testGatewayYAMLWithModels("v1", "v2")
	withAlias := strings.Replace(baseRaw, "tag_policies:\n", "model_aliases:\n  latest: v1\ntag_policies:\n", 1)
	tests := []struct {
		name    string
		current string
		next    string
		kind    string
	}{
		{name: "added", current: baseRaw, next: withAlias, kind: "added"},
		{name: "removed", current: withAlias, next: baseRaw, kind: "removed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := config.LoadGateway(strings.NewReader(tt.current))
			if err != nil {
				t.Fatal(err)
			}
			srv := NewServer(cfg)
			req := httptest.NewRequest(http.MethodPost, "/ui/api/config/dry-run", strings.NewReader(tt.next))
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
			change, ok := findConfigChange(resp.Changes, "model_aliases.latest", tt.kind)
			if !ok {
				t.Fatalf("changes = %+v, want %s latest alias", resp.Changes, tt.kind)
			}
			if change.RequiresGatewayRestart || change.RequiresWorkerRestart {
				t.Fatalf("change = %+v, want alias hot apply without restart", change)
			}
			if resp.ApplyMode != "hot_apply" || resp.RequiresGatewayRestart {
				t.Fatalf("response = %+v, want hot_apply without gateway restart", resp)
			}
		})
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

func TestUIConfigDryRunReportsLoadedWorkerImpactForRemovedModel(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "worker-1",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker-1:6006",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/dry-run", strings.NewReader(testGatewayYAMLWithModels("other")))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp uiConfigDryRunResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !hasConfigChange(resp.Changes, "models.qwen", "removed") {
		t.Fatalf("changes = %+v, want removed qwen", resp.Changes)
	}
	impact, ok := findConfigImpact(resp.Impacts, "qwen", "worker-1")
	if !ok || !impact.RequiresWorkerRestart {
		t.Fatalf("impacts = %+v, want qwen worker restart", resp.Impacts)
	}
}

func TestUIConfigApplyRejectsRemovalWithDanglingTagReference(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	raw := strings.Replace(testGatewayYAMLWithModels("other"), "      - other", "      - qwen", 1)
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(raw))
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
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
		t.Fatal(err)
	}
	if _, ok := agentResp.Models["qwen"]; !ok {
		t.Fatalf("agent models = %+v, want unchanged qwen", agentResp.Models)
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

func TestUIConfigDryRunReportsModelDirImpact(t *testing.T) {
	raw := testGatewayYAMLWithModels("qwen")
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cfg)
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	})
	nextRaw := strings.Replace(raw, "  qwen:\n", "  qwen:\n    model_dir: qwen-20260720\n", 1)
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
	change, ok := findConfigChange(resp.Changes, "models.qwen", "changed")
	if !ok || change.Detail != "runtime command or artifact changed" || !change.RequiresWorkerRestart {
		t.Fatalf("changes = %+v, want runtime change requiring loaded worker restart", resp.Changes)
	}
	impact, ok := findConfigImpact(resp.Impacts, "qwen", "gpu-01")
	if !ok || !impact.Loaded || !impact.RequiresWorkerRestart {
		t.Fatalf("impacts = %+v, want loaded qwen impact on gpu-01", resp.Impacts)
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

func TestUIConfigDryRunRuntimeChangeForStoppedModelDoesNotRequireWorkerRestart(t *testing.T) {
	srv := NewServer(testUIGatewayConfig())
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		Tags:         []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "stopped"},
		},
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
		t.Fatalf("impacts = %+v, want none for stopped model", resp.Impacts)
	}
	if change, ok := findConfigChange(resp.Changes, "models.qwen", "changed"); !ok || change.RequiresWorkerRestart {
		t.Fatalf("changes = %+v, want stopped model change without worker restart", resp.Changes)
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

func testGatewayYAMLWithFRPSecret(secret string, models ...string) string {
	raw := testGatewayYAMLWithModels(models...)
	transport := "transport:\n" +
		"  type: frp_tcp\n" +
		"  frp:\n" +
		"    server_addr: frps.example.com\n" +
		"    server_port: 7000\n" +
		"    auth_token: " + secret + "\n" +
		"    port_start: 2000\n" +
		"    port_end: 3000\n" +
		"    lease_ttl_seconds: 180\n"
	return strings.Replace(raw, "oss:\n", transport+"oss:\n", 1)
}

func testGatewayYAMLWithFRPShape(secret string, shape string, models ...string) string {
	raw := testGatewayYAMLWithModels(models...)
	frpFields := "    server_addr: frps.example.com\n" +
		"    server_port: 7000\n" +
		"    auth_token: " + secret + "\n" +
		"    port_start: 2000\n" +
		"    port_end: 3000\n" +
		"    lease_ttl_seconds: 180\n"
	var transport string
	switch shape {
	case "transport_alias":
		transport = "transport_defaults: &transport_defaults\n" +
			"  type: frp_tcp\n" +
			"  frp:\n" + frpFields +
			"transport: *transport_defaults\n"
	case "frp_alias":
		transport = "frp_defaults: &frp_defaults\n" + strings.ReplaceAll(frpFields, "    ", "  ") +
			"transport:\n" +
			"  type: frp_tcp\n" +
			"  frp: *frp_defaults\n"
	case "scalar_alias":
		transport = "frp_secret: &frp_secret " + secret + "\n" +
			"transport:\n" +
			"  type: frp_tcp\n" +
			"  frp:\n" +
			"    server_addr: frps.example.com\n" +
			"    server_port: 7000\n" +
			"    auth_token: *frp_secret\n" +
			"    port_start: 2000\n" +
			"    port_end: 3000\n" +
			"    lease_ttl_seconds: 180\n"
	case "merge_map":
		transport = "frp_defaults: &frp_defaults\n" + strings.ReplaceAll(frpFields, "    ", "  ") +
			"transport:\n" +
			"  type: frp_tcp\n" +
			"  frp:\n" +
			"    <<: *frp_defaults\n"
	case "merge_sequence":
		transport = "frp_auth: &frp_auth\n" +
			"  auth_token: " + secret + "\n" +
			"frp_network: &frp_network\n" +
			"  server_addr: frps.example.com\n" +
			"  server_port: 7000\n" +
			"  port_start: 2000\n" +
			"  port_end: 3000\n" +
			"  lease_ttl_seconds: 180\n" +
			"transport:\n" +
			"  type: frp_tcp\n" +
			"  frp:\n" +
			"    <<: [*frp_auth, *frp_network]\n"
	case "merge_direct_override":
		transport = "unrelated_credentials:\n" +
			"  auth_token: unrelated-auth-token\n" +
			"frp_defaults: &frp_defaults\n" +
			"  server_addr: frps.example.com\n" +
			"  server_port: 7000\n" +
			"  auth_token: inactive-default-secret\n" +
			"  port_start: 2000\n" +
			"  port_end: 3000\n" +
			"  lease_ttl_seconds: 180\n" +
			"transport:\n" +
			"  type: frp_tcp\n" +
			"  frp:\n" +
			"    <<: *frp_defaults\n" +
			"    auth_token: " + secret + "\n"
	default:
		panic("unknown FRP YAML shape: " + shape)
	}
	return strings.Replace(raw, "oss:\n", transport+"oss:\n", 1)
}
