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

func TestReconcileHeartbeatIncludesRunningModels(t *testing.T) {
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
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact(oss.URL, artifact), &heartbeats, protocol.HeartbeatResponse{})
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
		RunningModels: &fakeRunningModelsClient{
			models: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if got := heartbeats[0].RunningModels; len(got) != 1 || got[0].Model != "qwen" || got[0].State != "ready" {
		t.Fatalf("running models = %+v, want qwen ready", got)
	}
}

func TestReconcileHeartbeatIncludesGPUDevices(t *testing.T) {
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
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact(oss.URL, artifact), &heartbeats, protocol.HeartbeatResponse{})
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
		GPUDevices: &fakeGPUDevicesClient{devices: []protocol.GPUDevice{{
			Index:              0,
			Name:               "NVIDIA GeForce RTX 4090",
			UUID:               "GPU-test",
			MemoryTotalMiB:     24564,
			MemoryUsedMiB:      4096,
			MemoryFreeMiB:      20468,
			UtilizationPercent: 25,
			TemperatureCelsius: 58,
		}}},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if got := heartbeats[0].GPUDevices; len(got) != 1 || got[0].Name != "NVIDIA GeForce RTX 4090" || got[0].MemoryUsedMiB != 4096 {
		t.Fatalf("gpu devices = %+v, want one RTX 4090", got)
	}
}

func TestReconcileReportsRunningModelLoadStateChangeAndUnloadEvents(t *testing.T) {
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
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact(oss.URL, artifact), &heartbeats, protocol.HeartbeatResponse{})
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
		RunningModels: &sequenceRunningModelsClient{sequences: [][]protocol.RunningModel{
			{{Model: "qwen", State: "loading"}},
			{{Model: "qwen", State: "ready"}},
			{},
		}},
	}

	for i := 0; i < 3; i++ {
		if _, err := rec.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile(%d) error = %v", i+1, err)
		}
	}
	if len(heartbeats) != 3 {
		t.Fatalf("heartbeats = %d, want 3", len(heartbeats))
	}
	assertHeartbeatEvent(t, heartbeats[0], "model_loaded", "qwen")
	assertHeartbeatEvent(t, heartbeats[1], "model_state_changed", "qwen")
	assertHeartbeatEvent(t, heartbeats[2], "model_unloaded", "qwen")
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

