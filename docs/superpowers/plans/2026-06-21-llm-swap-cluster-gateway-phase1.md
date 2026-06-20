# llama-swap Cluster Gateway Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Phase 1 Go implementation of the cluster gateway and thin worker agent described in `docs/superpowers/specs/2026-06-21-llm-swap-cluster-gateway-design.md`.

**Architecture:** The gateway owns configuration, worker state, scheduling, request accounting, retry, streaming proxying, and metrics. The agent owns local artifact installation, llama-swap config rendering, drain-aware restart, and heartbeat. Shared packages define config, protocol types, and small reusable helpers.

**Tech Stack:** Go 1.23+, YAML via `gopkg.in/yaml.v3`, Prometheus via `github.com/prometheus/client_golang`, standard `net/http`, standard `hash/crc64`, standard `archive/tar` + `compress/gzip`.

---

## Scope

This plan implements Phase 1 only:

- Single gateway process with in-memory state.
- Go agent process.
- Tag-scoped config distribution.
- Heartbeat every 3 seconds and 6-second stale-worker cutoff.
- Drain-before-restart handshake.
- OpenAI-compatible request proxy for JSON and streaming requests.
- Up to 3 dispatch attempts with the retry whitelist from the spec.
- Gateway-driven unload through llama-swap API.
- Artifact install for `file` and `tar_gz`.
- CRC64 ECMA verification.
- llama-swap config rendering.
- Basic gateway Prometheus metrics.

Deferred to later plans:

- Redis state.
- Full KV-cache runtime integration.
- External log stack.
- Signed manifests or SHA-256.
- Multi-gateway active/active.

## File Structure

Create these files:

- `go.mod`: Go module definition.
- `cmd/gateway/main.go`: Gateway CLI entrypoint.
- `cmd/agent/main.go`: Agent CLI entrypoint.
- `internal/config/config.go`: Gateway and agent config structs.
- `internal/config/load.go`: YAML loading and validation.
- `internal/protocol/agent.go`: Agent config and heartbeat request/response types.
- `internal/protocol/openai.go`: Minimal OpenAI-compatible request/error helpers.
- `internal/gateway/server.go`: Gateway HTTP server wiring.
- `internal/gateway/auth.go`: Bearer-token middleware.
- `internal/gateway/workers.go`: Worker registry, heartbeat state, drain state.
- `internal/gateway/scheduler.go`: Candidate filtering and scoring.
- `internal/gateway/accounting.go`: In-flight counters and queue-cap checks.
- `internal/gateway/limits.go`: Model, tag, and worker queue wait limits.
- `internal/gateway/proxy.go`: Reverse proxy, streaming handling, retry release logic.
- `internal/gateway/llamaswap_client.go`: `/running`, `/api/models/unload`, metrics pull client.
- `internal/gateway/metrics.go`: Gateway Prometheus metrics.
- `internal/agent/config_client.go`: Pull tag config and post heartbeat.
- `internal/agent/artifacts.go`: OSS download, HEAD check, CRC64, marker install.
- `internal/agent/render.go`: Render llama-swap YAML.
- `internal/agent/reconcile.go`: Agent loop, local lock, drain-aware restart.
- `internal/agent/service.go`: llama-swap service restart abstraction.
- `internal/testutil/fake_llamaswap.go`: Fake llama-swap server for tests.
- `examples/gateway.yaml`: Minimal gateway config.
- `examples/agent.yaml`: Minimal agent config.

Tests:

- `internal/config/config_test.go`
- `internal/gateway/workers_test.go`
- `internal/gateway/scheduler_test.go`
- `internal/gateway/accounting_test.go`
- `internal/gateway/limits_test.go`
- `internal/gateway/proxy_test.go`
- `internal/agent/artifacts_test.go`
- `internal/agent/render_test.go`
- `internal/agent/reconcile_test.go`

## Task 1: Go Module And Config Types

**Files:**
- Create: `go.mod`
- Create: `internal/config/config.go`
- Create: `internal/config/load.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Create `go.mod`**

```go
module llm-swap

go 1.23

require (
	github.com/prometheus/client_golang v1.20.5
	gopkg.in/yaml.v3 v3.0.1
)
```

- [ ] **Step 2: Write config validation tests**

Create `internal/config/config_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func TestLoadGatewayConfigValidatesWarmModel(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
  llama_swap: worker-token
models:
  qwen:
    priority: 100
    min_loaded: 1
    max_loaded: 2
    max_concurrency: 4
    max_queue: 8
    queue_timeout_ms: 30000
    ttl: 900
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
  gpu-4090:
    max_concurrency: 8
    max_queue: 16
    worker_defaults:
      max_concurrency: 2
      max_queue: 4
    allowed_models: [qwen]
    warm_when_idle: missing
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "warm_when_idle") {
		t.Fatalf("error = %v, want warm_when_idle", err)
	}
}

func TestLoadGatewayConfigAcceptsMinimalValidConfig(t *testing.T) {
	raw := `
oss:
  base_url: https://oss.example.com
tokens:
  client: client-token
  agent: agent-token
  llama_swap: worker-token
models:
  qwen:
    priority: 100
    min_loaded: 1
    max_loaded: 2
    max_concurrency: 4
    max_queue: 8
    queue_timeout_ms: 30000
    ttl: 900
    artifact:
      object: qwen.tar.gz
      kind: tar_gz
      crc64ecma: "123"
    run: "vllm serve {{model_path}} --port ${PORT}"
tag_policies:
  gpu-4090:
    max_concurrency: 8
    max_queue: 16
    worker_defaults:
      max_concurrency: 2
      max_queue: 4
    allowed_models: [qwen]
    warm_when_idle: qwen
`
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.TagPolicies["gpu-4090"].WarmWhenIdle != "qwen" {
		t.Fatalf("warm_when_idle = %q", cfg.TagPolicies["gpu-4090"].WarmWhenIdle)
	}
}
```

- [ ] **Step 3: Run test and verify it fails**

Run: `go test ./internal/config`

Expected: FAIL because package files and `LoadGateway` do not exist.

- [ ] **Step 4: Implement config structs and validation**

Create `internal/config/config.go`:

```go
package config

type GatewayConfig struct {
	OSS         OSSConfig            `yaml:"oss"`
	Tokens      TokenConfig          `yaml:"tokens"`
	Models      map[string]Model     `yaml:"models"`
	TagPolicies map[string]TagPolicy `yaml:"tag_policies"`
}

type OSSConfig struct {
	BaseURL string `yaml:"base_url"`
}

type TokenConfig struct {
	Client    string `yaml:"client"`
	Agent     string `yaml:"agent"`
	LlamaSwap string `yaml:"llama_swap"`
}

type Model struct {
	Priority       int      `yaml:"priority"`
	MinLoaded      int      `yaml:"min_loaded"`
	MaxLoaded      int      `yaml:"max_loaded"`
	MaxConcurrency int      `yaml:"max_concurrency"`
	MaxQueue       int      `yaml:"max_queue"`
	QueueTimeoutMS int      `yaml:"queue_timeout_ms"`
	TTL            int      `yaml:"ttl"`
	Artifact       Artifact `yaml:"artifact"`
	Run            string   `yaml:"run"`
	CmdStop        string   `yaml:"cmd_stop"`
}

