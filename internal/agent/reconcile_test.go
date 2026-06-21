package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestReconcileInstallsAllowedArtifactAndHeartbeatReportsReady(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	oss := artifactServer(t, payload, crc)
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGateway(t, oss.URL, crc, &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()

	modelRoot := t.TempDir()
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: filepath.Join(t.TempDir(), "llama-swap.yaml"),
		LlamaSwapURL:    "http://worker",
		LlamaSwapToken:  "worker-token",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         &FakeService{},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	got, err := os.ReadFile(filepath.Join(modelRoot, "qwen", "model.gguf"))
	if err != nil {
		t.Fatalf("read installed artifact: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("installed artifact = %q, want %q", got, payload)
	}
	rendered, err := os.ReadFile(rec.LlamaSwapConfig)
	if err != nil {
		t.Fatalf("read rendered llama-swap config: %v", err)
	}
	if !strings.Contains(string(rendered), "apiKeys:\n    - worker-token") {
		t.Fatalf("rendered llama-swap config did not use worker token:\n%s", rendered)
	}
	if strings.Contains(string(rendered), "agent-token") {
		t.Fatalf("rendered llama-swap config used gateway token:\n%s", rendered)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if heartbeats[0].Artifacts["qwen"] != "ready" {
		t.Fatalf("artifact status = %q, want ready", heartbeats[0].Artifacts["qwen"])
	}
}

func TestReconcileMarkerSkipStillReportsReady(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	if err := WriteMarker(filepath.Join(modelRoot, "qwen"), "qwen", artifact); err != nil {
		t.Fatalf("WriteMarker() error = %v", err)
	}

	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected OSS request %s %s", r.Method, r.URL.Path)
	}))
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, protocol.AgentConfigResponse{
		OSS: ossConfig(oss.URL),
		Models: map[string]config.Model{
			"qwen": {Artifact: artifact, Run: "llama --model {{model_path}}"},
		},
		TagPolicy: protocol.AgentTagPolicy{
			Tag:           "gpu-4090",
			AllowedModels: []string{"qwen"},
		},
	}, &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()

	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: filepath.Join(t.TempDir(), "llama-swap.yaml"),
		LlamaSwapURL:    "http://worker",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         &FakeService{},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if heartbeats[0].Artifacts["qwen"] != "ready" {
		t.Fatalf("artifact status = %q, want ready", heartbeats[0].Artifacts["qwen"])
	}
}

func TestReconcileConfigChangedRestartNotAllowedPersistsPendingRestart(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	oss := artifactServer(t, payload, crc)
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGateway(t, oss.URL, crc, &heartbeats, protocol.HeartbeatResponse{WorkerState: "active", RestartAllowed: false})
	defer gateway.Close()

	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       t.TempDir(),
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         &FakeService{},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if !heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = false, want true")
	}
	if _, err := os.Stat(configPath + ".restart-pending"); err != nil {
		t.Fatalf("pending restart marker missing: %v", err)
	}
}

func TestReconcileLoadsPendingRestartMarkerWhenConfigUnchanged(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	oss := artifactServer(t, payload, crc)
	defer oss.Close()

	var firstHeartbeats []protocol.HeartbeatRequest
	firstGateway := reconcileGateway(t, oss.URL, crc, &firstHeartbeats, protocol.HeartbeatResponse{WorkerState: "active", RestartAllowed: false})
	defer firstGateway.Close()

	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	modelRoot := t.TempDir()
	first := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		Gateway:         ConfigClient{BaseURL: firstGateway.URL, Token: "agent-token", HTTP: firstGateway.Client()},
		HTTPClient:      firstGateway.Client(),
		Service:         &FakeService{},
	}
	if _, err := first.Reconcile(context.Background()); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}

	var secondHeartbeats []protocol.HeartbeatRequest
	secondGateway := reconcileGateway(t, oss.URL, crc, &secondHeartbeats, protocol.HeartbeatResponse{WorkerState: "active", RestartAllowed: false})
	defer secondGateway.Close()

	second := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		Gateway:         ConfigClient{BaseURL: secondGateway.URL, Token: "agent-token", HTTP: secondGateway.Client()},
		HTTPClient:      secondGateway.Client(),
		Service:         &FakeService{},
	}
	if _, err := second.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if len(secondHeartbeats) != 1 {
		t.Fatalf("second heartbeats = %d, want 1", len(secondHeartbeats))
	}
	if !secondHeartbeats[0].NeedsRestart {
		t.Fatalf("second heartbeat needs_restart = false, want true from pending restart marker")
	}
}