func TestReconcileRunOnceRendersReadySubsetWhileOtherArtifactInstalls(t *testing.T) {
	payload := []byte("cold payload")
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: crc64String(payload)}
	modelRoot := t.TempDir()
	if err := WriteMarker(filepath.Join(modelRoot, "qwen"), "qwen", artifact); err != nil {
		t.Fatalf("WriteMarker(qwen) error = %v", err)
	}

	downloadStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	var closeStarted sync.Once
	var closeRelease sync.Once

	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/model.gguf" {
			t.Fatalf("unexpected OSS request path %s", r.URL.Path)
		}
		w.Header().Set("x-oss-hash-crc64ecma", artifact.CRC64ECMA)
		switch r.Method {
		case http.MethodHead:
			return
		case http.MethodGet:
			closeStarted.Do(func() { close(downloadStarted) })
			select {
			case <-releaseDownload:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(payload)
		default:
			t.Fatalf("unexpected OSS method %s", r.Method)
		}
	}))
	t.Cleanup(func() {
		closeRelease.Do(func() { close(releaseDownload) })
		oss.Close()
	})

	cfg := reconcileConfigWithTwoModels(oss.URL, artifact, "serve qwen --model {{model_path}}", "serve cold --model {{model_path}}")

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, cfg, &heartbeats, protocol.HeartbeatResponse{})
	defer gateway.Close()

	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		LlamaSwapToken:  "worker-token",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      oss.Client(),
		Service:         &FakeService{},
	}

	installs := make(map[string]*artifactInstallState)
	installDone := make(chan artifactInstallResult, 2)
	if _, err := rec.reconcileRunOnce(context.Background(), installs, installDone); err != nil {
		t.Fatalf("reconcileRunOnce() error = %v", err)
	}

	select {
	case <-downloadStarted:
	case <-time.After(time.Second):
		t.Fatal("cold artifact download did not start")
	}

	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read rendered llama-swap config: %v", err)
	}
	text := string(rendered)
	if !strings.Contains(text, "qwen") {
		t.Fatalf("rendered config missing ready model qwen:\n%s", text)
	}
	if strings.Contains(text, "cold") {
		t.Fatalf("rendered config unexpectedly included installing model cold:\n%s", text)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if got := heartbeats[0].Artifacts["qwen"]; got != "ready" {
		t.Fatalf("qwen artifact status = %q, want ready", got)
	}
	if got := heartbeats[0].Artifacts["cold"]; got != "installing" {
		t.Fatalf("cold artifact status = %q, want installing", got)
	}

	closeRelease.Do(func() { close(releaseDownload) })
	select {
	case <-installDone:
	case <-time.After(time.Second):
		t.Fatal("cold artifact install did not finish after release")
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

func TestReconcileConfigChangedForUnloadedModelDoesNotRequestRestart(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	for _, modelName := range []string{"qwen", "cold"} {
		if err := WriteMarker(filepath.Join(modelRoot, modelName), modelName, artifact); err != nil {
			t.Fatalf("WriteMarker(%s) error = %v", modelName, err)
		}
	}

	oldCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "llama --model {{model_path}}", "cold-old --model {{model_path}}")
	newCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "llama --model {{model_path}}", "cold-new --model {{model_path}}")
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	oldRendered, err := RenderLlamaSwapConfig(oldCfg, modelRoot, "worker-token")
	if err != nil {
		t.Fatalf("RenderLlamaSwapConfig(old) error = %v", err)
	}
	if err := os.WriteFile(configPath, oldRendered, 0o644); err != nil {
		t.Fatal(err)
	}

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, newCfg, &heartbeats, protocol.HeartbeatResponse{WorkerState: "active", RestartAllowed: true})
	defer gateway.Close()
	svc := &FakeService{}
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		LlamaSwapToken:  "worker-token",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         svc,
		RunningModels:   &fakeRunningModelsClient{models: []protocol.RunningModel{{Model: "qwen", State: "ready"}}},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = true, want false for unloaded model config change")
	}
	if svc.Restarts != 0 {
		t.Fatalf("service restarts = %d, want 0", svc.Restarts)
	}
	if _, err := os.Stat(configPath + ".restart-pending"); !os.IsNotExist(err) {
		t.Fatalf("pending restart marker err = %v, want not exist", err)
	}
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "cold-old") {
		t.Fatalf("config was updated while another model was loaded:\n%s", rendered)
	}
}

func TestReconcileConfigChangedForUnloadedModelWritesWhenNoModelRunning(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	for _, modelName := range []string{"qwen", "cold"} {
		if err := WriteMarker(filepath.Join(modelRoot, modelName), modelName, artifact); err != nil {
			t.Fatalf("WriteMarker(%s) error = %v", modelName, err)
		}
	}

	oldCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "llama --model {{model_path}}", "cold-old --model {{model_path}}")
	newCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "llama --model {{model_path}}", "cold-new --model {{model_path}}")
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	oldRendered, err := RenderLlamaSwapConfig(oldCfg, modelRoot, "worker-token")
	if err != nil {
		t.Fatalf("RenderLlamaSwapConfig(old) error = %v", err)
	}
	if err := os.WriteFile(configPath, oldRendered, 0o644); err != nil {
		t.Fatal(err)
	}

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, newCfg, &heartbeats, protocol.HeartbeatResponse{WorkerState: "active", RestartAllowed: true})
	defer gateway.Close()
	svc := &FakeService{}
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		LlamaSwapToken:  "worker-token",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         svc,
		RunningModels:   &fakeRunningModelsClient{},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = true, want false for idle unloaded model config change")
	}
	if svc.Restarts != 0 {
		t.Fatalf("service restarts = %d, want 0", svc.Restarts)
	}
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "cold-new") {
		t.Fatalf("config was not updated for idle worker:\n%s", rendered)
	}
}

