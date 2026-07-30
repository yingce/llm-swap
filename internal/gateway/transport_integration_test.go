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
	"llm-swap/internal/transport"
)

func TestAgentConfigEndpointSealsFRPBootstrapForAgentIdentity(t *testing.T) {
	cfg := testFRPGatewayConfig()
	srv := NewServer(cfg)
	req := httptest.NewRequest(http.MethodGet, "/internal/agent/config?agent_id=worker-gpu0&tags=gpu-4090", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.Bytes()
	for _, secret := range []string{"frp-secret", "agent-secret"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("response contains plaintext secret %q", secret)
		}
	}
	var resp protocol.AgentConfigResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Transport == nil {
		t.Fatal("transport bootstrap is nil")
	}
	bootstrap, err := transport.OpenBootstrap("agent-secret", "worker-gpu0", *resp.Transport)
	if err != nil {
		t.Fatalf("open transport bootstrap: %v", err)
	}
	if bootstrap.Type != "frp_tcp" || bootstrap.ServerAddr != "frps.example.test" || bootstrap.ServerPort != 7000 {
		t.Fatalf("bootstrap endpoint = %+v", bootstrap)
	}
	if bootstrap.AuthToken != "frp-secret" || bootstrap.LlamaSwapToken != "agent-secret" {
		t.Fatalf("bootstrap credentials were not populated from effective gateway config")
	}
	if bootstrap.PortStart != 2000 || bootstrap.PortEnd != 2007 || bootstrap.LeaseTTLSeconds != 180 {
		t.Fatalf("bootstrap lease policy = %+v", bootstrap)
	}
	if _, err := transport.OpenBootstrap("agent-secret", "worker-gpu1", *resp.Transport); err == nil {
		t.Fatal("different agent identity decrypted bootstrap")
	}
}

func TestAgentConfigEndpointFRPRequiresAgentIdentityAndLegacyDoesNot(t *testing.T) {
	frp := NewServer(testFRPGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	frp.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("FRP status=%d, want 400", rr.Code)
	}

	legacy := NewServer(testGatewayConfig())
	req = httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr = httptest.NewRecorder()
	legacy.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("legacy status=%d, want 200: %s", rr.Code, rr.Body.String())
	}
	var response protocol.AgentConfigResponse
	if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Transport != nil {
		t.Fatalf("legacy response transport=%+v, want nil", response.Transport)
	}
}

func TestTransportLeaseEndpointAllocatesFirstConfiguredPort(t *testing.T) {
	srv := NewServer(testFRPGatewayConfig())
	status, lease := requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{
		AgentID:    "worker-gpu0",
		Generation: 1,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if lease.LeaseID == "" || lease.Slot != 0 || lease.RemotePort != 2000 || lease.Generation != 1 || lease.ExpiresAt.IsZero() {
		t.Fatalf("lease = %+v", lease)
	}
}

func TestTransportLeaseEndpointMaintainsStickyOwnershipAndExactRelease(t *testing.T) {
	srv := NewServer(testFRPGatewayConfig())
	request := protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1}
	status, first := requestTransportLease(t, srv, "agent-secret", request)
	if status != http.StatusOK {
		t.Fatalf("first status = %d", status)
	}

	status, sticky := requestTransportLease(t, srv, "agent-secret", request)
	if status != http.StatusOK || sticky.LeaseID != first.LeaseID || sticky.Slot != first.Slot {
		t.Fatalf("sticky status=%d lease=%+v, want %+v", status, sticky, first)
	}

	request.LeaseID = first.LeaseID
	request.ExcludeSlots = []int{first.Slot}
	status, replacement := requestTransportLease(t, srv, "agent-secret", request)
	if status != http.StatusOK || replacement.LeaseID == first.LeaseID || replacement.Slot != 1 || replacement.RemotePort != 2001 {
		t.Fatalf("replacement status=%d lease=%+v", status, replacement)
	}

	status, _ = requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{
		AgentID: "worker-gpu0", Generation: 1, LeaseID: first.LeaseID,
	})
	if status != http.StatusConflict {
		t.Fatalf("stale current lease status=%d, want 409", status)
	}

	status, _ = requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{
		AgentID: "worker-gpu0", Generation: 2, LeaseID: replacement.LeaseID,
	})
	if status != http.StatusConflict {
		t.Fatalf("stale generation status=%d, want 409", status)
	}

	status, _ = requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{
		AgentID: "worker-gpu0", Generation: 1, LeaseID: replacement.LeaseID, Release: true,
	})
	if status != http.StatusNoContent {
		t.Fatalf("release status=%d, want 204", status)
	}
	status, _ = requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{
		AgentID: "worker-gpu0", Generation: 1, LeaseID: replacement.LeaseID, Release: true,
	})
	if status != http.StatusConflict {
		t.Fatalf("duplicate release status=%d, want 409", status)
	}
}

