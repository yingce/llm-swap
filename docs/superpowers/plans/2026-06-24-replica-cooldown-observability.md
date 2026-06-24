# Replica Cooldown Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop routing new requests to a ready replica that just failed retryably, retry other ready replicas, and expose cooldown/retry state in logs, metrics, and UI.

**Architecture:** Add a gateway-local `ReplicaCooldowns` tracker keyed by `worker_id + model`. `Scheduler`/`Placement` consume an active cooldown snapshot to exclude only cooled-down replicas from request routing. `proxy.go` marks cooldown on retryable dispatch failures, clears it on successful proxy completion, and emits metrics/logs; `ui.go` and `metrics.go` expose the state.

**Tech Stack:** Go standard library, existing gateway tests, Prometheus client, existing `/ui/status` JSON and embedded dashboard HTML.

---

## File Structure

- Create `internal/gateway/replica_cooldown.go`
  - Owns cooldown state, expiry, snapshots, mark/clear operations, and reason normalization.
- Create `internal/gateway/replica_cooldown_test.go`
  - Unit tests for tracker behavior.
- Modify `internal/gateway/server.go`
  - Add `replicaCooldowns *ReplicaCooldowns` to `Server` and initialize it with a 30 second TTL.
  - Pass cooldown snapshots into metrics and UI.
- Modify `internal/gateway/scheduler.go`
  - Add `Cooldowns ReplicaCooldownSnapshot` to `Scheduler`.
  - Pass cooldowns into `Placement`.
- Modify `internal/gateway/placement.go`
  - Add cooldown-aware ready-candidate exclusion.
- Modify `internal/gateway/scheduler_test.go`
  - Add routing exclusion regression test.
- Modify `internal/gateway/proxy.go`
  - Mark cooldown on retryable proxy failures.
  - Clear cooldown after a successful final proxy attempt.
  - Emit `proxy_retry`, `proxy_retry_exhausted`, `replica_unhealthy_marked`, and `replica_unhealthy_cleared` logs.
- Modify `internal/gateway/proxy_test.go`
  - Add retry/cooldown/clear tests.
- Modify `internal/gateway/metrics.go`
  - Add cooldown gauge/counters and proxy retry counter.
- Modify `internal/gateway/metrics_test.go`
  - Assert cooldown and retry metrics.
- Modify `internal/gateway/ui.go`
  - Add cooldown fields to model worker rows and worker cards.
- Modify `internal/gateway/ui_test.go`
  - Assert `/ui/status` includes cooldown details and HTML has renderer text.
- Modify `docs/agents/project-map.md`
  - Document gateway-only replica cooldown behavior.

---

### Task 1: Cooldown Tracker

**Files:**
- Create: `internal/gateway/replica_cooldown.go`
- Create: `internal/gateway/replica_cooldown_test.go`

- [ ] **Step 1: Write the failing tracker test**

Add this test file:

```go
package gateway

import (
	"testing"
	"time"
)

func TestReplicaCooldownsMarkClearAndExpire(t *testing.T) {
	now := time.Unix(1000, 0)
	cooldowns := NewReplicaCooldowns(30 * time.Second)

	entry, marked := cooldowns.Mark("worker-a", "qwen", "upstream_503", now)
	if !marked {
		t.Fatal("Mark returned marked=false, want true")
	}
	if entry.WorkerID != "worker-a" || entry.Model != "qwen" || entry.Reason != "upstream_503" {
		t.Fatalf("entry = %+v, want worker/model/reason", entry)
	}
	if got, ok := cooldowns.Get("worker-a", "qwen", now.Add(29*time.Second)); !ok || got.RemainingSeconds != 1 {
		t.Fatalf("Get before expiry = %+v, %v; want active with 1s remaining", got, ok)
	}
	if cooldowns.Active("worker-a", "qwen", now.Add(31*time.Second)) {
		t.Fatal("Active after expiry = true, want false")
	}

	cooldowns.Mark("worker-a", "qwen", "connection_error", now.Add(40*time.Second))
	cleared, ok := cooldowns.Clear("worker-a", "qwen", now.Add(41*time.Second))
	if !ok || cleared.Reason != "connection_error" {
		t.Fatalf("Clear = %+v, %v; want cleared connection_error", cleared, ok)
	}
	if cooldowns.Active("worker-a", "qwen", now.Add(42*time.Second)) {
		t.Fatal("Active after clear = true, want false")
	}
}
```

