package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/protocol"
)

func TestBillingSummaryReportsUSDUsageCostsAndTokenBreakdown(t *testing.T) {
	start := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	models := calculateModelReadyCosts([]billingReadyInterval{
		{WorkerID: "worker-a", Model: "qwen", Start: start, End: end},
	}, start, end, 24*0.14)
	summary := buildBillingSummary(BillingQuery{
		Start:            start,
		End:              end,
		WorkerDayCostRMB: 24,
		IncludeRequests:  true,
		ExchangeRate: BillingExchangeRate{
			CNYToUSD: 0.14,
			Stale:    true,
		},
		ModelPricing: map[string]config.ModelBilling{
			"qwen": {
				PerRequestUSD:            0.01,
				InputPerMillionUSD:       0.20,
				OutputPerMillionUSD:      0.80,
				CachedInputPerMillionUSD: 0.05,
			},
		},
	}, models, []billingRequestRecord{
		{RequestID: "req-a", Time: start.Add(10 * time.Minute), Model: "qwen", AppID: "app-a", PromptTokens: 100, CompletionTokens: 50, CacheTokens: 25, TotalTokens: 175},
		{RequestID: "req-b", Time: start.Add(20 * time.Minute), Model: "qwen", AppID: "app-b", PromptTokens: 300, CompletionTokens: 100, CacheTokens: 50, TotalTokens: 450},
	})

	if summary.Currency != "USD" || summary.ExchangeRateCNYToUSD != 0.14 || !summary.ExchangeRateStale {
		t.Fatalf("exchange fields = currency %q rate %v stale %v, want USD/0.14/true", summary.Currency, summary.ExchangeRateCNYToUSD, summary.ExchangeRateStale)
	}
	if len(summary.Models) != 1 {
		t.Fatalf("models = %+v, want one qwen row", summary.Models)
	}
	model := summary.Models[0]
	if model.ModelCost != 0.14 || model.ModelUsedCost != 0.020204 || model.ModelIdleCost != 0.119796 {
		t.Fatalf("model billing = %+v, want model_cost=0.14 used=0.020204 idle=0.119796", model)
	}
	if model.InputTokens != 400 || model.OutputTokens != 150 || model.CachedInputTokens != 75 || model.TotalTokens != 625 {
		t.Fatalf("model tokens = %+v, want input=400 output=150 cached=75 total=625", model)
	}
	if len(summary.Apps) != 2 {
		t.Fatalf("apps = %+v, want app-a/app-b", summary.Apps)
	}
	apps := billingAppsByID(summary.Apps)
	if apps["app-a"].InputTokens != 100 || apps["app-a"].OutputTokens != 50 || apps["app-a"].CachedInputTokens != 25 {
		t.Fatalf("app-a tokens = %+v, want input/output/cached token breakdown", apps["app-a"])
	}
	if apps["app-a"].ModelUsedCost != 0.010061 || apps["app-b"].ModelUsedCost != 0.010143 {
		t.Fatalf("app costs = app-a %+v app-b %+v, want configured usage costs", apps["app-a"], apps["app-b"])
	}
	if len(summary.RequestCosts) != 2 {
		t.Fatalf("request costs = %+v, want two rows", summary.RequestCosts)
	}
	if summary.RequestCosts[0].InputTokens != 100 || summary.RequestCosts[0].OutputTokens != 50 || summary.RequestCosts[0].CachedInputTokens != 25 || summary.RequestCosts[0].ModelUsedCost != 0.010061 {
		t.Fatalf("request cost row = %+v, want token breakdown and configured usage cost", summary.RequestCosts[0])
	}
}

func TestBillingReadyCostSplitsConcurrentModelsOnSameWorker(t *testing.T) {
	start := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	models := calculateModelReadyCosts([]billingReadyInterval{
		{WorkerID: "worker-a", Model: "qwen", Start: start, End: end},
		{WorkerID: "worker-a", Model: "vision", Start: start, End: end},
	}, start, end, 24)

	if got := roundMoney(models["qwen"].ModelCost); got != 0.5 {
		t.Fatalf("qwen cost = %v, want 0.5", got)
	}
	if got := roundMoney(models["vision"].ModelCost); got != 0.5 {
		t.Fatalf("vision cost = %v, want 0.5", got)
	}
}