func TestReconcileConfigChangedRestartAllowedRestartsServiceAndClearsNeedsRestart(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	oss := artifactServer(t, payload, crc)
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	responses := []protocol.HeartbeatResponse{
		{WorkerState: "draining", RestartAllowed: true},
		{WorkerState: "active", RestartAllowed: false},
	}
	gateway := reconcileGatewayWithDynamicHeartbeat(t, reconcileConfig(oss.URL, crc), &heartbeats, func() protocol.HeartbeatResponse {
		resp := responses[0]
		if len(responses) > 1 {
			responses = responses[1:]
		}
		return resp
	})
	defer gateway.Close()

	svc := &FakeService{}
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       t.TempDir(),
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         svc,
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if svc.Restarts != 1 {
		t.Fatalf("restarts = %d, want 1", svc.Restarts)
	}
	if len(heartbeats) != 1 || !heartbeats[0].NeedsRestart {
		t.Fatalf("first heartbeat needs_restart = %v, want true", heartbeats)
	}
	if _, err := os.Stat(configPath + ".restart-pending"); !os.IsNotExist(err) {
		t.Fatalf("pending restart marker err = %v, want not exist after successful restart", err)
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if len(heartbeats) != 2 {
		t.Fatalf("heartbeats = %d, want 2", len(heartbeats))
	}
	if heartbeats[1].NeedsRestart {
		t.Fatalf("second heartbeat needs_restart = true, want false after successful restart")
	}
}

func TestReconcileInstallErrorReportsArtifactErrorAndDoesNotMarkReady(t *testing.T) {
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGateway(t, oss.URL, "123456789", &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()

	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       t.TempDir(),
		LlamaSwapConfig: filepath.Join(t.TempDir(), "llama-swap.yaml"),
		LlamaSwapURL:    "http://worker",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         &FakeService{},
	}

	if _, err := rec.Reconcile(context.Background()); err == nil {
		t.Fatalf("Reconcile() error = nil, want install error")
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if got := heartbeats[0].Artifacts["qwen"]; got != "error" {
		t.Fatalf("artifact status = %q, want error", got)
	}
	if strings.Contains(strings.ToLower(heartbeats[0].Artifacts["qwen"]), "ready") {
		t.Fatalf("artifact status must not be ready: %q", heartbeats[0].Artifacts["qwen"])
	}
	if heartbeats[0].LastError == "" {
		t.Fatalf("last_error is empty, want install error context")
	}
}

func TestRunHeartbeatsWhileArtifactInstallBlocked(t *testing.T) {
	crc := crc64String([]byte("model payload"))
	getStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	var closeGetStarted sync.Once
	var closeRelease sync.Once
	var getCount atomic.Int32

	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-oss-hash-crc64ecma", crc)
		switch r.Method {
		case http.MethodHead:
			return
		case http.MethodGet:
			getCount.Add(1)
			closeGetStarted.Do(func() { close(getStarted) })
			select {
			case <-releaseDownload:
			case <-r.Context().Done():
			}
		default:
			t.Fatalf("unexpected OSS method %s", r.Method)
		}
	}))
	t.Cleanup(func() {
		closeRelease.Do(func() { close(releaseDownload) })
		oss.Close()
	})

	heartbeatCh := make(chan protocol.HeartbeatRequest, 16)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/agent/config":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(reconcileConfig(oss.URL, crc)); err != nil {
				t.Fatal(err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/internal/agent/heartbeat":
			var hb protocol.HeartbeatRequest
			if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
				t.Fatal(err)
			}
			heartbeatCh <- hb
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(protocol.HeartbeatResponse{}); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected gateway request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer gateway.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       t.TempDir(),
		LlamaSwapConfig: filepath.Join(t.TempDir(), "llama-swap.yaml"),
		LlamaSwapURL:    "http://worker",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      oss.Client(),
		Service:         &FakeService{},
		RunInterval:     10 * time.Millisecond,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- rec.Run(ctx)
	}()

	select {
	case <-getStarted:
	case <-time.After(time.Second):
		t.Fatal("artifact GET did not start")
	}

	installingHeartbeats := 0
	for installingHeartbeats < 2 {
		select {
		case hb := <-heartbeatCh:
			if got := hb.Artifacts["qwen"]; got == "ready" {
				t.Fatalf("artifact status = ready while download is blocked")
			} else if got == "installing" {
				installingHeartbeats++
			}
		case <-time.After(time.Second):
			t.Fatalf("observed %d installing heartbeats, want at least 2", installingHeartbeats)
		}
	}
	if got := getCount.Load(); got != 1 {
		t.Fatalf("artifact GET count = %d, want 1 while install is blocked", got)
	}

	cancel()
	closeRelease.Do(func() { close(releaseDownload) })
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func TestWriteConfigSkipsRestartWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llama-swap.yaml")
	content := []byte("models: {}\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	svc := &FakeService{}
	changed, err := WriteConfigIfChanged(path, content, svc)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("unchanged config should not be rewritten")
	}
	if svc.Restarts != 0 {
		t.Fatalf("restarts=%d want 0", svc.Restarts)
	}
}

func TestWriteConfigChangedWritesNewBytesWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llama-swap.yaml")
	if err := os.WriteFile(path, []byte("models: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := &FakeService{}
	content := []byte("models: new\n")
	changed, err := WriteConfigIfChanged(path, content, svc)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed config should be rewritten")
	}
	if svc.Restarts != 0 {
		t.Fatalf("restarts=%d want 0", svc.Restarts)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("content=%q want %q", got, content)
	}
}

func TestWriteConfigCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "llama-swap.yaml")
	content := []byte("models: {}\n")

	svc := &FakeService{}
	changed, err := WriteConfigIfChanged(path, content, svc)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("missing config should be written")
	}
	if svc.Restarts != 0 {
		t.Fatalf("restarts=%d want 0", svc.Restarts)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("content=%q want %q", got, content)
	}
}

func TestBuildHeartbeatCopiesTagsAndSetsCapacityArtifactsAndRestart(t *testing.T) {
	tags := []string{"gpu-4090"}
	hb := BuildHeartbeat("gpu-01", tags, "http://worker", protocol.AgentConfigResponse{
		TagPolicy: protocol.AgentTagPolicy{
			WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
		},
	}, true)
	tags[0] = "mutated"

	if hb.AgentID != "gpu-01" {
		t.Fatalf("agent id=%q want gpu-01", hb.AgentID)
	}
	if len(hb.Tags) != 1 || hb.Tags[0] != "gpu-4090" {
		t.Fatalf("tags=%v want copied original tag", hb.Tags)
	}
	if hb.LlamaSwapURL != "http://worker" {
		t.Fatalf("llama swap url=%q want http://worker", hb.LlamaSwapURL)
	}
	if hb.Capacity.MaxConcurrency != 2 || hb.Capacity.MaxQueue != 4 {
		t.Fatalf("capacity=%+v want worker defaults", hb.Capacity)
	}
	if !hb.NeedsRestart {
		t.Fatal("heartbeat should include needs_restart")
	}
	if hb.Artifacts == nil {
		t.Fatal("artifacts map should be initialized")
	}
	if len(hb.Artifacts) != 0 {
		t.Fatalf("artifacts=%v want empty map", hb.Artifacts)
	}
}

func TestConfigClientGetConfigSuccess(t *testing.T) {
	want := protocol.AgentConfigResponse{
		OSS: config.OSSConfig{BaseURL: "https://oss.example.com"},
		Models: map[string]config.Model{
			"qwen": {
				Artifact: config.Artifact{Object: "qwen.tar.gz", Kind: "tar_gz", CRC64ECMA: "123"},
				Run:      "vllm serve {{model_path}}",
			},
		},
		TagPolicy: protocol.AgentTagPolicy{
			Tag:            "gpu-4090",
			AllowedModels:  []string{"qwen"},
			WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method=%s want GET", r.Method)
		}
		if r.URL.Path != "/internal/agent/config" {
			t.Fatalf("path=%s want /internal/agent/config", r.URL.Path)
		}
		if got := r.URL.Query().Get("tags"); got != "gpu 4090,gpu/foo" {
			t.Fatalf("tags query=%q want escaped comma-joined tags", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("authorization=%q want bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(want); err != nil {
			t.Fatal(err)
		}
	}))
	defer srv.Close()

	got, err := (ConfigClient{BaseURL: srv.URL + "/", Token: "agent-token", HTTP: srv.Client()}).GetConfig([]string{"gpu 4090", "gpu/foo"})
	if err != nil {
		t.Fatal(err)
	}
	if got.TagPolicy.Tag != want.TagPolicy.Tag || got.TagPolicy.WorkerDefaults.MaxConcurrency != 2 {
		t.Fatalf("response=%+v want %+v", got, want)
	}
	if got.Models["qwen"].Artifact.CRC64ECMA != "123" {
		t.Fatalf("models=%+v want decoded model artifact", got.Models)
	}
}