- [ ] **Step 2: Run tracker test and verify RED**

Run:

```bash
go test ./internal/gateway -run TestReplicaCooldownsMarkClearAndExpire -count=1
```

Expected: FAIL with undefined `NewReplicaCooldowns`.

- [ ] **Step 3: Implement the tracker**

Create `internal/gateway/replica_cooldown.go`:

```go
package gateway

import (
	"sync"
	"time"
)

const defaultReplicaCooldownTTL = 30 * time.Second

type ReplicaCooldowns struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[replicaCooldownKey]ReplicaCooldown
}

type replicaCooldownKey struct {
	WorkerID string
	Model    string
}

type ReplicaCooldown struct {
	WorkerID         string    `json:"worker_id"`
	Model            string    `json:"model"`
	Reason           string    `json:"reason"`
	FirstFailure     time.Time `json:"first_failure"`
	LastFailure      time.Time `json:"last_failure"`
	FailureCount     int       `json:"failure_count"`
	CooldownUntil    time.Time `json:"cooldown_until"`
	RemainingSeconds int64     `json:"remaining_seconds"`
}

type ReplicaCooldownSnapshot map[string]map[string]ReplicaCooldown

func NewReplicaCooldowns(ttl time.Duration) *ReplicaCooldowns {
	if ttl <= 0 {
		ttl = defaultReplicaCooldownTTL
	}
	return &ReplicaCooldowns{ttl: ttl, entries: make(map[replicaCooldownKey]ReplicaCooldown)}
}

func (c *ReplicaCooldowns) Mark(workerID, model, reason string, now time.Time) (ReplicaCooldown, bool) {
	if c == nil || workerID == "" || model == "" {
		return ReplicaCooldown{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	key := replicaCooldownKey{WorkerID: workerID, Model: model}
	entry := c.entries[key]
	if entry.FirstFailure.IsZero() {
		entry.FirstFailure = now
	}
	entry.WorkerID = workerID
	entry.Model = model
	entry.Reason = reason
	entry.LastFailure = now
	entry.FailureCount++
	entry.CooldownUntil = now.Add(c.ttl)
	entry.RemainingSeconds = int64(c.ttl.Seconds())
	c.entries[key] = entry
	return entry, true
}

func (c *ReplicaCooldowns) Clear(workerID, model string, now time.Time) (ReplicaCooldown, bool) {
	if c == nil || workerID == "" || model == "" {
		return ReplicaCooldown{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)

	key := replicaCooldownKey{WorkerID: workerID, Model: model}
	entry, ok := c.entries[key]
	if !ok {
		return ReplicaCooldown{}, false
	}
	delete(c.entries, key)
	entry.RemainingSeconds = 0
	return entry, true
}

func (c *ReplicaCooldowns) Active(workerID, model string, now time.Time) bool {
	_, ok := c.Get(workerID, model, now)
	return ok
}

func (c *ReplicaCooldowns) Get(workerID, model string, now time.Time) (ReplicaCooldown, bool) {
	if c == nil || workerID == "" || model == "" {
		return ReplicaCooldown{}, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)

	entry, ok := c.entries[replicaCooldownKey{WorkerID: workerID, Model: model}]
	if !ok {
		return ReplicaCooldown{}, false
	}
	entry.RemainingSeconds = remainingSeconds(entry.CooldownUntil, now)
	return entry, true
}

func (c *ReplicaCooldowns) Snapshot(now time.Time) ReplicaCooldownSnapshot {
	out := ReplicaCooldownSnapshot{}
	if c == nil {
		return out
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)

	for _, entry := range c.entries {
		entry.RemainingSeconds = remainingSeconds(entry.CooldownUntil, now)
		if out[entry.WorkerID] == nil {
			out[entry.WorkerID] = map[string]ReplicaCooldown{}
		}
		out[entry.WorkerID][entry.Model] = entry
	}
	return out
}

func (s ReplicaCooldownSnapshot) Active(workerID, model string, now time.Time) bool {
	if s == nil {
		return false
	}
	byModel := s[workerID]
	if byModel == nil {
		return false
	}
	entry, ok := byModel[model]
	return ok && entry.CooldownUntil.After(now)
}

func (s ReplicaCooldownSnapshot) Get(workerID, model string, now time.Time) (ReplicaCooldown, bool) {
	if s == nil {
		return ReplicaCooldown{}, false
	}
	byModel := s[workerID]
	if byModel == nil {
		return ReplicaCooldown{}, false
	}
	entry, ok := byModel[model]
	if !ok || !entry.CooldownUntil.After(now) {
		return ReplicaCooldown{}, false
	}
	entry.RemainingSeconds = remainingSeconds(entry.CooldownUntil, now)
	return entry, true
}

func (c *ReplicaCooldowns) pruneLocked(now time.Time) {
	for key, entry := range c.entries {
		if !entry.CooldownUntil.After(now) {
			delete(c.entries, key)
		}
	}
}

func remainingSeconds(until time.Time, now time.Time) int64 {
	if !until.After(now) {
		return 0
	}
	remaining := until.Sub(now)
	seconds := int64(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	return seconds
}
```

