package gateway

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"llm-swap/internal/config"
)

const defaultWorkerDayCostRMB = 55.0
const fallbackCNYToUSDRate = 0.14
const unattributedBillingAppID = "_unattributed"

var billingLocalLocation = time.FixedZone("UTC+8", 8*60*60)

type BillingSummary struct {
	Start                time.Time               `json:"start"`
	End                  time.Time               `json:"end"`
	Currency             string                  `json:"currency"`
	ExchangeRateCNYToUSD float64                 `json:"exchange_rate_cny_to_usd"`
	ExchangeRateTime     time.Time               `json:"exchange_rate_time,omitempty"`
	ExchangeRateStale    bool                    `json:"exchange_rate_stale"`
	WorkerDayCostRMB     float64                 `json:"worker_day_cost_rmb"`
	WorkerDayCostUSD     float64                 `json:"worker_day_cost_usd"`
	Models               []BillingModelSummary   `json:"models"`
	Apps                 []BillingAppSummary     `json:"apps"`
	Totals               BillingCostTotals       `json:"totals"`
	RequestCosts         []BillingRequestCostRow `json:"request_costs,omitempty"`
}

type BillingCostTotals struct {
	ReadySeconds           float64 `json:"ready_seconds"`
	BillableWorkerSeconds  float64 `json:"billable_worker_seconds"`
	RequestDurationSeconds float64 `json:"request_duration_seconds"`
	ModelCost              float64 `json:"model_cost"`
	ModelUsedCost          float64 `json:"model_used_cost"`
	ModelIdleCost          float64 `json:"model_idle_cost"`
	Requests               int     `json:"requests"`
	InputTokens            int64   `json:"input_tokens"`
	OutputTokens           int64   `json:"output_tokens"`
	CachedInputTokens      int64   `json:"cached_input_tokens"`
	TotalTokens            int64   `json:"total_tokens"`
}

type BillingModelSummary struct {
	Model                  string  `json:"model"`
	ReadySeconds           float64 `json:"ready_seconds"`
	BillableWorkerSeconds  float64 `json:"billable_worker_seconds"`
	RequestDurationSeconds float64 `json:"request_duration_seconds"`
	ReadyShare             float64 `json:"ready_share"`
	CostShare              float64 `json:"cost_share"`
	ModelCost              float64 `json:"model_cost"`
	ModelUsedCost          float64 `json:"model_used_cost"`
	ModelIdleCost          float64 `json:"model_idle_cost"`
	Requests               int     `json:"requests"`
	InputTokens            int64   `json:"input_tokens"`
	OutputTokens           int64   `json:"output_tokens"`
	CachedInputTokens      int64   `json:"cached_input_tokens"`
	TotalTokens            int64   `json:"total_tokens"`
}

type BillingAppSummary struct {
	AppID                  string  `json:"app_id"`
	Requests               int     `json:"requests"`
	RequestDurationSeconds float64 `json:"request_duration_seconds"`
	InputTokens            int64   `json:"input_tokens"`
	OutputTokens           int64   `json:"output_tokens"`
	CachedInputTokens      int64   `json:"cached_input_tokens"`
	TotalTokens            int64   `json:"total_tokens"`
	ModelCost              float64 `json:"model_cost"`
	ModelUsedCost          float64 `json:"model_used_cost"`
	ModelIdleCost          float64 `json:"model_idle_cost"`
}

type BillingRequestCostRow struct {
	RequestID                       string     `json:"request_id"`
	Time                            time.Time  `json:"time"`
	Model                           string     `json:"model"`
	AppID                           string     `json:"app_id,omitempty"`
	WorkerID                        string     `json:"worker_id,omitempty"`
	InputTokens                     int        `json:"input_tokens"`
	OutputTokens                    int        `json:"output_tokens"`
	CachedInputTokens               int        `json:"cached_input_tokens"`
	TotalTokens                     int        `json:"total_tokens"`
	BillingPerRequestUSD            float64    `json:"billing_per_request_usd,omitempty"`
	BillingInputPerMillionUSD       float64    `json:"billing_input_per_million_usd,omitempty"`
	BillingOutputPerMillionUSD      float64    `json:"billing_output_per_million_usd,omitempty"`
	BillingCachedInputPerMillionUSD float64    `json:"billing_cached_input_per_million_usd,omitempty"`
	ModelUsedCost                   float64    `json:"model_used_cost"`
	CostCalculatedAt                *time.Time `json:"cost_calculated_at,omitempty"`
}

