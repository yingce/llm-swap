package agent

import (
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
)

type transportRestartService struct {
	restarts  int
	onRestart func()
}

type unauthenticatedHealthClient struct{}

func (unauthenticatedHealthClient) HealthContext(context.Context) error { return nil }

type preparationBarrierRunningClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *preparationBarrierRunningClient) RunningModelsContext(context.Context) ([]protocol.RunningModel, error) {
	return nil, nil
}

func (c *preparationBarrierRunningClient) RunningModelsContextWithToken(ctx context.Context, token string) ([]protocol.RunningModel, error) {
	if token != "new-bootstrap-token" {
		return nil, errors.New("unauthorized")
	}
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type preparationCycleGateway struct {
	cfg         protocol.AgentConfigResponse
	configCalls chan struct{}
}

func (g *preparationCycleGateway) GetConfigContext(context.Context, []string) (protocol.AgentConfigResponse, error) {
	g.configCalls <- struct{}{}
	return g.cfg, nil
}

func (*preparationCycleGateway) HeartbeatContext(context.Context, protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error) {
	return protocol.HeartbeatResponse{}, nil
}

func (s *transportRestartService) Restart(context.Context) error {
	s.restarts++
	if s.onRestart != nil {
		s.onRestart()
	}
	return nil
}

type mutableTransportState struct {
	snapshot      RuntimeTransportSnapshot
	afterSnapshot func()
}

func (s *mutableTransportState) Snapshot() RuntimeTransportSnapshot {
	snapshot := s.snapshot
	if s.afterSnapshot != nil {
		s.afterSnapshot()
	}
	return snapshot
}

func TestReconcileUsesOneDynamicTransportSnapshotPerCycle(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	if err := WriteMarker(filepath.Join(modelRoot, "qwen"), "qwen", artifact); err != nil {
		t.Fatal(err)
	}
	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact("https://oss.invalid", artifact), &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	if err := os.WriteFile(configPath, []byte("sentinel: keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := &mutableTransportState{}
	rec := Reconciler{
		AgentID: "gpu-01", Tags: []string{"gpu-4090"}, ModelRoot: modelRoot,
		LlamaSwapConfig: configPath, LlamaSwapURL: "http://legacy-should-not-leak", LlamaSwapToken: "legacy-should-not-leak",
		TransportState: state,
		Gateway:        ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:     gateway.Client(), Service: &FakeService{},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(configPath); err != nil || string(got) != "sentinel: keep\n" {
		t.Fatalf("not-ready reconcile changed config: %q, err=%v", got, err)
	}
	assertNotReadyTransportHeartbeat(t, heartbeats[0])
	assertTransportStateEvent(t, heartbeats[0], "", "unresolved")

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertNotReadyTransportHeartbeat(t, heartbeats[1])
	assertNoTransportStateEvent(t, heartbeats[1])

	state.snapshot = RuntimeTransportSnapshot{ModeResolved: true, Managed: true}
	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertNotReadyTransportHeartbeat(t, heartbeats[2])
	assertTransportStateEvent(t, heartbeats[2], "unresolved", "not_ready")

	state.snapshot = RuntimeTransportSnapshot{
		ModeResolved: true, Managed: true, Ready: true, LlamaSwapURL: "http://frps.example.test:2000", LlamaSwapToken: "dynamic-llama-token",
		LeaseID: "lease-1", Generation: 42,
	}
	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	ready := heartbeats[3]
	if ready.LlamaSwapURL != state.snapshot.LlamaSwapURL || ready.TransportLeaseID != "lease-1" || ready.TransportGeneration != 42 {
		t.Fatalf("ready heartbeat transport = %+v", ready)
	}
	assertTransportStateEvent(t, ready, "not_ready", "ready")
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rendered)
	if !strings.Contains(text, "apiKeys:\n    - dynamic-llama-token") {
		t.Fatalf("rendered config missing dynamic token:\n%s", text)
	}
	for _, forbidden := range []string{"legacy-should-not-leak", "frps.example.test", "lease-1"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("rendered config contains transport value %q:\n%s", forbidden, text)
		}
	}
}

func TestReconcileResolvedLegacyModeUsesStaticURLAndToken(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	if err := WriteMarker(filepath.Join(modelRoot, "qwen"), "qwen", artifact); err != nil {
		t.Fatal(err)
	}
	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact("https://oss.invalid", artifact), &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()
	rec := Reconciler{
		AgentID: "gpu-01", Tags: []string{"gpu-4090"}, ModelRoot: modelRoot,
		LlamaSwapConfig: filepath.Join(t.TempDir(), "llama-swap.yaml"),
		LlamaSwapURL:    "http://legacy-worker:6006", LlamaSwapToken: "legacy-token",
		TransportState: &mutableTransportState{snapshot: RuntimeTransportSnapshot{ModeResolved: true}},
		Gateway:        ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:     gateway.Client(), Service: &FakeService{},
	}
	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if heartbeats[0].LlamaSwapURL != "http://legacy-worker:6006" || heartbeats[0].TransportLeaseID != "" || heartbeats[0].TransportGeneration != 0 {
		t.Fatalf("legacy heartbeat = %+v", heartbeats[0])
	}
	rendered, err := os.ReadFile(rec.LlamaSwapConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "apiKeys:\n    - legacy-token") {
		t.Fatalf("legacy config missing token:\n%s", rendered)
	}
	assertTransportStateEvent(t, heartbeats[0], "", "legacy_direct")
}

func TestReconcileRunOnceDoesNotRenderWhileTransportNotReady(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	if err := WriteMarker(filepath.Join(modelRoot, "qwen"), "qwen", artifact); err != nil {
		t.Fatal(err)
	}
	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact("https://oss.invalid", artifact), &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	rec := Reconciler{
		AgentID: "gpu-01", Tags: []string{"gpu-4090"}, ModelRoot: modelRoot, LlamaSwapConfig: configPath,
		TransportState: &mutableTransportState{},
		Gateway:        ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:     gateway.Client(), Service: &FakeService{},
	}
	if _, err := rec.reconcileRunOnce(context.Background(), map[string]*artifactInstallState{}, make(chan artifactInstallResult, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("not-ready reconcileRunOnce created config: %v", err)
	}
	assertNotReadyTransportHeartbeat(t, heartbeats[0])
}

func TestPrepareManagedTransportWritesTokenWithZeroReadyModelsBeforeHealthCheck(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	if err := os.WriteFile(configPath, []byte("models: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var healthRequests int
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthRequests++
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/running" {
			http.NotFound(w, r)
			return
		}
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if r.Header.Get("Authorization") != "Bearer bootstrap-llama-token" ||
			!strings.Contains(string(content), "apiKeys:\n    - bootstrap-llama-token") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"running":[]}`))
	}))
	defer local.Close()

	cfg := protocol.AgentConfigResponse{
		Models:    map[string]config.Model{},
		TagPolicy: protocol.AgentTagPolicy{Tag: "gpu-4090", AllowedModels: nil},
	}
	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, cfg, &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()
	stateClient := LlamaSwapStateClient{BaseURL: local.URL, HTTP: local.Client()}
	service := &FakeService{}
	reconciler := Reconciler{
		AgentID: "worker-gpu0", Tags: []string{"gpu-4090"}, ModelRoot: t.TempDir(), LlamaSwapConfig: configPath,
		Gateway: ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		Health:  stateClient, RunningModels: stateClient, Service: service,
	}

	if err := reconciler.PrepareManagedTransport(context.Background(), "bootstrap-llama-token"); err != nil {
		t.Fatal(err)
	}
	if healthRequests != 2 {
		t.Fatalf("authenticated running requests = %d, want negative and positive probes after atomic token update", healthRequests)
	}
	if service.Restarts != 0 {
		t.Fatalf("service restarts = %d, want hot-reload success without restart", service.Restarts)
	}
	if len(heartbeats) != 0 {
		t.Fatalf("heartbeats = %+v, want no restart authorization request", heartbeats)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("llama-swap config mode = %o, want 600 for embedded credential", got)
	}
}

func TestPrepareManagedTransportFailsClosedWithoutTokenAuthenticatedRunningEndpoint(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	if err := os.WriteFile(configPath, []byte("models: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reconciler := Reconciler{
		AgentID: "worker-gpu0", ModelRoot: t.TempDir(), LlamaSwapConfig: configPath,
		Health: unauthenticatedHealthClient{}, Service: &FakeService{},
	}
	if err := reconciler.PrepareManagedTransport(context.Background(), "bootstrap-token"); err == nil {
		t.Fatal("managed preparation passed with only an unauthenticated health endpoint")
	}
}

func TestPrepareManagedTransportRejectsRunningEndpointThatDoesNotEnforceAPIKeys(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	if err := os.WriteFile(configPath, []byte("models: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	openRunning := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"running":[]}`))
	}))
	defer openRunning.Close()
	stateClient := LlamaSwapStateClient{BaseURL: openRunning.URL, HTTP: openRunning.Client()}
	reconciler := Reconciler{
		AgentID: "worker-gpu0", ModelRoot: t.TempDir(), LlamaSwapConfig: configPath,
		RunningModels: stateClient, Health: stateClient, Service: &FakeService{},
	}
	if err := reconciler.PrepareManagedTransport(context.Background(), "bootstrap-token"); err == nil {
		t.Fatal("managed preparation passed although /running accepted an intentionally wrong token")
	}
}