type Artifact struct {
	Object    string `yaml:"object"`
	Kind      string `yaml:"kind"`
	CRC64ECMA string `yaml:"crc64ecma"`
}

type TagPolicy struct {
	MaxConcurrency int            `yaml:"max_concurrency"`
	MaxQueue       int            `yaml:"max_queue"`
	WorkerDefaults WorkerDefaults `yaml:"worker_defaults"`
	AllowedModels  []string       `yaml:"allowed_models"`
	WarmWhenIdle   string         `yaml:"warm_when_idle"`
}

type WorkerDefaults struct {
	MaxConcurrency int `yaml:"max_concurrency"`
	MaxQueue       int `yaml:"max_queue"`
}

type AgentConfig struct {
	Agent struct {
		ID              string   `yaml:"id"`
		Tags            []string `yaml:"tags"`
		ModelRoot       string   `yaml:"model_root"`
		LlamaSwapConfig string   `yaml:"llama_swap_config"`
		LlamaSwapService string  `yaml:"llama_swap_service"`
		LlamaSwapURL    string   `yaml:"llama_swap_url"`
		GatewayURL      string   `yaml:"gateway_url"`
		Token           string   `yaml:"token"`
	} `yaml:"agent"`
}
```

Create `internal/config/load.go`:

```go
package config

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadGateway(r io.Reader) (GatewayConfig, error) {
	var cfg GatewayConfig
	if err := yaml.NewDecoder(r).Decode(&cfg); err != nil {
		return cfg, err
	}
	return cfg, validateGateway(cfg)
}

func LoadAgent(r io.Reader) (AgentConfig, error) {
	var cfg AgentConfig
	if err := yaml.NewDecoder(r).Decode(&cfg); err != nil {
		return cfg, err
	}
	if cfg.Agent.ID == "" {
		return cfg, fmt.Errorf("agent.id is required")
	}
	if len(cfg.Agent.Tags) == 0 {
		return cfg, fmt.Errorf("agent.tags is required")
	}
	if cfg.Agent.ModelRoot == "" || cfg.Agent.LlamaSwapConfig == "" || cfg.Agent.GatewayURL == "" {
		return cfg, fmt.Errorf("agent model_root, llama_swap_config, and gateway_url are required")
	}
	return cfg, nil
}

func validateGateway(cfg GatewayConfig) error {
	if strings.TrimSpace(cfg.OSS.BaseURL) == "" {
		return fmt.Errorf("oss.base_url is required")
	}
	if cfg.Tokens.Agent == "" || cfg.Tokens.LlamaSwap == "" {
		return fmt.Errorf("tokens.agent and tokens.llama_swap are required")
	}
	if len(cfg.Models) == 0 {
		return fmt.Errorf("models is required")
	}
	for name, model := range cfg.Models {
		if model.Artifact.Object == "" {
			return fmt.Errorf("model %s artifact.object is required", name)
		}
		if model.Artifact.Kind != "file" && model.Artifact.Kind != "tar_gz" {
			return fmt.Errorf("model %s artifact.kind must be file or tar_gz", name)
		}
		if model.Artifact.CRC64ECMA == "" {
			return fmt.Errorf("model %s artifact.crc64ecma is required", name)
		}
		if strings.TrimSpace(model.Run) == "" {
			return fmt.Errorf("model %s run is required", name)
		}
		if model.MaxLoaded > 0 && model.MinLoaded > model.MaxLoaded {
			return fmt.Errorf("model %s min_loaded cannot exceed max_loaded", name)
		}
		if model.MaxConcurrency < 0 || model.MaxQueue < 0 {
			return fmt.Errorf("model %s concurrency and queue limits must be non-negative", name)
		}
	}
	for tag, policy := range cfg.TagPolicies {
		for _, model := range policy.AllowedModels {
			if _, ok := cfg.Models[model]; !ok {
				return fmt.Errorf("tag %s allowed model %s is not defined", tag, model)
			}
		}
		if policy.WarmWhenIdle != "" && !slices.Contains(policy.AllowedModels, policy.WarmWhenIdle) {
			return fmt.Errorf("tag %s warm_when_idle %s must be in allowed_models", tag, policy.WarmWhenIdle)
		}
	}
	return nil
}
```

- [ ] **Step 5: Run config tests**

Run: `go test ./internal/config`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config
git commit -m "feat: add config loading and validation"
```

## Task 2: Agent Protocol And Gateway Auth

**Files:**
- Create: `internal/protocol/agent.go`
- Create: `internal/protocol/openai.go`
- Create: `internal/gateway/auth.go`
- Test: `internal/gateway/workers_test.go`

- [ ] **Step 1: Add protocol types**

Create `internal/protocol/agent.go`:

```go
package protocol

import "llm-swap/internal/config"

type AgentConfigResponse struct {
	OSS       config.OSSConfig         `yaml:"oss" json:"oss"`
	Models    map[string]config.Model  `yaml:"models" json:"models"`
	TagPolicy AgentTagPolicy           `yaml:"tag_policy" json:"tag_policy"`
}

type AgentTagPolicy struct {
	Tag            string                `yaml:"tag" json:"tag"`
	AllowedModels  []string              `yaml:"allowed_models" json:"allowed_models"`
	WarmWhenIdle   string                `yaml:"warm_when_idle" json:"warm_when_idle"`
	WorkerDefaults config.WorkerDefaults `yaml:"worker_defaults" json:"worker_defaults"`
}

type RunningModel struct {
	Model string `json:"model"`
	State string `json:"state"`
}

type HeartbeatRequest struct {
	AgentID      string                  `json:"agent_id"`
	Tags         []string                `json:"tags"`
	LlamaSwapURL string                  `json:"llama_swap_url"`
	RunningModels []RunningModel        `json:"running_models"`
	Artifacts    map[string]string      `json:"artifacts"`
	Capacity     config.WorkerDefaults  `json:"capacity"`
	NeedsRestart bool                   `json:"needs_restart"`
	LastError     string                 `json:"last_error"`
}

type HeartbeatResponse struct {
	WorkerState    string `json:"worker_state"`
	RestartAllowed bool   `json:"restart_allowed"`
}
```

Create `internal/protocol/openai.go`:

```go
package protocol

import (
	"encoding/json"
	"net/http"
)

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

func WriteOpenAIError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Error: ErrorBody{
			Message: message,
			Type:    "llm_swap_cluster_error",
			Code:    code,
		},
	})
}
```

- [ ] **Step 2: Add auth middleware test**

Create `internal/gateway/workers_test.go` with the first test:

```go
package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuthRejectsWrongToken(t *testing.T) {
	h := bearerAuth("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}
```

- [ ] **Step 3: Run test and verify it fails**

Run: `go test ./internal/gateway -run TestBearerAuthRejectsWrongToken`

Expected: FAIL because `bearerAuth` does not exist.

- [ ] **Step 4: Implement bearer auth**

Create `internal/gateway/auth.go`:

```go
package gateway

import (
	"net/http"
	"strings"
)

func bearerAuth(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimSpace(r.Header.Get("Authorization"))
		if got != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 5: Run gateway tests**

Run: `go test ./internal/gateway`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/protocol internal/gateway/auth.go internal/gateway/workers_test.go
git commit -m "feat: add protocol types and auth middleware"
```