func TestTransportLeaseEndpointMapsAuthenticationValidationAndCapacity(t *testing.T) {
	cfg := testFRPGatewayConfig()
	cfg.Transport.FRP.PortEnd = cfg.Transport.FRP.PortStart
	srv := NewServer(cfg)

	status, _ := requestTransportLease(t, srv, "wrong", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d, want 401", status)
	}
	status, _ = requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{AgentID: "", Generation: 1})
	if status != http.StatusBadRequest {
		t.Fatalf("missing id status=%d, want 400", status)
	}
	status, _ = requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1, ExcludeSlots: []int{1}})
	if status != http.StatusBadRequest {
		t.Fatalf("invalid exclusion status=%d, want 400", status)
	}
	status, _ = requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if status != http.StatusOK {
		t.Fatalf("first lease status=%d, want 200", status)
	}
	status, _ = requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu1", Generation: 1})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("capacity status=%d, want 503", status)
	}
}

func TestServerReloadsTransportLeasesFromConfigDirectory(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "gateway.yaml")
	firstServer := NewServerWithGatewayConfigPath(testFRPGatewayConfig(), configPath)
	status, first := requestTransportLease(t, firstServer, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if status != http.StatusOK {
		t.Fatalf("first status=%d", status)
	}
	leasePath := filepath.Join(directory, "transport-leases.json")
	if _, err := os.Stat(leasePath); err != nil {
		t.Fatalf("lease store at config directory: %v", err)
	}

	reloaded := NewServerWithGatewayConfigPath(testFRPGatewayConfig(), configPath)
	status, second := requestTransportLease(t, reloaded, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if status != http.StatusOK || second.LeaseID != first.LeaseID || second.RemotePort != first.RemotePort {
		t.Fatalf("reloaded status=%d lease=%+v, want sticky %+v", status, second, first)
	}
}

func TestCorruptTransportLeaseStoreFailsFRPOperationsButPreservesLegacy(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "gateway.yaml")
	if err := os.WriteFile(filepath.Join(directory, "transport-leases.json"), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	frp := NewServerWithGatewayConfigPath(testFRPGatewayConfig(), configPath)

	configReq := httptest.NewRequest(http.MethodGet, "/internal/agent/config?agent_id=worker-gpu0&tags=gpu-4090", nil)
	configReq.Header.Set("Authorization", "Bearer agent-secret")
	configRR := httptest.NewRecorder()
	frp.ServeHTTP(configRR, configReq)
	if configRR.Code != http.StatusServiceUnavailable || strings.Contains(configRR.Body.String(), "corrupt") {
		t.Fatalf("FRP config status=%d body=%q", configRR.Code, configRR.Body.String())
	}
	status, _ := requestTransportLease(t, frp, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("lease status=%d, want 503", status)
	}
	if got := postFRPHeartbeatStatus(t, frp, protocol.HeartbeatRequest{AgentID: "worker-gpu0", TransportLeaseID: "lease", TransportGeneration: 1}); got != http.StatusServiceUnavailable {
		t.Fatalf("heartbeat status=%d, want 503", got)
	}
	if workers := frp.workers.Snapshot(time.Now()); len(workers) != 0 {
		t.Fatalf("failed-init heartbeat registered workers: %+v", workers)
	}

	legacy := NewServerWithGatewayConfigPath(testGatewayConfig(), configPath)
	legacyConfigReq := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090", nil)
	legacyConfigReq.Header.Set("Authorization", "Bearer agent-secret")
	legacyConfigRR := httptest.NewRecorder()
	legacy.ServeHTTP(legacyConfigRR, legacyConfigReq)
	if legacyConfigRR.Code != http.StatusOK {
		t.Fatalf("legacy config status=%d, want 200", legacyConfigRR.Code)
	}
	if got := postFRPHeartbeatStatus(t, legacy, protocol.HeartbeatRequest{AgentID: "legacy-worker", LlamaSwapURL: "http://legacy-worker:6006"}); got != http.StatusOK {
		t.Fatalf("legacy heartbeat status=%d, want 200", got)
	}
}

func TestFRPHeartbeatRenewsExactLeaseBeforeRegisteringWorker(t *testing.T) {
	srv := NewServer(testFRPGatewayConfig())
	now := time.Unix(1_000, 0)
	srv.transportLeases.now = func() time.Time { return now }
	status, lease := requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if status != http.StatusOK {
		t.Fatalf("lease status=%d", status)
	}
	now = now.Add(time.Minute)

	status = postFRPHeartbeatStatus(t, srv, protocol.HeartbeatRequest{
		AgentID:             "worker-gpu0",
		Tags:                []string{"gpu-4090"},
		LlamaSwapURL:        "http://frps.example.test:2000",
		TransportLeaseID:    lease.LeaseID,
		TransportGeneration: lease.Generation,
	})
	if status != http.StatusOK {
		t.Fatalf("heartbeat status=%d, want 200", status)
	}

	srv.transportLeases.mu.Lock()
	stored := append([]TransportLease(nil), srv.transportLeases.leases...)
	srv.transportLeases.mu.Unlock()
	if len(stored) != 1 || !stored[0].RenewedAt.Equal(now) || !stored[0].ExpiresAt.Equal(now.Add(180*time.Second)) {
		t.Fatalf("stored lease after heartbeat = %+v", stored)
	}
	workers := srv.workers.Snapshot(time.Now())
	if len(workers) != 1 || workers[0].ID != "worker-gpu0" || workers[0].LlamaSwapURL != "http://frps.example.test:2000" {
		t.Fatalf("workers = %+v", workers)
	}
}

func TestFRPHeartbeatRejectsInvalidLeaseOrURLWithoutRegisteringWorker(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*protocol.HeartbeatRequest)
	}{
		{name: "missing lease", mutate: func(hb *protocol.HeartbeatRequest) { hb.TransportLeaseID = "" }},
		{name: "wrong lease", mutate: func(hb *protocol.HeartbeatRequest) { hb.TransportLeaseID = "wrong" }},
		{name: "wrong generation", mutate: func(hb *protocol.HeartbeatRequest) { hb.TransportGeneration++ }},
		{name: "wrong URL", mutate: func(hb *protocol.HeartbeatRequest) { hb.LlamaSwapURL = "http://frps.example.test:2001" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := NewServer(testFRPGatewayConfig())
			status, lease := requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
			if status != http.StatusOK {
				t.Fatalf("lease status=%d", status)
			}
			hb := protocol.HeartbeatRequest{
				AgentID:             "worker-gpu0",
				Tags:                []string{"gpu-4090"},
				LlamaSwapURL:        "http://frps.example.test:2000",
				TransportLeaseID:    lease.LeaseID,
				TransportGeneration: lease.Generation,
			}
			tt.mutate(&hb)
			if got := postFRPHeartbeatStatus(t, srv, hb); got != http.StatusConflict {
				t.Fatalf("status=%d, want 409", got)
			}
			if workers := srv.workers.Snapshot(time.Now()); len(workers) != 0 {
				t.Fatalf("rejected heartbeat registered workers: %+v", workers)
			}
		})
	}
}