func TestReconcileConfigChangedForStoppedModelDoesNotRequestRestart(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	for _, modelName := range []string{"qwen", "cold"} {
		if err := WriteMarker(filepath.Join(modelRoot, modelName), modelName, artifact); err != nil {
			t.Fatalf("WriteMarker(%s) error = %v", modelName, err)
		}
	}

	oldCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "qwen --model {{model_path}}", "cold-old --model {{model_path}}")
	newCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "qwen --model {{model_path}}", "cold-new --model {{model_path}}")
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	oldRendered, err := RenderLlamaSwapConfig(oldCfg, modelRoot, "worker-token")
	if err != nil {
		t.Fatalf("RenderLlamaSwapConfig(old) error = %v", err)
	}
	if err := os.WriteFile(configPath, oldRendered, 0o644); err != nil {
		t.Fatal(err)
	}

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, newCfg, &heartbeats, protocol.HeartbeatResponse{WorkerState: "active", RestartAllowed: true})
	defer gateway.Close()
	svc := &FakeService{}
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		LlamaSwapToken:  "worker-token",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         svc,
		RunningModels: &fakeRunningModelsClient{models: []protocol.RunningModel{
			{Model: "qwen", State: "ready"},
			{Model: "cold", State: "stopped"},
		}},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = true, want false for stopped model config change")
	}
	if svc.Restarts != 0 {
		t.Fatalf("service restarts = %d, want 0", svc.Restarts)
	}
	if _, err := os.Stat(configPath + ".restart-pending"); !os.IsNotExist(err) {
		t.Fatalf("pending restart marker err = %v, want not exist", err)
	}
}

func TestReconcileConfigChangedForLoadedModelRequestsRestart(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	for _, modelName := range []string{"qwen", "cold"} {
		if err := WriteMarker(filepath.Join(modelRoot, modelName), modelName, artifact); err != nil {
			t.Fatalf("WriteMarker(%s) error = %v", modelName, err)
		}
	}

	oldCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "qwen-old --model {{model_path}}", "cold --model {{model_path}}")
	newCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "qwen-new --model {{model_path}}", "cold --model {{model_path}}")
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	oldRendered, err := RenderLlamaSwapConfig(oldCfg, modelRoot, "worker-token")
	if err != nil {
		t.Fatalf("RenderLlamaSwapConfig(old) error = %v", err)
	}
	if err := os.WriteFile(configPath, oldRendered, 0o644); err != nil {
		t.Fatal(err)
	}

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, newCfg, &heartbeats, protocol.HeartbeatResponse{WorkerState: "active", RestartAllowed: false})
	defer gateway.Close()
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		LlamaSwapToken:  "worker-token",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         &FakeService{},
		RunningModels:   &fakeRunningModelsClient{models: []protocol.RunningModel{{Model: "qwen", State: "ready"}}},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if !heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = false, want true for loaded model config change")
	}
	if _, err := os.Stat(configPath + ".restart-pending"); err != nil {
		t.Fatalf("pending restart marker missing: %v", err)
	}
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "qwen-old") {
		t.Fatalf("config was updated before restart was allowed:\n%s", rendered)
	}
}

