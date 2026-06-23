# Conservative Predictive Scale-Out Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add conservative predictive warm scale-out so the gateway can preload additional model replicas from sustained pressure while keeping request routing limited to ready workers.

**Architecture:** Add a focused in-memory `PressureTracker` for rolling queue/request signals, extend `Placement` with warm control-action planning, and execute warm actions from the existing loaded-replica reconciler. The request path records pressure only; it does not synchronously start runtimes, unload models, or route to starting workers.

**Tech Stack:** Go, existing gateway tests, existing llama-swap HTTP client, existing worker registry and request log accounting.

---

## Scope

This plan implements the first conservative predictive scale-out slice from
`docs/superpowers/specs/2026-06-24-conservative-predictive-scaleout-design.md`.

It includes:

- in-memory rolling model pressure observations;
- conservative demand scoring;
- warm action planning in Placement;
- empty-worker-first warm selection;
- high switch-cost eviction warm selection;
- reconciler execution for one warm action per cycle;
- structured logs and worker events for warm actions.

It does not include:

- external persistence for rolling pressure;
- UI controls for scores;
- machine-learning prediction;
- gateway direct runtime startup;
- aggressive tuning knobs.

## File Structure

- Create `internal/gateway/pressure.go`
  - Owns rolling observations, p95 wait calculation, and model pressure snapshots.

- Create `internal/gateway/pressure_test.go`
  - Tests observation recording, expiry, p95, and demand score behavior.

- Modify `internal/gateway/server.go`
  - Adds `pressure *PressureTracker` to `Server` and initializes it.

- Modify `internal/gateway/proxy.go`
  - Records queue/request pressure observations after existing queue and request accounting.

- Modify `internal/gateway/placement.go`
  - Adds `Pressure *PressureTracker` to Placement.
  - Adds `ControlActionWarm`.
  - Plans conservative warm actions after existing unload actions.

- Modify `internal/gateway/placement_test.go`
  - Adds warm scale-out planning tests.

- Modify `internal/gateway/llamaswap_client.go`
  - Adds `Load(ctx, baseURL, model)` for the llama-swap manual model load endpoint.

- Modify `internal/gateway/reconcile.go`
  - Executes warm actions with `LlamaSwapClient.Load`.
  - Records gateway warm events and structured logs.

- Modify `internal/gateway/reconcile_test.go`
  - Tests warm execution, single-action-per-cycle behavior, and active-worker guard.

- Modify `docs/agents/project-map.md`
  - Documents pressure tracking and conservative warm scale-out behavior.

## Constants and Scoring Defaults

Use these defaults in `internal/gateway/pressure.go`:

```go
const (
	defaultPressureWindow = 5 * time.Minute
	minScaleOutRequests   = 3
	minScaleOutScore      = 120
	defaultSwitchCost     = 80
)
```

Use this scoring shape:

```text
demand_score =
  priority
  + min(recent_requests*10, 60)
  + min(recent_tokens/200, 40)
  + min(waited_requests*25, 75)
  + min(queue_full_or_timeout*40, 80)
  + min(p95_wait_ms/100, 40)
  + utilization_bonus
  - starting_penalty
```

Rules:

- if recent request count is below `minScaleOutRequests`, demand score is zero;
- utilization bonus is 35 when ready replicas are positive and `active >= ready`;
- utilization bonus is 20 when there are no ready replicas but there is recent pressure;
- starting penalty is 60 per occupied replica above ready count;
- negative scores clamp to zero.

Use this keep score shape:

```text
keep_score =
  priority
  + 120 when loaded count is at or below min_loaded
  + 80 when ProtectedUntil is in the future
  + 50 when worker is active
  + 30 when worker-model was accessed in the last 5 minutes
  - 40 when min_loaded is zero
```

Placement must still skip active workers and protected replicas before scoring,
so the active/protected keep bonuses are explanatory and useful for logs.

## Task 1: Add PressureTracker

**Files:**
- Create: `internal/gateway/pressure.go`
- Create: `internal/gateway/pressure_test.go`

- [ ] **Step 1: Write failing pressure tests**

Create `internal/gateway/pressure_test.go`:

```go
package gateway

import (
	"testing"
	"time"
)

func TestPressureTrackerSnapshotExpiresOldObservationsAndComputesP95(t *testing.T) {
	now := time.Unix(1000, 0)
	tracker := NewPressureTracker(5 * time.Minute)
	tracker.RecordQueue(PressureQueueObservation{
		Time:              now.Add(-10 * time.Minute),
		Model:             "qwen",
		Result:            QueueResultAdmittedAfterWait,
		WaitMS:            9000,
		ReadyReplicas:     1,
		OccupiedReplicas:  1,
		ActiveBefore:      1,
	})
	tracker.RecordQueue(PressureQueueObservation{
		Time:              now.Add(-20 * time.Second),
		Model:             "qwen",
		Result:            QueueResultAdmittedAfterWait,
		WaitMS:            200,
		ReadyReplicas:     1,
		OccupiedReplicas:  1,
		ActiveBefore:      1,
	})
	tracker.RecordQueue(PressureQueueObservation{
		Time:              now.Add(-10 * time.Second),
		Model:             "qwen",
		Result:            QueueResultTimeout,
		WaitMS:            500,
		ReadyReplicas:     1,
		OccupiedReplicas:  1,
		ActiveBefore:      1,
	})
	tracker.RecordRequest(PressureRequestObservation{
		Time:        now.Add(-5 * time.Second),
		Model:       "qwen",
		TotalTokens: 800,
	})

	snapshot := tracker.Model("qwen", now)
	if snapshot.RecentRequests != 1 {
		t.Fatalf("RecentRequests = %d, want 1", snapshot.RecentRequests)
	}
	if snapshot.WaitedRequests != 1 {
		t.Fatalf("WaitedRequests = %d, want 1", snapshot.WaitedRequests)
	}
	if snapshot.QueueErrors != 1 {
		t.Fatalf("QueueErrors = %d, want 1", snapshot.QueueErrors)
	}
	if snapshot.P95WaitMS != 500 {
		t.Fatalf("P95WaitMS = %d, want 500", snapshot.P95WaitMS)
	}
	if snapshot.RecentTokens != 800 {
		t.Fatalf("RecentTokens = %d, want 800", snapshot.RecentTokens)
	}
}

func TestPressureDemandScoreRequiresSustainedRequests(t *testing.T) {
	now := time.Unix(1000, 0)
	tracker := NewPressureTracker(5 * time.Minute)
	for i := 0; i < minScaleOutRequests-1; i++ {
		tracker.RecordRequest(PressureRequestObservation{
			Time:  now.Add(time.Duration(i) * time.Second),
			Model: "qwen",
		})
	}
	snapshot := tracker.Model("qwen", now.Add(time.Minute))
	if score := DemandScore(snapshot, DemandScoreInput{Priority: 100, ReadyReplicas: 1, OccupiedReplicas: 1, Active: 1}); score != 0 {
		t.Fatalf("DemandScore = %d, want 0 for burst below request floor", score)
	}

	tracker.RecordRequest(PressureRequestObservation{Time: now.Add(3 * time.Second), Model: "qwen"})
	snapshot = tracker.Model("qwen", now.Add(time.Minute))
	score := DemandScore(snapshot, DemandScoreInput{Priority: 100, ReadyReplicas: 1, OccupiedReplicas: 1, Active: 1})
	if score == 0 {
		t.Fatalf("DemandScore = 0, want positive score after sustained requests")
	}
}
```

- [ ] **Step 2: Run the tests and verify failure**

Run:

```bash
go test ./internal/gateway -run 'TestPressureTracker|TestPressureDemandScore' -count=1
```

Expected: FAIL because `PressureTracker` and related types are undefined.

- [ ] **Step 3: Implement PressureTracker**

Create `internal/gateway/pressure.go`:

```go
package gateway

import (
	"sort"
	"sync"
	"time"
)

const (
	defaultPressureWindow = 5 * time.Minute
	minScaleOutRequests   = 3
	minScaleOutScore      = 120
	defaultSwitchCost     = 80
)

type PressureTracker struct {
	mu       sync.Mutex
	window   time.Duration
	queues   []PressureQueueObservation
	requests []PressureRequestObservation
}

type PressureQueueObservation struct {
	Time             time.Time
	Model            string
	Result           string
	WaitMS           int64
	ReadyReplicas    int
	OccupiedReplicas int
	ActiveBefore     int
	QueuedBefore     int
}

type PressureRequestObservation struct {
	Time        time.Time
	Model       string
	WorkerID    string
	TotalTokens int
	DurationMS  int64
	StatusCode  int
}

type PressureSnapshot struct {
	Model            string
	RecentRequests   int
	RecentTokens     int
	WaitedRequests   int
	QueueErrors      int
	P95WaitMS        int64
	ReadyReplicas    int
	OccupiedReplicas int
	MaxActive         int
	LastAccess       time.Time
}

type DemandScoreInput struct {
	Priority         int
	ReadyReplicas    int
	OccupiedReplicas int
	Active           int
}

func NewPressureTracker(window time.Duration) *PressureTracker {
	if window <= 0 {
		window = defaultPressureWindow
	}
	return &PressureTracker{window: window}
}

func (p *PressureTracker) RecordQueue(obs PressureQueueObservation) {
	if p == nil || obs.Model == "" || obs.Result == "" {
		return
	}
	if obs.Time.IsZero() {
		obs.Time = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queues = append(p.queues, obs)
	p.pruneLocked(obs.Time)
}

func (p *PressureTracker) RecordRequest(obs PressureRequestObservation) {
	if p == nil || obs.Model == "" {
		return
	}
	if obs.Time.IsZero() {
		obs.Time = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, obs)
	p.pruneLocked(obs.Time)
}

func (p *PressureTracker) Model(model string, now time.Time) PressureSnapshot {
	if p == nil || model == "" {
		return PressureSnapshot{Model: model}
	}
	if now.IsZero() {
		now = time.Now()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked(now)

	out := PressureSnapshot{Model: model}
	waits := []int64{}
	for _, obs := range p.requests {
		if obs.Model != model {
			continue
		}
		out.RecentRequests++
		out.RecentTokens += maxInt(obs.TotalTokens, 0)
		if obs.Time.After(out.LastAccess) {
			out.LastAccess = obs.Time
		}
	}
	for _, obs := range p.queues {
		if obs.Model != model {
			continue
		}
		if obs.Result == QueueResultAdmittedAfterWait {
			out.WaitedRequests++
		}
		if obs.Result == QueueResultFull || obs.Result == QueueResultTimeout {
			out.QueueErrors++
		}
		if obs.WaitMS > 0 {
			waits = append(waits, obs.WaitMS)
		}
		if obs.ReadyReplicas > out.ReadyReplicas {
			out.ReadyReplicas = obs.ReadyReplicas
		}
		if obs.OccupiedReplicas > out.OccupiedReplicas {
			out.OccupiedReplicas = obs.OccupiedReplicas
		}
		if obs.ActiveBefore > out.MaxActive {
			out.MaxActive = obs.ActiveBefore
		}
	}
	out.P95WaitMS = percentile95(waits)
	return out
}

func DemandScore(snapshot PressureSnapshot, input DemandScoreInput) int {
	if snapshot.RecentRequests < minScaleOutRequests {
		return 0
	}
	score := input.Priority
	score += minInt(snapshot.RecentRequests*10, 60)
	score += minInt(snapshot.RecentTokens/200, 40)
	score += minInt(snapshot.WaitedRequests*25, 75)
	score += minInt(snapshot.QueueErrors*40, 80)
	score += minInt(int(snapshot.P95WaitMS/100), 40)
	if input.ReadyReplicas > 0 && input.Active >= input.ReadyReplicas {
		score += 35
	}
	if input.ReadyReplicas == 0 && snapshot.RecentRequests > 0 {
		score += 20
	}
	starting := input.OccupiedReplicas - input.ReadyReplicas
	if starting > 0 {
		score -= starting * 60
	}
	if score < 0 {
		return 0
	}
	return score
}

func (p *PressureTracker) pruneLocked(now time.Time) {
	cutoff := now.Add(-p.window)
	p.queues = pruneQueueObservations(p.queues, cutoff)
	p.requests = pruneRequestObservations(p.requests, cutoff)
}

func pruneQueueObservations(in []PressureQueueObservation, cutoff time.Time) []PressureQueueObservation {
	out := in[:0]
	for _, obs := range in {
		if obs.Time.After(cutoff) || obs.Time.Equal(cutoff) {
			out = append(out, obs)
		}
	}
	return out
}

func pruneRequestObservations(in []PressureRequestObservation, cutoff time.Time) []PressureRequestObservation {
	out := in[:0]
	for _, obs := range in {
		if obs.Time.After(cutoff) || obs.Time.Equal(cutoff) {
			out = append(out, obs)
		}
	}
	return out
}

func percentile95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(float64(len(values)-1) * 0.95)
	if index < 0 {
		index = 0
	}
	if index >= len(values) {
		index = len(values) - 1
	}
	return values[index]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 4: Run pressure tests**

Run:

```bash
go test ./internal/gateway -run 'TestPressureTracker|TestPressureDemandScore' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/pressure.go internal/gateway/pressure_test.go
git commit -m "feat: track gateway model pressure"
```

## Task 2: Record Pressure From Proxy

**Files:**
- Modify: `internal/gateway/server.go`
- Modify: `internal/gateway/proxy.go`
- Modify: `internal/gateway/proxy_test.go`

- [ ] **Step 1: Add failing proxy pressure test**

Add to `internal/gateway/proxy_test.go`:

```go
func TestProxyRecordsQueueAndRequestPressure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "ok"}}},
			"usage": map[string]any{
				"prompt_tokens":     3,
				"completion_tokens": 4,
				"total_tokens":      7,
			},
		})
	}))
	defer upstream.Close()

	cfg := testProxyConfig()
	srv := NewServer(cfg)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	snapshot := srv.pressure.Model("qwen", time.Now())
	if snapshot.RecentRequests != 1 {
		t.Fatalf("RecentRequests = %d, want 1", snapshot.RecentRequests)
	}
	if snapshot.RecentTokens != 7 {
		t.Fatalf("RecentTokens = %d, want 7", snapshot.RecentTokens)
	}
}
```

- [ ] **Step 2: Run test and verify failure**

Run:

```bash
go test ./internal/gateway -run TestProxyRecordsQueueAndRequestPressure -count=1
```

Expected: FAIL because `Server.pressure` is undefined.

- [ ] **Step 3: Add tracker to Server**

Modify `internal/gateway/server.go`:

```go
type Server struct {
	config             config.GatewayConfig
	workers            *WorkerRegistry
	accounting         *Accounting
	limiter            *QueueLimiter
	metrics            *Metrics
	scraper            *MetricsScraper
	access             *AccessTracker
	pressure           *PressureTracker
	requestLogPath     string
	workerEventLogPath string
	proxyAttempts      int
	logger             *log.Logger
	eventMu            sync.Mutex
	recentEvents       []uiAgentEvent
	mux                *http.ServeMux
}
```

Initialize it in `newServer`:

```go
pressure:           NewPressureTracker(defaultPressureWindow),
```

- [ ] **Step 4: Record request pressure**

Modify `recordRequestStats` in `internal/gateway/proxy.go`:

```go
if s.pressure != nil {
	s.pressure.RecordRequest(PressureRequestObservation{
		Time:        entry.Time,
		Model:       entry.Model,
		WorkerID:    entry.WorkerID,
		TotalTokens: entry.TotalTokens,
		DurationMS:  entry.DurationMS,
		StatusCode:  entry.StatusCode,
	})
}
```

- [ ] **Step 5: Record queue pressure and logs**

Modify `observeQueue` in `internal/gateway/proxy.go` before `s.logEvent`:

```go
if s.pressure != nil {
	s.pressure.RecordQueue(PressureQueueObservation{
		Time:             time.Now(),
		Model:            model,
		Result:           stats.Result,
		WaitMS:           stats.WaitMS,
		ReadyReplicas:    replicas.readyReplicas,
		OccupiedReplicas: replicas.occupiedReplicas,
		ActiveBefore:     stats.ActiveBefore,
		QueuedBefore:     stats.QueuedBefore,
	})
}
```

- [ ] **Step 6: Run proxy pressure test**

Run:

```bash
go test ./internal/gateway -run TestProxyRecordsQueueAndRequestPressure -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/gateway/server.go internal/gateway/proxy.go internal/gateway/proxy_test.go
git commit -m "feat: record proxy pressure signals"
```

## Task 3: Add Warm Action Planning to Placement

**Files:**
- Modify: `internal/gateway/placement.go`
- Modify: `internal/gateway/placement_test.go`

- [ ] **Step 1: Add failing empty-worker warm test**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementPlansWarmActionOnEmptyIdleWorkerForSustainedPressure(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {Priority: 100, MinLoaded: 1},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "ready",
		Tags:          []string{"gpu"},
		LlamaSwapURL:  "http://ready",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests; i++ {
		pressure.RecordRequest(PressureRequestObservation{Time: now.Add(time.Duration(i) * time.Second), Model: "qwen", TotalTokens: 1000})
	}
	pressure.RecordQueue(PressureQueueObservation{
		Time:             now.Add(5 * time.Second),
		Model:            "qwen",
		Result:           QueueResultAdmittedAfterWait,
		WaitMS:           800,
		ReadyReplicas:    1,
		OccupiedReplicas: 1,
		ActiveBefore:     1,
	})

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(10 * time.Second))
	if len(actions) != 1 {
		t.Fatalf("actions = %#v, want one warm action", actions)
	}
	if actions[0].Type != ControlActionWarm || actions[0].Worker.ID != "empty" || actions[0].Model != "qwen" {
		t.Fatalf("action = %#v, want warm qwen on empty", actions[0])
	}
}
```