## Task 3: Worker Registry, Heartbeat, And Drain State

**Files:**
- Create: `internal/gateway/workers.go`
- Modify: `internal/gateway/workers_test.go`

- [ ] **Step 1: Add worker registry tests**

Append to `internal/gateway/workers_test.go`:

```go
func TestWorkerRegistryMarksStaleWorkerUnavailable(t *testing.T) {
	now := time.Unix(100, 0)
	reg := NewWorkerRegistry(6 * time.Second)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID: "gpu-01",
		Tags: []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		Capacity: config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4},
	}, now)

	if !reg.Healthy("gpu-01", now.Add(5*time.Second)) {
		t.Fatal("worker should be healthy before stale cutoff")
	}
	if reg.Healthy("gpu-01", now.Add(7*time.Second)) {
		t.Fatal("worker should be unavailable after stale cutoff")
	}
}

func TestHeartbeatDrainResponseAllowsRestartWhenIdle(t *testing.T) {
	reg := NewWorkerRegistry(6 * time.Second)
	resp := reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID: "gpu-01",
		Tags: []string{"gpu-4090"},
		LlamaSwapURL: "http://worker",
		NeedsRestart: true,
	}, time.Unix(100, 0))
	if resp.WorkerState != "draining" {
		t.Fatalf("state = %q, want draining", resp.WorkerState)
	}
	if !resp.RestartAllowed {
		t.Fatal("idle worker with needs_restart should be allowed to restart")
	}
}
```

Add imports to `internal/gateway/workers_test.go`:

```go
import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)
```

- [ ] **Step 2: Run test and verify it fails**

Run: `go test ./internal/gateway -run 'TestWorkerRegistry|TestHeartbeatDrain'`

Expected: FAIL because `NewWorkerRegistry` does not exist.

- [ ] **Step 3: Implement worker registry**

Create `internal/gateway/workers.go`:

```go
package gateway

import (
	"sync"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

type WorkerState string

const (
	WorkerActive WorkerState = "active"
	WorkerDraining WorkerState = "draining"
)

type Worker struct {
	ID string
	Tags []string
	LlamaSwapURL string
	RunningModels []protocol.RunningModel
	Artifacts map[string]string
	Capacity config.WorkerDefaults
	NeedsRestart bool
	LastError string
	LastHeartbeat time.Time
	State WorkerState
}

type WorkerRegistry struct {
	mu sync.RWMutex
	staleAfter time.Duration
	workers map[string]*Worker
	active map[string]int
}

func NewWorkerRegistry(staleAfter time.Duration) *WorkerRegistry {
	return &WorkerRegistry{
		staleAfter: staleAfter,
		workers: make(map[string]*Worker),
		active: make(map[string]int),
	}
}

func (r *WorkerRegistry) UpsertHeartbeat(hb protocol.HeartbeatRequest, now time.Time) protocol.HeartbeatResponse {
	r.mu.Lock()
	defer r.mu.Unlock()

	w := &Worker{
		ID: hb.AgentID,
		Tags: append([]string(nil), hb.Tags...),
		LlamaSwapURL: hb.LlamaSwapURL,
		RunningModels: append([]protocol.RunningModel(nil), hb.RunningModels...),
		Artifacts: hb.Artifacts,
		Capacity: hb.Capacity,
		NeedsRestart: hb.NeedsRestart,
		LastError: hb.LastError,
		LastHeartbeat: now,
		State: WorkerActive,
	}
	if hb.NeedsRestart {
		w.State = WorkerDraining
	}
	r.workers[hb.AgentID] = w

	restartAllowed := hb.NeedsRestart && r.active[hb.AgentID] == 0
	return protocol.HeartbeatResponse{WorkerState: string(w.State), RestartAllowed: restartAllowed}
}

func (r *WorkerRegistry) Healthy(id string, now time.Time) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	w, ok := r.workers[id]
	if !ok {
		return false
	}
	if now.Sub(w.LastHeartbeat) > r.staleAfter {
		return false
	}
	return w.State == WorkerActive
}

func (r *WorkerRegistry) Snapshot(now time.Time) []Worker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Worker, 0, len(r.workers))
	for _, w := range r.workers {
		cp := *w
		out = append(out, cp)
		_ = now
	}
	return out
}

func (r *WorkerRegistry) Acquire(workerID string, now time.Time) (func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.workers[workerID]
	if !ok {
		return nil, false
	}
	if now.Sub(w.LastHeartbeat) >= r.staleAfter {
		return nil, false
	}
	if w.State != WorkerActive {
		return nil, false
	}

	r.active[workerID]++

	var once sync.Once
	release := func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()

			if r.active[workerID] <= 1 {
				delete(r.active, workerID)
				return
			}
			r.active[workerID]--
		})
	}

	return release, true
}
```

- [ ] **Step 4: Run gateway tests**

Run: `go test ./internal/gateway`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/workers.go internal/gateway/workers_test.go
git commit -m "feat: add worker heartbeat registry"
```

## Task 4: Accounting And Scheduler

**Files:**
- Create: `internal/gateway/accounting.go`
- Create: `internal/gateway/scheduler.go`
- Test: `internal/gateway/accounting_test.go`
- Test: `internal/gateway/scheduler_test.go`

- [ ] **Step 1: Write accounting tests**

Create `internal/gateway/accounting_test.go`:

```go
package gateway

import "testing"

func TestAccountingReleasesOnce(t *testing.T) {
	a := NewAccounting()
	release := a.Acquire("req-1", "qwen", "gpu-4090", "gpu-01")
	release()
	release()

	if got := a.WorkerActive("gpu-01"); got != 0 {
		t.Fatalf("worker active = %d, want 0", got)
	}
}
```

- [ ] **Step 2: Write scheduler tests**

Create `internal/gateway/scheduler_test.go`:

```go
package gateway

import (
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestSchedulerPrefersLoadedHealthyWorker(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {Priority: 100}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID: "cold",
		Tags: []string{"gpu-4090"},
		LlamaSwapURL: "http://cold",
		Artifacts: map[string]string{"qwen": "ready"},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID: "loaded",
		Tags: []string{"gpu-4090"},
		LlamaSwapURL: "http://loaded",
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
		Artifacts: map[string]string{"qwen": "ready"},
	}, now)

	s := Scheduler{Config: cfg, Workers: reg}
	pick, err := s.Pick("qwen", now, nil)
	if err != nil {
		t.Fatalf("Pick returned error: %v", err)
	}
	if pick.ID != "loaded" {
		t.Fatalf("picked %s, want loaded", pick.ID)
	}
}
```

- [ ] **Step 3: Run tests and verify they fail**

Run: `go test ./internal/gateway -run 'TestAccounting|TestScheduler'`

Expected: FAIL because accounting and scheduler are not implemented.

- [ ] **Step 4: Implement accounting**

Create `internal/gateway/accounting.go`:

```go
package gateway

import "sync"

type Accounting struct {
	mu sync.Mutex
	inFlight map[string]inFlight
	modelActive map[string]int
	tagActive map[string]int
	workerActive map[string]int
}

type inFlight struct {
	model string
	tag string
	worker string
	released bool
}