func TestConfigClientHeartbeatSuccess(t *testing.T) {
	want := protocol.HeartbeatResponse{WorkerState: "active", RestartAllowed: true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s want POST", r.Method)
		}
		if r.URL.Path != "/internal/agent/heartbeat" {
			t.Fatalf("path=%s want /internal/agent/heartbeat", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("authorization=%q want bearer token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type=%q want application/json", got)
		}
		var hb protocol.HeartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
			t.Fatal(err)
		}
		if hb.AgentID != "gpu-01" || !hb.NeedsRestart {
			t.Fatalf("heartbeat=%+v want posted JSON body", hb)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(want); err != nil {
			t.Fatal(err)
		}
	}))
	defer srv.Close()

	got, err := (ConfigClient{BaseURL: srv.URL, Token: "agent-token", HTTP: srv.Client()}).Heartbeat(protocol.HeartbeatRequest{
		AgentID:      "gpu-01",
		NeedsRestart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("response=%+v want %+v", got, want)
	}
}

func TestConfigClientReturnsErrorOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := ConfigClient{BaseURL: srv.URL, Token: "agent-token", HTTP: srv.Client()}
	if _, err := client.GetConfig([]string{"gpu-4090"}); err == nil {
		t.Fatal("GetConfig should return error for non-2xx status")
	}
	if _, err := client.Heartbeat(protocol.HeartbeatRequest{AgentID: "gpu-01"}); err == nil {
		t.Fatal("Heartbeat should return error for non-2xx status")
	}
}

func reconcileGateway(t *testing.T, ossURL, crc string, heartbeats *[]protocol.HeartbeatRequest, heartbeatResp protocol.HeartbeatResponse) *httptest.Server {
	t.Helper()
	return reconcileGatewayWithConfig(t, reconcileConfig(ossURL, crc), heartbeats, heartbeatResp)
}

func reconcileGatewayWithConfig(t *testing.T, cfg protocol.AgentConfigResponse, heartbeats *[]protocol.HeartbeatRequest, heartbeatResp protocol.HeartbeatResponse) *httptest.Server {
	t.Helper()
	return reconcileGatewayWithDynamicHeartbeat(t, cfg, heartbeats, func() protocol.HeartbeatResponse {
		return heartbeatResp
	})
}

func reconcileGatewayWithDynamicHeartbeat(t *testing.T, cfg protocol.AgentConfigResponse, heartbeats *[]protocol.HeartbeatRequest, heartbeatResp func() protocol.HeartbeatResponse) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/agent/config":
			if got := r.URL.Query().Get("tags"); got != "gpu-4090" {
				t.Fatalf("tags = %q, want gpu-4090", got)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(cfg); err != nil {
				t.Fatal(err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/internal/agent/heartbeat":
			var hb protocol.HeartbeatRequest
			if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
				t.Fatal(err)
			}
			*heartbeats = append(*heartbeats, hb)
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(heartbeatResp()); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected gateway request %s %s", r.Method, r.URL.Path)
		}
	}))
}

func reconcileConfig(ossURL, crc string) protocol.AgentConfigResponse {
	return protocol.AgentConfigResponse{
		OSS: ossConfig(ossURL),
		Models: map[string]config.Model{
			"qwen": {
				Artifact: config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: crc},
				Run:      "llama --model {{model_path}}",
			},
		},
		TagPolicy: protocol.AgentTagPolicy{
			Tag:           "gpu-4090",
			AllowedModels: []string{"qwen"},
			WorkerDefaults: config.WorkerDefaults{
				MaxConcurrency: 2,
				MaxQueue:       4,
			},
		},
	}
}

func ossConfig(baseURL string) config.OSSConfig {
	return config.OSSConfig{BaseURL: baseURL}
}