- [ ] **Step 2: Add failing duplicate-start guard test**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementDoesNotPlanWarmWhenTargetAlreadyLoading(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {Priority: 100, MinLoaded: 0, MaxLoaded: 2, MaxLoadedSet: true},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "loading",
		Tags:          []string{"gpu"},
		LlamaSwapURL:  "http://loading",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "loading"}},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests+3; i++ {
		pressure.RecordRequest(PressureRequestObservation{Time: now.Add(time.Duration(i) * time.Second), Model: "qwen", TotalTokens: 1000})
	}

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(10 * time.Second))
	if len(actions) != 0 {
		t.Fatalf("actions = %#v, want no duplicate warm while loading", actions)
	}
}
```

- [ ] **Step 3: Run tests and verify failure**

Run:

```bash
go test ./internal/gateway -run 'TestPlacementPlansWarmAction|TestPlacementDoesNotPlanWarmWhenTargetAlreadyLoading' -count=1
```

Expected: FAIL because `ControlActionWarm` and `Placement.Pressure` are undefined.

- [ ] **Step 4: Extend Placement types**

Modify `internal/gateway/placement.go`:

```go
type Placement struct {
	Config   config.GatewayConfig
	Workers  *WorkerRegistry
	Access   *AccessTracker
	Pressure *PressureTracker
}
```

Add the action type:

```go
const (
	ControlActionUnload ControlActionType = "unload"
	ControlActionWarm   ControlActionType = "warm"
)
```

Extend `ControlAction`:

```go
type ControlAction struct {
	Type        ControlActionType
	Worker      Worker
	Model       string
	Reason      string
	VictimModel string
	DemandScore int
	KeepScore   int
	SwitchCost  int
}
```

- [ ] **Step 5: Add warm planning after existing min_loaded actions**

At the end of `PlanControlActions`, before `return nil`, add:

```go
if action, ok := p.planWarmAction(now, workers, active, loadedCounts); ok {
	return []ControlAction{action}
}
return nil
```

Add helper methods:

```go
func (p Placement) planWarmAction(now time.Time, workers []Worker, active map[string]int, loadedCounts map[string]int) (ControlAction, bool) {
	if p.Pressure == nil {
		return ControlAction{}, false
	}
	models := placementModelNamesByPriority(p.Config)
	var best ControlAction
	found := false
	for _, modelName := range models {
		model := p.Config.Models[modelName]
		ready := loadedCounts[modelName]
		occupied := occupiedModelCount(workers, now, p.Workers, modelName)
		maxLoaded, _ := p.effectiveMaxLoaded(model, workers, modelName, now)
		if maxLoaded > 0 && occupied >= maxLoaded {
			continue
		}
		snapshot := p.Pressure.Model(modelName, now)
		score := DemandScore(snapshot, DemandScoreInput{
			Priority:         model.Priority,
			ReadyReplicas:    ready,
			OccupiedReplicas: occupied,
			Active:           activeCountForReadyModel(workers, active, modelName),
		})
		if score < minScaleOutScore {
			continue
		}
		action, ok := p.pickWarmTarget(now, workers, active, loadedCounts, modelName, score)
		if !ok {
			continue
		}
		if !found || action.DemandScore > best.DemandScore || (action.DemandScore == best.DemandScore && action.Model < best.Model) {
			best = action
			found = true
		}
	}
	return best, found
}
```

Add these helpers:

```go
func (p Placement) pickWarmTarget(now time.Time, workers []Worker, active map[string]int, loadedCounts map[string]int, targetModel string, demandScore int) (ControlAction, bool) {
	for _, worker := range sortedWorkersByID(workers) {
		if !p.warmEligibleWorker(now, worker, active, targetModel) {
			continue
		}
		if len(worker.RunningModels) == 0 {
			return ControlAction{Type: ControlActionWarm, Worker: worker, Model: targetModel, Reason: "empty_worker_predictive_scaleout", DemandScore: demandScore}, true
		}
	}

	var bestWorker Worker
	var bestVictim string
	bestKeep := 0
	found := false
	for _, worker := range sortedWorkersByID(workers) {
		if !p.warmEligibleWorker(now, worker, active, targetModel) {
			continue
		}
		for _, running := range worker.RunningModels {
			if !strings.EqualFold(running.State, "ready") || running.Model == targetModel {
				continue
			}
			if !running.ProtectedUntil.IsZero() && running.ProtectedUntil.After(now) {
				continue
			}
			if !p.canUnloadModelForPlacement(running.Model, loadedCounts) {
				continue
			}
			keep := p.keepScore(now, worker.ID, running.Model, loadedCounts)
			if !found || keep < bestKeep || (keep == bestKeep && worker.ID < bestWorker.ID) {
				bestWorker = worker
				bestVictim = running.Model
				bestKeep = keep
				found = true
			}
		}
	}
	if !found || demandScore <= bestKeep+defaultSwitchCost {
		return ControlAction{}, false
	}
	return ControlAction{Type: ControlActionWarm, Worker: bestWorker, Model: targetModel, Reason: "evict_for_predictive_scaleout", VictimModel: bestVictim, DemandScore: demandScore, KeepScore: bestKeep, SwitchCost: defaultSwitchCost}, true
}