- [ ] **Step 4: Run tracker test and verify GREEN**

Run:

```bash
go test ./internal/gateway -run TestReplicaCooldownsMarkClearAndExpire -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit tracker**

```bash
git add internal/gateway/replica_cooldown.go internal/gateway/replica_cooldown_test.go
git commit -m "feat: add replica cooldown tracker"
```

---

### Task 2: Scheduler Excludes Cooled-Down Replicas

**Files:**
- Modify: `internal/gateway/placement.go`
- Modify: `internal/gateway/scheduler.go`
- Modify: `internal/gateway/scheduler_test.go`

- [ ] **Step 1: Write failing scheduler test**

Append to `internal/gateway/scheduler_test.go`:

```go
func TestSchedulerSkipsReadyReplicaInCooldown(t *testing.T) {
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{"qwen": {MaxLoaded: 2}},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-4090": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(6 * time.Second)
	now := time.Unix(100, 0)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "bad",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://bad",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "good",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://good",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	}, now)
	cooldowns := NewReplicaCooldowns(30 * time.Second)
	cooldowns.Mark("bad", "qwen", "upstream_503", now)

	decision, err := (Scheduler{Config: cfg, Workers: reg, Cooldowns: cooldowns.Snapshot(now)}).PickDecision("qwen", now, nil)
	if err != nil {
		t.Fatalf("PickDecision returned error: %v", err)
	}
	if decision.Worker.ID != "good" {
		t.Fatalf("picked %s, want good", decision.Worker.ID)
	}
	for _, candidate := range decision.Candidates {
		if candidate.WorkerID == "bad" {
			t.Fatalf("cooled-down worker appeared in candidates: %+v", decision.Candidates)
		}
	}
}
```

- [ ] **Step 2: Run scheduler test and verify RED**

Run:

```bash
go test ./internal/gateway -run TestSchedulerSkipsReadyReplicaInCooldown -count=1
```

Expected: FAIL because `Scheduler` has no `Cooldowns` field or the cooled-down worker is still selected.

- [ ] **Step 3: Add cooldown plumbing**

Modify structs:

```go
type Placement struct {
	Config    config.GatewayConfig
	Workers   *WorkerRegistry
	Access    *AccessTracker
	Pressure  *PressureTracker
	Cooldowns ReplicaCooldownSnapshot
}