func TestFRPHeartbeatUsesHostSafeIPv6URL(t *testing.T) {
	cfg := testFRPGatewayConfig()
	cfg.Transport.FRP.ServerAddr = "2001:db8::1"
	srv := NewServer(cfg)
	status, lease := requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if status != http.StatusOK {
		t.Fatalf("lease status=%d", status)
	}
	status = postFRPHeartbeatStatus(t, srv, protocol.HeartbeatRequest{
		AgentID:             "worker-gpu0",
		LlamaSwapURL:        "http://[2001:db8::1]:2000",
		TransportLeaseID:    lease.LeaseID,
		TransportGeneration: 1,
	})
	if status != http.StatusOK {
		t.Fatalf("heartbeat status=%d, want 200", status)
	}
}

func TestTransportPersistenceErrorsReturnServiceUnavailable(t *testing.T) {
	srv := NewServer(testFRPGatewayConfig())
	manager, err := NewTransportLeaseManager(alwaysFailingTransportLeaseStore{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.transportLeases = manager
	srv.transportLeaseErr = nil

	status, _ := requestTransportLease(t, srv, "agent-secret", protocol.TransportLeaseRequest{AgentID: "worker-gpu0", Generation: 1})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("persistence failure status=%d, want 503", status)
	}
}

type alwaysFailingTransportLeaseStore struct{}

func (alwaysFailingTransportLeaseStore) Load() ([]TransportLease, error) { return nil, nil }
func (alwaysFailingTransportLeaseStore) Save([]TransportLease) error {
	return errors.New("sensitive persistence detail")
}

func postFRPHeartbeatStatus(t *testing.T, server http.Handler, heartbeat protocol.HeartbeatRequest) int {
	t.Helper()
	body, err := json.Marshal(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/agent/heartbeat", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer agent-secret")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), "frp-secret") || strings.Contains(rr.Body.String(), "agent-secret") {
		t.Fatal("heartbeat response leaked configured secret")
	}
	return rr.Code
}

func requestTransportLease(t *testing.T, server http.Handler, token string, request protocol.TransportLeaseRequest) (int, protocol.TransportLeaseResponse) {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/internal/agent/transport/lease", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	server.ServeHTTP(rr, req)
	if strings.Contains(rr.Body.String(), "frp-secret") || strings.Contains(rr.Body.String(), "agent-secret") {
		t.Fatalf("response leaked a configured secret")
	}
	var response protocol.TransportLeaseResponse
	if rr.Code == http.StatusOK {
		if err := json.NewDecoder(rr.Body).Decode(&response); err != nil {
			t.Fatalf("decode lease: %v", err)
		}
	}
	return rr.Code, response
}

func testFRPGatewayConfig() config.GatewayConfig {
	cfg := testGatewayConfig()
	cfg.Transport = config.TransportConfig{
		Type: "frp_tcp",
		FRP: config.FRPTCPConfig{
			ServerAddr:      "frps.example.test",
			ServerPort:      7000,
			AuthToken:       "frp-secret",
			PortStart:       2000,
			PortEnd:         2007,
			LeaseTTLSeconds: 180,
		},
	}
	return cfg
}
