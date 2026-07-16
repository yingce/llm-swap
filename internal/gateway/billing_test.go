package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBillingSummaryAllocatesModelCostByTokenAndRequest(t *testing.T) {
	start := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	models := calculateModelReadyCosts([]billingReadyInterval{
		{WorkerID: "worker-a", Model: "qwen", Start: start, End: end},
	}, start, end, 24)
	summary := buildBillingSummary(BillingQuery{
		Start:            start,
		End:              end,
		WorkerDayCostRMB: 24,
		IncludeRequests:  true,
	}, models, []billingRequestRecord{
		{RequestID: "req-a", Time: start.Add(10 * time.Minute), Model: "qwen", AppID: "app-a", TotalTokens: 100},
		{RequestID: "req-b", Time: start.Add(20 * time.Minute), Model: "qwen", AppID: "app-b", TotalTokens: 300},
	})

	if len(summary.Models) != 1 {
		t.Fatalf("models = %+v, want one qwen row", summary.Models)
	}
	model := summary.Models[0]
	if model.ModelCostRMB != 1 || model.CostPerRequestRMB != 0.5 || model.CostPerMillionTokensRMB != 2500 {
		t.Fatalf("model billing = %+v, want cost=1 per_request=0.5 per_million=2500", model)
	}
	if len(summary.Apps) != 2 {
		t.Fatalf("apps = %+v, want app-a/app-b", summary.Apps)
	}
	apps := billingAppsByID(summary.Apps)
	if apps["app-a"].RequestCostByTokenRMB != 0.25 || apps["app-a"].RequestCostByRequestRMB != 0.5 {
		t.Fatalf("app-a = %+v, want token=0.25 request=0.5", apps["app-a"])
	}
	if apps["app-b"].RequestCostByTokenRMB != 0.75 || apps["app-b"].RequestCostByRequestRMB != 0.5 {
		t.Fatalf("app-b = %+v, want token=0.75 request=0.5", apps["app-b"])
	}
}

func TestBillingReadyCostSplitsConcurrentModelsOnSameWorker(t *testing.T) {
	start := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	models := calculateModelReadyCosts([]billingReadyInterval{
		{WorkerID: "worker-a", Model: "qwen", Start: start, End: end},
		{WorkerID: "worker-a", Model: "vision", Start: start, End: end},
	}, start, end, 24)

	if got := roundMoney(models["qwen"].ModelCostRMB); got != 0.5 {
		t.Fatalf("qwen cost = %v, want 0.5", got)
	}
	if got := roundMoney(models["vision"].ModelCostRMB); got != 0.5 {
		t.Fatalf("vision cost = %v, want 0.5", got)
	}
}

func TestBillingEndpointRequiresRecordsStore(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/api/billing", nil)
	req.AddCookie(&http.Cookie{Name: uiAuthCookieName, Value: "agent-secret"})
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestLegacyUIBillingEndpointStillWorks(t *testing.T) {
	srv := NewServer(testGatewayConfig())
	req := httptest.NewRequest(http.MethodGet, "/ui/api/billing", nil)
	req.AddCookie(&http.Cookie{Name: uiAuthCookieName, Value: "agent-secret"})
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestPostgresBillingSummaryPersistsRequestCosts(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("PG_DSN"))
	if dsn == "" {
		t.Skip("PG_DSN is required for postgres billing test")
	}
	ctx := context.Background()
	store, err := NewPostgresRecordsStore(ctx, dsn, "billing-test", 3*time.Second, true)
	if err != nil {
		t.Fatalf("connect postgres records store: %v", err)
	}
	defer store.Close()

	testPrefix := "billing-test-" + strings.NewReplacer("/", "-", " ", "-").Replace(t.Name())
	prefix := testPrefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	workerID := prefix + "-worker"
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(time.Now().UnixNano()%int64(24*time.Hour)) * time.Nanosecond).Truncate(time.Millisecond)
	end := start.Add(time.Hour)
	cleanupBillingTestRows(t, store.db, testPrefix)
	defer cleanupBillingTestRows(t, store.db, testPrefix)

	if _, err := store.AppendImportedWorkerEvent(ctx, WorkerEventRecord{
		ReceivedAt: start,
		WorkerID:   workerID,
		Time:       start,
		Event:      "model_state_changed",
		Model:      "qwen",
		FromState:  "loading",
		ToState:    "ready",
	}, prefix+"-event-open"); err != nil {
		t.Fatalf("open ready interval: %v", err)
	}
	if _, err := store.AppendImportedWorkerEvent(ctx, WorkerEventRecord{
		ReceivedAt: end,
		WorkerID:   workerID,
		Time:       end,
		Event:      "model_unloaded",
		Model:      "qwen",
		FromState:  "ready",
	}, prefix+"-event-close"); err != nil {
		t.Fatalf("close ready interval: %v", err)
	}
	for _, request := range []RequestLogEntry{
		{Time: start.Add(10 * time.Minute), RequestID: prefix + "-req-a", Model: "qwen", WorkerID: workerID, TotalTokens: 100, RequestHeaders: httpHeader{"x-app-id": "app-a"}},
		{Time: start.Add(20 * time.Minute), RequestID: prefix + "-req-b", Model: "qwen", WorkerID: workerID, TotalTokens: 300, RequestHeaders: httpHeader{"x-app-id": "app-b"}},
	} {
		if _, err := store.AppendImportedRequestRecord(ctx, request, request.RequestID); err != nil {
			t.Fatalf("insert request %s: %v", request.RequestID, err)
		}
	}

	summary, err := store.BillingSummary(ctx, BillingQuery{
		Start:            start,
		End:              end,
		WorkerDayCostRMB: 24,
		IncludeRequests:  true,
		Persist:          true,
	})
	if err != nil {
		t.Fatalf("billing summary: %v", err)
	}
	if len(summary.Models) != 1 || summary.Models[0].ModelCostRMB != 1 {
		data, _ := json.MarshalIndent(summary, "", "  ")
		t.Fatalf("summary = %s, want one qwen row with cost 1", data)
	}
	var tokenCost, requestCost float64
	err = store.db.QueryRowContext(ctx, `
SELECT cost_by_token_rmb::float8, cost_by_request_rmb::float8
FROM request_records
WHERE request_id = $1`, prefix+"-req-a").Scan(&tokenCost, &requestCost)
	if err != nil {
		t.Fatalf("read persisted request costs: %v", err)
	}
	if tokenCost != 0.25 || requestCost != 0.5 {
		t.Fatalf("persisted req-a costs token=%v request=%v, want 0.25/0.5", tokenCost, requestCost)
	}
}

func billingAppsByID(apps []BillingAppSummary) map[string]BillingAppSummary {
	out := map[string]BillingAppSummary{}
	for _, app := range apps {
		out[app.AppID] = app
	}
	return out
}

func cleanupBillingTestRows(t *testing.T, db *sql.DB, prefix string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM request_records WHERE request_id LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("cleanup billing test request rows: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM worker_model_ready_intervals WHERE worker_id LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("cleanup billing test interval rows: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM worker_events WHERE source_hash LIKE $1`, prefix+"%"); err != nil {
		t.Fatalf("cleanup billing test event rows: %v", err)
	}
}