func TestParseBillingQuerySupportsShanghaiLocalNaturalRanges(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/billing?day=2035-01-02", nil)
	query, err := parseBillingQuery(req)
	if err != nil {
		t.Fatalf("parse natural day: %v", err)
	}
	if want := time.Date(2035, 1, 1, 16, 0, 0, 0, time.UTC); !query.Start.Equal(want) {
		t.Fatalf("day start = %s, want %s", query.Start, want)
	}
	if want := time.Date(2035, 1, 2, 16, 0, 0, 0, time.UTC); !query.End.Equal(want) {
		t.Fatalf("day end = %s, want %s", query.End, want)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/billing?hour=2035-01-02T03", nil)
	query, err = parseBillingQuery(req)
	if err != nil {
		t.Fatalf("parse natural hour: %v", err)
	}
	if want := time.Date(2035, 1, 1, 19, 0, 0, 0, time.UTC); !query.Start.Equal(want) {
		t.Fatalf("hour start = %s, want %s", query.Start, want)
	}
	if want := time.Date(2035, 1, 1, 20, 0, 0, 0, time.UTC); !query.End.Equal(want) {
		t.Fatalf("hour end = %s, want %s", query.End, want)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/billing?start=2035-01-02%2003:04:05&end=2035-01-02T05:04:05%2B09:00", nil)
	query, err = parseBillingQuery(req)
	if err != nil {
		t.Fatalf("parse explicit range: %v", err)
	}
	if want := time.Date(2035, 1, 1, 19, 4, 5, 0, time.UTC); !query.Start.Equal(want) {
		t.Fatalf("local start = %s, want %s", query.Start, want)
	}
	if want := time.Date(2035, 1, 1, 20, 4, 5, 0, time.UTC); !query.End.Equal(want) {
		t.Fatalf("zoned end = %s, want %s", query.End, want)
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
		ExchangeRate:     BillingExchangeRate{CNYToUSD: 1},
		ModelPricing: map[string]config.ModelBilling{
			"qwen": {PerRequestUSD: 0.01, InputPerMillionUSD: 1},
		},
	})
	if err != nil {
		t.Fatalf("billing summary: %v", err)
	}
	if len(summary.Models) != 1 || summary.Models[0].ModelCost != 1 {
		data, _ := json.MarshalIndent(summary, "", "  ")
		t.Fatalf("summary = %s, want one qwen row with cost 1", data)
	}
	var modelUsedCost float64
	err = store.db.QueryRowContext(ctx, `
SELECT model_used_cost_usd::float8
FROM request_records
WHERE request_id = $1`, prefix+"-req-a").Scan(&modelUsedCost)
	if err != nil {
		t.Fatalf("read persisted request costs: %v", err)
	}
	if modelUsedCost != 0.01 {
		t.Fatalf("persisted req-a model_used_cost_usd=%v, want 0.01", modelUsedCost)
	}
}

func TestPostgresBillingReadyIntervalsCapStaleOpenIntervals(t *testing.T) {
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
	start := time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC).Add(time.Duration(time.Now().UnixNano()%int64(24*time.Hour)) * time.Nanosecond).Truncate(time.Millisecond)
	lastSeen := start.Add(10 * time.Hour)
	end := start.Add(4 * 24 * time.Hour)
	cleanupBillingTestRows(t, store.db, testPrefix)
	defer cleanupBillingTestRows(t, store.db, testPrefix)

	if _, err := store.db.ExecContext(ctx, `
INSERT INTO worker_model_ready_intervals (gateway_id, worker_id, model, started_at, share_ratio, last_seen_at)
VALUES ($1, $2, $3, $4, 1, $5)`,
		"billing-test", workerID, "qwen", start, lastSeen); err != nil {
		t.Fatalf("insert stale open interval: %v", err)
	}

	summary, err := store.BillingSummary(ctx, BillingQuery{
		Start:            start,
		End:              end,
		WorkerDayCostRMB: 55,
		ExchangeRate:     BillingExchangeRate{CNYToUSD: 1},
	})
	if err != nil {
		t.Fatalf("billing summary: %v", err)
	}
	if len(summary.Models) != 1 {
		t.Fatalf("models = %+v, want one qwen row", summary.Models)
	}
	want := lastSeen.Sub(start).Seconds()
	if summary.Models[0].ReadySeconds != want {
		t.Fatalf("ready seconds = %v, want %v", summary.Models[0].ReadySeconds, want)
	}
}