func NewAccounting() *Accounting {
	return &Accounting{
		inFlight: make(map[string]inFlight),
		modelActive: make(map[string]int),
		tagActive: make(map[string]int),
		workerActive: make(map[string]int),
	}
}

func (a *Accounting) Acquire(requestID, model, tag, worker string) func() {
	a.mu.Lock()
	a.inFlight[requestID] = inFlight{model: model, tag: tag, worker: worker}
	a.modelActive[model]++
	a.tagActive[tag]++
	a.workerActive[worker]++
	a.mu.Unlock()

	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		rec, ok := a.inFlight[requestID]
		if !ok || rec.released {
			return
		}
		delete(a.inFlight, requestID)
		dec(a.modelActive, rec.model)
		dec(a.tagActive, rec.tag)
		dec(a.workerActive, rec.worker)
	}
}

func (a *Accounting) WorkerActive(worker string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workerActive[worker]
}

func dec(m map[string]int, key string) {
	if m[key] <= 1 {
		delete(m, key)
		return
	}
	m[key]--
}
```

- [ ] **Step 5: Implement scheduler**

Create `internal/gateway/scheduler.go`:

```go
package gateway

import (
	"errors"
	"slices"
	"time"

	"llm-swap/internal/config"
)

var ErrNoWorker = errors.New("no eligible worker")

type Scheduler struct {
	Config config.GatewayConfig
	Workers *WorkerRegistry
}

func (s Scheduler) Pick(model string, now time.Time, exclude map[string]bool) (Worker, error) {
	var best Worker
	bestScore := -1 << 30
	found := false
	for _, worker := range s.Workers.Snapshot(now) {
		if exclude != nil && exclude[worker.ID] {
			continue
		}
		if !s.Workers.Healthy(worker.ID, now) {
			continue
		}
		tag, ok := s.matchingTag(worker.Tags, model)
		if !ok || tag == "" {
			continue
		}
		if worker.Artifacts[model] != "ready" {
			continue
		}
		score := s.score(worker, model)
		if !found || score > bestScore {
			best = worker
			bestScore = score
			found = true
		}
	}
	if !found {
		return Worker{}, ErrNoWorker
	}
	return best, nil
}

func (s Scheduler) matchingTag(tags []string, model string) (string, bool) {
	for _, tag := range tags {
		policy, ok := s.Config.TagPolicies[tag]
		if !ok {
			continue
		}
		if slices.Contains(policy.AllowedModels, model) {
			return tag, true
		}
	}
	return "", false
}

func (s Scheduler) score(worker Worker, model string) int {
	score := s.Config.Models[model].Priority
	for _, running := range worker.RunningModels {
		if running.Model == model && (running.State == "ready" || running.State == "loading") {
			score += 10000
		}
	}
	return score
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/gateway`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/gateway/accounting.go internal/gateway/scheduler.go internal/gateway/*_test.go
git commit -m "feat: add accounting and scheduler"
```

## Task 4A: Queue Wait Limits

**Files:**
- Create: `internal/gateway/limits.go`
- Test: `internal/gateway/limits_test.go`
- Modify: `internal/gateway/proxy.go`

- [ ] **Step 1: Write queue limit tests**

Create `internal/gateway/limits_test.go`:

```go
package gateway

import (
	"context"
	"testing"
	"time"
)

func TestQueueLimiterRejectsWhenQueueFull(t *testing.T) {
	limiter := NewQueueLimiter()
	first, releaseFirst, err := limiter.Acquire(context.Background(), "model:qwen", 1, 1)
	if err != nil || !first {
		t.Fatalf("first acquire ok=%v err=%v", first, err)
	}
	defer releaseFirst()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	secondStarted := make(chan error, 1)
	go func() {
		_, _, err := limiter.Acquire(ctx, "model:qwen", 1, 1)
		secondStarted <- err
	}()

	time.Sleep(5 * time.Millisecond)
	_, _, err = limiter.Acquire(context.Background(), "model:qwen", 1, 1)
	if err != ErrQueueFull {
		t.Fatalf("third acquire err=%v want ErrQueueFull", err)
	}
	if err := <-secondStarted; err != context.DeadlineExceeded {
		t.Fatalf("queued acquire err=%v want context deadline exceeded", err)
	}
}

func TestQueueLimiterQueuedRequestRunsAfterRelease(t *testing.T) {
	limiter := NewQueueLimiter()
	_, releaseFirst, err := limiter.Acquire(context.Background(), "worker:gpu-01", 1, 1)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, releaseSecond, err := limiter.Acquire(ctx, "worker:gpu-01", 1, 1)
		if err == nil {
			releaseSecond()
		}
		done <- err
	}()

	releaseFirst()
	if err := <-done; err != nil {
		t.Fatalf("queued acquire error: %v", err)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run: `go test ./internal/gateway -run TestQueueLimiter`

Expected: FAIL because `NewQueueLimiter` does not exist.

- [ ] **Step 3: Implement queue limiter**

Create `internal/gateway/limits.go`:

```go
package gateway

import (
	"context"
	"errors"
	"sync"
)

var ErrQueueFull = errors.New("queue full")

type QueueLimiter struct {
	mu sync.Mutex
	limits map[string]*limitState
}

type limitState struct {
	active int
	queued int
	waiters []chan struct{}
}

func NewQueueLimiter() *QueueLimiter {
	return &QueueLimiter{limits: make(map[string]*limitState)}
}

func (l *QueueLimiter) Acquire(ctx context.Context, key string, maxActive int, maxQueue int) (bool, func(), error) {
	l.mu.Lock()
	st := l.state(key)
	if maxActive <= 0 || st.active < maxActive {
		st.active++
		l.mu.Unlock()
		return true, l.release(key), nil
	}
	if st.queued >= maxQueue {
		l.mu.Unlock()
		return false, nil, ErrQueueFull
	}
	waiter := make(chan struct{})
	st.queued++
	st.waiters = append(st.waiters, waiter)
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.cancelWaiter(key, waiter)
		return false, nil, ctx.Err()
	case <-waiter:
		return true, l.release(key), nil
	}
}

func (l *QueueLimiter) state(key string) *limitState {
	st := l.limits[key]
	if st == nil {
		st = &limitState{}
		l.limits[key] = st
	}
	return st
}

func (l *QueueLimiter) release(key string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			st := l.state(key)
			if len(st.waiters) > 0 {
				next := st.waiters[0]
				st.waiters = st.waiters[1:]
				st.queued--
				close(next)
				return
			}
			if st.active > 0 {
				st.active--
			}
		})
	}
}