type Scheduler struct {
	Config    config.GatewayConfig
	Workers   *WorkerRegistry
	Access    *AccessTracker
	Cooldowns ReplicaCooldownSnapshot
}
```

In `Scheduler.PickDecision`, pass `Cooldowns` into `Placement`.

In `Placement.PickReadyWorker`, skip candidates with active cooldown:

```go
if p.Cooldowns.Active(worker.ID, model, now) {
	continue
}
```

Apply the skip in the candidate loop only. Keep ready/occupied counts based on worker-reported state so logs still show actual replica state.

- [ ] **Step 4: Run scheduler test and gateway tests**

Run:

```bash
go test ./internal/gateway -run TestSchedulerSkipsReadyReplicaInCooldown -count=1
go test ./internal/gateway -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit scheduler exclusion**

```bash
git add internal/gateway/placement.go internal/gateway/scheduler.go internal/gateway/scheduler_test.go
git commit -m "fix: skip cooled down model replicas"
```

---

### Task 3: Proxy Marks And Clears Cooldown

**Files:**
- Modify: `internal/gateway/server.go`
- Modify: `internal/gateway/proxy.go`
- Modify: `internal/gateway/proxy_test.go`

- [ ] **Step 1: Write failing proxy tests**

Append to `internal/gateway/proxy_test.go`:

```go
func TestProxyMarksRetryableFailureCooldownAndSkipsReplicaOnNextRequest(t *testing.T) {
	var firstRequests atomic.Int32
	var secondRequests atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstRequests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondRequests.Add(1)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","choices":[]}`))
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if !srv.replicaCooldowns.Active("first", "qwen", time.Now()) {
		t.Fatal("first replica cooldown is not active after retryable failure")
	}

	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if firstRequests.Load() != 1 {
		t.Fatalf("first requests = %d, want 1 because cooldown skips second request", firstRequests.Load())
	}
	if secondRequests.Load() != 2 {
		t.Fatalf("second requests = %d, want 2", secondRequests.Load())
	}
}

func TestProxyClearsReplicaCooldownOnSuccessfulRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"chatcmpl-ok","choices":[]}`))
	}))
	defer upstream.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)
	srv.replicaCooldowns.Mark("worker-a", "qwen", "connection_error", time.Now())

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if srv.replicaCooldowns.Active("worker-a", "qwen", time.Now()) {
		t.Fatal("cooldown remained active after successful proxy")
	}
}

func TestProxyDoesNotCooldownNonRetryableJSON404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"model_not_found"}}`))
	}))
	defer upstream.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if srv.replicaCooldowns.Active("worker-a", "qwen", time.Now()) {
		t.Fatal("JSON 404 should not mark cooldown")
	}
}
```

- [ ] **Step 2: Run proxy tests and verify RED**

Run:

```bash
go test ./internal/gateway -run 'TestProxyMarksRetryableFailureCooldownAndSkipsReplicaOnNextRequest|TestProxyClearsReplicaCooldownOnSuccessfulRequest|TestProxyDoesNotCooldownNonRetryableJSON404' -count=1
```

Expected: FAIL because `Server` has no `replicaCooldowns` field and proxy does not mark/clear.

- [ ] **Step 3: Add server cooldown field**

In `Server`:

```go
replicaCooldowns *ReplicaCooldowns
```

In `newServer`:

```go
replicaCooldowns: NewReplicaCooldowns(defaultReplicaCooldownTTL),
```

In `handleModelProxy`, pass active cooldowns into scheduler:

```go
decision, err := (Scheduler{
	Config:    s.config,
	Workers:   s.workers,
	Access:    s.access,
	Cooldowns: s.replicaCooldowns.Snapshot(now),
}).PickDecision(model, now, exclude)
```

- [ ] **Step 4: Add proxy mark/clear helpers**

Add to `proxy.go`:

```go
func (s *Server) markReplicaCooldown(requestID, model string, worker Worker, failure *proxyDispatchFailure, statusCode int, attempt int) {
	if s == nil || s.replicaCooldowns == nil || failure == nil {
		return
	}
	now := time.Now()
	entry, marked := s.replicaCooldowns.Mark(worker.ID, model, failure.code, now)
	if !marked {
		return
	}
	s.logEvent("replica_unhealthy_marked", map[string]any{
		"request_id":         requestID,
		"model":              model,
		"worker_id":          worker.ID,
		"reason":             failure.code,
		"status_code":        statusCode,
		"attempt":            attempt,
		"cooldown_seconds":   entry.RemainingSeconds,
		"cooldown_until":     entry.CooldownUntil.UTC(),
		"failure_count":      entry.FailureCount,
	})
}

