# Gateway Placement Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a gateway Placement module that separates request routing from async scale-out/unload policy and implements the confirmed `min_loaded` / `max_loaded` semantics.

**Architecture:** Add `internal/gateway/placement.go` as the single module for placement reasoning. Keep `Scheduler` as a compatibility adapter initially, then move request proxy and loaded-replica reconciliation to Placement. Async control actions are planned by Placement and executed by the existing reconciler loop.

**Tech Stack:** Go, existing `internal/gateway` tests, existing in-memory worker registry, existing request JSONL access accounting.

---

## Scope

This plan covers the first implementation slice from
`docs/superpowers/specs/2026-06-24-gateway-placement-runtime-design.md`:

- Placement module.
- Request routing chooses only immediately routable workers.
- Scale-out and unload intent are async control actions.
- `max_loaded` omitted means automatic capacity-bounded ceiling.
- `min_loaded=0` means opportunity cache.
- Loading/starting workers count as occupied but are not routable.

This plan does not implement runtime adapters, UI embed, or full agent reconciler
splitting. Those should be separate plans after this slice is merged.

## File Structure

- Create `internal/gateway/placement.go`
  - Owns placement state derivation, request worker selection, and async control action planning.
  - Exposes `Placement`, `PlacementDecision`, `PlacementCandidate`, and `ControlAction`.

- Create `internal/gateway/placement_test.go`
  - Focused tests for routing and async control semantics.

- Modify `internal/gateway/scheduler.go`
  - Reduce `Scheduler` to a compatibility wrapper over `Placement`.
  - Keep existing public methods `Pick` and `PickDecision` so old tests and callers keep working during migration.

- Modify `internal/gateway/proxy.go`
  - Use `Placement.PickReadyWorker` directly or through the scheduler adapter.
  - Preserve existing logging fields.

- Modify `internal/gateway/reconcile.go`
  - Use `Placement.PlanControlActions` for unload actions.
  - Keep unload execution in the reconciler.

- Modify `internal/config/config.go`
  - Change `EffectiveMaxLoaded` semantics.
  - Add explicit helper for hard ceiling vs automatic ceiling.

- Modify `internal/config/load.go`
  - Update validation for `min_loaded` and explicit `max_loaded`.

- Modify existing tests:
  - `internal/gateway/scheduler_test.go`
  - `internal/gateway/reconcile_test.go`
  - `internal/config/config_test.go`

## Task 1: Add Placement Types and Preserve Current Scheduler Behavior

**Files:**
- Create: `internal/gateway/placement.go`
- Create: `internal/gateway/placement_test.go`
- Modify: `internal/gateway/scheduler.go`

- [ ] **Step 1: Write failing placement adapter test**

Add this test to `internal/gateway/placement_test.go`:

```go
package gateway

import (
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestPlacementPickReadyWorkerMatchesSchedulerReadyPreference(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {MinLoaded: 1},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "loaded",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://loaded",
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "ready"},
		},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)

	placement := Placement{Config: cfg, Workers: reg}
	decision, err := placement.PickReadyWorker("qwen", now, nil)
	if err != nil {
		t.Fatalf("PickReadyWorker returned error: %v", err)
	}
	if decision.Worker.ID != "loaded" {
		t.Fatalf("picked worker = %q, want loaded", decision.Worker.ID)
	}
	if decision.ReadyReplicas != 1 || decision.OccupiedReplicas != 1 {
		t.Fatalf("replicas ready=%d occupied=%d, want 1/1", decision.ReadyReplicas, decision.OccupiedReplicas)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
go test ./internal/gateway -run TestPlacementPickReadyWorkerMatchesSchedulerReadyPreference -count=1
```

Expected: FAIL because `Placement` is undefined.

- [ ] **Step 3: Add initial Placement module**

Create `internal/gateway/placement.go`:

```go
package gateway

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"llm-swap/internal/config"
)

type Placement struct {
	Config  config.GatewayConfig
	Workers *WorkerRegistry
	Access  *AccessTracker
}

type PlacementDecision struct {
	Worker           Worker
	Reason           string
	ReadyReplicas    int
	OccupiedReplicas int
	MaxLoaded        int
	MaxLoadedAuto    bool
	Candidates       []PlacementCandidate
}

type PlacementCandidate struct {
	WorkerID       string `json:"worker_id"`
	Reason         string `json:"reason"`
	Score          int    `json:"score"`
	ActiveRequests int    `json:"active_requests"`
	RunningState   string `json:"running_state,omitempty"`
	RunningModels  int    `json:"running_models"`
}

func (p Placement) PickReadyWorker(model string, now time.Time, exclude map[string]bool) (PlacementDecision, error) {
	modelCfg, ok := p.Config.Models[model]
	if !ok {
		return PlacementDecision{}, fmt.Errorf("unknown model %q", model)
	}
	if p.Workers == nil {
		return PlacementDecision{}, fmt.Errorf("no healthy worker for model %q", model)
	}

	workers := p.Workers.Snapshot(now)
	active := p.Workers.ActiveSnapshot()
	readyCount := 0
	occupiedCount := 0
	for _, worker := range workers {
		if !p.Workers.Healthy(worker.ID, now) {
			continue
		}
		if !workerAllowsModel(p.Config, worker, model) || !artifactReady(worker, model) {
			continue
		}
		state, running := runningModelState(worker, model)
		if running {
			occupiedCount++
		}
		if strings.EqualFold(state, "ready") {
			readyCount++
		}
	}

	maxLoaded, maxLoadedAuto := effectivePlacementMaxLoaded(modelCfg, workers, model, p.Config, p.Workers, now)
	canScaleOut := maxLoaded > 0 && occupiedCount < maxLoaded

	candidates := make([]scoredPlacementWorker, 0)
	for _, worker := range workers {
		if exclude != nil && exclude[worker.ID] {
			continue
		}
		if !p.Workers.Healthy(worker.ID, now) {
			continue
		}
		if !workerAllowsModel(p.Config, worker, model) || !artifactReady(worker, model) {
			continue
		}
		state, running := runningModelState(worker, model)
		if running && !strings.EqualFold(state, "ready") {
			continue
		}
		if readyCount > 0 && !running {
			continue
		}
		score, reason := scoreScheduleCandidate(worker, state, running, canScaleOut, readyCount > 0, readyCount, active[worker.ID])
		candidates = append(candidates, scoredPlacementWorker{
			worker:         worker,
			score:          score,
			reason:         reason,
			activeRequests: active[worker.ID],
			runningState:   state,
		})
	}
	if len(candidates) == 0 {
		if occupiedCount > 0 || readyCount > 0 {
			return PlacementDecision{
				ReadyReplicas:    readyCount,
				OccupiedReplicas: occupiedCount,
				MaxLoaded:        maxLoaded,
				MaxLoadedAuto:    maxLoadedAuto,
			}, fmt.Errorf("no ready worker for model %q", model)
		}
		return PlacementDecision{}, fmt.Errorf("no healthy worker for model %q", model)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].worker.ID < candidates[j].worker.ID
	})
	picked := candidates[0]
	return PlacementDecision{
		Worker:           picked.worker,
		Reason:           picked.reason,
		ReadyReplicas:    readyCount,
		OccupiedReplicas: occupiedCount,
		MaxLoaded:        maxLoaded,
		MaxLoadedAuto:    maxLoadedAuto,
		Candidates:       placementCandidates(candidates),
	}, nil
}

type scoredPlacementWorker struct {
	worker         Worker
	score          int
	reason         string
	activeRequests int
	runningState   string
}

func placementCandidates(scored []scoredPlacementWorker) []PlacementCandidate {
	out := make([]PlacementCandidate, 0, len(scored))
	for _, candidate := range scored {
		out = append(out, PlacementCandidate{
			WorkerID:       candidate.worker.ID,
			Reason:         candidate.reason,
			Score:          candidate.score,
			ActiveRequests: candidate.activeRequests,
			RunningState:   candidate.runningState,
			RunningModels:  len(candidate.worker.RunningModels),
		})
	}
	return out
}

func effectivePlacementMaxLoaded(model config.Model, workers []Worker, modelName string, cfg config.GatewayConfig, reg *WorkerRegistry, now time.Time) (int, bool) {
	if model.MaxLoadedSet || model.MaxLoaded > 0 {
		return model.MaxLoaded, false
	}
	count := 0
	for _, worker := range workers {
		if reg != nil && !reg.Healthy(worker.ID, now) {
			continue
		}
		if workerAllowsModel(cfg, worker, modelName) && artifactReady(worker, modelName) {
			count++
		}
	}
	return count, true
}
```

- [ ] **Step 4: Update Scheduler to wrap Placement**

Modify `internal/gateway/scheduler.go` so `PickDecision` delegates to Placement and converts types:

```go
func (s Scheduler) PickDecision(model string, now time.Time, exclude map[string]bool) (ScheduleDecision, error) {
	decision, err := (Placement{Config: s.Config, Workers: s.Workers, Access: s.Access}).PickReadyWorker(model, now, exclude)
	if err != nil {
		return ScheduleDecision{
			ReadyReplicas:    decision.ReadyReplicas,
			OccupiedReplicas: decision.OccupiedReplicas,
			MaxLoaded:        decision.MaxLoaded,
			Candidates:       scheduleCandidatesFromPlacement(decision.Candidates),
		}, err
	}
	return ScheduleDecision{
		Worker:           decision.Worker,
		Reason:           decision.Reason,
		ReadyReplicas:    decision.ReadyReplicas,
		OccupiedReplicas: decision.OccupiedReplicas,
		MaxLoaded:        decision.MaxLoaded,
		Candidates:       scheduleCandidatesFromPlacement(decision.Candidates),
	}, nil
}

func scheduleCandidatesFromPlacement(in []PlacementCandidate) []ScheduleCandidate {
	out := make([]ScheduleCandidate, 0, len(in))
	for _, candidate := range in {
		out = append(out, ScheduleCandidate{
			WorkerID:       candidate.WorkerID,
			Reason:         candidate.Reason,
			Score:          candidate.Score,
			ActiveRequests: candidate.ActiveRequests,
			RunningState:   candidate.RunningState,
			RunningModels:  candidate.RunningModels,
		})
	}
	return out
}
```