type billingReadyInterval struct {
	WorkerID string
	Model    string
	Start    time.Time
	End      time.Time
}

type billingRequestRecord struct {
	ID                              int64
	RequestID                       string
	Time                            time.Time
	Model                           string
	WorkerID                        string
	AppID                           string
	TotalTokens                     int
	PromptTokens                    int
	CompletionTokens                int
	CacheTokens                     int
	DurationMS                      int64
	StatusCode                      int
	BillingPerRequestUSD            float64
	BillingInputPerMillionUSD       float64
	BillingOutputPerMillionUSD      float64
	BillingCachedInputPerMillionUSD float64
	ModelUsedCostUSD                float64
	CostCalculatedAt                *time.Time
}

type billingAppModelUsage struct {
	Requests        int
	DurationSeconds float64
}

func (s *Server) handleBilling(w http.ResponseWriter, r *http.Request) {
	if s.recordsStore == nil {
		http.Error(w, "records store is not enabled", http.StatusServiceUnavailable)
		return
	}
	store, ok := s.recordsStore.(interface {
		BillingSummary(context.Context, BillingQuery) (BillingSummary, error)
	})
	if !ok {
		http.Error(w, "records store does not support billing", http.StatusServiceUnavailable)
		return
	}
	query, err := parseBillingQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query.ExchangeRate = s.exchangeRates.CNYToUSD(r.Context())
	query.ModelPricing = modelBillingPricing(s.currentConfig())
	resp, err := store.BillingSummary(r.Context(), query)
	if err != nil {
		http.Error(w, "failed to query billing", http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

func modelBillingPricing(cfg config.GatewayConfig) map[string]config.ModelBilling {
	out := make(map[string]config.ModelBilling, len(cfg.Models))
	for name, model := range cfg.Models {
		out[name] = model.Billing
	}
	return out
}

type BillingQuery struct {
	Start            time.Time
	End              time.Time
	WorkerDayCostRMB float64
	IncludeRequests  bool
	Persist          bool
	ExchangeRate     BillingExchangeRate
	ModelPricing     map[string]config.ModelBilling
}

type BillingExchangeRate struct {
	CNYToUSD float64
	Time     time.Time
	Stale    bool
}

func parseBillingQuery(r *http.Request) (BillingQuery, error) {
	now := time.Now()
	query := BillingQuery{
		Start:            now.Add(-24 * time.Hour),
		End:              now,
		WorkerDayCostRMB: defaultWorkerDayCostRMB,
	}
	values := r.URL.Query()
	if raw := strings.TrimSpace(values.Get("day")); raw == "" {
		if raw = strings.TrimSpace(values.Get("date")); raw != "" {
			start, end, err := parseBillingNaturalDay(raw)
			if err != nil {
				return BillingQuery{}, err
			}
			query.Start = start
			query.End = end
		}
	} else {
		start, end, err := parseBillingNaturalDay(raw)
		if err != nil {
			return BillingQuery{}, err
		}
		query.Start = start
		query.End = end
	}
	if raw := strings.TrimSpace(values.Get("hour")); raw != "" {
		start, end, err := parseBillingNaturalHour(raw)
		if err != nil {
			return BillingQuery{}, err
		}
		query.Start = start
		query.End = end
	}
	if raw := strings.TrimSpace(values.Get("start")); raw != "" {
		parsed, err := parseBillingTimestamp(raw)
		if err != nil {
			return BillingQuery{}, err
		}
		query.Start = parsed
	}
	if raw := strings.TrimSpace(values.Get("end")); raw != "" {
		parsed, err := parseBillingTimestamp(raw)
		if err != nil {
			return BillingQuery{}, err
		}
		query.End = parsed
	}
	if raw := strings.TrimSpace(values.Get("worker_day_cost_rmb")); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil || parsed <= 0 {
			return BillingQuery{}, billingQueryError("worker_day_cost_rmb must be greater than 0")
		}
		query.WorkerDayCostRMB = parsed
	}
	query.IncludeRequests = parseBoolQuery(values.Get("include_requests"))
	query.Persist = parseBoolQuery(values.Get("persist"))
	if !query.End.After(query.Start) {
		return BillingQuery{}, errInvalidBillingRange
	}
	return query, nil
}

func parseBillingNaturalDay(raw string) (time.Time, time.Time, error) {
	parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(raw), billingLocalLocation)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := parsed.UTC()
	return start, parsed.AddDate(0, 0, 1).UTC(), nil
}

func parseBillingNaturalHour(raw string) (time.Time, time.Time, error) {
	value := strings.TrimSpace(raw)
	for _, layout := range []string{"2006-01-02T15", "2006-01-02 15"} {
		parsed, err := time.ParseInLocation(layout, value, billingLocalLocation)
		if err == nil {
			start := parsed.UTC()
			return start, parsed.Add(time.Hour).UTC(), nil
		}
	}
	parsed, err := parseBillingTimestamp(value)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	local := parsed.In(billingLocalLocation)
	start := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, billingLocalLocation)
	return start.UTC(), start.Add(time.Hour).UTC(), nil
}