func (l *QueueLimiter) cancelWaiter(key string, waiter chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.state(key)
	for i, ch := range st.waiters {
		if ch == waiter {
			st.waiters = append(st.waiters[:i], st.waiters[i+1:]...)
			st.queued--
			return
		}
	}
}
```

- [ ] **Step 4: Wire limiter into gateway server**

Modify `internal/gateway/server.go` `Server` struct and constructor:

```go
type Server struct {
	cfg config.GatewayConfig
	workers *WorkerRegistry
	accounting *Accounting
	limiter *QueueLimiter
	mux *http.ServeMux
}
```

```go
s := &Server{
	cfg: cfg,
	workers: NewWorkerRegistry(6 * time.Second),
	accounting: NewAccounting(),
	limiter: NewQueueLimiter(),
	mux: http.NewServeMux(),
}
```

Modify `internal/gateway/proxy.go` before scheduler selection:

```go
modelCfg := s.cfg.Models[model]
ctx, cancel := context.WithTimeout(r.Context(), time.Duration(modelCfg.QueueTimeoutMS)*time.Millisecond)
defer cancel()
_, releaseModelQueue, err := s.limiter.Acquire(ctx, "model:"+model, modelCfg.MaxConcurrency, modelCfg.MaxQueue)
if err != nil {
	protocol.WriteOpenAIError(w, http.StatusTooManyRequests, "queue_full", "model queue is full or timed out")
	return
}
defer releaseModelQueue()
```

Add imports to `internal/gateway/proxy.go`:

```go
"context"
```

The model limiter is the first V1 queue gate. Tag and worker limiters are added with the same `QueueLimiter` API after scheduler selection:

```go
tagPolicy := s.cfg.TagPolicies[tag]
_, releaseTagQueue, err := s.limiter.Acquire(ctx, "tag:"+tag, tagPolicy.MaxConcurrency, tagPolicy.MaxQueue)
if err != nil {
	protocol.WriteOpenAIError(w, http.StatusTooManyRequests, "queue_full", "tag queue is full or timed out")
	return
}
defer releaseTagQueue()

_, releaseWorkerQueue, err := s.limiter.Acquire(ctx, "worker:"+pick.ID, pick.Capacity.MaxConcurrency, pick.Capacity.MaxQueue)
if err != nil {
	protocol.WriteOpenAIError(w, http.StatusTooManyRequests, "queue_full", "worker queue is full or timed out")
	return
}
defer releaseWorkerQueue()
```

- [ ] **Step 5: Run gateway tests**

Run: `go test ./internal/gateway`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/limits.go internal/gateway/limits_test.go internal/gateway/server.go internal/gateway/proxy.go
git commit -m "feat: add gateway queue limiter"
```

## Task 5: Gateway HTTP Server And Agent APIs

**Files:**
- Create: `internal/gateway/server.go`
- Create: `cmd/gateway/main.go`
- Modify: `internal/gateway/workers_test.go`

- [ ] **Step 1: Add agent config API test**

Append to `internal/gateway/workers_test.go`:

```go
func TestAgentConfigEndpointReturnsTagScopedModels(t *testing.T) {
	cfg := config.GatewayConfig{
		OSS: config.OSSConfig{BaseURL: "https://oss.example.com"},
		Tokens: config.TokenConfig{Agent: "agent-token"},
		Models: map[string]config.Model{
			"qwen": {Run: "run", Artifact: config.Artifact{Object: "qwen.tar.gz", Kind: "tar_gz", CRC64ECMA: "123"}},
			"other": {Run: "run", Artifact: config.Artifact{Object: "other.tar.gz", Kind: "tar_gz", CRC64ECMA: "456"}},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}, WarmWhenIdle: "qwen"},
		},
	}
	s := NewServer(cfg)
	req := httptest.NewRequest(http.MethodGet, "/internal/agent/config?tags=gpu-4090", nil)
	req.Header.Set("Authorization", "Bearer agent-token")
	rr := httptest.NewRecorder()

	s.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "qwen") || strings.Contains(rr.Body.String(), "other.tar.gz") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}
```

Add `strings` to the test imports.

- [ ] **Step 2: Run test and verify it fails**

Run: `go test ./internal/gateway -run TestAgentConfigEndpointReturnsTagScopedModels`

Expected: FAIL because `NewServer` does not exist.

- [ ] **Step 3: Implement gateway server**

Create `internal/gateway/server.go`:

```go
package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

type Server struct {
	cfg config.GatewayConfig
	workers *WorkerRegistry
	accounting *Accounting
	mux *http.ServeMux
}

func NewServer(cfg config.GatewayConfig) *Server {
	s := &Server{
		cfg: cfg,
		workers: NewWorkerRegistry(6 * time.Second),
		accounting: NewAccounting(),
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	agent := func(h http.HandlerFunc) http.Handler {
		return bearerAuth(s.cfg.Tokens.Agent, h)
	}
	s.mux.Handle("/internal/agent/config", agent(s.handleAgentConfig()))
	s.mux.Handle("/internal/agent/heartbeat", agent(s.handleHeartbeat()))
}

func (s *Server) handleAgentConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags := strings.Split(r.URL.Query().Get("tags"), ",")
		var matchedTag string
		var policy config.TagPolicy
		matches := 0
		for _, tag := range tags {
			tag = strings.TrimSpace(tag)
			if p, ok := s.cfg.TagPolicies[tag]; ok {
				matches++
				matchedTag = tag
				policy = p
			}
		}
		if matches != 1 {
			http.Error(w, "exactly one workload tag must match", http.StatusBadRequest)
			return
		}
		models := make(map[string]config.Model)
		for _, model := range policy.AllowedModels {
			models[model] = s.cfg.Models[model]
		}
		resp := protocol.AgentConfigResponse{
			OSS: s.cfg.OSS,
			Models: models,
			TagPolicy: protocol.AgentTagPolicy{
				Tag: matchedTag,
				AllowedModels: policy.AllowedModels,
				WarmWhenIdle: policy.WarmWhenIdle,
				WorkerDefaults: policy.WorkerDefaults,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func (s *Server) handleHeartbeat() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var hb protocol.HeartbeatRequest
		if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
			http.Error(w, "invalid heartbeat", http.StatusBadRequest)
			return
		}
		resp := s.workers.UpsertHeartbeat(hb, time.Now())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
```

Create `cmd/gateway/main.go`:

```go
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"llm-swap/internal/config"
	"llm-swap/internal/gateway"
)

func main() {
	configPath := flag.String("config", "examples/gateway.yaml", "gateway config path")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	f, err := os.Open(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	cfg, err := config.LoadGateway(f)
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.ListenAndServe(*addr, gateway.NewServer(cfg)))
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/gateway`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/gateway internal/gateway/server.go internal/gateway/workers_test.go
git commit -m "feat: add gateway agent APIs"
```

## Task 6: llama-swap Client And Gateway Proxy

**Files:**
- Create: `internal/gateway/llamaswap_client.go`
- Create: `internal/gateway/proxy.go`
- Create: `internal/testutil/fake_llamaswap.go`
- Test: `internal/gateway/proxy_test.go`

- [ ] **Step 1: Write proxy retry and streaming release tests**

Create `internal/gateway/proxy_test.go`:

```go
package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestProxyRetriesDifferentWorkerBeforeHeaders(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer good.Close()

	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {Priority: 100}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	s := NewServer(cfg)
	now := time.Now()
	s.workers.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "bad", Tags: []string{"gpu-4090"}, LlamaSwapURL: bad.URL, Artifacts: map[string]string{"qwen": "ready"}}, now)
	s.workers.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "good", Tags: []string{"gpu-4090"}, LlamaSwapURL: good.URL, Artifacts: map[string]string{"qwen": "ready"}}, now)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen"}`))
	rr := httptest.NewRecorder()
	s.handleModelProxy().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run: `go test ./internal/gateway -run TestProxyRetriesDifferentWorkerBeforeHeaders`