func TestReconcileConfigChangedForLoadedModelWritesConfigWhenRestartAllowed(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	modelRoot := t.TempDir()
	for _, modelName := range []string{"qwen", "cold"} {
		if err := WriteMarker(filepath.Join(modelRoot, modelName), modelName, artifact); err != nil {
			t.Fatalf("WriteMarker(%s) error = %v", modelName, err)
		}
	}

	oldCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "qwen-old --model {{model_path}}", "cold --model {{model_path}}")
	newCfg := reconcileConfigWithTwoModels("https://oss.example.com", artifact, "qwen-new --model {{model_path}}", "cold --model {{model_path}}")
	configPath := filepath.Join(t.TempDir(), "llama-swap.yaml")
	oldRendered, err := RenderLlamaSwapConfig(oldCfg, modelRoot, "worker-token")
	if err != nil {
		t.Fatalf("RenderLlamaSwapConfig(old) error = %v", err)
	}
	if err := os.WriteFile(configPath, oldRendered, 0o644); err != nil {
		t.Fatal(err)
	}

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, newCfg, &heartbeats, protocol.HeartbeatResponse{WorkerState: "draining", RestartAllowed: true})
	defer gateway.Close()
	svc := &FakeService{}
	rec := Reconciler{
		AgentID:         "gpu-01",
		Tags:            []string{"gpu-4090"},
		ModelRoot:       modelRoot,
		LlamaSwapConfig: configPath,
		LlamaSwapURL:    "http://worker",
		LlamaSwapToken:  "worker-token",
		Gateway:         ConfigClient{BaseURL: gateway.URL, Token: "agent-token", HTTP: gateway.Client()},
		HTTPClient:      gateway.Client(),
		Service:         svc,
		RunningModels:   &fakeRunningModelsClient{models: []protocol.RunningModel{{Model: "qwen", State: "ready"}}},
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if !heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = false, want true for loaded model config change")
	}
	if svc.Restarts != 1 {
		t.Fatalf("service restarts = %d, want 1", svc.Restarts)
	}
	if _, err := os.Stat(configPath + ".restart-pending"); !os.IsNotExist(err) {
		t.Fatalf("pending restart marker err = %v, want not exist after restart", err)
	}
	rendered, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "qwen-new") {
		t.Fatalf("config was not updated before restart:\n%s", rendered)
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

func TestWriteConfigChangedMarkerFailureDoesNotCommitNewBytes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "llama-swap.yaml")
	markerPath := restartPendingMarkerPath(configPath)
	oldConfig := []byte("models: old\n")
	if err := os.WriteFile(configPath, oldConfig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, []byte("pending\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	markerErr := os.ErrPermission
	changed, err := writeConfigIfChangedAndMarkPending(configPath, []byte("models: new\n"), func() error {
		return markerErr
	})
	if !errors.Is(err, markerErr) {
		t.Fatalf("writeConfigIfChangedAndMarkPending() error = %v, want %v", err, markerErr)
	}
	if changed {
		t.Fatalf("changed = true, want false when marker write fails before commit")
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after failed write: %v", err)
	}
	if string(got) != string(oldConfig) {
		t.Fatalf("config changed despite marker write failure:\n%s", got)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("pending marker was cleared after failed write: %v", err)
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

func TestReconcileRestartAllowedVerifiesLlamaSwapHealthAndRunningBeforeClearingPending(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	oss := artifactServer(t, payload, crc)
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGateway(t, oss.URL, crc, &heartbeats, protocol.HeartbeatResponse{WorkerState: "draining", RestartAllowed: true})
	defer gateway.Close()

	svc := &FakeService{}
	checker := &fakeLlamaSwapHealthChecker{}
	running := &fakeRunningModelsClient{models: []protocol.RunningModel{{Model: "qwen", State: "ready"}}}
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
		Health:          checker,
		RunningModels:   running,
	}

	if _, err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if svc.Restarts != 1 {
		t.Fatalf("restarts = %d, want 1", svc.Restarts)
	}
	if checker.calls != 1 {
		t.Fatalf("health checks = %d, want 1", checker.calls)
	}
	if running.calls != 2 {
		t.Fatalf("running calls = %d, want pre-heartbeat and post-restart checks", running.calls)
	}
	if _, err := os.Stat(configPath + ".restart-pending"); !os.IsNotExist(err) {
		t.Fatalf("pending restart marker err = %v, want not exist after verified restart", err)
	}
}

func TestReconcileRestartHealthFailureKeepsPendingMarker(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	oss := artifactServer(t, payload, crc)
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGateway(t, oss.URL, crc, &heartbeats, protocol.HeartbeatResponse{WorkerState: "draining", RestartAllowed: true})
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
		Health:          &fakeLlamaSwapHealthChecker{err: errors.New("health not ready")},
	}

	if _, err := rec.Reconcile(context.Background()); err == nil {
		t.Fatalf("Reconcile() error = nil, want health check error")
	} else if !strings.Contains(err.Error(), "verify llama-swap health") {
		t.Fatalf("Reconcile() error = %q, want health context", err)
	}
	if _, err := os.Stat(configPath + ".restart-pending"); err != nil {
		t.Fatalf("pending restart marker missing after failed health check: %v", err)
	}
}

func TestReconcileLoggingServiceRestartErrorKeepsPendingMarker(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	oss := artifactServer(t, payload, crc)
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGateway(t, oss.URL, crc, &heartbeats, protocol.HeartbeatResponse{WorkerState: "draining", RestartAllowed: true})
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
		Service:         LoggingService{},
	}

	if _, err := rec.Reconcile(context.Background()); err == nil {
		t.Fatalf("Reconcile() error = nil, want logging service restart error")
	} else if !strings.Contains(err.Error(), "llama_swap_service") {
		t.Fatalf("Reconcile() error = %q, want llama_swap_service context", err)
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if !heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = false, want true before restart attempt")
	}
	if _, err := os.Stat(configPath + ".restart-pending"); err != nil {
		t.Fatalf("pending restart marker missing after failed logging restart: %v", err)
	}
}

