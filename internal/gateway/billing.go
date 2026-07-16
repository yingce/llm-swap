package gateway

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultWorkerDayCostRMB = 55.0

type BillingSummary struct {
	Start            time.Time               `json:"start"`
	End              time.Time               `json:"end"`
	WorkerDayCostRMB float64                 `json:"worker_day_cost_rmb"`
	Models           []BillingModelSummary   `json:"models"`
	Apps             []BillingAppSummary     `json:"apps"`
	Totals           BillingCostTotals       `json:"totals"`
	RequestCosts     []BillingRequestCostRow `json:"request_costs,omitempty"`
}

type BillingCostTotals struct {
	ReadySeconds            float64 `json:"ready_seconds"`
	BillableWorkerSeconds   float64 `json:"billable_worker_seconds"`
	ModelCostRMB            float64 `json:"model_cost_rmb"`
	RequestCostByTokenRMB   float64 `json:"request_cost_by_token_rmb"`
	RequestCostByRequestRMB float64 `json:"request_cost_by_request_rmb"`
	Requests                int     `json:"requests"`
	TotalTokens             int64   `json:"total_tokens"`
}

type BillingModelSummary struct {
	Model                   string  `json:"model"`
	ReadySeconds            float64 `json:"ready_seconds"`
	BillableWorkerSeconds   float64 `json:"billable_worker_seconds"`
	ReadyShare              float64 `json:"ready_share"`
	CostShare               float64 `json:"cost_share"`
	ModelCostRMB            float64 `json:"model_cost_rmb"`
	Requests                int     `json:"requests"`
	TotalTokens             int64   `json:"total_tokens"`
	CostPerRequestRMB       float64 `json:"cost_per_request_rmb"`
	CostPerMillionTokensRMB float64 `json:"cost_per_million_tokens_rmb"`
}

type BillingAppSummary struct {
	AppID                   string  `json:"app_id"`
	Requests                int     `json:"requests"`
	TotalTokens             int64   `json:"total_tokens"`
	RequestCostByTokenRMB   float64 `json:"request_cost_by_token_rmb"`
	RequestCostByRequestRMB float64 `json:"request_cost_by_request_rmb"`
}

type BillingRequestCostRow struct {
	RequestID               string    `json:"request_id"`
	Time                    time.Time `json:"time"`
	Model                   string    `json:"model"`
	AppID                   string    `json:"app_id,omitempty"`
	WorkerID                string    `json:"worker_id,omitempty"`
	TotalTokens             int       `json:"total_tokens"`
	RequestCostByTokenRMB   float64   `json:"request_cost_by_token_rmb"`
	RequestCostByRequestRMB float64   `json:"request_cost_by_request_rmb"`
}

type billingReadyInterval struct {
	WorkerID string
	Model    string
	Start    time.Time
	End      time.Time
}

type billingRequestRecord struct {
	ID          int64
	RequestID   string
	Time        time.Time
	Model       string
	WorkerID    string
	AppID       string
	TotalTokens int
}