Expected: FAIL because `handleModelProxy` is not implemented.

- [ ] **Step 3: Implement llama-swap client**

Create `internal/gateway/llamaswap_client.go`:

```go
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type LlamaSwapClient struct {
	Token string
	Client *http.Client
}

func (c LlamaSwapClient) Unload(ctx context.Context, baseURL, model string) error {
	path := strings.TrimRight(baseURL, "/") + "/api/models/unload/" + model
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(nil))
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &HTTPStatusError{StatusCode: resp.StatusCode}
	}
	return nil
}

func ExtractModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &payload)
	return payload.Model
}

type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return http.StatusText(e.StatusCode)
}
```

- [ ] **Step 4: Implement proxy handler**

Create `internal/gateway/proxy.go`:

```go
package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"llm-swap/internal/protocol"
)

func (s *Server) handleModelProxy() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			protocol.WriteOpenAIError(w, http.StatusBadRequest, "bad_request", "failed to read request body")
			return
		}
		model := ExtractModel(body)
		if model == "" {
			protocol.WriteOpenAIError(w, http.StatusBadRequest, "missing_model", "request body must include model")
			return
		}
		now := time.Now()
		exclude := map[string]bool{}
		var lastStatus int
		for attempt := 0; attempt < 3; attempt++ {
			pick, err := (Scheduler{Config: s.cfg, Workers: s.workers}).Pick(model, now, exclude)
			if err != nil {
				protocol.WriteOpenAIError(w, http.StatusServiceUnavailable, "no_healthy_worker", "no healthy worker can serve model")
				return
			}
			exclude[pick.ID] = true
			releaseWorker, ok := s.workers.Acquire(pick.ID, time.Now())
			if !ok {
				continue
			}
			tag := pick.Tags[0]
			releaseAccounting := s.accounting.Acquire(r.Header.Get("X-Request-Id"), model, tag, pick.ID)
			ok, status := func() (bool, int) {
				defer releaseWorker()
				defer releaseAccounting()
				return s.tryProxy(w, r, body, pick.LlamaSwapURL)
			}()
			if ok {
				return
			}
			lastStatus = status
			if !retryableStatus(status) {
				break
			}
		}
		protocol.WriteOpenAIError(w, http.StatusServiceUnavailable, "worker_unavailable", http.StatusText(lastStatus))
	}
}

func (s *Server) tryProxy(w http.ResponseWriter, original *http.Request, body []byte, baseURL string) (bool, int) {
	target := strings.TrimRight(baseURL, "/") + original.URL.Path
	req, err := http.NewRequestWithContext(original.Context(), original.Method, target, bytes.NewReader(body))
	if err != nil {
		return false, 0
	}
	req.Header = original.Header.Clone()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, 0
	}
	defer resp.Body.Close()
	if retryableStatus(resp.StatusCode) {
		return false, resp.StatusCode
	}
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	return true, resp.StatusCode
}

func retryableStatus(status int) bool {
	return status == 0 || status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}
```

Modify `internal/gateway/server.go` route wiring:

```go
s.mux.Handle("/v1/chat/completions", bearerAuth(s.cfg.Tokens.Client, s.handleModelProxy()))
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/gateway`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/llamaswap_client.go internal/gateway/proxy.go internal/gateway/proxy_test.go internal/gateway/server.go
git commit -m "feat: add gateway request proxy"
```

## Task 7: Agent Artifact Installer

**Files:**
- Create: `internal/agent/artifacts.go`
- Test: `internal/agent/artifacts_test.go`

- [ ] **Step 1: Write artifact marker and CRC tests**

Create `internal/agent/artifacts_test.go`:

```go
package agent

import (
	"hash/crc64"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"llm-swap/internal/config"
)

func TestMarkerMatchSkipsDownload(t *testing.T) {
	dir := t.TempDir()
	artifact := config.Artifact{Object: "model.gguf", Kind: "file", CRC64ECMA: "123"}
	if err := WriteMarker(dir, "qwen", artifact); err != nil {
		t.Fatal(err)
	}
	ok, err := MarkerMatches(dir, "qwen", artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("marker should match")
	}
}