Remove duplicated selection logic from `PickDecision`, but keep helper functions
like `workerAllowsModel`, `runningModelState`, and `artifactReady` available in
`scheduler.go` for now.

- [ ] **Step 5: Run tests**

Run:

```bash
go test ./internal/gateway -run 'TestPlacementPickReadyWorkerMatchesSchedulerReadyPreference|TestScheduler' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/placement.go internal/gateway/placement_test.go internal/gateway/scheduler.go
git commit -m "refactor: introduce gateway placement module"
```

## Task 2: Make Starting and Loading Non-Routable but Occupied

**Files:**
- Modify: `internal/gateway/placement_test.go`
- Modify: `internal/gateway/placement.go`

- [ ] **Step 1: Write failing test for loading replica at ceiling**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementCountsLoadingReplicaAsOccupiedButNotRoutable(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {MinLoaded: 0, MaxLoaded: 1, MaxLoadedSet: true},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "loading",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://loading",
		Artifacts:    map[string]string{"qwen": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "qwen", State: "loading"},
		},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "empty",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://empty",
		Artifacts:    map[string]string{"qwen": "ready"},
	}, now)

	decision, err := (Placement{Config: cfg, Workers: reg}).PickReadyWorker("qwen", now, nil)
	if err == nil {
		t.Fatalf("PickReadyWorker error = nil, want no ready worker")
	}
	if decision.OccupiedReplicas != 1 {
		t.Fatalf("occupied replicas = %d, want 1", decision.OccupiedReplicas)
	}
	if decision.ReadyReplicas != 0 {
		t.Fatalf("ready replicas = %d, want 0", decision.ReadyReplicas)
	}
}
```

- [ ] **Step 2: Run the test and verify it fails if current code routes cold worker**

Run:

```bash
go test ./internal/gateway -run TestPlacementCountsLoadingReplicaAsOccupiedButNotRoutable -count=1
```

Expected: FAIL if the empty worker is selected while a loading replica already
occupies the explicit ceiling.

- [ ] **Step 3: Implement occupied ceiling guard**

In `Placement.PickReadyWorker`, add this after calculating `maxLoaded`:

```go
loadingAtCeiling := maxLoaded > 0 && readyCount == 0 && occupiedCount >= maxLoaded
```

Then keep this candidate rejection:

```go
if loadingAtCeiling && !running {
	continue
}
```

Ensure non-ready same-model runtimes are still skipped:

```go
if running && !strings.EqualFold(state, "ready") {
	continue
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
go test ./internal/gateway -run 'TestPlacementCountsLoadingReplicaAsOccupiedButNotRoutable|TestSchedulerDoesNotRouteOrDuplicateColdStartWhenSameModelIsLoadingAtMaxLoaded' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/placement.go internal/gateway/placement_test.go
git commit -m "fix: keep loading replicas non-routable but occupied"
```

## Task 3: Change Omitted max_loaded to Automatic Ceiling

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/gateway/placement_test.go`
- Modify: `internal/gateway/scheduler_test.go`

- [ ] **Step 1: Replace config expectation test**

In `internal/config/config_test.go`, replace the old omitted max_loaded test with:

```go
func TestLoadGatewayConfigTreatsMissingMaxLoadedAsAutomatic(t *testing.T) {
	raw := `
oss:
  base_url: https://example.com
tokens:
  client: c
  agent: a
models:
  qwen:
    min_loaded: 1
    artifact:
      object: models/qwen.tar.gz
      kind: tar_gz
      crc64ecma: "1"
    run: echo qwen
tag_policies:
  gpu:
    allowed_models: [qwen]
`
	cfg, err := LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("LoadGateway returned error: %v", err)
	}
	if cfg.Models["qwen"].MaxLoadedSet {
		t.Fatalf("MaxLoadedSet = true, want false")
	}
	if cfg.Models["qwen"].HardMaxLoaded() != 0 {
		t.Fatalf("HardMaxLoaded = %d, want 0 for automatic", cfg.Models["qwen"].HardMaxLoaded())
	}
}
```

- [ ] **Step 2: Add explicit max_loaded validation test**

Add to `internal/config/config_test.go`:

```go
func TestLoadGatewayConfigRejectsExplicitMaxLoadedBelowMinLoaded(t *testing.T) {
	raw := `
oss:
  base_url: https://example.com
tokens:
  client: c
  agent: a
models:
  qwen:
    min_loaded: 2
    max_loaded: 1
    artifact:
      object: models/qwen.tar.gz
      kind: tar_gz
      crc64ecma: "1"
    run: echo qwen
tag_policies:
  gpu:
    allowed_models: [qwen]
`
	_, err := LoadGateway(strings.NewReader(raw))
	if err == nil {
		t.Fatalf("LoadGateway error = nil, want max_loaded validation error")
	}
	if !strings.Contains(err.Error(), "min_loaded cannot exceed max_loaded") {
		t.Fatalf("error = %v, want min_loaded/max_loaded validation", err)
	}
}
```

- [ ] **Step 3: Run config tests and verify failure**

Run:

```bash
go test ./internal/config -run 'TestLoadGatewayConfigTreatsMissingMaxLoadedAsAutomatic|TestLoadGatewayConfigRejectsExplicitMaxLoadedBelowMinLoaded' -count=1
```

Expected: FAIL because `HardMaxLoaded` is undefined and validation still uses
`EffectiveMaxLoaded`.

- [ ] **Step 4: Implement config helpers**

Modify `internal/config/config.go`:

```go
func (m Model) HardMaxLoaded() int {
	if m.MaxLoadedSet || m.MaxLoaded > 0 {
		return m.MaxLoaded
	}
	return 0
}

func (m Model) MaxLoadedIsAuto() bool {
	return !m.MaxLoadedSet && m.MaxLoaded == 0
}

func (m Model) EffectiveMaxLoaded() int {
	return m.HardMaxLoaded()
}
```

This keeps old callers compiling while changing the meaning: `0` now means
automatic/no hard ceiling for placement, not `min_loaded`.

- [ ] **Step 5: Update validation**

Modify `internal/config/load.go`:

```go
if model.MaxLoadedSet && model.MinLoaded > model.MaxLoaded {
	return fmt.Errorf("model %s min_loaded cannot exceed max_loaded", name)
}
```

Do not reject `min_loaded > 0` when `max_loaded` is omitted.

- [ ] **Step 6: Add placement auto ceiling test**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementMissingMaxLoadedUsesEligibleWorkerCountAsAutoCeiling(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"qwen": {MinLoaded: 1},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"qwen"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	for _, id := range []string{"a", "b", "c"} {
		reg.UpsertHeartbeat(protocol.HeartbeatRequest{
			AgentID:      id,
			Tags:         []string{"gpu"},
			LlamaSwapURL: "http://" + id,
			Artifacts:    map[string]string{"qwen": "ready"},
		}, now)
	}

	decision, err := (Placement{Config: cfg, Workers: reg}).PickReadyWorker("qwen", now, nil)
	if err != nil {
		t.Fatalf("PickReadyWorker returned error: %v", err)
	}
	if decision.MaxLoaded != 3 {
		t.Fatalf("MaxLoaded = %d, want 3 eligible workers", decision.MaxLoaded)
	}
	if !decision.MaxLoadedAuto {
		t.Fatalf("MaxLoadedAuto = false, want true")
	}
}
```

- [ ] **Step 7: Update old scheduler test expectation**

Find `TestSchedulerUsesMinLoadedAsDefaultMaxLoaded` in
`internal/gateway/scheduler_test.go`. Rename it to:

```go
func TestSchedulerUsesAutoMaxLoadedWhenMaxLoadedIsMissing(t *testing.T)
```

Update assertions so omitted `max_loaded` expects the eligible worker count
rather than `min_loaded`.

- [ ] **Step 8: Run tests**

Run:

```bash
go test ./internal/config ./internal/gateway -run 'TestLoadGatewayConfigTreatsMissingMaxLoadedAsAutomatic|TestLoadGatewayConfigRejectsExplicitMaxLoadedBelowMinLoaded|TestPlacementMissingMaxLoadedUsesEligibleWorkerCountAsAutoCeiling|TestSchedulerUsesAutoMaxLoadedWhenMaxLoadedIsMissing' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/config/config.go internal/config/load.go internal/config/config_test.go internal/gateway/placement.go internal/gateway/placement_test.go internal/gateway/scheduler_test.go
git commit -m "feat: make omitted max_loaded automatic"
```

## Task 4: Add ControlAction Planning for Unload Decisions

**Files:**
- Modify: `internal/gateway/placement.go`
- Modify: `internal/gateway/placement_test.go`
- Modify: `internal/gateway/reconcile.go`
- Modify: `internal/gateway/reconcile_test.go`

- [ ] **Step 1: Add tests for opportunity-cache eviction order**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementPlanControlActionsEvictsMinLoadedZeroBeforeProtectedFloor(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 100, MinLoaded: 1},
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "hot-worker",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://hot-worker",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "hot", State: "ready"},
		},
	}, now)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "cold-worker",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://cold-worker",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready"},
		},
	}, now)

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker()}).PlanControlActions(now)
	if len(actions) == 0 {
		t.Fatalf("PlanControlActions returned no actions, want cold unload")
	}
	if actions[0].Type != ControlActionUnload || actions[0].Worker.ID != "cold-worker" || actions[0].Model != "cold" {
		t.Fatalf("first action = %#v, want unload cold from cold-worker", actions[0])
	}
}
```

- [ ] **Step 2: Run test and verify failure**

Run:

```bash
go test ./internal/gateway -run TestPlacementPlanControlActionsEvictsMinLoadedZeroBeforeProtectedFloor -count=1
```

Expected: FAIL because `PlanControlActions` and `ControlAction` are undefined.

- [ ] **Step 3: Add ControlAction types**

Add to `internal/gateway/placement.go`:

```go
type ControlActionType string

const (
	ControlActionUnload ControlActionType = "unload"
)

type ControlAction struct {
	Type   ControlActionType
	Worker Worker
	Model  string
	Reason string
}
```

- [ ] **Step 4: Implement initial PlanControlActions**

Add to `internal/gateway/placement.go`:

```go
func (p Placement) PlanControlActions(now time.Time) []ControlAction {
	if p.Workers == nil {
		return nil
	}
	workers := p.Workers.Snapshot(now)
	active := p.Workers.ActiveSnapshot()
	loadedCounts := runningModelCounts(workers, now, p.Workers)

	for modelName, model := range p.Config.Models {
		if model.MinLoaded <= 0 {
			continue
		}
		if loadedCounts[modelName] >= model.MinLoaded {
			continue
		}
		victim, victimModel, ok := p.pickEvictionVictimForModel(workers, active, loadedCounts, modelName)
		if !ok {
			continue
		}
		return []ControlAction{{
			Type:   ControlActionUnload,
			Worker: victim,
			Model:  victimModel,
			Reason: "free_capacity_for_min_loaded",
		}}
	}
	return nil
}

func (p Placement) pickEvictionVictimForModel(workers []Worker, active map[string]int, loadedCounts map[string]int, targetModel string) (Worker, string, bool) {
	var bestWorker Worker
	var bestModel string
	var bestRank evictionRank
	found := false
	for _, worker := range workers {
		if active[worker.ID] > 0 {
			continue
		}
		if runningModelReady(worker, targetModel) {
			continue
		}
		if !workerAllowsModel(p.Config, worker, targetModel) || !artifactReady(worker, targetModel) {
			continue
		}
		for _, running := range worker.RunningModels {
			if !strings.EqualFold(running.State, "ready") || running.Model == targetModel {
				continue
			}
			if !p.canUnloadModelForPlacement(running.Model, loadedCounts) {
				continue
			}
			rank := p.evictionRank(worker.ID, running.Model, loadedCounts)
			if !found || rank.less(bestRank) {
				bestWorker = worker
				bestModel = running.Model
				bestRank = rank
				found = true
			}
		}
	}
	return bestWorker, bestModel, found
}

type evictionRank struct {
	minLoadedZero bool
	priority      int
	lastAccess    time.Time
	workerID      string
}

func (r evictionRank) less(other evictionRank) bool {
	if r.minLoadedZero != other.minLoadedZero {
		return r.minLoadedZero
	}
	if r.priority != other.priority {
		return r.priority < other.priority
	}
	if !r.lastAccess.Equal(other.lastAccess) {
		return r.lastAccess.Before(other.lastAccess)
	}
	return r.workerID < other.workerID
}

func (p Placement) evictionRank(workerID string, modelName string, loadedCounts map[string]int) evictionRank {
	model := p.Config.Models[modelName]
	last := time.Time{}
	if p.Access != nil {
		last = p.Access.WorkerModelLastAccess(workerID, modelName)
	}
	return evictionRank{
		minLoadedZero: model.MinLoaded == 0,
		priority:      model.Priority,
		lastAccess:    last,
		workerID:      workerID,
	}
}

func (p Placement) canUnloadModelForPlacement(modelName string, loadedCounts map[string]int) bool {
	model, ok := p.Config.Models[modelName]
	if !ok {
		return true
	}
	return loadedCounts[modelName] > model.MinLoaded || model.MinLoaded == 0
}
```

- [ ] **Step 5: Run placement control tests**

Run:

```bash
go test ./internal/gateway -run TestPlacementPlanControlActionsEvictsMinLoadedZeroBeforeProtectedFloor -count=1
```

Expected: PASS.

- [ ] **Step 6: Wire reconciler to PlanControlActions**

Modify `LoadedReconciler.Reconcile` in `internal/gateway/reconcile.go` so after
the explicit hard-ceiling unload pass it executes Placement actions:

```go
placement := Placement{Config: r.Config, Workers: r.Workers, Access: r.Access}
for _, action := range placement.PlanControlActions(now) {
	if action.Type != ControlActionUnload {
		continue
	}
	if active[action.Worker.ID] > 0 {
		continue
	}
	if err := r.Client.Unload(ctx, action.Worker.LlamaSwapURL, action.Model); err != nil {
		r.recordUnloadEvent(action.Worker.ID, action.Model, "gateway_model_unload_error", err)
		outErr = errors.Join(outErr, err)
		continue
	}
	r.recordUnloadEvent(action.Worker.ID, action.Model, "gateway_model_unload_done", nil)
}
```

Keep the existing excess-over-hard-max unload behavior, but stop calling
`unloadColdModelsForUnderloadedHotModels` directly after the new control action
path is in place.

- [ ] **Step 7: Run reconciler tests**

Run:

```bash
go test ./internal/gateway -run 'TestLoadedReconciler' -count=1
```

Expected: PASS. If old tests assert the exact helper path, update them to assert
observable unload behavior instead.

- [ ] **Step 8: Commit**

```bash
git add internal/gateway/placement.go internal/gateway/placement_test.go internal/gateway/reconcile.go internal/gateway/reconcile_test.go
git commit -m "refactor: plan replica control actions through placement"
```

## Task 5: Preserve min_loaded=0 Models Unless Capacity Is Needed

**Files:**
- Modify: `internal/gateway/placement_test.go`
- Modify: `internal/gateway/reconcile_test.go`
- Modify: `internal/gateway/placement.go`
- Modify: `internal/gateway/reconcile.go`

- [ ] **Step 1: Add regression test for no opportunistic idle unload**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementDoesNotUnloadMinLoadedZeroWhenNoCapacityIsNeeded(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "worker-a",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://worker-a",
		Artifacts:    map[string]string{"cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready"},
		},
	}, now)

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker()}).PlanControlActions(now)
	if len(actions) != 0 {
		t.Fatalf("PlanControlActions returned %#v, want no unload while capacity is not needed", actions)
	}
}
```

- [ ] **Step 2: Run test**

Run:

```bash
go test ./internal/gateway -run TestPlacementDoesNotUnloadMinLoadedZeroWhenNoCapacityIsNeeded -count=1
```

Expected: PASS with the Task 4 implementation because actions are only created
when a protected model needs capacity. If it fails, remove any unconditional
`min_loaded=0` unload path.

- [ ] **Step 3: Add reconciler regression test**

Add to `internal/gateway/reconcile_test.go`:

```go
func TestLoadedReconcilerKeepsOpportunityCacheWhenNoModelNeedsCapacity(t *testing.T) {
	now := time.Now()
	var unloadCold atomic.Int32
	coldServer := unloadServerForModel(t, "cold", &unloadCold)
	defer coldServer.Close()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"cold": {MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "worker-a",
		Tags:         []string{"gpu"},
		LlamaSwapURL: coldServer.URL,
		Artifacts:    map[string]string{"cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready"},
		},
	}, now)
	reconciler := LoadedReconciler{
		Config:  cfg,
		Workers: reg,
		Client:  LlamaSwapClient{BearerToken: "llama-secret"},
		Access:  NewAccessTracker(),
	}

	if err := reconciler.Reconcile(context.Background(), now); err != nil {
		t.Fatalf("Reconcile returned error: %v", err)
	}
	if unloadCold.Load() != 0 {
		t.Fatalf("cold unload calls = %d, want 0", unloadCold.Load())
	}
}
```

- [ ] **Step 4: Run reconciler test**

Run:

```bash
go test ./internal/gateway -run TestLoadedReconcilerKeepsOpportunityCacheWhenNoModelNeedsCapacity -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gateway/placement_test.go internal/gateway/reconcile_test.go internal/gateway/placement.go internal/gateway/reconcile.go
git commit -m "fix: keep opportunity cache until capacity is needed"
```

## Task 6: Add Replica Protection Metadata Hook

**Files:**
- Modify: `internal/gateway/workers.go`
- Modify: `internal/gateway/workers_test.go`
- Modify: `internal/gateway/placement.go`
- Modify: `internal/gateway/placement_test.go`
- Modify: `internal/protocol/agent.go`

- [ ] **Step 1: Add protection fields to protocol running model**

Modify `internal/protocol/agent.go` `RunningModel` with:

```go
ProtectedUntil time.Time `json:"protected_until,omitempty"`
```

If `RunningModel` does not currently import `time`, add:

```go
import "time"
```

- [ ] **Step 2: Add placement test for protected replica**

Add to `internal/gateway/placement_test.go`:

```go
func TestPlacementDoesNotEvictProtectedReplica(t *testing.T) {
	now := time.Now()
	cfg := config.GatewayConfig{
		Models: map[string]config.Model{
			"hot":  {Priority: 100, MinLoaded: 1},
			"cold": {Priority: 10, MinLoaded: 0},
		},
		TagPolicies: map[string]config.TagPolicy{
			"gpu": {AllowedModels: []string{"hot", "cold"}},
		},
	}
	reg := NewWorkerRegistry(time.Minute)
	reg.UpsertHeartbeat(protocol.HeartbeatRequest{
		AgentID:      "protected-cold",
		Tags:         []string{"gpu"},
		LlamaSwapURL: "http://protected-cold",
		Artifacts:    map[string]string{"hot": "ready", "cold": "ready"},
		RunningModels: []protocol.RunningModel{
			{Model: "cold", State: "ready", ProtectedUntil: now.Add(time.Minute)},
		},
	}, now)

	actions := (Placement{Config: cfg, Workers: reg, Access: NewAccessTracker()}).PlanControlActions(now)
	if len(actions) != 0 {
		t.Fatalf("PlanControlActions returned %#v, want no eviction of protected replica", actions)
	}
}
```

- [ ] **Step 3: Run test and verify failure**

Run:

```bash
go test ./internal/gateway -run TestPlacementDoesNotEvictProtectedReplica -count=1
```

Expected: FAIL until eviction checks `ProtectedUntil`.

- [ ] **Step 4: Implement protection check**

In `Placement.pickEvictionVictimForModel`, before ranking a running model, add:

```go
if !running.ProtectedUntil.IsZero() && running.ProtectedUntil.After(time.Now()) {
	continue
}
```

Then change it to use the `now` argument instead of `time.Now()` by passing
`now` through from `PlanControlActions`:

```go
victim, victimModel, ok := p.pickEvictionVictimForModel(now, workers, active, loadedCounts, modelName)
```

and:

```go
func (p Placement) pickEvictionVictimForModel(now time.Time, workers []Worker, active map[string]int, loadedCounts map[string]int, targetModel string) (Worker, string, bool)
```

Use:

```go
if !running.ProtectedUntil.IsZero() && running.ProtectedUntil.After(now) {
	continue
}
```

- [ ] **Step 5: Run protocol and gateway tests**

Run:

```bash
go test ./internal/protocol ./internal/gateway -run 'TestPlacementDoesNotEvictProtectedReplica|TestHeartbeat' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/protocol/agent.go internal/gateway/placement.go internal/gateway/placement_test.go internal/gateway/workers.go internal/gateway/workers_test.go
git commit -m "feat: protect newly started replicas from eviction"
```

## Task 7: Update Proxy Logs to Use Placement Fields

**Files:**
- Modify: `internal/gateway/proxy.go`
- Modify: `internal/gateway/proxy_test.go`

- [ ] **Step 1: Add proxy log assertion for auto max_loaded**

In `internal/gateway/proxy_test.go`, extend the existing scheduler decision log
test or add:

```go
func TestProxySchedulerDecisionLogIncludesAutoMaxLoaded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	}))
	defer upstream.Close()

	cfg := testProxyConfig()
	model := cfg.Models["qwen"]
	model.MinLoaded = 1
	model.MaxLoaded = 0
	model.MaxLoadedSet = false
	cfg.Models["qwen"] = model
	srv := NewServer(cfg)
	var logs bytes.Buffer
	srv.logger = log.New(&logs, "", 0)
	registerProxyWorker(t, srv, "worker-a", upstream.URL, true)
	registerProxyWorker(t, srv, "worker-b", upstream.URL, false)

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, proxyRequest(`{"model":"qwen","messages":[]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rr.Code, http.StatusOK, rr.Body.String())
	}

	logText := logs.String()
	for _, want := range []string{`"event":"scheduler_decision"`, `"max_loaded":2`, `"max_loaded_auto":true`} {
		if !strings.Contains(logText, want) {
			t.Fatalf("logs missing %s:\n%s", want, logText)
		}
	}
}
```

- [ ] **Step 2: Run test and verify failure**

Run:

```bash
go test ./internal/gateway -run TestProxySchedulerDecisionLogIncludesAutoMaxLoaded -count=1
```

Expected: FAIL because the log does not include `max_loaded_auto`.

- [ ] **Step 3: Add MaxLoadedAuto to ScheduleDecision compatibility type**

Modify `internal/gateway/scheduler.go`:

```go
type ScheduleDecision struct {
	Worker           Worker
	Reason           string
	ReadyReplicas    int
	OccupiedReplicas int
	MaxLoaded        int
	MaxLoadedAuto    bool
	Candidates       []ScheduleCandidate
}
```

Set `MaxLoadedAuto` when converting from Placement.

- [ ] **Step 4: Include field in proxy logs**

Modify the `scheduler_decision` log fields in `internal/gateway/proxy.go`:

```go
"max_loaded_auto":   decision.MaxLoadedAuto,
```

- [ ] **Step 5: Run proxy test**

Run:

```bash
go test ./internal/gateway -run TestProxySchedulerDecisionLogIncludesAutoMaxLoaded -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gateway/scheduler.go internal/gateway/proxy.go internal/gateway/proxy_test.go
git commit -m "chore: log automatic max_loaded decisions"
```

## Task 8: Update Documentation and Project Map

**Files:**
- Modify: `docs/agents/project-map.md`
- Modify: `examples/gateway.yaml`

- [ ] **Step 1: Update project map semantics**

In `docs/agents/project-map.md`, update the Domain Vocabulary and Gateway
Modules sections to say:

```markdown
- `min_loaded`: target floor for ready replicas. The async control loop tries
  to satisfy it when capacity allows.
- `max_loaded`: optional hard ceiling. When omitted, Placement treats the
  ceiling as automatic and bounded by eligible workers, other models'
  `min_loaded`, and priority protection.
- `min_loaded=0`: opportunity-cache model. It is not proactively protected, but
  loaded replicas can remain while capacity is spare and are preferred eviction
  candidates when another model needs capacity.
```

Add a `Placement` entry:

```markdown
- `internal/gateway/placement.go`
  - Owns request placement and async control-action planning.
  - Request placement only returns workers that can handle the current request.
  - Starting/loading runtimes count as occupied but are not routable.
```

- [ ] **Step 2: Update example config comments**

In `examples/gateway.yaml`, add comments near `max_loaded`:

```yaml
    # Optional hard ceiling. When omitted, gateway uses automatic expansion
    # bounded by eligible workers and protected model floors.
    max_loaded: 1
```

- [ ] **Step 3: Run doc sanity search**

Run:

```bash
rg -n "max_loaded.*min_loaded|default.*min_loaded|omitted.*min_loaded" docs examples internal
```

Expected: No stale statement says omitted `max_loaded` equals `min_loaded`.

- [ ] **Step 4: Commit**

```bash
git add docs/agents/project-map.md examples/gateway.yaml
git commit -m "docs: document placement replica semantics"
```

## Task 9: Full Verification

**Files:**
- No code edits expected.

- [ ] **Step 1: Run gateway tests**

Run:

```bash
go test ./internal/gateway -count=1
```

Expected: PASS.

- [ ] **Step 2: Run config and protocol tests**

Run:

```bash
go test ./internal/config ./internal/protocol -count=1
```

Expected: PASS.

- [ ] **Step 3: Run full suite**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Commit final fixups if needed**

If verification required small fixes:

```bash
git status --short
git add internal/gateway/placement.go internal/gateway/placement_test.go internal/gateway/scheduler.go internal/gateway/proxy.go internal/gateway/proxy_test.go internal/gateway/reconcile.go internal/gateway/reconcile_test.go internal/gateway/workers.go internal/gateway/workers_test.go internal/config/config.go internal/config/load.go internal/config/config_test.go internal/protocol/agent.go docs/agents/project-map.md examples/gateway.yaml
git commit -m "test: stabilize placement phase one"
```

If no fixes were needed, do not create an empty commit.

## Task 10: Deployment Notes

**Files:**
- Modify: `docs/agents/project-map.md`

- [ ] **Step 1: Add rollout note**

Add this to `docs/agents/project-map.md`:

```markdown
## Placement Rollout Notes

- Requests route only to ready workers for the requested model.
- Starting/loading workers are visible as occupied replicas but do not receive
  current requests.
- Omitted `max_loaded` now means automatic expansion rather than `min_loaded`.
  Use explicit `max_loaded` to cap expensive models.
- `min_loaded=0` models behave as opportunity cache and can remain loaded until
  capacity is needed elsewhere.
```

- [ ] **Step 2: Commit rollout note**

```bash
git add docs/agents/project-map.md
git commit -m "docs: add placement rollout notes"
```

## Execution Order

Implement tasks in order. Do not start runtime adapters, UI embed, or agent
reconciler splitting until Task 9 passes. Those are separate implementation
plans.

## Self-Review

Spec coverage:

- Placement module: Tasks 1, 2, 4.
- Request routing vs async control: Tasks 1, 2, 4, 7.
- `max_loaded` automatic semantics: Task 3.
- `min_loaded=0` opportunity cache: Tasks 4, 5.
- Newly started replica protection hook: Task 6.
- Docs and rollout: Tasks 8, 10.

Known follow-up plans:

- Runtime config adapters and runtime-based request normalization.
- UI static asset embed.
- Agent reconciler internal split.