func TestLlamaSwapConfigWithAPIKeyDropsAliasedOldCredentialContributors(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	old := "apiKeys: &old-keys\n    - old-hidden-token\ncredential_shadow: *old-keys\nmodels: {}\n"
	if err := os.WriteFile(configPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := llamaSwapConfigWithAPIKey(configPath, t.TempDir(), "new-bootstrap-token")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, "old-hidden-token") || strings.Contains(text, "credential_shadow") {
		t.Fatalf("prepared config retained an old credential contributor:\n%s", text)
	}
	if !strings.Contains(text, "apiKeys:\n    - new-bootstrap-token") {
		t.Fatalf("prepared config missing exact new token:\n%s", text)
	}
}

func TestManagedTransportPreparationSerializesAgainstReconcileCycle(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	if err := WriteMarker(filepath.Join(modelRoot, "qwen"), "qwen", artifact); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	if err := os.WriteFile(configPath, []byte("models: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	barrier := &preparationBarrierRunningClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	gateway := &preparationCycleGateway{
		cfg:         reconcileConfigWithArtifact("https://oss.invalid", artifact),
		configCalls: make(chan struct{}, 2),
	}
	reconciler := Reconciler{
		AgentID: "worker-gpu0", Tags: []string{"gpu-4090"}, ModelRoot: modelRoot, LlamaSwapConfig: configPath,
		TransportState: &mutableTransportState{snapshot: RuntimeTransportSnapshot{ModeResolved: true, Managed: true, Ready: true,
			LlamaSwapURL: "http://frps.example.test:2003", LlamaSwapToken: "old-token", LeaseID: "lease-7", Generation: 7}},
		Gateway: gateway, RunningModels: barrier, Service: &FakeService{},
	}
	prepareDone := make(chan error, 1)
	go func() { prepareDone <- reconciler.PrepareManagedTransport(context.Background(), "new-bootstrap-token") }()
	receiveWithTimeout(t, barrier.started)
	reconcileDone := make(chan error, 1)
	go func() {
		_, err := reconciler.Reconcile(context.Background())
		reconcileDone <- err
	}()
	select {
	case <-gateway.configCalls:
		t.Fatal("reconcile cycle entered while managed token preparation was unverified")
	case <-time.After(50 * time.Millisecond):
	}
	close(barrier.release)
	if err := receiveWithTimeout(t, prepareDone); err != nil {
		t.Fatal(err)
	}
	receiveWithTimeout(t, gateway.configCalls)
	_ = receiveWithTimeout(t, reconcileDone)
}

func TestPrepareManagedTransportRestartsOnlyAfterGatewayAuthorizationAndVerifiesNewToken(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	if err := os.WriteFile(configPath, []byte("apiKeys:\n    - old-token\nmodels: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	activeToken := "old-token"
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/running" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+activeToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"running":[]}`))
	}))
	defer local.Close()

	cfg := protocol.AgentConfigResponse{Models: map[string]config.Model{}, TagPolicy: protocol.AgentTagPolicy{Tag: "gpu-4090"}}
	var heartbeats []protocol.HeartbeatRequest
	restartAllowed := false
	gateway := reconcileGatewayWithDynamicHeartbeat(t, cfg, &heartbeats, func() protocol.HeartbeatResponse {
		return protocol.HeartbeatResponse{WorkerState: "draining", RestartAllowed: restartAllowed}
	})
	defer gateway.Close()
	service := &transportRestartService{onRestart: func() { activeToken = "new-bootstrap-token" }}
	reconciler := Reconciler{
		AgentID: "worker-gpu0", Tags: []string{"gpu-4090"}, ModelRoot: t.TempDir(), LlamaSwapConfig: configPath,
		Gateway:       ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		Health:        LlamaSwapStateClient{BaseURL: local.URL, HTTP: local.Client()},
		RunningModels: LlamaSwapStateClient{BaseURL: local.URL, HTTP: local.Client()}, Service: service,
	}

	if err := reconciler.PrepareManagedTransport(context.Background(), "new-bootstrap-token"); err == nil {
		t.Fatal("PrepareManagedTransport succeeded without gateway restart authorization")
	}
	if service.restarts != 0 {
		t.Fatalf("service restarted %d times without gateway authorization", service.restarts)
	}
	if len(heartbeats) != 1 || !heartbeats[0].NeedsRestart || heartbeats[0].LlamaSwapURL != "" ||
		heartbeats[0].TransportLeaseID != "" || heartbeats[0].TransportGeneration != 0 || len(heartbeats[0].RestartModels) != 0 {
		t.Fatalf("restart authorization heartbeat = %+v, want global not-ready restart request", heartbeats)
	}

	restartAllowed = true
	if err := reconciler.PrepareManagedTransport(context.Background(), "new-bootstrap-token"); err != nil {
		t.Fatal(err)
	}
	if service.restarts != 1 {
		t.Fatalf("service restarts = %d, want one authorized restart", service.restarts)
	}
	if activeToken != "new-bootstrap-token" {
		t.Fatalf("active token = %q, want new bootstrap token after restart", activeToken)
	}
	if len(heartbeats) < 2 || !heartbeats[1].NeedsRestart {
		t.Fatalf("heartbeats = %+v, want second authorized restart request", heartbeats)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"old-token", "agent-token"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("prepared config retained forbidden credential %q: %s", forbidden, content)
		}
	}
}