func TestCRC64ECMAString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(path, []byte("abc"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := CRC64ECMAFile(path)
	if err != nil {
		t.Fatal(err)
	}
	table := crc64.MakeTable(crc64.ECMA)
	want := strconv.FormatUint(crc64.Checksum([]byte("abc"), table), 10)
	if got != want {
		t.Fatalf("crc=%s want=%s", got, want)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run: `go test ./internal/agent -run 'TestMarker|TestCRC64'`

Expected: FAIL because artifact helpers do not exist.

- [ ] **Step 3: Implement artifact helpers**

Create `internal/agent/artifacts.go`:

```go
package agent

import (
	"encoding/json"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"llm-swap/internal/config"
)

const markerName = ".llm-agent-artifact.json"

type Marker struct {
	Model string `json:"model"`
	Object string `json:"object"`
	Kind string `json:"kind"`
	CRC64ECMA string `json:"crc64ecma"`
	InstalledPath string `json:"installed_path"`
}

func MarkerMatches(dir, model string, artifact config.Artifact) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, markerName))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false, err
	}
	return marker.Model == model && marker.Object == artifact.Object && marker.Kind == artifact.Kind && marker.CRC64ECMA == artifact.CRC64ECMA, nil
}

func WriteMarker(dir, model string, artifact config.Artifact) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Marker{
		Model: model,
		Object: artifact.Object,
		Kind: artifact.Kind,
		CRC64ECMA: artifact.CRC64ECMA,
		InstalledPath: dir,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, markerName), data, 0644)
}

func CRC64ECMAFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := crc64.New(crc64.MakeTable(crc64.ECMA))
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return strconv.FormatUint(h.Sum64(), 10), nil
}
```

- [ ] **Step 4: Run agent tests**

Run: `go test ./internal/agent`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/artifacts.go internal/agent/artifacts_test.go
git commit -m "feat: add agent artifact markers"
```

## Task 8: Agent llama-swap Renderer

**Files:**
- Create: `internal/agent/render.go`
- Test: `internal/agent/render_test.go`

- [ ] **Step 1: Write renderer test**

Create `internal/agent/render_test.go`:

```go
package agent

import (
	"strings"
	"testing"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestRenderLlamaSwapConfigReplacesModelPathAndKeepsPortMacro(t *testing.T) {
	resp := protocol.AgentConfigResponse{
		Models: map[string]config.Model{
			"qwen": {
				TTL: 900,
				Run: "vllm serve {{model_path}} --port ${PORT}",
			},
		},
		TagPolicy: protocol.AgentTagPolicy{
			AllowedModels: []string{"qwen"},
			WarmWhenIdle: "qwen",
			WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 2},
		},
	}
	out, err := RenderLlamaSwapConfig(resp, "/data/models", "internal-token")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if !strings.Contains(text, "/data/models/qwen") {
		t.Fatalf("rendered config missing model path: %s", text)
	}
	if !strings.Contains(text, "--port ${PORT}") {
		t.Fatalf("rendered config should keep PORT macro: %s", text)
	}
	if !strings.Contains(text, "concurrencyLimit: 2") {
		t.Fatalf("rendered config missing concurrencyLimit: %s", text)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run: `go test ./internal/agent -run TestRenderLlamaSwapConfig`

Expected: FAIL because renderer does not exist.

- [ ] **Step 3: Implement renderer**

Create `internal/agent/render.go`:

```go
package agent

import (
	"bytes"
	"path/filepath"
	"strings"

	"llm-swap/internal/protocol"
	"gopkg.in/yaml.v3"
)

type llamaSwapConfig struct {
	StartPort int `yaml:"startPort"`
	GlobalTTL int `yaml:"globalTTL"`
	APIKeys []string `yaml:"apiKeys,omitempty"`
	Performance map[string]any `yaml:"performance,omitempty"`
	Hooks map[string]any `yaml:"hooks,omitempty"`
	Models map[string]llamaSwapModel `yaml:"models"`
}

type llamaSwapModel struct {
	Cmd string `yaml:"cmd"`
	CmdStop string `yaml:"cmdStop,omitempty"`
	TTL int `yaml:"ttl"`
	ConcurrencyLimit int `yaml:"concurrencyLimit,omitempty"`
}

func RenderLlamaSwapConfig(resp protocol.AgentConfigResponse, modelRoot string, token string) ([]byte, error) {
	cfg := llamaSwapConfig{
		StartPort: 10001,
		GlobalTTL: 0,
		Performance: map[string]any{"enable": true, "every": "5s"},
		Models: map[string]llamaSwapModel{},
	}
	if token != "" {
		cfg.APIKeys = []string{token}
	}
	if resp.TagPolicy.WarmWhenIdle != "" {
		cfg.Hooks = map[string]any{
			"on_startup": map[string]any{"preload": []string{resp.TagPolicy.WarmWhenIdle}},
		}
	}
	for _, name := range resp.TagPolicy.AllowedModels {
		model := resp.Models[name]
		modelPath := filepath.ToSlash(filepath.Join(modelRoot, name))
		cmd := strings.ReplaceAll(model.Run, "{{model_path}}", modelPath)
		cfg.Models[name] = llamaSwapModel{
			Cmd: cmd,
			CmdStop: model.CmdStop,
			TTL: model.TTL,
			ConcurrencyLimit: resp.TagPolicy.WorkerDefaults.MaxConcurrency,
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/render.go internal/agent/render_test.go
git commit -m "feat: render llama-swap config"
```

## Task 9: Agent Config Client And Reconcile Loop

**Files:**
- Create: `internal/agent/config_client.go`
- Create: `internal/agent/service.go`
- Create: `internal/agent/reconcile.go`
- Create: `cmd/agent/main.go`
- Test: `internal/agent/reconcile_test.go`

- [ ] **Step 1: Write reconcile no-restart test**

Create `internal/agent/reconcile_test.go`:

```go
package agent

import (
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
	if err := os.WriteFile(path, content, 0644); err != nil {
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

func TestBuildHeartbeatIncludesNeedsRestart(t *testing.T) {
	hb := BuildHeartbeat("gpu-01", []string{"gpu-4090"}, "http://worker", protocol.AgentConfigResponse{
		TagPolicy: protocol.AgentTagPolicy{WorkerDefaults: config.WorkerDefaults{MaxConcurrency: 2, MaxQueue: 4}},
	}, true)
	if !hb.NeedsRestart {
		t.Fatal("heartbeat should include needs_restart")
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run: `go test ./internal/agent -run 'TestWriteConfig|TestBuildHeartbeat'`

Expected: FAIL because reconcile helpers do not exist.

- [ ] **Step 3: Implement service abstraction**

Create `internal/agent/service.go`:

```go
package agent

import (
	"context"
	"os/exec"
)

type Service interface {
	Restart(context.Context) error
}

type SystemdService struct {
	Name string
}

func (s SystemdService) Restart(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "restart", s.Name).Run()
}

type FakeService struct {
	Restarts int
	Err error
}

func (s *FakeService) Restart(context.Context) error {
	s.Restarts++
	return s.Err
}
```

- [ ] **Step 4: Implement reconcile helpers**

Create `internal/agent/reconcile.go`:

```go
package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"llm-swap/internal/protocol"
)

func WriteConfigIfChanged(path string, content []byte, service Service) (bool, error) {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	return true, nil
}

func RestartAfterGatewayAllows(ctx context.Context, service Service) error {
	return service.Restart(ctx)
}

func BuildHeartbeat(agentID string, tags []string, llamaSwapURL string, cfg protocol.AgentConfigResponse, needsRestart bool) protocol.HeartbeatRequest {
	return protocol.HeartbeatRequest{
		AgentID: agentID,
		Tags: tags,
		LlamaSwapURL: llamaSwapURL,
		Artifacts: map[string]string{},
		Capacity: cfg.TagPolicy.WorkerDefaults,
		NeedsRestart: needsRestart,
	}
}
```

Create `internal/agent/config_client.go`:

```go
package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"llm-swap/internal/protocol"
)

type ConfigClient struct {
	BaseURL string
	Token string
	HTTP *http.Client
}

func (c ConfigClient) GetConfig(tags []string) (protocol.AgentConfigResponse, error) {
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/internal/agent/config?tags=" + url.QueryEscape(strings.Join(tags, ","))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return protocol.AgentConfigResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := client.Do(req)
	if err != nil {
		return protocol.AgentConfigResponse{}, err
	}
	defer resp.Body.Close()
	var out protocol.AgentConfigResponse
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (c ConfigClient) Heartbeat(hb protocol.HeartbeatRequest) (protocol.HeartbeatResponse, error) {
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	data, _ := json.Marshal(hb)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/internal/agent/heartbeat", bytes.NewReader(data))
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return protocol.HeartbeatResponse{}, err
	}
	defer resp.Body.Close()
	var out protocol.HeartbeatResponse
	return out, json.NewDecoder(resp.Body).Decode(&out)
}
```

Create `cmd/agent/main.go`:

```go
package main

import (
	"flag"
	"log"
	"os"

	"llm-swap/internal/config"
)

func main() {
	configPath := flag.String("config", "examples/agent.yaml", "agent config path")
	flag.Parse()

	f, err := os.Open(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if _, err := config.LoadAgent(f); err != nil {
		log.Fatal(err)
	}
	log.Println("agent config loaded")
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/agent`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/agent internal/agent/config_client.go internal/agent/service.go internal/agent/reconcile.go internal/agent/reconcile_test.go
git commit -m "feat: add agent reconcile scaffolding"
```

## Task 10: Metrics, Worker Scrape, And Examples

**Files:**
- Create: `internal/gateway/metrics.go`
- Create: `internal/gateway/metrics_scrape.go`
- Modify: `internal/gateway/server.go`
- Create: `examples/gateway.yaml`
- Create: `examples/agent.yaml`

- [ ] **Step 1: Write worker metrics dedupe test**

Create `internal/gateway/metrics_test.go`:

```go
package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMetricsScraperDeduplicatesActivityRows(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/metrics" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"id":1,"model":"qwen"},{"id":1,"model":"qwen"}]`)
	}))
	defer worker.Close()

	scraper := NewMetricsScraper()
	first, err := scraper.PullActivity("gpu-01", worker.URL)
	if err != nil {
		t.Fatal(err)
	}
	second, err := scraper.PullActivity("gpu-01", worker.URL)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("dedupe counts first=%d second=%d, want 1 and 0", first, second)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run: `go test ./internal/gateway -run TestMetricsScraperDeduplicatesActivityRows`