func (p Placement) warmEligibleWorker(now time.Time, worker Worker, active map[string]int, targetModel string) bool {
	if p.Workers != nil && !p.Workers.Healthy(worker.ID, now) {
		return false
	}
	if active[worker.ID] > 0 {
		return false
	}
	if !workerAllowsModel(p.Config, worker, targetModel) || !artifactReady(worker, targetModel) {
		return false
	}
	_, running := runningModelState(worker, targetModel)
	return !running
}

func (p Placement) keepScore(now time.Time, workerID string, modelName string, loadedCounts map[string]int) int {
	model := p.Config.Models[modelName]
	score := model.Priority
	if loadedCounts[modelName] <= model.MinLoaded {
		score += 120
	}
	if model.MinLoaded == 0 {
		score -= 40
	}
	if p.Access != nil {
		last := p.Access.WorkerModelLastAccess(workerID, modelName)
		if !last.IsZero() && now.Sub(last) <= defaultPressureWindow {
			score += 30
		}
	}
	return score
}
```

Add utility helpers:

```go
func sortedWorkersByID(workers []Worker) []Worker {
	out := append([]Worker(nil), workers...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func occupiedModelCount(workers []Worker, now time.Time, reg *WorkerRegistry, model string) int {
	count := 0
	for _, worker := range workers {
		if reg != nil && !reg.Healthy(worker.ID, now) {
			continue
		}
		if _, running := runningModelState(worker, model); running {
			count++
		}
	}
	return count
}

func activeCountForReadyModel(workers []Worker, active map[string]int, model string) int {
	total := 0
	for _, worker := range workers {
		if runningModelReady(worker, model) {
			total += active[worker.ID]
		}
	}
	return total
}
```

- [ ] **Step 6: Run warm placement tests**

Run:

```bash
go test ./internal/gateway -run 'TestPlacementPlansWarmAction|TestPlacementDoesNotPlanWarmWhenTargetAlreadyLoading' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/gateway/placement.go internal/gateway/placement_test.go
git commit -m "feat: plan conservative warm scale-out"
```

## Task 4: Add LlamaSwap Load Client

**Files:**
- Modify: `internal/gateway/llamaswap_client.go`
- Create: `internal/gateway/llamaswap_client_test.go`

- [ ] **Step 1: Add failing load client test**

Create or append to `internal/gateway/llamaswap_client_test.go`:

```go
package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLlamaSwapClientLoadPostsModelLoadEndpoint(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/models/load/qwen" {
			t.Fatalf("unexpected load request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer llama-secret" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := LlamaSwapClient{BearerToken: "llama-secret"}
	if err := client.Load(context.Background(), server.URL, "qwen"); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !called {
		t.Fatalf("load endpoint was not called")
	}
}
```

- [ ] **Step 2: Run test and verify failure**

Run:

```bash
go test ./internal/gateway -run TestLlamaSwapClientLoadPostsModelLoadEndpoint -count=1
```

Expected: FAIL because `Load` is undefined.

- [ ] **Step 3: Implement Load**

Modify `internal/gateway/llamaswap_client.go`:

```go
func (c LlamaSwapClient) Load(ctx context.Context, baseURL, model string) error {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/models/load/" + url.PathEscape(model)
	return c.post(ctx, endpoint)
}
```

Note: llama-swap README documents unload APIs and UI manual loading, while the
load endpoint is version-sensitive. Keep this method small so the endpoint can
be changed in one place after real-version verification.

- [ ] **Step 4: Run client test**

Run:

```bash
go test ./internal/gateway -run TestLlamaSwapClientLoadPostsModelLoadEndpoint -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/llamaswap_client.go internal/gateway/llamaswap_client_test.go
git commit -m "feat: add llama-swap model load client"
```

## Task 5: Execute Warm Actions in Reconciler

**Files:**
- Modify: `internal/gateway/reconcile.go`
- Modify: `internal/gateway/reconcile_test.go`

- [ ] **Step 1: Add failing warm execution test**

Add to `internal/gateway/reconcile_test.go`:

```go
func TestLoadedReconcilerExecutesWarmAction(t *testing.T) {
	var loadCalls atomic.Int32
	loadServer := loadServerForModel(t, "qwen", &loadCalls)
	defer loadServer.Close()
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {Priority: 100, MinLoaded: 1},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
		Tokens: config.TokenConfig{LlamaSwap: "llama-secret"},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "ready",
		Tags:          []string{"gpu"},
		LlamaSwapURL:  "http://ready.invalid",
		Artifacts:     map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "qwen", State: "ready"}},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: loadServer.URL,
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests; i++ {
		pressure.RecordRequest(PressureRequestObservation{Time: now.Add(time.Duration(i) * time.Second), Model: "qwen", TotalTokens: 1000})
	}
	pressure.RecordQueue(PressureQueueObservation{Time: now.Add(5 * time.Second), Model: "qwen", Result: QueueResultAdmittedAfterWait, WaitMS: 900, ReadyReplicas: 1, OccupiedReplicas: 1, ActiveBefore: 1})

	reconciler := LoadedReconciler{
		Config:   cfg,
		Workers:  reg,
		Client:   LlamaSwapClient{BearerToken: "llama-secret"},
		Access:   NewAccessTracker(),
		Pressure: pressure,
	}
	if err := reconciler.Reconcile(context.Background(), now.Add(10*time.Second)); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if loadCalls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1", loadCalls.Load())
	}
}
```

Add helper:

```go
func loadServerForModel(t *testing.T, model string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/models/load/"+model {
			t.Fatalf("unexpected load request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer llama-secret" {
			t.Fatalf("authorization = %q, want llama-secret bearer", got)
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
}
```

- [ ] **Step 2: Run test and verify failure**

Run:

```bash
go test ./internal/gateway -run TestLoadedReconcilerExecutesWarmAction -count=1
```

Expected: FAIL because `LoadedReconciler.Pressure` is undefined and warm execution is missing.

- [ ] **Step 3: Add Pressure to LoadedReconciler and server wiring**

Modify `internal/gateway/reconcile.go`:

```go
type LoadedReconciler struct {
	Config      config.GatewayConfig
	Workers     *WorkerRegistry
	Client      LlamaSwapClient
	Access      *AccessTracker
	Pressure    *PressureTracker
	RecordEvent func(workerID string, event protocol.AgentEvent)
	LogEvent     func(event string, fields map[string]any)
}
```

In `RunLoadedReconciler`, pass:

```go
Pressure:    s.pressure,
LogEvent:    s.logEvent,
```

- [ ] **Step 4: Execute warm actions**

Modify the control-action loop in `Reconcile`:

```go
placement := Placement{Config: r.Config, Workers: r.Workers, Access: r.Access, Pressure: r.Pressure}
for _, action := range placement.PlanControlActions(now) {
	switch action.Type {
	case ControlActionUnload:
		if active[action.Worker.ID] > 0 {
			continue
		}
		if err := r.Client.Unload(ctx, action.Worker.LlamaSwapURL, action.Model); err != nil {
			r.recordUnloadEvent(action.Worker.ID, action.Model, "gateway_model_unload_error", err)
			outErr = errors.Join(outErr, err)
			continue
		}
		r.recordUnloadEvent(action.Worker.ID, action.Model, "gateway_model_unload_done", nil)
	case ControlActionWarm:
		if active[action.Worker.ID] > 0 {
			continue
		}
		r.logControlAction("control_action_planned", action, nil)
		r.recordWarmEvent(action.Worker.ID, action.Model, "gateway_model_warm_start", nil)
		if err := r.Client.Load(ctx, action.Worker.LlamaSwapURL, action.Model); err != nil {
			r.recordWarmEvent(action.Worker.ID, action.Model, "gateway_model_warm_error", err)
			r.logControlAction("control_action_error", action, err)
			outErr = errors.Join(outErr, err)
			continue
		}
		r.recordWarmEvent(action.Worker.ID, action.Model, "gateway_model_warm_done", nil)
		r.logControlAction("control_action_done", action, nil)
	}
}
```

Add helpers:

```go
func (r LoadedReconciler) recordWarmEvent(workerID string, modelName string, eventName string, err error) {
	if r.RecordEvent == nil {
		return
	}
	event := protocol.AgentEvent{Event: eventName, Model: modelName}
	if err != nil {
		event.Error = err.Error()
	}
	r.RecordEvent(workerID, event)
}

func (r LoadedReconciler) logControlAction(event string, action ControlAction, err error) {
	if r.LogEvent == nil {
		return
	}
	fields := map[string]any{
		"action":       string(action.Type),
		"model":        action.Model,
		"worker_id":    action.Worker.ID,
		"reason":       action.Reason,
		"demand_score": action.DemandScore,
		"keep_score":   action.KeepScore,
		"switch_cost":  action.SwitchCost,
	}
	if action.VictimModel != "" {
		fields["victim_model"] = action.VictimModel
	}
	if err != nil {
		fields["error"] = err.Error()
	}
	r.LogEvent(event, fields)
}
```

- [ ] **Step 5: Run warm reconciler test**

Run:

```bash
go test ./internal/gateway -run TestLoadedReconcilerExecutesWarmAction -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/reconcile.go internal/gateway/reconcile_test.go
git commit -m "feat: execute predictive warm actions"
```

## Task 6: Add Conservative Eviction Warm Tests

**Files:**
- Modify: `internal/gateway/placement_test.go`
- Modify: `internal/gateway/reconcile_test.go`

- [ ] **Step 1: Add placement eviction threshold test**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementWarmEvictsOpportunityCacheOnlyWhenDemandBeatsSwitchCost(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 200, MinLoaded: 0},
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:       "cold-worker",
		Tags:          []string{"gpu"},
		LlamaSwapURL:  "http://cold-worker",
		Artifacts:     map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{{Model: "cold", State: "ready"}},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests+5; i++ {
		pressure.RecordRequest(PressureRequestObservation{Time: now.Add(time.Duration(i) * time.Second), Model: "hot", TotalTokens: 2000})
	}
	pressure.RecordQueue(PressureQueueObservation{Time: now.Add(10 * time.Second), Model: "hot", Result: QueueResultAdmittedAfterWait, WaitMS: 1200})

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(20 * time.Second))
	if len(actions) != 1 {
		t.Fatalf("actions = %#v, want one warm action", actions)
	}
	if actions[0].Type != ControlActionWarm || actions[0].VictimModel != "cold" || actions[0].Worker.ID != "cold-worker" {
		t.Fatalf("action = %#v, want warm hot by evicting cold", actions[0])
	}
}
```

- [ ] **Step 2: Add protected victim regression test**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementWarmDoesNotEvictProtectedReplica(t *testing.T) {
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 200, MinLoaded: 0},
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "protected",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://protected",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready", ProtectedUntil: now.Add(time.Minute)},
		},
	}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests+5; i++ {
		pressure.RecordRequest(PressureRequestObservation{Time: now.Add(time.Duration(i) * time.Second), Model: "hot", TotalTokens: 2000})
	}

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker(), Pressure: pressure}).PlanControlActions(now.Add(20 * time.Second))
	if len(actions) != 0 {
		t.Fatalf("actions = %#v, want no protected eviction", actions)
	}
}
```

- [ ] **Step 3: Run placement tests**

Run:

```bash
go test ./internal/gateway -run 'TestPlacementWarmEvictsOpportunityCacheOnlyWhenDemandBeatsSwitchCost|TestPlacementWarmDoesNotEvictProtectedReplica' -count=1
```

Expected: PASS.

- [ ] **Step 4: Add reconciler one-action-per-cycle test**

Add to `internal/gateway/reconcile_test.go`:

```go
func TestLoadedReconcilerExecutesOnlyOneWarmActionPerCycle(t *testing.T) {
	var loadA atomic.Int32
	var loadB atomic.Int32
	serverA := loadServerForModel(t, "a", &loadA)
	defer serverA.Close()
	serverB := loadServerForModel(t, "b", &loadB)
	defer serverB.Close()
	now := time.Unix(1000, 0)
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"a": {Priority: 100, MinLoaded: 0},
			"b": {Priority: 90, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu-a": {AllowedModels: []string{"a"}},
			"gpu-b": {AllowedModels: []string{"b"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "worker-a", Tags: []string{"gpu-a"}, LlamaSwapURL: serverA.URL, Artifacts: map[string]string{"a": "ready"}}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{AgentID: "worker-b", Tags: []string{"gpu-b"}, LlamaSwapURL: serverB.URL, Artifacts: map[string]string{"b": "ready"}}, now)
	pressure := NewPressureTracker(defaultPressureWindow)
	for i := 0; i < minScaleOutRequests+3; i++ {
		pressure.RecordRequest(PressureRequestObservation{Time: now.Add(time.Duration(i) * time.Second), Model: "a", TotalTokens: 2000})
		pressure.RecordRequest(PressureRequestObservation{Time: now.Add(time.Duration(i) * time.Second), Model: "b", TotalTokens: 2000})
	}

	reconciler := LoadedReconciler{Config: cfg, Workers: reg, Client: LlamaSwapClient{BearerToken: "llama-secret"}, Access: NewAccessTracker(), Pressure: pressure}
	if err := reconciler.Reconcile(context.Background(), now.Add(20*time.Second)); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if loadA.Load()+loadB.Load() != 1 {
		t.Fatalf("load calls = a:%d b:%d, want exactly one warm action", loadA.Load(), loadB.Load())
	}
}
```

- [ ] **Step 5: Run reconciler one-action test**

Run:

```bash
go test ./internal/gateway -run TestLoadedReconcilerExecutesOnlyOneWarmActionPerCycle -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/placement_test.go internal/gateway/reconcile_test.go
git commit -m "test: cover conservative warm eviction policy"
```

## Task 7: Documentation and Full Verification

**Files:**
- Modify: `docs/agents/project-map.md`

- [ ] **Step 1: Update project map**

In `docs/agents/project-map.md`, add to Gateway Modules:

```markdown
- `internal/gateway/pressure.go`
  - Tracks rolling in-memory model pressure from request and queue observations.
  - Computes conservative demand scores used by Placement warm scale-out.
  - Rolling queue pressure is not persisted and starts empty after gateway
    restart.
```

Update `internal/gateway/placement.go` entry:

```markdown
  - Plans conservative warm scale-out actions when sustained demand beats
    current replica value plus switch cost.
```

Update `internal/gateway/reconcile.go` entry:

```markdown
  - Executes at most one predictive warm action per cycle after hard ceiling and
    min_loaded capacity actions.
```

- [ ] **Step 2: Run doc sanity search**

Run:

```bash
rg -n "predictive|PressureTracker|warm scale-out|ControlActionWarm" docs internal/gateway
```

Expected: output includes the new project map text, `pressure.go`, and placement/reconciler tests.

- [ ] **Step 3: Run focused tests**

Run:

```bash
go test ./internal/gateway -run 'TestPressure|TestPlacement.*Warm|TestLoadedReconciler.*Warm|TestProxyRecordsQueueAndRequestPressure|TestLlamaSwapClientLoad' -count=1
```

Expected: PASS.

- [ ] **Step 4: Run full suite**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 5: Commit docs**

```bash
git add docs/agents/project-map.md
git commit -m "docs: document predictive warm scale-out"
```

## Task 8: Real-Machine Compatibility Check

**Files:**
- No source edits expected unless the llama-swap load endpoint needs adjustment.

- [ ] **Step 1: Build Linux binaries**

Run from a Linux build host:

```bash
go test ./...
GOOS=linux GOARCH=amd64 go build -o dist/llm-swap-gateway ./cmd/gateway
GOOS=linux GOARCH=amd64 go build -o dist/llm-swap-agent ./cmd/agent
```

Expected: tests pass and both binaries are created.

- [ ] **Step 2: Verify llama-swap load endpoint on one worker**

Run against a non-busy worker:

```bash
curl -i -X POST \
  -H "Authorization: Bearer ${LLMSWAP_LLAMA_SWAP_TOKEN}" \
  http://127.0.0.1:6006/api/models/load/qwen2.5
```

Expected: HTTP 2xx if the installed llama-swap supports this endpoint.

If the installed llama-swap returns 404, inspect `/ui` network calls or the
installed llama-swap version and update only `LlamaSwapClient.Load` plus
`TestLlamaSwapClientLoadPostsModelLoadEndpoint` to the correct endpoint.

- [ ] **Step 3: Deploy gateway only for scale-out test**

Deploy the new gateway binary and restart gateway supervisor. Do not restart
worker llama-swap unless the load endpoint test requires a llama-swap upgrade.

- [ ] **Step 4: Generate sustained pressure**

Send at least `minScaleOutRequests` requests to a model already ready on one
worker while another eligible worker is empty or running an opportunity-cache
model.

Expected gateway logs:

```text
control_action_planned action=warm
control_action_done action=warm
```

Expected worker state: the selected second worker transitions through
`loading` or `starting` and then `ready` for the target model.

- [ ] **Step 5: Restore expected worker state**

If the test loaded a temporary model, unload it through existing llama-swap
unload API:

```bash
curl -X POST \
  -H "Authorization: Bearer ${LLMSWAP_LLAMA_SWAP_TOKEN}" \
  http://127.0.0.1:6006/api/models/unload/<model>
```

Expected: workers return to the intended baseline models.

## Self-Review

Spec coverage:

- Demand signals: Tasks 1 and 2.
- Conservative scoring: Tasks 1 and 3.
- Warm control action: Tasks 3 and 5.
- Empty-worker-first selection: Task 3.
- Eviction with switch cost: Task 6.
- Request path boundary: Task 2 records only; routing remains unchanged.
- Logging and events: Task 5.
- Tests and docs: Tasks 1 through 8.

Endpoint risk:

- llama-swap README documents unload APIs and UI manual loading, but not a
  stable public load endpoint. Task 8 explicitly verifies and localizes any
  endpoint adjustment to `LlamaSwapClient.Load`.