type invalidationRecordingTransportState struct {
	snapshot RuntimeTransportSnapshot
	calls    chan struct {
		leaseID    string
		generation uint64
	}
}

func (s *invalidationRecordingTransportState) Snapshot() RuntimeTransportSnapshot { return s.snapshot }

func (s *invalidationRecordingTransportState) InvalidateTransportLease(leaseID string, generation uint64) {
	s.calls <- struct {
		leaseID    string
		generation uint64
	}{leaseID: leaseID, generation: generation}
}

func TestReconcileSignalsExactLeaseInvalidationOnlyForTypedHeartbeatConflict(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		invalidate bool
	}{
		{name: "lease conflict", status: http.StatusConflict, invalidate: true},
		{name: "transient gateway failure", status: http.StatusServiceUnavailable, invalidate: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := &invalidationRecordingTransportState{
				snapshot: RuntimeTransportSnapshot{ModeResolved: true, Managed: true, Ready: true,
					LlamaSwapURL: "http://frps.example.test:2003", LlamaSwapToken: "llama-token", LeaseID: "lease-7", Generation: 7},
				calls: make(chan struct {
					leaseID    string
					generation uint64
				}, 1),
			}
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/internal/agent/config":
					_ = json.NewEncoder(w).Encode(protocol.AgentConfigResponse{Models: map[string]config.Model{}})
				case "/internal/agent/heartbeat":
					http.Error(w, "gateway detail must stay private", test.status)
				default:
					http.NotFound(w, r)
				}
			}))
			defer gateway.Close()
			reconciler := Reconciler{
				AgentID: "worker-gpu0", Tags: []string{"gpu-4090"}, ModelRoot: t.TempDir(),
				LlamaSwapConfig: filepath.Join(t.TempDir(), "llama-swap.yaml"), TransportState: state,
				Gateway: ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
				Service: &FakeService{},
			}
			_, err := reconciler.Reconcile(context.Background())
			if err == nil {
				t.Fatal("Reconcile returned nil heartbeat error")
			}
			var conflict *HeartbeatTransportLeaseConflictError
			if got := errors.As(err, &conflict); got != test.invalidate {
				t.Fatalf("typed conflict = %v, want %v: %v", got, test.invalidate, err)
			}
			select {
			case call := <-state.calls:
				if !test.invalidate {
					t.Fatalf("transient error invalidated lease: %+v", call)
				}
				if call.leaseID != "lease-7" || call.generation != 7 {
					t.Fatalf("lease invalidation = %+v, want exact active lease", call)
				}
			default:
				if test.invalidate {
					t.Fatal("typed heartbeat conflict did not invalidate active lease")
				}
			}
		})
	}
}