func TestPostgresRecordWorkerModelSnapshotMaintainsReadyIntervals(t *testing.T) {
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
	start := time.Date(2000, 1, 3, 0, 0, 0, 0, time.UTC).Add(time.Duration(time.Now().UnixNano()%int64(24*time.Hour)) * time.Nanosecond).Truncate(time.Millisecond)
	secondSeen := start.Add(time.Hour)
	closedAt := start.Add(2 * time.Hour)
	cleanupBillingTestRows(t, store.db, testPrefix)
	defer cleanupBillingTestRows(t, store.db, testPrefix)

	if err := store.RecordWorkerModelSnapshot(ctx, workerID, []protocol.RunningModel{{Model: "qwen", State: "ready"}}, start); err != nil {
		t.Fatalf("open snapshot interval: %v", err)
	}
	if err := store.RecordWorkerModelSnapshot(ctx, workerID, []protocol.RunningModel{{Model: "qwen", State: "ready"}}, secondSeen); err != nil {
		t.Fatalf("refresh snapshot interval: %v", err)
	}
	if err := store.RecordWorkerModelSnapshot(ctx, workerID, nil, closedAt); err != nil {
		t.Fatalf("close snapshot interval: %v", err)
	}

	var startedAt, endedAt, lastSeenAt time.Time
	err = store.db.QueryRowContext(ctx, `
SELECT started_at, ended_at, last_seen_at
FROM worker_model_ready_intervals
WHERE worker_id = $1 AND model = 'qwen'`, workerID).Scan(&startedAt, &endedAt, &lastSeenAt)
	if err != nil {
		t.Fatalf("read snapshot interval: %v", err)
	}
	if !startedAt.Equal(start) || !endedAt.Equal(closedAt) || !lastSeenAt.Equal(closedAt) {
		t.Fatalf("interval started=%s ended=%s last_seen=%s, want %s/%s/%s", startedAt, endedAt, lastSeenAt, start, closedAt, closedAt)
	}
}

func TestPostgresRecordWorkerModelSnapshotDoesNotReviveLegacyOpenInterval(t *testing.T) {
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
	start := time.Date(2000, 1, 4, 0, 0, 0, 0, time.UTC).Add(time.Duration(time.Now().UnixNano()%int64(24*time.Hour)) * time.Nanosecond).Truncate(time.Millisecond)
	seenAt := start.Add(4 * 24 * time.Hour)
	cleanupBillingTestRows(t, store.db, testPrefix)
	defer cleanupBillingTestRows(t, store.db, testPrefix)

	if _, err := store.db.ExecContext(ctx, `
INSERT INTO worker_model_ready_intervals (gateway_id, worker_id, model, started_at, share_ratio)
VALUES ($1, $2, $3, $4, 1)`,
		"billing-test", workerID, "qwen", start); err != nil {
		t.Fatalf("insert legacy open interval: %v", err)
	}
	if err := store.RecordWorkerModelSnapshot(ctx, workerID, []protocol.RunningModel{{Model: "qwen", State: "ready"}}, seenAt); err != nil {
		t.Fatalf("record ready snapshot: %v", err)
	}

	rows, err := store.db.QueryContext(ctx, `
SELECT started_at, ended_at, last_seen_at
FROM worker_model_ready_intervals
WHERE worker_id = $1 AND model = 'qwen'
ORDER BY started_at`, workerID)
	if err != nil {
		t.Fatalf("query intervals: %v", err)
	}
	defer rows.Close()
	var intervals []struct {
		startedAt  time.Time
		endedAt    sql.NullTime
		lastSeenAt sql.NullTime
	}
	for rows.Next() {
		var interval struct {
			startedAt  time.Time
			endedAt    sql.NullTime
			lastSeenAt sql.NullTime
		}
		if err := rows.Scan(&interval.startedAt, &interval.endedAt, &interval.lastSeenAt); err != nil {
			t.Fatalf("scan interval: %v", err)
		}
		intervals = append(intervals, interval)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate intervals: %v", err)
	}
	if len(intervals) != 2 {
		t.Fatalf("intervals = %+v, want sealed legacy interval and new current interval", intervals)
	}
	if !intervals[0].startedAt.Equal(start) || !intervals[0].endedAt.Valid || !intervals[0].endedAt.Time.Equal(start) {
		t.Fatalf("legacy interval = %+v, want ended at %s", intervals[0], start)
	}
	if !intervals[1].startedAt.Equal(seenAt) || intervals[1].endedAt.Valid || !intervals[1].lastSeenAt.Valid || !intervals[1].lastSeenAt.Time.Equal(seenAt) {
		t.Fatalf("current interval = %+v, want open at %s", intervals[1], seenAt)
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

func assertClose(t *testing.T, got, want float64, name string) {
	t.Helper()
	if math.Abs(got-want) > 0.000001 {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}