Expected: FAIL because `NewMetricsScraper` does not exist.

- [ ] **Step 3: Add metrics endpoint and scraper**

Create `internal/gateway/metrics.go`:

```go
package gateway

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	reg *prometheus.Registry
	active *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	active := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "llm_swap_gateway_active_requests",
		Help: "Active requests by worker and model.",
	}, []string{"worker_id", "model"})
	reg.MustRegister(active)
	return &Metrics{reg: reg, active: active}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
```

Create `internal/gateway/metrics_scrape.go`:

```go
package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type MetricsScraper struct {
	client *http.Client
	mu sync.Mutex
	seenActivity map[string]struct{}
	seenPerformance map[string]struct{}
}

func NewMetricsScraper() *MetricsScraper {
	return &MetricsScraper{
		client: &http.Client{Timeout: 3 * time.Second},
		seenActivity: make(map[string]struct{}),
		seenPerformance: make(map[string]struct{}),
	}
}

func (s *MetricsScraper) PullActivity(workerID string, baseURL string) (int, error) {
	resp, err := s.client.Get(strings.TrimRight(baseURL, "/") + "/api/metrics")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return 0, err
	}
	added := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range rows {
		key := workerID + ":" + stableMetricKey(row)
		if _, ok := s.seenActivity[key]; ok {
			continue
		}
		s.seenActivity[key] = struct{}{}
		added++
	}
	return added, nil
}

func stableMetricKey(row map[string]any) string {
	for _, key := range []string{"id", "request_id", "timestamp", "created_at"} {
		if v, ok := row[key]; ok {
			return key + "=" + strings.TrimSpace(toString(v))
		}
	}
	return toString(row["model"]) + ":" + toString(row["path"]) + ":" + toString(row["duration_ms"])
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(jsonString(v)), "\n", " "), "\t", " "))
}

func jsonString(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
```

Modify `internal/gateway/server.go`:

```go
metrics := NewMetrics()
s.mux.Handle("/metrics", metrics.Handler())
```

- [ ] **Step 4: Add example configs**

Create `examples/gateway.yaml`:

```yaml
oss:
  base_url: https://llm-models.oss-cn-hangzhou.aliyuncs.com
tokens:
  client: client-token
  agent: agent-token
  llama_swap: worker-token
models:
  qwen3-32b-awq:
    priority: 100
    min_loaded: 1
    max_loaded: 4
    max_concurrency: 32
    max_queue: 128
    queue_timeout_ms: 30000
    ttl: 900
    artifact:
      object: qwen3-32b-awq.tar.gz
      kind: tar_gz
      crc64ecma: "3161812495027030000"
    run: >
      vllm serve {{model_path}}
      --host 127.0.0.1
      --port ${PORT}
      --served-model-name qwen3-32b-awq
tag_policies:
  gpu-4090:
    max_concurrency: 64
    max_queue: 256
    worker_defaults:
      max_concurrency: 8
      max_queue: 16
    allowed_models:
      - qwen3-32b-awq
    warm_when_idle: qwen3-32b-awq
```

Create `examples/agent.yaml`:

```yaml
agent:
  id: gpu-01
  tags:
    - gpu-4090
  model_root: /data/models
  llama_swap_config: /etc/llama-swap/config.yaml
  llama_swap_service: llama-swap
  llama_swap_url: http://10.0.0.11:8080
  gateway_url: http://gateway.internal:8080
  token: agent-token
```

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/metrics.go internal/gateway/metrics_scrape.go internal/gateway/metrics_test.go internal/gateway/server.go examples
git commit -m "feat: add metrics endpoint and worker scrape dedupe"
```

## Task 11: End-To-End Smoke Test Harness

**Files:**
- Create: `internal/testutil/fake_llamaswap.go`
- Modify: `internal/gateway/proxy_test.go`

- [ ] **Step 1: Create fake llama-swap helper**

Create `internal/testutil/fake_llamaswap.go`:

```go
package testutil

import (
	"fmt"
	"net/http"
	"net/http/httptest"
)

type FakeLlamaSwap struct {
	Server *httptest.Server
	Running []string
}

func NewFakeLlamaSwap() *FakeLlamaSwap {
	f := &FakeLlamaSwap{}
	mux := http.NewServeMux()
	mux.HandleFunc("/running", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"running":[]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-test","choices":[]}`)
	})
	mux.HandleFunc("/api/models/unload/qwen", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})
	f.Server = httptest.NewServer(mux)
	return f
}

func (f *FakeLlamaSwap) Close() {
	f.Server.Close()
}
```

- [ ] **Step 2: Add smoke test**

Append to `internal/gateway/proxy_test.go`:

```go
func TestSmokeGatewayToFakeLlamaSwap(t *testing.T) {
	fake := testutil.NewFakeLlamaSwap()
	defer fake.Close()

	cfg := config.GatewayConfig{
		Tokens: config.TokenConfig{Client: "client-token"},
		Models: map[string]config.Model{"qwen": {Priority: 100}},
		TagPolicies: map[string]config.TagPolicy{"gpu-4090": {AllowedModels: []string{"qwen"}}},
	}
	s := NewServer(cfg)
	s.workers.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID: "gpu-01",
		Tags: []string{"gpu-4090"},
		LlamaSwapURL: fake.Server.URL,
		Artifacts: map[string]string{"qwen": "ready"},
	}, time.Now())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen"}`))
	req.Header.Set("Authorization", "Bearer client-token")
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}
```

Add import:

```go
"llm-swap/internal/testutil"
```

- [ ] **Step 3: Run full test suite**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/testutil internal/gateway/proxy_test.go
git commit -m "test: add fake llama-swap smoke test"
```

## Task 12: Final Verification

**Files:**
- Modify only if verification finds a concrete defect.

- [ ] **Step 1: Run all tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 2: Run gofmt**

Run: `gofmt -w cmd internal`

Expected: no terminal output.

- [ ] **Step 3: Run tests again after formatting**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 4: Inspect git status**

Run: `git status --short`

Expected: no unexpected untracked files. If gofmt changed files, commit them.

- [ ] **Step 5: Commit final cleanup when formatting changed files**

```bash
git add cmd internal examples go.mod go.sum
git commit -m "chore: format phase 1 implementation"
```

Skip this commit only if `git status --short` is empty.

## Self-Review Notes

Spec coverage:

- Tag-scoped config: Task 5.
- Heartbeat and 6-second stale cutoff: Task 3.
- Drain-before-restart: Tasks 3 and 9.
- Artifact markers and CRC64: Task 7.
- llama-swap rendering with `${PORT}` preserved: Task 8.
- Gateway scheduling: Task 4.
- Queue limits and queue wait timeout: Task 4A.
- Request retry and streaming-safe release foundation: Task 6.
- Metrics endpoint and worker activity dedupe: Task 10.
- Fake worker smoke coverage: Task 11.
