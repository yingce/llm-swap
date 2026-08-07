package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"llm-swap/internal/config"
)

func TestFileConfigRevisionStoreSurvivesRestartAndHotApply(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config-revision.json")
	store := NewFileConfigRevisionStore(path)

	first, err := NewConfigManagerWithRevisionStore(ctx, testUIGatewayConfig(), "", store)
	if err != nil {
		t.Fatalf("start first config manager: %v", err)
	}
	_, firstRevision := first.Snapshot()
	if firstRevision != 1 {
		t.Fatalf("first startup revision = %d, want 1", firstRevision)
	}

	firstApply, err := first.Apply([]byte(testGatewayYAMLWithModels("qwen", "other")))
	if err != nil {
		t.Fatalf("first hot apply: %v", err)
	}
	if firstApply.RequiresGatewayRestart {
		t.Fatalf("first apply = %+v, want hot apply", firstApply)
	}
	if firstApply.Version != 2 {
		t.Fatalf("first hot-apply revision = %d, want 2", firstApply.Version)
	}

	appliedConfig, _ := first.Snapshot()
	second, err := NewConfigManagerWithRevisionStore(ctx, appliedConfig, "", NewFileConfigRevisionStore(path))
	if err != nil {
		t.Fatalf("start second config manager: %v", err)
	}
	_, secondRevision := second.Snapshot()
	if secondRevision != 3 {
		t.Fatalf("second startup revision = %d, want 3", secondRevision)
	}

	secondApply, err := second.Apply([]byte(testGatewayYAMLWithModels("qwen")))
	if err != nil {
		t.Fatalf("second hot apply: %v", err)
	}
	if secondApply.RequiresGatewayRestart {
		t.Fatalf("second apply = %+v, want hot apply", secondApply)
	}
	if secondApply.Version != 4 {
		t.Fatalf("second hot-apply revision = %d, want 4", secondApply.Version)
	}
}

func TestFileConfigRevisionStoreSerializesIndependentInstances(t *testing.T) {
	const allocations = 24
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config-revision.json")
	start := make(chan struct{})
	revisions := make(chan int64, allocations)
	errorsSeen := make(chan error, allocations)
	var ready sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(allocations)
	done.Add(allocations)
	for range allocations {
		go func() {
			defer done.Done()
			store := NewFileConfigRevisionStore(path)
			ready.Done()
			<-start
			revision, err := store.Allocate(ctx)
			if err != nil {
				errorsSeen <- err
				return
			}
			revisions <- revision
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	close(errorsSeen)
	close(revisions)
	for err := range errorsSeen {
		t.Errorf("allocate revision: %v", err)
	}
	got := make([]int, 0, allocations)
	for revision := range revisions {
		got = append(got, int(revision))
	}
	sort.Ints(got)
	if len(got) != allocations {
		t.Fatalf("successful allocations = %d, want %d", len(got), allocations)
	}
	for i, revision := range got {
		if want := i + 1; revision != want {
			t.Fatalf("sorted revisions = %v, want every value from 1 through %d", got, allocations)
		}
	}
}

func TestHotApplyRevisionFailureDoesNotPublishOrPersistConfig(t *testing.T) {
	const allocationFailure = "revision backend unavailable"
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	raw := testGatewayYAMLWithModels("qwen")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadGateway(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewConfigManagerWithRevisionStore(context.Background(), cfg, path, &failAfterStartupRevisionStore{err: errors.New(allocationFailure)})
	if err != nil {
		t.Fatalf("start config manager: %v", err)
	}

	_, err = manager.Apply([]byte(testGatewayYAMLWithModels("qwen", "other")))
	if err == nil || !strings.Contains(err.Error(), allocationFailure) {
		t.Fatalf("hot apply error = %v, want %q", err, allocationFailure)
	}
	current, revision := manager.Snapshot()
	if revision != 1 {
		t.Fatalf("runtime revision = %d, want unchanged 1", revision)
	}
	if _, exists := current.Models["other"]; exists {
		t.Fatalf("runtime models = %+v, want failed hot apply unpublished", current.Models)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != raw {
		t.Fatalf("persisted config changed after revision failure")
	}
}

func TestServerStartupPublishesRevisionFromConfiguredStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config-revision.json")
	first, err := NewServerWithConfigRevisionStore(
		context.Background(), testUIGatewayConfig(), "", "", "", config.GatewayRuntimeOverrides{}, NewFileConfigRevisionStore(path),
	)
	if err != nil {
		t.Fatalf("start first server: %v", err)
	}
	_, firstRevision := first.configManager.Snapshot()

	second, err := NewServerWithConfigRevisionStore(
		context.Background(), testUIGatewayConfig(), "", "", "", config.GatewayRuntimeOverrides{}, NewFileConfigRevisionStore(path),
	)
	if err != nil {
		t.Fatalf("start second server: %v", err)
	}
	_, secondRevision := second.configManager.Snapshot()
	if firstRevision != 1 || secondRevision != 2 {
		t.Fatalf("startup revisions = %d, %d; want 1, 2", firstRevision, secondRevision)
	}
}

func TestConfigApplyUsesRequestContextForRevisionAllocation(t *testing.T) {
	store := &requestContextRevisionStore{}
	srv, err := NewServerWithConfigRevisionStore(
		context.Background(), testUIGatewayConfig(), "", "", "", config.GatewayRuntimeOverrides{}, store,
	)
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/ui/api/config/apply", strings.NewReader(testGatewayYAMLWithModels("qwen", "other"))).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer agent-secret")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), context.Canceled.Error()) {
		t.Fatalf("response = %s, want request cancellation", rr.Body.String())
	}
}

type failAfterStartupRevisionStore struct {
	calls int
	err   error
}

type requestContextRevisionStore struct {
	calls int
}

func (s *requestContextRevisionStore) Allocate(ctx context.Context) (int64, error) {
	s.calls++
	if s.calls == 1 {
		return 1, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("revision allocation received an uncancelled context")
}

func (s *failAfterStartupRevisionStore) Allocate(context.Context) (int64, error) {
	s.calls++
	if s.calls == 1 {
		return 1, nil
	}
	return 0, s.err
}