func parseBillingTimestamp(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	for _, layout := range []string{
		"2006-01-02",
		"2006-01-02 15",
		"2006-01-02T15",
		"2006-01-02 15:04",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	} {
		parsed, err := time.ParseInLocation(layout, value, billingLocalLocation)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, billingQueryError("time must be RFC3339 or local UTC+8 timestamp")
}

var errInvalidBillingRange = billingQueryError("end must be after start")

type billingQueryError string

func (e billingQueryError) Error() string {
	return string(e)
}

func parseBoolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *PostgresRecordsStore) BillingSummary(ctx context.Context, query BillingQuery) (BillingSummary, error) {
	if query.WorkerDayCostRMB <= 0 {
		query.WorkerDayCostRMB = defaultWorkerDayCostRMB
	}
	if query.ExchangeRate.CNYToUSD <= 0 {
		query.ExchangeRate.CNYToUSD = fallbackCNYToUSDRate
		query.ExchangeRate.Stale = true
	}
	if err := s.backfillReadyIntervalsFromWorkerEvents(ctx, query.End); err != nil {
		return BillingSummary{}, err
	}
	intervals, err := s.billingReadyIntervals(ctx, query.Start, query.End)
	if err != nil {
		return BillingSummary{}, err
	}
	requests, err := s.billingRequests(ctx, query.Start, query.End)
	if err != nil {
		return BillingSummary{}, err
	}
	modelCosts := calculateModelReadyCosts(intervals, query.Start, query.End, query.WorkerDayCostRMB*query.ExchangeRate.CNYToUSD)
	summary := buildBillingSummary(query, modelCosts, requests)
	if query.Persist {
		if err := s.persistBillingRequestCosts(ctx, summary.RequestCosts); err != nil {
			return BillingSummary{}, err
		}
	}
	if !query.IncludeRequests {
		summary.RequestCosts = nil
	}
	return summary, nil
}

