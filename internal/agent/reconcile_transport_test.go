package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

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