func (s *Server) handleUIBilling(w http.ResponseWriter, r *http.Request) {
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
	resp, err := store.BillingSummary(r.Context(), query)
	if err != nil {
		http.Error(w, "failed to query billing", http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

type BillingQuery struct {
	Start            time.Time
	End              time.Time
	WorkerDayCostRMB float64
	IncludeRequests  bool
	Persist          bool
}

func parseBillingQuery(r *http.Request) (BillingQuery, error) {
	now := time.Now()
	query := BillingQuery{
		Start:            now.Add(-24 * time.Hour),
		End:              now,
		WorkerDayCostRMB: defaultWorkerDayCostRMB,
	}
	values := r.URL.Query()
	if raw := strings.TrimSpace(values.Get("start")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return BillingQuery{}, err
		}
		query.Start = parsed
	}
	if raw := strings.TrimSpace(values.Get("end")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return BillingQuery{}, err
		}
		query.End = parsed
	}
	if raw := strings.TrimSpace(values.Get("worker_day_cost_rmb")); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil || parsed <= 0 {
			return BillingQuery{}, err
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
	modelCosts := calculateModelReadyCosts(intervals, query.Start, query.End, query.WorkerDayCostRMB)
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
SELECT worker_id, model, GREATEST(started_at, $1), LEAST(COALESCE(ended_at, $2), $2)
FROM worker_model_ready_intervals
WHERE started_at < $2 AND COALESCE(ended_at, $2) > $1
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
		if err := rows.Scan(&request.ID, &request.RequestID, &request.Time, &request.Model, &request.WorkerID, &request.AppID, &request.TotalTokens); err != nil {
			return nil, err
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
		if row.RequestID == "" {
			continue
		}
		if _, err := tx.ExecContext(runCtx, `
UPDATE request_records
SET cost_by_token_rmb = $1,
    cost_by_request_rmb = $2,
    cost_calculated_at = $3
WHERE request_id = $4 AND event_time = $5 AND model = $6`,
			row.RequestCostByTokenRMB,
			row.RequestCostByRequestRMB,
			now,
			row.RequestID,
			row.Time.UTC(),
			row.Model,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func calculateModelReadyCosts(intervals []billingReadyInterval, start, end time.Time, workerDayCostRMB float64) map[string]*BillingModelSummary {
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
	ratePerSecond := workerDayCostRMB / 86400.0
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
				row.ModelCostRMB += shareSeconds * ratePerSecond
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
		totalModelCost += row.ModelCostRMB
		row.Model = model
	}
	for _, request := range requests {
		requestsByModel[request.Model] = append(requestsByModel[request.Model], request)
		row := ensureBillingModel(models, request.Model)
		row.Requests++
		row.TotalTokens += int64(request.TotalTokens)
	}

	requestCostRows := make([]BillingRequestCostRow, 0, len(requests))
	apps := map[string]*BillingAppSummary{}
	for model, modelRequests := range requestsByModel {
		row := ensureBillingModel(models, model)
		if row.Requests > 0 {
			row.CostPerRequestRMB = row.ModelCostRMB / float64(row.Requests)
		}
		if row.TotalTokens > 0 {
			row.CostPerMillionTokensRMB = row.ModelCostRMB / float64(row.TotalTokens) * 1_000_000
		}
		for _, request := range modelRequests {
			tokenCost := 0.0
			if row.TotalTokens > 0 && request.TotalTokens > 0 {
				tokenCost = row.ModelCostRMB * float64(request.TotalTokens) / float64(row.TotalTokens)
			}
			countCost := 0.0
			if row.Requests > 0 {
				countCost = row.ModelCostRMB / float64(row.Requests)
			}
			requestCost := BillingRequestCostRow{
				RequestID:               request.RequestID,
				Time:                    request.Time,
				Model:                   request.Model,
				AppID:                   request.AppID,
				WorkerID:                request.WorkerID,
				TotalTokens:             request.TotalTokens,
				RequestCostByTokenRMB:   roundMoney(tokenCost),
				RequestCostByRequestRMB: roundMoney(countCost),
			}
			requestCostRows = append(requestCostRows, requestCost)
			if strings.TrimSpace(request.AppID) != "" {
				app := ensureBillingApp(apps, request.AppID)
				app.Requests++
				app.TotalTokens += int64(request.TotalTokens)
				app.RequestCostByTokenRMB += tokenCost
				app.RequestCostByRequestRMB += countCost
			}
		}
	}

	var modelRows []BillingModelSummary
	for _, row := range models {
		if totalReadySeconds > 0 {
			row.ReadyShare = row.ReadySeconds / totalReadySeconds
		}
		if totalModelCost > 0 {
			row.CostShare = row.ModelCostRMB / totalModelCost
		}
		row.ReadySeconds = roundSeconds(row.ReadySeconds)
		row.BillableWorkerSeconds = roundSeconds(row.BillableWorkerSeconds)
		row.ModelCostRMB = roundMoney(row.ModelCostRMB)
		row.CostPerRequestRMB = roundMoney(row.CostPerRequestRMB)
		row.CostPerMillionTokensRMB = roundMoney(row.CostPerMillionTokensRMB)
		modelRows = append(modelRows, *row)
	}
	sort.Slice(modelRows, func(i, j int) bool {
		if modelRows[i].ModelCostRMB == modelRows[j].ModelCostRMB {
			return modelRows[i].Model < modelRows[j].Model
		}
		return modelRows[i].ModelCostRMB > modelRows[j].ModelCostRMB
	})

	var appRows []BillingAppSummary
	for _, row := range apps {
		row.RequestCostByTokenRMB = roundMoney(row.RequestCostByTokenRMB)
		row.RequestCostByRequestRMB = roundMoney(row.RequestCostByRequestRMB)
		appRows = append(appRows, *row)
	}
	sort.Slice(appRows, func(i, j int) bool {
		if appRows[i].RequestCostByTokenRMB == appRows[j].RequestCostByTokenRMB {
			return appRows[i].AppID < appRows[j].AppID
		}
		return appRows[i].RequestCostByTokenRMB > appRows[j].RequestCostByTokenRMB
	})
	sort.Slice(requestCostRows, func(i, j int) bool { return requestCostRows[i].Time.Before(requestCostRows[j].Time) })

	totalRequests := 0
	totalTokens := int64(0)
	totalTokenCost := 0.0
	totalRequestCost := 0.0
	for _, row := range modelRows {
		totalRequests += row.Requests
		totalTokens += row.TotalTokens
	}
	for _, row := range requestCostRows {
		totalTokenCost += row.RequestCostByTokenRMB
		totalRequestCost += row.RequestCostByRequestRMB
	}

	return BillingSummary{
		Start:            query.Start.UTC(),
		End:              query.End.UTC(),
		WorkerDayCostRMB: query.WorkerDayCostRMB,
		Models:           modelRows,
		Apps:             appRows,
		Totals: BillingCostTotals{
			ReadySeconds:            roundSeconds(totalReadySeconds),
			BillableWorkerSeconds:   roundSeconds(totalBillableSeconds),
			ModelCostRMB:            roundMoney(totalModelCost),
			RequestCostByTokenRMB:   roundMoney(totalTokenCost),
			RequestCostByRequestRMB: roundMoney(totalRequestCost),
			Requests:                totalRequests,
			TotalTokens:             totalTokens,
		},
		RequestCosts: requestCostRows,
	}
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

func roundSeconds(value float64) float64 {
	return math.Round(value*1000) / 1000
}
