package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestEffectiveCapacity(t *testing.T) {
	tests := []struct {
		derived int
		legacy  int
		want    int
	}{
		{derived: 8, legacy: 0, want: 8},
		{derived: 8, legacy: 4, want: 4},
		{derived: 2, legacy: 4, want: 2},
		{derived: 0, legacy: 0, want: 0},
		{derived: 0, legacy: 1, want: 1},
	}
	for _, tt := range tests {
		if got := effectiveCapacity(tt.derived, tt.legacy); got != tt.want {
			t.Errorf("effectiveCapacity(%d, %d) = %d, want %d", tt.derived, tt.legacy, got, tt.want)
		}
	}
}

func TestProxyUsesReadyWorkerCapacity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	cfg := testProxyConfig()
	policy := cfg.TagPolicies["gpu-4090"]
	policy.WorkerDefaults = config.WorkerDefaults{MaxConcurrency: 1, MaxQueue: 0}
	cfg.TagPolicies["gpu-4090"] = policy
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "gpu-a", upstream.URL, true)
	registerProxyWorker(t, srv, "gpu-b", upstream.URL, true)

	releaseA, err := srv.limiter.Acquire(context.Background(), "model:qwen", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	releaseB, err := srv.limiter.Acquire(context.Background(), "model:qwen", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}
	assertOpenAIErrorCode(t, rr.Body.Bytes(), "queue_full")
}

func TestModelCapacitySumsReadyEligibleWorkers(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	tests := []struct {
		name    string
		workers []struct {
			id       string
			tag      string
			artifact string
			state    string
			seenAt   time.Time
		}
		want modelCapacity
	}{
		{
			name: "one ready worker",
			workers: []struct {
				id       string
				tag      string
				artifact string
				state    string
				seenAt   time.Time
			}{{id: "gpu-01", tag: "gpu-4090", artifact: "ready", state: "ready", seenAt: now}},
			want: modelCapacity{MaxConcurrency: 2, MaxQueue: 4},
		},
		{
			name: "mixed tag capacity",
			workers: []struct {
				id       string
				tag      string
				artifact string
				state    string
				seenAt   time.Time
			}{
				{id: "gpu-01", tag: "gpu-4090", artifact: "ready", state: "ready", seenAt: now},
				{id: "gpu-02", tag: "gpu-large", artifact: "ready", state: "ready", seenAt: now},
			},
			want: modelCapacity{MaxConcurrency: 6, MaxQueue: 12},
		},
		{
			name: "excludes stale missing and loading workers",
			workers: []struct {
				id       string
				tag      string
				artifact string
				state    string
				seenAt   time.Time
			}{
				{id: "stale", tag: "gpu-4090", artifact: "ready", state: "ready", seenAt: now.Add(-7 * time.Second)},
				{id: "missing", tag: "gpu-4090", artifact: "missing", state: "ready", seenAt: now},
				{id: "loading", tag: "gpu-4090", artifact: "ready", state: "loading", seenAt: now},
			},
			want: modelCapacity{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testGatewayConfig()
			cfg.TagPolicies["gpu-large"] = config.TagPolicy{
				AllowedModels:  []string{"qwen"},
				WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 4, MaxQueue: 8},
			}
			srv := NewServer(cfg)
			for _, worker := range tt.workers {
				srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{
					AgentID:       worker.id,
					Tags:          []string{worker.tag},
					LlamaSwapURL:  "http://" + worker.id,
					Artifacts:     map[string]string{"qwen": worker.artifact},
					RunningModels: []protocol.RunningModel{{Model: "qwen", State: worker.state}},
					Capacity:      config.WorkerDefaults{MaxConcurrency: 99, MaxQueue: 99},
				}, worker.seenAt)
			}

			if got := srv.modelCapacity("qwen", now); got != tt.want {
				t.Fatalf("modelCapacity(qwen) = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestModelCapacityUsesModelTagCapacityPerReadyWorker(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	cfg := testGatewayConfig()
	model := cfg.Models["qwen"]
	model.TagCapacity = map[string]config.WorkerDefaults{
		"gpu-4090":  {MaxConcurrency: 1, MaxQueue: 2},
		"gpu-large": {MaxConcurrency: 3, MaxQueue: 7},
	}
	cfg.Models["qwen"] = model
	cfg.TagPolicies["gpu-large"] = config.TagPolicy{
		AllowedModels:  []string{"qwen"},
		WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 40, MaxQueue: 80},
	}
	srv := NewServer(cfg)
	for _, worker := range []struct {
		id  string
		tag string
	}{
		{id: "gpu-01", tag: "gpu-4090"},
		{id: "gpu-02", tag: "gpu-large"},
	} {
		srv.workers.UpsertHeartbeat(protocol.HeartbeatRequest{
			AgentID:       worker.id,
			Tags:          []string{worker.tag},
			LlamaSwapURL:  "http://" + worker.id,
			Artifacts:     map[string]string{"qwen": "ready"},
			RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		}, now)
	}

	want := modelCapacity{MaxConcurrency: 4, MaxQueue: 9}
	if got := srv.modelCapacity("qwen", now); got != want {
		t.Fatalf("modelCapacity(qwen) = %+v, want %+v", got, want)
	}
}

func TestModelTagCapacityDefaultsToOneWhenNoLegacyCapacityExists(t *testing.T) {
	cfg := testGatewayConfig()
	policy := cfg.TagPolicies["gpu-4090"]
	policy.WorkerDefaults = config.WorkerDefaults{}
	cfg.TagPolicies["gpu-4090"] = policy

	want := config.WorkerDefaults{MaxConcurrency: 1, MaxQueue: 1}
	if got := modelTagCapacity(cfg, "qwen", "gpu-4090"); got != want {
		t.Fatalf("modelTagCapacity(qwen, gpu-4090) = %+v, want %+v", got, want)
	}
}

func TestModelTagCapacityChangeIsHotApply(t *testing.T) {
	oldCfg := testGatewayConfig()
	newCfg := cloneGatewayConfig(oldCfg)
	model := newCfg.Models["qwen"]
	model.TagCapacity = map[string]config.WorkerDefaults{
		"gpu-4090": {MaxConcurrency: 2, MaxQueue: 3},
	}
	newCfg.Models["qwen"] = model

	changes := diffGatewayConfig(oldCfg, newCfg)
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want one model change", changes)
	}
	if changes[0].Path != "models.qwen" || changes[0].RequiresWorkerRestart || changes[0].RequiresGatewayRestart {
		t.Fatalf("change = %+v, want hot model capacity update", changes[0])
	}
}

func TestCloneGatewayConfigDeepCopiesModelTagCapacity(t *testing.T) {
	cfg := testGatewayConfig()
	model := cfg.Models["qwen"]
	model.TagCapacity = map[string]config.WorkerDefaults{
		"gpu-4090": {MaxConcurrency: 1, MaxQueue: 1},
	}
	cfg.Models["qwen"] = model

	cloned := cloneGatewayConfig(cfg)
	clonedModel := cloned.Models["qwen"]
	clonedModel.TagCapacity["gpu-4090"] = config.WorkerDefaults{MaxConcurrency: 9, MaxQueue: 9}
	cloned.Models["qwen"] = clonedModel

	if got := cfg.Models["qwen"].TagCapacity["gpu-4090"]; got.MaxConcurrency != 1 || got.MaxQueue != 1 {
		t.Fatalf("source tag capacity mutated through clone: %+v", got)
	}
}