func TestReconcileInstallErrorReportsArtifactErrorAndDoesNotMarkReady(t *testing.T) {
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGateway(t, oss.URL, "123456789", &heartbeats, protocol.HeartbeatResponse{RestartAllowed: true})
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
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("llama-swap config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(configPath + ".restart-pending"); !os.IsNotExist(err) {
		t.Fatalf("pending restart marker err = %v, want not exist", err)
	}
	if svc.Restarts != 0 {
		t.Fatalf("service restarts = %d, want 0", svc.Restarts)
	}
	if heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = true, want false")
	}
}

func TestReconcileRunOnceInstallErrorSkipsConfigAndRestart(t *testing.T) {
	artifact := config.Artifact{Object: "models/model.gguf", Kind: "file", CRC64ECMA: "123456789"}
	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected OSS request %s %s", r.Method, r.URL.Path)
	}))
	defer oss.Close()

	var heartbeats []protocol.HeartbeatRequest
	gateway := reconcileGatewayWithConfig(t, reconcileConfigWithArtifact(oss.URL, artifact), &heartbeats, protocol.HeartbeatResponse{RestartAllowed: true})
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
	installs := map[string]*artifactInstallState{
		"qwen": {
			key: artifactKey("qwen", artifact.Object, artifact.Kind, artifact.CRC64ECMA),
			err: errors.New("download failed"),
		},
	}

	if _, err := rec.reconcileRunOnce(context.Background(), installs, make(chan artifactInstallResult, 1)); err == nil {
		t.Fatalf("reconcileRunOnce() error = nil, want install error")
	}
	if len(heartbeats) != 1 {
		t.Fatalf("heartbeats = %d, want 1", len(heartbeats))
	}
	if got := heartbeats[0].Artifacts["qwen"]; got != "error" {
		t.Fatalf("artifact status = %q, want error", got)
	}
	if heartbeats[0].LastError == "" {
		t.Fatalf("last_error is empty, want install error context")
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("llama-swap config stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(configPath + ".restart-pending"); !os.IsNotExist(err) {
		t.Fatalf("pending restart marker err = %v, want not exist", err)
	}
	if svc.Restarts != 0 {
		t.Fatalf("service restarts = %d, want 0", svc.Restarts)
	}
	if heartbeats[0].NeedsRestart {
		t.Fatalf("heartbeat needs_restart = true, want false")
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
	sawInstallStartEvent := false
	for installingHeartbeats < 2 {
		select {
		case hb := <-heartbeatCh:
			if got := hb.Artifacts["qwen"]; got == "ready" {
				t.Fatalf("artifact status = ready while download is blocked")
			} else if got == "installing" {
				installingHeartbeats++
			}
			for _, event := range hb.Events {
				if event.Event == "artifact_install_start" && event.Model == "qwen" {
					sawInstallStartEvent = true
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("observed %d installing heartbeats, want at least 2", installingHeartbeats)
		}
	}
	if !sawInstallStartEvent {
		t.Fatal("heartbeat did not include artifact_install_start event")
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

func TestRunDoesNotStartNewArtifactInstallWhileSameModelRunning(t *testing.T) {
	payloadA := []byte("model payload A")
	payloadB := []byte("model payload B")
	crcA := crc64String(payloadA)
	crcB := crc64String(payloadB)
	artifactA := config.Artifact{Object: "models/a.gguf", Kind: "file", CRC64ECMA: crcA}
	artifactB := config.Artifact{Object: "models/b.gguf", Kind: "file", CRC64ECMA: crcB}

	aGetStarted := make(chan struct{})
	bGetStarted := make(chan struct{})
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	var closeAGetStarted sync.Once
	var closeBGetStarted sync.Once
	var closeReleaseA sync.Once
	var closeReleaseB sync.Once
	var getCount atomic.Int32
	var activeGETs atomic.Int32
	concurrentGET := make(chan int32, 1)

	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload []byte
		var crc string
		var started chan struct{}
		var closeStarted *sync.Once
		var release <-chan struct{}
		switch r.URL.Path {
		case "/models/a.gguf":
			payload = payloadA
			crc = crcA
			started = aGetStarted
			closeStarted = &closeAGetStarted
			release = releaseA
		case "/models/b.gguf":
			payload = payloadB
			crc = crcB
			started = bGetStarted
			closeStarted = &closeBGetStarted
			release = releaseB
		default:
			t.Fatalf("unexpected artifact path %s", r.URL.Path)
		}

		w.Header().Set("x-oss-hash-crc64ecma", crc)
		switch r.Method {
		case http.MethodHead:
			return
		case http.MethodGet:
			getCount.Add(1)
			if got := activeGETs.Add(1); got > 1 {
				select {
				case concurrentGET <- got:
				default:
				}
			}
			defer activeGETs.Add(-1)
			closeStarted.Do(func() { close(started) })
			select {
			case <-release:
				_, _ = w.Write(payload)
			case <-r.Context().Done():
			}
		default:
			t.Fatalf("unexpected OSS method %s", r.Method)
		}
	}))
	t.Cleanup(func() {
		closeReleaseA.Do(func() { close(releaseA) })
		closeReleaseB.Do(func() { close(releaseB) })
		oss.Close()
	})

	var cfgMu sync.RWMutex
	cfg := reconcileConfigWithArtifact(oss.URL, artifactA)
	bConfigServed := make(chan struct{})
	postBHeartbeat := make(chan protocol.HeartbeatRequest, 16)
	var closeBConfigServed sync.Once

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer agent-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/internal/agent/config":
			cfgMu.RLock()
			current := cfg
			cfgMu.RUnlock()
			if current.Models["qwen"].Artifact.Object == artifactB.Object {
				closeBConfigServed.Do(func() { close(bConfigServed) })
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(current); err != nil {
				t.Fatal(err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/internal/agent/heartbeat":
			var hb protocol.HeartbeatRequest
			if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
				t.Fatal(err)
			}
			select {
			case <-bConfigServed:
				postBHeartbeat <- hb
			default:
			}
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
	case <-aGetStarted:
	case <-time.After(time.Second):
		t.Fatal("artifact A GET did not start")
	}

	cfgMu.Lock()
	cfg = reconcileConfigWithArtifact(oss.URL, artifactB)
	cfgMu.Unlock()

	select {
	case hb := <-postBHeartbeat:
		if got := hb.Artifacts["qwen"]; got != "installing" {
			t.Fatalf("post-change artifact status = %q, want installing", got)
		}
	case <-time.After(time.Second):
		t.Fatal("heartbeat after artifact B config was not observed")
	}
	select {
	case <-bGetStarted:
		t.Fatal("artifact B GET started while artifact A install was still running")
	case got := <-concurrentGET:
		t.Fatalf("concurrent artifact GETs = %d, want at most 1", got)
	case <-time.After(100 * time.Millisecond):
	}
	if got := getCount.Load(); got != 1 {
		t.Fatalf("artifact GET count before releasing A = %d, want 1", got)
	}

	closeReleaseA.Do(func() { close(releaseA) })
	select {
	case <-bGetStarted:
	case <-time.After(time.Second):
		t.Fatal("artifact B GET did not start after artifact A finished")
	}
	closeReleaseB.Do(func() { close(releaseB) })

	cancel()
	closeReleaseA.Do(func() { close(releaseA) })
	closeReleaseB.Do(func() { close(releaseB) })
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}

func TestInstallAllowedArtifactsAsyncStartsOnlyOneNewInstall(t *testing.T) {
	artifactA := config.Artifact{Object: "models/a.gguf", Kind: "file", CRC64ECMA: "111"}
	artifactB := config.Artifact{Object: "models/b.gguf", Kind: "file", CRC64ECMA: "222"}
	getStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	var closeGetStarted sync.Once
	var closeRelease sync.Once
	var getCount atomic.Int32

	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models/a.gguf":
			w.Header().Set("x-oss-hash-crc64ecma", artifactA.CRC64ECMA)
		case "/models/b.gguf":
			w.Header().Set("x-oss-hash-crc64ecma", artifactB.CRC64ECMA)
		default:
			t.Fatalf("unexpected artifact path %s", r.URL.Path)
		}
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

	rec := Reconciler{
		AgentID:         "gpu-01",
		ModelRoot:       t.TempDir(),
		LlamaSwapConfig: filepath.Join(t.TempDir(), "llama-swap.yaml"),
		HTTPClient:      oss.Client(),
	}
	cfg := protocol.AgentConfigResponse{
		OSS: ossConfig(oss.URL),
		Models: map[string]config.Model{
			"a": {Artifact: artifactA, Run: "serve a"},
			"b": {Artifact: artifactB, Run: "serve b"},
		},
		TagPolicy: protocol.AgentTagPolicy{AllowedModels: []string{"a", "b"}},
	}
	installs := make(map[string]*artifactInstallState)
	installDone := make(chan artifactInstallResult, 2)

	status, installing, err := rec.installAllowedArtifactsAsync(context.Background(), cfg, installs, installDone)
	if err != nil {
		t.Fatalf("installAllowedArtifactsAsync() error = %v", err)
	}
	if !installing {
		t.Fatal("installing = false, want true")
	}
	if status["a"] != "installing" || status["b"] != "pending" {
		t.Fatalf("status = %#v, want a installing and b pending", status)
	}

	select {
	case <-getStarted:
	case <-time.After(time.Second):
		t.Fatal("first artifact GET did not start")
	}

	status, installing, err = rec.installAllowedArtifactsAsync(context.Background(), cfg, installs, installDone)
	if err != nil {
		t.Fatalf("second installAllowedArtifactsAsync() error = %v", err)
	}
	if !installing {
		t.Fatal("second installing = false, want true")
	}
	if status["a"] != "installing" || status["b"] != "pending" {
		t.Fatalf("second status = %#v, want a installing and b pending", status)
	}
	if got := getCount.Load(); got != 1 {
		t.Fatalf("artifact GET count = %d, want 1", got)
	}
}

func TestRunRetriesAsyncInstallAfterTransientFailure(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	var getAttempts atomic.Int32

	oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-oss-hash-crc64ecma", crc)
		switch r.Method {
		case http.MethodHead:
			return
		case http.MethodGet:
			if attempt := getAttempts.Add(1); attempt == 1 {
				http.Error(w, "temporary outage", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write(payload)
		default:
			t.Fatalf("unexpected OSS method %s", r.Method)
		}
	}))
	defer oss.Close()

	heartbeatCh := make(chan protocol.HeartbeatRequest, 32)
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

	deadline := time.After(2 * time.Second)
	sawError := false
	for !sawError {
		select {
		case hb := <-heartbeatCh:
			if hb.Artifacts["qwen"] == "error" {
				if hb.LastError == "" {
					t.Fatal("error heartbeat last_error is empty")
				}
				sawError = true
			}
		case <-deadline:
			t.Fatal("did not observe async install error heartbeat")
		}
	}

	readyDeadline := time.After(2 * time.Second)
	for {
		select {
		case hb := <-heartbeatCh:
			if hb.Artifacts["qwen"] == "ready" {
				if got := getAttempts.Load(); got < 2 {
					t.Fatalf("GET attempts = %d, want at least 2 after retry", got)
				}
				cancel()
				select {
				case err := <-errCh:
					if err != context.Canceled {
						t.Fatalf("Run() error = %v, want context.Canceled", err)
					}
				case <-time.After(time.Second):
					t.Fatal("Run() did not stop after cancellation")
				}
				return
			}
		case <-readyDeadline:
			t.Fatal("did not observe ready heartbeat after transient failure")
		}
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
	return reconcileConfigWithArtifact(ossURL, config.Artifact{
		Object:    "models/model.gguf",
		Kind:      "file",
		CRC64ECMA: crc,
	})
}

func reconcileConfigWithArtifact(ossURL string, artifact config.Artifact) protocol.AgentConfigResponse {
	return protocol.AgentConfigResponse{
		OSS: ossConfig(ossURL),
		Models: map[string]config.Model{
			"qwen": {
				Artifact: artifact,
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

func reconcileConfigWithTwoModels(ossURL string, artifact config.Artifact, qwenRun string, coldRun string) protocol.AgentConfigResponse {
	return protocol.AgentConfigResponse{
		OSS: ossConfig(ossURL),
		Models: map[string]config.Model{
			"qwen": {
				Artifact: artifact,
				Run:      qwenRun,
			},
			"cold": {
				Artifact: artifact,
				Run:      coldRun,
			},
		},
		TagPolicy: protocol.AgentTagPolicy{
			Tag:           "gpu-4090",
			AllowedModels: []string{"qwen", "cold"},
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

type fakeRunningModelsClient struct {
	models []protocol.RunningModel
	err    error
	calls  int
}

func (f *fakeRunningModelsClient) RunningModelsContext(context.Context) ([]protocol.RunningModel, error) {
	f.calls++
	return f.models, f.err
}

type sequenceRunningModelsClient struct {
	sequences [][]protocol.RunningModel
	calls     int
}

func (f *sequenceRunningModelsClient) RunningModelsContext(context.Context) ([]protocol.RunningModel, error) {
	if f.calls >= len(f.sequences) {
		f.calls++
		return nil, nil
	}
	models := append([]protocol.RunningModel(nil), f.sequences[f.calls]...)
	f.calls++
	return models, nil
}

type fakeGPUDevicesClient struct {
	devices []protocol.GPUDevice
	err     error
}

func (f *fakeGPUDevicesClient) GPUDevicesContext(context.Context) ([]protocol.GPUDevice, error) {
	return f.devices, f.err
}

func assertHeartbeatEvent(t *testing.T, hb protocol.HeartbeatRequest, eventName string, model string) {
	t.Helper()
	for _, event := range hb.Events {
		if event.Event == eventName && event.Model == model {
			return
		}
	}
	t.Fatalf("heartbeat events = %+v, want %s for %s", hb.Events, eventName, model)
}

type fakeLlamaSwapHealthChecker struct {
	calls int
	err   error
}

func (f *fakeLlamaSwapHealthChecker) HealthContext(context.Context) error {
	f.calls++
	return f.err
}
