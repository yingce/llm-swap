package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

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