func (s *PostgresRecordsStore) backfillReadyIntervalsFromWorkerEvents(ctx context.Context, end time.Time) error {
	runCtx, cancel := s.context(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(runCtx, `
SELECT we.id, we.received_at, we.worker_id, we.event_time, we.event, we.model, we.from_state, we.to_state
FROM worker_events we
WHERE we.model <> ''
  AND we.event_time <= $1
  AND we.event IN ('model_loaded', 'model_state_changed', 'model_unloaded', 'gateway_model_warm_done', 'gateway_model_unload_done')
  AND NOT EXISTS (
    SELECT 1 FROM worker_model_ready_intervals i
    WHERE i.source_event_id = we.id OR i.close_event_id = we.id
  )
ORDER BY we.event_time, we.id`, end.UTC())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var eventID int64
		var event WorkerEventRecord
		if err := rows.Scan(&eventID, &event.ReceivedAt, &event.WorkerID, &event.Time, &event.Event, &event.Model, &event.FromState, &event.ToState); err != nil {
			return err
		}
		if err := s.applyWorkerModelReadyEvent(runCtx, event, eventID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *PostgresRecordsStore) billingReadyIntervals(ctx context.Context, start, end time.Time) ([]billingReadyInterval, error) {
	runCtx, cancel := s.context(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(runCtx, `
WITH intervals AS (
  SELECT worker_id, model, started_at,
    LEAST(COALESCE(ended_at, last_seen_at, started_at), $2) AS effective_end
  FROM worker_model_ready_intervals
)
SELECT worker_id, model, GREATEST(started_at, $1), LEAST(effective_end, $2)
FROM intervals
WHERE started_at < $2 AND effective_end > $1
ORDER BY worker_id, started_at`, start.UTC(), end.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var intervals []billingReadyInterval
	for rows.Next() {
		var interval billingReadyInterval
		if err := rows.Scan(&interval.WorkerID, &interval.Model, &interval.Start, &interval.End); err != nil {
			return nil, err
		}
		if interval.End.After(interval.Start) && interval.Model != "" && interval.WorkerID != "" {
			intervals = append(intervals, interval)
		}
	}
	return intervals, rows.Err()
}

func (s *PostgresRecordsStore) billingRequests(ctx context.Context, start, end time.Time) ([]billingRequestRecord, error) {
	runCtx, cancel := s.context(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(runCtx, `
SELECT id, request_id, event_time, model, worker_id, app_id, total_tokens
  , prompt_tokens, completion_tokens, cache_tokens, duration_ms, status_code
  , billing_per_request_usd::float8, billing_input_per_million_usd::float8
  , billing_output_per_million_usd::float8, billing_cached_input_per_million_usd::float8
  , model_used_cost_usd::float8, cost_calculated_at
FROM request_records
WHERE event_time >= $1 AND event_time < $2
ORDER BY id`, start.UTC(), end.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var requests []billingRequestRecord
	for rows.Next() {
		var request billingRequestRecord
		var costCalculatedAt sql.NullTime
		if err := rows.Scan(
			&request.ID, &request.RequestID, &request.Time, &request.Model, &request.WorkerID, &request.AppID, &request.TotalTokens,
			&request.PromptTokens, &request.CompletionTokens, &request.CacheTokens, &request.DurationMS, &request.StatusCode,
			&request.BillingPerRequestUSD, &request.BillingInputPerMillionUSD,
			&request.BillingOutputPerMillionUSD, &request.BillingCachedInputPerMillionUSD,
			&request.ModelUsedCostUSD, &costCalculatedAt,
		); err != nil {
			return nil, err
		}
		if costCalculatedAt.Valid {
			request.CostCalculatedAt = &costCalculatedAt.Time
		}
		if request.Model != "" {
			requests = append(requests, request)
		}
	}
	return requests, rows.Err()
}

func (s *PostgresRecordsStore) persistBillingRequestCosts(ctx context.Context, rows []BillingRequestCostRow) error {
	runCtx, cancel := s.context(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(runCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, row := range rows {
		if row.RequestID == "" || row.CostCalculatedAt != nil {
			continue
		}
		if _, err := tx.ExecContext(runCtx, `
UPDATE request_records
SET model_used_cost_usd = $1,
    cost_calculated_at = $2,
    billing_per_request_usd = $3,
    billing_input_per_million_usd = $4,
    billing_output_per_million_usd = $5,
    billing_cached_input_per_million_usd = $6
WHERE request_id = $7 AND event_time = $8 AND model = $9 AND cost_calculated_at IS NULL`,
			row.ModelUsedCost,
			now,
			row.BillingPerRequestUSD,
			row.BillingInputPerMillionUSD,
			row.BillingOutputPerMillionUSD,
			row.BillingCachedInputPerMillionUSD,
			row.RequestID,
			row.Time.UTC(),
			row.Model,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func calculateModelReadyCosts(intervals []billingReadyInterval, start, end time.Time, workerDayCostUSD float64) map[string]*BillingModelSummary {
	byWorker := map[string][]billingReadyInterval{}
	for _, interval := range intervals {
		if interval.Start.Before(start) {
			interval.Start = start
		}
		if interval.End.After(end) {
			interval.End = end
		}
		if !interval.End.After(interval.Start) {
			continue
		}
		byWorker[interval.WorkerID] = append(byWorker[interval.WorkerID], interval)
	}
	models := map[string]*BillingModelSummary{}
	ratePerSecond := workerDayCostUSD / 86400.0
	for _, workerIntervals := range byWorker {
		points := make([]time.Time, 0, len(workerIntervals)*2)
		for _, interval := range workerIntervals {
			points = append(points, interval.Start, interval.End)
		}
		sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })
		points = uniqueTimes(points)
		for i := 0; i+1 < len(points); i++ {
			segStart, segEnd := points[i], points[i+1]
			if !segEnd.After(segStart) {
				continue
			}
			active := map[string]bool{}
			for _, interval := range workerIntervals {
				if interval.Start.Before(segEnd) && interval.End.After(segStart) {
					active[interval.Model] = true
				}
			}
			if len(active) == 0 {
				continue
			}
			seconds := segEnd.Sub(segStart).Seconds()
			shareSeconds := seconds / float64(len(active))
			for model := range active {
				row := ensureBillingModel(models, model)
				row.ReadySeconds += seconds
				row.BillableWorkerSeconds += shareSeconds
				row.ModelCost += shareSeconds * ratePerSecond
			}
		}
	}
	return models
}

func buildBillingSummary(query BillingQuery, models map[string]*BillingModelSummary, requests []billingRequestRecord) BillingSummary {
	requestsByModel := map[string][]billingRequestRecord{}
	totalReadySeconds := 0.0
	totalBillableSeconds := 0.0
	totalModelCost := 0.0
	for model, row := range models {
		totalReadySeconds += row.ReadySeconds
		totalBillableSeconds += row.BillableWorkerSeconds
		totalModelCost += row.ModelCost
		row.Model = model
	}
	for _, request := range requests {
		requestsByModel[request.Model] = append(requestsByModel[request.Model], request)
		row := ensureBillingModel(models, request.Model)
		row.Requests++
		row.RequestDurationSeconds += requestDurationSeconds(request)
		row.InputTokens += int64(request.PromptTokens)
		row.OutputTokens += int64(request.CompletionTokens)
		row.CachedInputTokens += int64(request.CacheTokens)
		row.TotalTokens += int64(request.TotalTokens)
	}

	requestCostRows := make([]BillingRequestCostRow, 0, len(requests))
	apps := map[string]*BillingAppSummary{}
	appUsageByModel := map[string]map[string]*billingAppModelUsage{}
	modelUsageByModel := map[string]*billingAppModelUsage{}
	for model, modelRequests := range requestsByModel {
		row := ensureBillingModel(models, model)
		pricing := query.ModelPricing[model]
		for _, request := range modelRequests {
			durationSeconds := requestDurationSeconds(request)
			appID := billingAppID(request.AppID)
			modelUsage := ensureBillingAppModelUsage(modelUsageByModel, model)
			modelUsage.Requests++
			modelUsage.DurationSeconds += durationSeconds
			effectivePricing := billingRequestPricing(pricing, request)
			usedCost := billingRequestUsedCost(effectivePricing, request)
			row.ModelUsedCost += usedCost
			requestCost := BillingRequestCostRow{
				RequestID:                       request.RequestID,
				Time:                            request.Time,
				Model:                           request.Model,
				AppID:                           appID,
				WorkerID:                        request.WorkerID,
				InputTokens:                     request.PromptTokens,
				OutputTokens:                    request.CompletionTokens,
				CachedInputTokens:               request.CacheTokens,
				TotalTokens:                     request.TotalTokens,
				BillingPerRequestUSD:            effectivePricing.PerRequestUSD,
				BillingInputPerMillionUSD:       effectivePricing.InputPerMillionUSD,
				BillingOutputPerMillionUSD:      effectivePricing.OutputPerMillionUSD,
				BillingCachedInputPerMillionUSD: effectivePricing.CachedInputPerMillionUSD,
				ModelUsedCost:                   roundMoney(usedCost),
				CostCalculatedAt:                request.CostCalculatedAt,
			}
			requestCostRows = append(requestCostRows, requestCost)
			app := ensureBillingApp(apps, appID)
			appUsage := ensureBillingAppUsage(appUsageByModel, request.Model, appID)
			appUsage.Requests++
			appUsage.DurationSeconds += durationSeconds
			app.Requests++
			app.RequestDurationSeconds += durationSeconds
			app.InputTokens += int64(request.PromptTokens)
			app.OutputTokens += int64(request.CompletionTokens)
			app.CachedInputTokens += int64(request.CacheTokens)
			app.TotalTokens += int64(request.TotalTokens)
			app.ModelUsedCost += usedCost
		}
	}
	allocateAppIdleCosts(models, apps, appUsageByModel, modelUsageByModel)

	modelRows := make([]BillingModelSummary, 0, len(models))
	for _, row := range models {
		if totalReadySeconds > 0 {
			row.ReadyShare = row.ReadySeconds / totalReadySeconds
		}
		if totalModelCost > 0 {
			row.CostShare = row.ModelCost / totalModelCost
		}
		row.ReadySeconds = roundSeconds(row.ReadySeconds)
		row.BillableWorkerSeconds = roundSeconds(row.BillableWorkerSeconds)
		row.RequestDurationSeconds = roundSeconds(row.RequestDurationSeconds)
		row.ModelCost = roundMoney(row.ModelCost)
		row.ModelUsedCost = roundMoney(row.ModelUsedCost)
		row.ModelIdleCost = roundMoney(row.ModelCost - row.ModelUsedCost)
		modelRows = append(modelRows, *row)
	}
	sort.Slice(modelRows, func(i, j int) bool {
		if modelRows[i].ModelCost == modelRows[j].ModelCost {
			return modelRows[i].Model < modelRows[j].Model
		}
		return modelRows[i].ModelCost > modelRows[j].ModelCost
	})

	appRows := make([]BillingAppSummary, 0, len(apps))
	for _, row := range apps {
		row.RequestDurationSeconds = roundSeconds(row.RequestDurationSeconds)
		row.ModelCost = roundMoney(row.ModelUsedCost + row.ModelIdleCost)
		row.ModelUsedCost = roundMoney(row.ModelUsedCost)
		row.ModelIdleCost = roundMoney(row.ModelIdleCost)
		appRows = append(appRows, *row)
	}
	sort.Slice(appRows, func(i, j int) bool {
		if appRows[i].ModelCost == appRows[j].ModelCost {
			return appRows[i].AppID < appRows[j].AppID
		}
		return appRows[i].ModelCost > appRows[j].ModelCost
	})
	sort.Slice(requestCostRows, func(i, j int) bool { return requestCostRows[i].Time.Before(requestCostRows[j].Time) })

	totalRequests := 0
	totalInputTokens := int64(0)
	totalOutputTokens := int64(0)
	totalCachedInputTokens := int64(0)
	totalTokens := int64(0)
	totalRequestDurationSeconds := 0.0
	totalUsedCost := 0.0
	for _, row := range modelRows {
		totalRequests += row.Requests
		totalRequestDurationSeconds += row.RequestDurationSeconds
		totalInputTokens += row.InputTokens
		totalOutputTokens += row.OutputTokens
		totalCachedInputTokens += row.CachedInputTokens
		totalTokens += row.TotalTokens
		totalUsedCost += row.ModelUsedCost
	}

	return BillingSummary{
		Start:                query.Start.UTC(),
		End:                  query.End.UTC(),
		Currency:             "USD",
		ExchangeRateCNYToUSD: roundUnitPrice(query.ExchangeRate.CNYToUSD),
		ExchangeRateTime:     query.ExchangeRate.Time.UTC(),
		ExchangeRateStale:    query.ExchangeRate.Stale,
		WorkerDayCostRMB:     query.WorkerDayCostRMB,
		WorkerDayCostUSD:     roundMoney(query.WorkerDayCostRMB * query.ExchangeRate.CNYToUSD),
		Models:               modelRows,
		Apps:                 appRows,
		Totals: BillingCostTotals{
			ReadySeconds:           roundSeconds(totalReadySeconds),
			BillableWorkerSeconds:  roundSeconds(totalBillableSeconds),
			RequestDurationSeconds: roundSeconds(totalRequestDurationSeconds),
			ModelCost:              roundMoney(totalModelCost),
			ModelUsedCost:          roundMoney(totalUsedCost),
			ModelIdleCost:          roundMoney(totalModelCost - totalUsedCost),
			Requests:               totalRequests,
			InputTokens:            totalInputTokens,
			OutputTokens:           totalOutputTokens,
			CachedInputTokens:      totalCachedInputTokens,
			TotalTokens:            totalTokens,
		},
		RequestCosts: requestCostRows,
	}
}

func calculateConfiguredUsageCost(pricing config.ModelBilling, request billingRequestRecord) float64 {
	uncachedInputTokens := max(request.PromptTokens-request.CacheTokens, 0)
	tokenCost := float64(uncachedInputTokens)*pricing.InputPerMillionUSD/1_000_000 +
		float64(request.CompletionTokens)*pricing.OutputPerMillionUSD/1_000_000 +
		float64(request.CacheTokens)*pricing.CachedInputPerMillionUSD/1_000_000
	if pricing.PerRequestUSD > 0 {
		return pricing.PerRequestUSD
	}
	return tokenCost
}

func billingRequestUsedCost(pricing config.ModelBilling, request billingRequestRecord) float64 {
	if request.CostCalculatedAt != nil {
		return request.ModelUsedCostUSD
	}
	return calculateConfiguredUsageCost(pricing, request)
}

func billingRequestPricing(fallback config.ModelBilling, request billingRequestRecord) config.ModelBilling {
	if request.CostCalculatedAt == nil {
		return fallback
	}
	return config.ModelBilling{
		PerRequestUSD:            request.BillingPerRequestUSD,
		InputPerMillionUSD:       request.BillingInputPerMillionUSD,
		OutputPerMillionUSD:      request.BillingOutputPerMillionUSD,
		CachedInputPerMillionUSD: request.BillingCachedInputPerMillionUSD,
	}
}

func billingAppID(appID string) string {
	trimmed := strings.TrimSpace(appID)
	if trimmed == "" {
		return unattributedBillingAppID
	}
	return trimmed
}

func requestDurationSeconds(request billingRequestRecord) float64 {
	if request.DurationMS <= 0 {
		return 0
	}
	return float64(request.DurationMS) / 1000.0
}

func ensureBillingModel(rows map[string]*BillingModelSummary, model string) *BillingModelSummary {
	row := rows[model]
	if row == nil {
		row = &BillingModelSummary{Model: model}
		rows[model] = row
	}
	return row
}

func ensureBillingApp(rows map[string]*BillingAppSummary, appID string) *BillingAppSummary {
	row := rows[appID]
	if row == nil {
		row = &BillingAppSummary{AppID: appID}
		rows[appID] = row
	}
	return row
}

func ensureBillingAppModelUsage(rows map[string]*billingAppModelUsage, model string) *billingAppModelUsage {
	row := rows[model]
	if row == nil {
		row = &billingAppModelUsage{}
		rows[model] = row
	}
	return row
}

func ensureBillingAppUsage(rows map[string]map[string]*billingAppModelUsage, model, appID string) *billingAppModelUsage {
	byApp := rows[model]
	if byApp == nil {
		byApp = map[string]*billingAppModelUsage{}
		rows[model] = byApp
	}
	row := byApp[appID]
	if row == nil {
		row = &billingAppModelUsage{}
		byApp[appID] = row
	}
	return row
}

func allocateAppIdleCosts(models map[string]*BillingModelSummary, apps map[string]*BillingAppSummary, appUsageByModel map[string]map[string]*billingAppModelUsage, modelUsageByModel map[string]*billingAppModelUsage) {
	for model, byApp := range appUsageByModel {
		modelRow := models[model]
		totalUsage := modelUsageByModel[model]
		if modelRow == nil || totalUsage == nil {
			continue
		}
		idleCost := modelRow.ModelCost - modelRow.ModelUsedCost
		if idleCost <= 0 {
			continue
		}
		totalBasis := totalUsage.DurationSeconds
		useDuration := totalBasis > 0
		if !useDuration {
			totalBasis = float64(totalUsage.Requests)
		}
		if totalBasis <= 0 {
			continue
		}
		for appID, usage := range byApp {
			app := apps[appID]
			if app == nil || usage == nil {
				continue
			}
			basis := usage.DurationSeconds
			if !useDuration {
				basis = float64(usage.Requests)
			}
			if basis <= 0 {
				continue
			}
			app.ModelIdleCost += idleCost * basis / totalBasis
		}
	}
}

func uniqueTimes(points []time.Time) []time.Time {
	if len(points) == 0 {
		return points
	}
	out := points[:1]
	for _, point := range points[1:] {
		if !point.Equal(out[len(out)-1]) {
			out = append(out, point)
		}
	}
	return out
}

func roundMoney(value float64) float64 {
	return math.Round(value*1_000_000) / 1_000_000
}

func roundUnitPrice(value float64) float64 {
	return math.Round(value*1_000_000_000_000) / 1_000_000_000_000
}

func roundSeconds(value float64) float64 {
	return math.Round(value*1000) / 1000
}