func (s *Server) clearReplicaCooldown(requestID, model string, worker Worker) {
	if s == nil || s.replicaCooldowns == nil {
		return
	}
	entry, ok := s.replicaCooldowns.Clear(worker.ID, model, time.Now())
	if !ok {
		return
	}
	s.logEvent("replica_unhealthy_cleared", map[string]any{
		"request_id": requestID,
		"model":      model,
		"worker_id":  worker.ID,
		"reason":     entry.Reason,
	})
}
```

When `retry == true`, call `markReplicaCooldown(...)` before continuing. When the final attempt succeeds with status code `< 500`, call `clearReplicaCooldown(...)` before recording request stats.

- [ ] **Step 5: Run proxy tests**

Run:

```bash
go test ./internal/gateway -run 'TestProxyMarksRetryableFailureCooldownAndSkipsReplicaOnNextRequest|TestProxyClearsReplicaCooldownOnSuccessfulRequest|TestProxyDoesNotCooldownNonRetryableJSON404|TestProxyRetriesDifferentWorkerBeforeHeaders|TestProxyForwardsJSON404WithoutRetry' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit proxy cooldown behavior**

```bash
git add internal/gateway/server.go internal/gateway/proxy.go internal/gateway/proxy_test.go
git commit -m "fix: cooldown failed model replicas"
```

---

### Task 4: Retry And Cooldown Logs/Metrics

**Files:**
- Modify: `internal/gateway/proxy.go`
- Modify: `internal/gateway/metrics.go`
- Modify: `internal/gateway/metrics_test.go`
- Modify: `internal/gateway/proxy_test.go`

- [ ] **Step 1: Write failing log and metrics tests**

Append to `internal/gateway/proxy_test.go`:

```go
func TestProxyLogsRetryAndCooldownEvents(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","choices":[]}`))
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	var logs bytes.Buffer
	srv.logger = log.New(&logs, "", 0)
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	logText := logs.String()
	for _, want := range []string{`"event":"proxy_retry"`, `"event":"replica_unhealthy_marked"`, `"reason":"upstream_retry_exhausted"`, `"worker_id":"first"`} {
		if !strings.Contains(logText, want) {
			t.Fatalf("logs missing %s:\n%s", want, logText)
		}
	}
}
```

Append to `internal/gateway/metrics_test.go`:

```go
func TestProxyReportsCooldownAndRetryMetrics(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"chatcmpl-second","choices":[]}`))
	}))
	defer second.Close()

	srv := NewServer(testProxyConfig())
	registerProxyWorker(t, srv, "first", first.URL, true)
	registerProxyWorker(t, srv, "second", second.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	body := scrapeMetrics(t, srv)
	assertMetricLine(t, body, `llm_swap_gateway_replica_unhealthy{model="qwen",reason="upstream_retry_exhausted",worker_id="first"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_replica_cooldown_marks_total{model="qwen",reason="upstream_retry_exhausted",worker_id="first"} 1`)
	assertMetricLine(t, body, `llm_swap_gateway_proxy_retries_total{model="qwen",reason="upstream_retry_exhausted",worker_id="first"} 1`)
}
```

- [ ] **Step 2: Run log/metrics tests and verify RED**

Run:

```bash
go test ./internal/gateway -run 'TestProxyLogsRetryAndCooldownEvents|TestProxyReportsCooldownAndRetryMetrics' -count=1
```

Expected: FAIL because metrics and logs are missing.

- [ ] **Step 3: Add metrics fields and methods**

In `Metrics` add:

```go
replicaUnhealthy       *prometheus.GaugeVec
replicaCooldownMarks   *prometheus.CounterVec
replicaCooldownClears  *prometheus.CounterVec
proxyRetries           *prometheus.CounterVec
```

Register:

```go
replicaUnhealthy := prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "llm_swap_gateway_replica_unhealthy",
	Help: "Whether a worker/model replica is temporarily excluded by gateway cooldown.",
}, []string{"worker_id", "model", "reason"})
replicaCooldownMarks := prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "llm_swap_gateway_replica_cooldown_marks_total",
	Help: "Replica cooldown marks by worker, model, and reason.",
}, []string{"worker_id", "model", "reason"})
replicaCooldownClears := prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "llm_swap_gateway_replica_cooldown_clears_total",
	Help: "Replica cooldown clears by worker, model, and reason.",
}, []string{"worker_id", "model", "reason"})
proxyRetries := prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "llm_swap_gateway_proxy_retries_total",
	Help: "Gateway proxy retries by worker, model, and reason.",
}, []string{"worker_id", "model", "reason"})
```

Add methods:

```go
func (m *Metrics) ObserveReplicaCooldowns(snapshot ReplicaCooldownSnapshot, now time.Time) {
	for workerID, byModel := range snapshot {
		for model, entry := range byModel {
			if entry.CooldownUntil.After(now) {
				m.replicaUnhealthy.WithLabelValues(workerID, model, entry.Reason).Set(1)
			}
		}
	}
}

func (m *Metrics) ObserveReplicaCooldownMark(entry ReplicaCooldown) {
	m.replicaCooldownMarks.WithLabelValues(entry.WorkerID, entry.Model, entry.Reason).Inc()
}

func (m *Metrics) ObserveReplicaCooldownClear(entry ReplicaCooldown) {
	m.replicaCooldownClears.WithLabelValues(entry.WorkerID, entry.Model, entry.Reason).Inc()
}

func (m *Metrics) ObserveProxyRetry(model, workerID, reason string) {
	m.proxyRetries.WithLabelValues(workerID, model, reason).Inc()
}
```

In `Server.handleMetrics`, call:

```go
s.metrics.ObserveReplicaCooldowns(s.replicaCooldowns.Snapshot(now), now)
```

Call mark/clear/retry metrics from proxy helpers.

- [ ] **Step 4: Add proxy retry logs**

When `retry == true`, log:

```go
s.logEvent("proxy_retry", map[string]any{
	"request_id":  requestID,
	"model":       model,
	"worker_id":   worker.ID,
	"reason":      dispatchFailure.code,
	"status_code": statusCode,
	"attempt":     dispatchAttempts,
})
```

Before returning `lastDispatchFailure.write(w)`, log:

```go
s.logEvent("proxy_retry_exhausted", map[string]any{
	"request_id": requestID,
	"model":      model,
	"reason":     lastDispatchFailure.code,
})
```

- [ ] **Step 5: Run log/metrics tests**

Run:

```bash
go test ./internal/gateway -run 'TestProxyLogsRetryAndCooldownEvents|TestProxyReportsCooldownAndRetryMetrics' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit observability metrics/logs**

```bash
git add internal/gateway/proxy.go internal/gateway/metrics.go internal/gateway/metrics_test.go internal/gateway/proxy_test.go
git commit -m "feat: observe replica cooldowns"
```

---

### Task 5: UI Cooldown State

**Files:**
- Modify: `internal/gateway/ui.go`
- Modify: `internal/gateway/ui_test.go`

- [ ] **Step 1: Write failing UI status test**

Append to `internal/gateway/ui_test.go`:

```go
func TestUIStatusIncludesReplicaCooldownDetails(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	now := time.Now()
	postHeartbeat(t, srv, protocol.HeartbeatRequest{
		AgentID:       "gpu-01",
		Tags:          []string{"gpu-4090"},
		LlamaSwapURL:  "http://worker",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	})
	srv.replicaCooldowns.Mark("gpu-01", "qwen", "upstream_retry_exhausted", now)

	req := httptest.NewRequest(http.MethodGet, "/ui/status", nil)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	var status uiStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	model, ok := findUIModel(status.Models, "qwen")
	if !ok || len(model.WorkerStatuses) != 1 {
		t.Fatalf("model statuses = %+v, want qwen worker status", status.Models)
	}
	if !model.WorkerStatuses[0].CooldownActive || model.WorkerStatuses[0].CooldownReason != "upstream_retry_exhausted" {
		t.Fatalf("model worker cooldown = %+v, want active upstream_retry_exhausted", model.WorkerStatuses[0])
	}
	if len(status.Workers) != 1 || len(status.Workers[0].ReplicaCooldowns) != 1 {
		t.Fatalf("worker cooldowns = %+v, want one cooldown", status.Workers)
	}
}
```

- [ ] **Step 2: Run UI test and verify RED**

Run:

```bash
go test ./internal/gateway -run TestUIStatusIncludesReplicaCooldownDetails -count=1
```

Expected: FAIL because UI structs have no cooldown fields.

- [ ] **Step 3: Add UI JSON fields**

In `uiModelWorker` add:

```go
CooldownActive           bool      `json:"cooldown_active"`
CooldownReason           string    `json:"cooldown_reason,omitempty"`
CooldownRemainingSeconds int64     `json:"cooldown_remaining_seconds,omitempty"`
CooldownUntil            time.Time `json:"cooldown_until,omitempty"`
```

In `uiWorker` add:

```go
ReplicaCooldowns []ReplicaCooldown `json:"replica_cooldowns"`
```

In `handleUIStatus`, capture:

```go
cooldowns := s.replicaCooldowns.Snapshot(now)
```

Pass cooldowns into `buildUIModels` and `buildUIWorkers`. For each model/worker status, use:

```go
if cooldown, ok := cooldowns.Get(worker.ID, name, now); ok {
	status.CooldownActive = true
	status.CooldownReason = cooldown.Reason
	status.CooldownRemainingSeconds = cooldown.RemainingSeconds
	status.CooldownUntil = cooldown.CooldownUntil.UTC()
}
```

For worker cards, append active cooldowns for the worker from the snapshot.

- [ ] **Step 4: Add minimal HTML rendering**

In model worker pill rendering, append cooldown text:

```js
const cooldown = w.cooldown_active ? " cooldown " + w.cooldown_remaining_seconds + "s " + w.cooldown_reason : "";
```

In worker card rendering, add a "Cooldowns" detail block using `w.replica_cooldowns || []`.

- [ ] **Step 5: Run UI tests**

Run:

```bash
go test ./internal/gateway -run 'TestUIStatusIncludesReplicaCooldownDetails|TestUIPageServesDashboardHTML|TestUIStatusEndpointUsesEmptyArraysInsteadOfNull' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit UI cooldown state**

```bash
git add internal/gateway/ui.go internal/gateway/ui_test.go
git commit -m "feat: show replica cooldowns in ui"
```

---

### Task 6: Docs And Full Verification

**Files:**
- Modify: `docs/agents/project-map.md`

- [ ] **Step 1: Update project map**

Add gateway cooldown notes:

```markdown
- `internal/gateway/replica_cooldown.go`
  - Tracks short-lived gateway-local cooldowns for failing `worker_id + model`
    replicas. Cooldown affects request routing only and does not change worker
    heartbeat health or trigger unloads.
```

Add placement rollout note:

```markdown
- Retryable proxy failures mark only the failing `worker_id + model` replica
  as cooled down for 30 seconds. Requests skip cooled-down ready replicas but
  reconciliation remains gateway-owned and policy-driven.
```

- [ ] **Step 2: Run full tests**

Run:

```bash
go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit docs and any final cleanup**

```bash
git add docs/agents/project-map.md
git commit -m "docs: document replica cooldown routing"
```

- [ ] **Step 4: Build production gateway candidate**

Run:

```bash
GOOS=linux GOARCH=amd64 go build -o /tmp/llm-swap-gateway-cooldown ./cmd/gateway
sha256sum /tmp/llm-swap-gateway-cooldown
```

Expected: build succeeds and prints a SHA256.

- [ ] **Step 5: Final status check**

Run:

```bash
git status --short
git log --oneline -6
```

Expected: clean worktree and recent cooldown commits visible.