func TestReconcileNotReadySkipsLocalLlamaSwapRequests(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	if err := WriteMarker(filepath.Join(modelRoot, "qwen"), "qwen", artifact); err != nil {
		t.Fatal(err)
	}
	var localRequests int
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		localRequests++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer local.Close()
	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact("https://oss.invalid", artifact), &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()
	stateClient := LlamaSwapStateClient{BaseURL: local.URL, TokenSource: func() string { return "must-not-read" }, HTTP: local.Client()}
	rec := Reconciler{
		AgentID: "gpu-01", Tags: []string{"gpu-4090"}, ModelRoot: modelRoot,
		LlamaSwapConfig: filepath.Join(t.TempDir(), "llama-swap.yaml"), TransportState: &mutableTransportState{},
		Gateway:    ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient: gateway.Client(), Service: &FakeService{}, Health: stateClient, RunningModels: stateClient,
	}
	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if localRequests != 0 {
		t.Fatalf("not-ready reconcile made %d local llama-swap requests, want 0", localRequests)
	}
	if heartbeats[0].LastError != "" {
		t.Fatalf("not-ready heartbeat last_error = %q", heartbeats[0].LastError)
	}
}

func TestReconcileUsesCapturedTransportTokenForLocalStateRenderAndHeartbeat(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	if err := WriteMarker(filepath.Join(modelRoot, "qwen"), "qwen", artifact); err != nil {
		t.Fatal(err)
	}
	state := &mutableTransportState{snapshot: RuntimeTransportSnapshot{
		ModeResolved: true, Managed: true, Ready: true,
		LlamaSwapURL: "http://frps.example.test:2000", LlamaSwapToken: "cycle-token",
		LeaseID: "cycle-lease", Generation: 7,
	}}
	state.afterSnapshot = func() {
		state.afterSnapshot = nil
		state.snapshot = RuntimeTransportSnapshot{
			ModeResolved: true, Managed: true, Ready: true,
			LlamaSwapURL: "http://frps.example.test:2001", LlamaSwapToken: "next-token",
			LeaseID: "next-lease", Generation: 8,
		}
	}
	var authorization string
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"running":[]}`))
	}))
	defer local.Close()
	stateClient := LlamaSwapStateClient{
		BaseURL: local.URL, HTTP: local.Client(),
		TokenSource: func() string { return state.snapshot.LlamaSwapToken },
	}
	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact("https://oss.invalid", artifact), &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	rec := Reconciler{
		AgentID: "gpu-01", Tags: []string{"gpu-4090"}, ModelRoot: modelRoot, LlamaSwapConfig: configPath,
		TransportState: state, Gateway: ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient: gateway.Client(), Service: &FakeService{}, Health: stateClient, RunningModels: stateClient,
	}
	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer cycle-token" {
		t.Fatalf("local authorization = %q, want captured cycle token", authorization)
	}
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "apiKeys:\n    - cycle-token") || strings.Contains(string(rendered), "next-token") {
		t.Fatalf("rendered config did not use captured token:\n%s", rendered)
	}
	hb := heartbeats[0]
	if hb.LlamaSwapURL != "http://frps.example.test:2000" || hb.TransportLeaseID != "cycle-lease" || hb.TransportGeneration != 7 {
		t.Fatalf("heartbeat did not use captured snapshot: %+v", hb)
	}
}

func assertNotReadyTransportHeartbeat(t *testing.T, hb protocol.HeartbeatRequest) {
	t.Helper()
	if hb.LlamaSwapURL != "" || hb.TransportLeaseID != "" || hb.TransportGeneration != 0 {
		t.Fatalf("not-ready transport fields = url %q lease %q generation %d", hb.LlamaSwapURL, hb.TransportLeaseID, hb.TransportGeneration)
	}
}

func assertTransportStateEvent(t *testing.T, hb protocol.HeartbeatRequest, from, to string) {
	t.Helper()
	for _, event := range hb.Events {
		if event.Event == "transport_state_changed" {
			if event.FromState != from || event.ToState != to {
				t.Fatalf("transport event = %+v, want %q -> %q", event, from, to)
			}
			return
		}
	}
	t.Fatalf("heartbeat missing transport_state_changed event: %+v", hb.Events)
}

func assertNoTransportStateEvent(t *testing.T, hb protocol.HeartbeatRequest) {
	t.Helper()
	for _, event := range hb.Events {
		if event.Event == "transport_state_changed" {
			t.Fatalf("unexpected repeated transport event: %+v", event)
		}
	}
}
