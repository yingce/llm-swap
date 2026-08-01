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
