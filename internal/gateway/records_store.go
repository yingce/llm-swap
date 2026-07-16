package gateway

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"llm-swap/internal/protocol"

	"github.com/lib/pq"
)

//go:embed migrations/*.sql
var recordsMigrations embed.FS

type RecordsStore interface {
	AppendRequestRecord(context.Context, RequestLogEntry) error
	AppendWorkerEvent(context.Context, uiAgentEvent) error
	PageRequestRecords(context.Context, int, int) (uiRequestsResponse, error)
	PageWorkerEvents(context.Context, int, int) (uiEventsResponse, error)
	Close() error
}

type PostgresRecordsStore struct {
	db        *sql.DB
	gatewayID string
	timeout   time.Duration
}

func NewPostgresRecordsStore(ctx context.Context, dsn string, gatewayID string, timeout time.Duration, autoMigrate bool) (*PostgresRecordsStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres records store dsn is required")
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	if gatewayID == "" {
		gatewayID = defaultGatewayID()
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	store := &PostgresRecordsStore{db: db, gatewayID: gatewayID, timeout: timeout}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if autoMigrate {
		if err := store.Migrate(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return store, nil
}

func defaultGatewayID() string {
	host, err := os.Hostname()
	if err == nil && strings.TrimSpace(host) != "" {
		return strings.TrimSpace(host)
	}
	return "gateway"
}

func (s *PostgresRecordsStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *PostgresRecordsStore) Migrate(ctx context.Context) error {
	data, err := recordsMigrations.ReadFile("migrations/001_records_store.sql")
	if err != nil {
		return err
	}
	for _, stmt := range strings.Split(string(data), "-- statement-breakpoint") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		runCtx, cancel := s.context(ctx)
		_, err := s.db.ExecContext(runCtx, stmt)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresRecordsStore) AppendRequestRecord(ctx context.Context, entry RequestLogEntry) error {
	_, err := s.AppendImportedRequestRecord(ctx, entry, "")
	return err
}

func (s *PostgresRecordsStore) AppendImportedRequestRecord(ctx context.Context, entry RequestLogEntry, sourceHash string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	headers, err := json.Marshal(map[string]string(entry.RequestHeaders))
	if err != nil {
		return false, err
	}
	if string(headers) == "null" {
		headers = []byte("{}")
	}
	runCtx, cancel := s.context(ctx)
	defer cancel()
	result, err := s.db.ExecContext(runCtx, `
INSERT INTO request_records (
  request_id, gateway_id, event_time, model, worker_id, tag, status_code, duration_ms,
  stream, request_bytes, response_bytes, message_count, image_count, video_count, audio_count,
  max_tokens, temperature, top_p, top_k, prompt_tokens, completion_tokens, total_tokens,
  cache_tokens, reasoning_tokens, finish_reason, error_type, error_code, error_message,
  retry_count, upstream_url, request_headers, app_id, source_hash, model_used_cost_usd,
  billing_per_request_usd, billing_input_per_million_usd, billing_output_per_million_usd,
  billing_cached_input_per_million_usd, cost_calculated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12, $13, $14, $15,
  $16, $17, $18, $19, $20, $21, $22,
  $23, $24, $25, $26, $27, $28,
  $29, $30, $31::jsonb, $32, $33, $34,
  $35, $36, $37, $38, $39
) ON CONFLICT DO NOTHING`,
		entry.RequestID, s.gatewayID, entry.Time.UTC(), entry.Model, entry.WorkerID, entry.Tag, entry.StatusCode, entry.DurationMS,
		entry.Stream, entry.RequestBytes, entry.ResponseBytes, entry.MessageCount, entry.ImageCount, entry.VideoCount, entry.AudioCount,
		entry.MaxTokens, entry.Temperature, entry.TopP, entry.TopK, entry.PromptTokens, entry.CompletionTokens, entry.TotalTokens,
		entry.CacheTokens, entry.ReasoningTokens, entry.FinishReason, entry.ErrorType, entry.ErrorCode, entry.ErrorMessage,
		entry.RetryCount, entry.UpstreamURL, string(headers), entry.RequestHeaders["x-app-id"], strings.TrimSpace(sourceHash), entry.ModelUsedCostUSD,
		entry.BillingPerRequestUSD, entry.BillingInputPerMillionUSD, entry.BillingOutputPerMillionUSD, entry.BillingCachedInputPerMillionUSD, entry.CostCalculatedAt,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return true, nil
	}
	return affected > 0, nil
}

func (s *PostgresRecordsStore) AppendWorkerEvent(ctx context.Context, event uiAgentEvent) error {
	_, err := s.AppendImportedWorkerEvent(ctx, event, "")
	return err
}

func (s *PostgresRecordsStore) AppendImportedWorkerEvent(ctx context.Context, event WorkerEventRecord, sourceHash string) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return false, err
	}
	runCtx, cancel := s.context(ctx)
	defer cancel()
	var eventID int64
	err = s.db.QueryRowContext(runCtx, `
INSERT INTO worker_events (
  gateway_id, received_at, worker_id, event_time, event, model, from_state, to_state,
  object, kind, downloaded_bytes, total_bytes, percent, duration_ms, error, raw, source_hash
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12, $13, $14, $15, $16::jsonb, $17
) ON CONFLICT DO NOTHING
RETURNING id`,
		s.gatewayID, event.ReceivedAt.UTC(), event.WorkerID, event.Time.UTC(), event.Event, event.Model, event.FromState, event.ToState,
		event.Object, event.Kind, event.DownloadedBytes, event.TotalBytes, event.Percent, event.DurationMS, event.Error, string(raw), strings.TrimSpace(sourceHash),
	).Scan(&eventID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if err := s.applyWorkerModelReadyEvent(runCtx, event, eventID); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresRecordsStore) applyWorkerModelReadyEvent(ctx context.Context, event WorkerEventRecord, eventID int64) error {
	workerID := strings.TrimSpace(event.WorkerID)
	model := strings.TrimSpace(event.Model)
	if workerID == "" || model == "" {
		return nil
	}
	eventTime := event.Time
	if eventTime.IsZero() {
		eventTime = event.ReceivedAt
	}
	if eventTime.IsZero() {
		eventTime = time.Now()
	}
	switch {
	case opensReadyInterval(event):
		_, err := s.db.ExecContext(ctx, `
INSERT INTO worker_model_ready_intervals (
  gateway_id, worker_id, model, started_at, share_ratio, source_event_id, last_seen_at
)
SELECT $1, $2, $3, $4, 1, $5, $4
WHERE NOT EXISTS (
  SELECT 1 FROM worker_model_ready_intervals
  WHERE source_event_id = $5
) AND NOT EXISTS (
  SELECT 1 FROM worker_model_ready_intervals
  WHERE worker_id = $2 AND model = $3 AND started_at = $4
) AND NOT EXISTS (
  SELECT 1 FROM worker_model_ready_intervals
  WHERE worker_id = $2 AND model = $3 AND ended_at IS NULL
)`, s.gatewayID, workerID, model, eventTime.UTC(), eventID)
		return err
	case closesReadyInterval(event):
		_, err := s.db.ExecContext(ctx, `
UPDATE worker_model_ready_intervals
SET ended_at = $3, close_event_id = $4, last_seen_at = $3, updated_at = now()
WHERE worker_id = $1 AND model = $2 AND ended_at IS NULL AND started_at <= $3`,
			workerID, model, eventTime.UTC(), eventID)
		return err
	default:
		return nil
	}
}

func opensReadyInterval(event WorkerEventRecord) bool {
	switch event.Event {
	case "model_loaded", "gateway_model_warm_done":
		return true
	case "model_state_changed":
		return readyState(event.ToState)
	default:
		return false
	}
}

func closesReadyInterval(event WorkerEventRecord) bool {
	switch event.Event {
	case "model_unloaded", "gateway_model_unload_done":
		return true
	case "model_state_changed":
		return readyState(event.FromState) && !readyState(event.ToState)
	default:
		return false
	}
}

func readyState(state string) bool {
	return strings.EqualFold(strings.TrimSpace(state), "ready")
}

func (s *PostgresRecordsStore) RecordWorkerModelSnapshot(ctx context.Context, workerID string, models []protocol.RunningModel, seenAt time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil
	}
	if seenAt.IsZero() {
		seenAt = time.Now()
	}
	ready := map[string]bool{}
	for _, model := range models {
		name := strings.TrimSpace(model.Model)
		if name == "" || !readyState(model.State) {
			continue
		}
		ready[name] = true
	}
	runCtx, cancel := s.context(ctx)
	defer cancel()
	tx, err := s.db.BeginTx(runCtx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for model := range ready {
		if _, err := tx.ExecContext(runCtx, `
UPDATE worker_model_ready_intervals
SET ended_at = started_at,
    last_seen_at = started_at,
    updated_at = now()
WHERE worker_id = $1
  AND model = $2
  AND ended_at IS NULL
  AND last_seen_at IS NULL
  AND started_at < $3`,
			workerID, model, seenAt.UTC()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(runCtx, `
INSERT INTO worker_model_ready_intervals (
  gateway_id, worker_id, model, started_at, share_ratio, last_seen_at
)
SELECT $1::text, $2::text, $3::text, $4::timestamptz, 1, $4::timestamptz
WHERE NOT EXISTS (
  SELECT 1 FROM worker_model_ready_intervals
  WHERE worker_id = $2::text AND model = $3::text AND ended_at IS NULL
)`, s.gatewayID, workerID, model, seenAt.UTC()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(runCtx, `
UPDATE worker_model_ready_intervals
SET last_seen_at = $3, updated_at = now()
WHERE worker_id = $1 AND model = $2 AND ended_at IS NULL AND started_at <= $3`,
			workerID, model, seenAt.UTC()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(runCtx, `
UPDATE worker_model_ready_intervals
SET ended_at = $2, last_seen_at = $2, updated_at = now()
WHERE worker_id = $1
  AND ended_at IS NULL
  AND NOT (model = ANY($3::text[]))`,
		workerID, seenAt.UTC(), pq.Array(mapKeys(ready))); err != nil {
		return err
	}
	return tx.Commit()
}

func mapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func (s *PostgresRecordsStore) PageRequestRecords(ctx context.Context, offset int, limit int) (uiRequestsResponse, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = uiEventLimit
	}
	if limit > 500 {
		limit = 500
	}
	runCtx, cancel := s.context(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(runCtx, `
SELECT request_id, event_time, model, worker_id, tag, status_code, duration_ms,
  stream, request_bytes, response_bytes, message_count, image_count, video_count, audio_count,
  max_tokens, temperature, top_p, top_k, prompt_tokens, completion_tokens, total_tokens,
  cache_tokens, reasoning_tokens, finish_reason, error_type, error_code, error_message,
  retry_count, upstream_url, request_headers, cost_by_token_rmb::float8, cost_by_request_rmb::float8,
  billing_per_request_usd::float8, billing_input_per_million_usd::float8,
  billing_output_per_million_usd::float8, billing_cached_input_per_million_usd::float8,
  model_used_cost_usd::float8, cost_calculated_at
FROM request_records
ORDER BY id DESC
OFFSET $1 LIMIT $2`, offset, limit+1)
	if err != nil {
		return uiRequestsResponse{}, err
	}
	defer rows.Close()
	requests := make([]RequestLogEntry, 0, limit+1)
	for rows.Next() {
		entry, err := scanRequestRecord(rows)
		if err != nil {
			return uiRequestsResponse{}, err
		}
		requests = append(requests, entry)
	}
	if err := rows.Err(); err != nil {
		return uiRequestsResponse{}, err
	}
	hasMore := len(requests) > limit
	if hasMore {
		requests = requests[:limit]
	}
	return uiRequestsResponse{Requests: requests, NextOffset: offset + len(requests), HasMore: hasMore}, nil
}

func (s *PostgresRecordsStore) PageWorkerEvents(ctx context.Context, offset int, limit int) (uiEventsResponse, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = uiEventLimit
	}
	if limit > 500 {
		limit = 500
	}
	runCtx, cancel := s.context(ctx)
	defer cancel()
	rows, err := s.db.QueryContext(runCtx, `
SELECT received_at, worker_id, event_time, event, model, from_state, to_state,
  object, kind, downloaded_bytes, total_bytes, percent, duration_ms, error
FROM worker_events
ORDER BY id DESC
OFFSET $1 LIMIT $2`, offset, limit+1)
	if err != nil {
		return uiEventsResponse{}, err
	}
	defer rows.Close()
	events := make([]uiAgentEvent, 0, limit+1)
	for rows.Next() {
		var event uiAgentEvent
		if err := rows.Scan(
			&event.ReceivedAt, &event.WorkerID, &event.Time, &event.Event, &event.Model, &event.FromState, &event.ToState,
			&event.Object, &event.Kind, &event.DownloadedBytes, &event.TotalBytes, &event.Percent, &event.DurationMS, &event.Error,
		); err != nil {
			return uiEventsResponse{}, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return uiEventsResponse{}, err
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	return uiEventsResponse{Events: events, NextOffset: offset + len(events), HasMore: hasMore}, nil
}

func scanRequestRecord(rows *sql.Rows) (RequestLogEntry, error) {
	var entry RequestLogEntry
	var temperature, topP, topK sql.NullFloat64
	var costByToken, costByRequest, billingPerRequestUSD, billingInputPerMillionUSD, billingOutputPerMillionUSD, billingCachedInputPerMillionUSD, modelUsedCostUSD sql.NullFloat64
	var costCalculatedAt sql.NullTime
	var headers []byte
	err := rows.Scan(
		&entry.RequestID, &entry.Time, &entry.Model, &entry.WorkerID, &entry.Tag, &entry.StatusCode, &entry.DurationMS,
		&entry.Stream, &entry.RequestBytes, &entry.ResponseBytes, &entry.MessageCount, &entry.ImageCount, &entry.VideoCount, &entry.AudioCount,
		&entry.MaxTokens, &temperature, &topP, &topK, &entry.PromptTokens, &entry.CompletionTokens, &entry.TotalTokens,
		&entry.CacheTokens, &entry.ReasoningTokens, &entry.FinishReason, &entry.ErrorType, &entry.ErrorCode, &entry.ErrorMessage,
		&entry.RetryCount, &entry.UpstreamURL, &headers, &costByToken, &costByRequest,
		&billingPerRequestUSD, &billingInputPerMillionUSD, &billingOutputPerMillionUSD, &billingCachedInputPerMillionUSD,
		&modelUsedCostUSD, &costCalculatedAt,
	)
	if err != nil {
		return RequestLogEntry{}, err
	}
	if temperature.Valid {
		entry.Temperature = &temperature.Float64
	}
	if topP.Valid {
		entry.TopP = &topP.Float64
	}
	if topK.Valid {
		entry.TopK = &topK.Float64
	}
	if len(headers) > 0 {
		_ = json.Unmarshal(headers, &entry.RequestHeaders)
	}
	if costByToken.Valid {
		entry.CostByTokenRMB = costByToken.Float64
	}
	if costByRequest.Valid {
		entry.CostByRequestRMB = costByRequest.Float64
	}
	if billingPerRequestUSD.Valid {
		entry.BillingPerRequestUSD = billingPerRequestUSD.Float64
	}
	if billingInputPerMillionUSD.Valid {
		entry.BillingInputPerMillionUSD = billingInputPerMillionUSD.Float64
	}
	if billingOutputPerMillionUSD.Valid {
		entry.BillingOutputPerMillionUSD = billingOutputPerMillionUSD.Float64
	}
	if billingCachedInputPerMillionUSD.Valid {
		entry.BillingCachedInputPerMillionUSD = billingCachedInputPerMillionUSD.Float64
	}
	if modelUsedCostUSD.Valid {
		entry.ModelUsedCostUSD = modelUsedCostUSD.Float64
	}
	if costCalculatedAt.Valid {
		calculatedAt := costCalculatedAt.Time
		entry.CostCalculatedAt = &calculatedAt
	}
	return entry, nil
}

func (s *PostgresRecordsStore) context(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.timeout)
}
