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

	_ "github.com/lib/pq"
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
  retry_count, upstream_url, request_headers, app_id, source_hash
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12, $13, $14, $15,
  $16, $17, $18, $19, $20, $21, $22,
  $23, $24, $25, $26, $27, $28,
  $29, $30, $31::jsonb, $32, $33
) ON CONFLICT DO NOTHING`,
		entry.RequestID, s.gatewayID, entry.Time.UTC(), entry.Model, entry.WorkerID, entry.Tag, entry.StatusCode, entry.DurationMS,
		entry.Stream, entry.RequestBytes, entry.ResponseBytes, entry.MessageCount, entry.ImageCount, entry.VideoCount, entry.AudioCount,
		entry.MaxTokens, entry.Temperature, entry.TopP, entry.TopK, entry.PromptTokens, entry.CompletionTokens, entry.TotalTokens,
		entry.CacheTokens, entry.ReasoningTokens, entry.FinishReason, entry.ErrorType, entry.ErrorCode, entry.ErrorMessage,
		entry.RetryCount, entry.UpstreamURL, string(headers), entry.RequestHeaders["x-app-id"], strings.TrimSpace(sourceHash),
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
	result, err := s.db.ExecContext(runCtx, `
INSERT INTO worker_events (
  gateway_id, received_at, worker_id, event_time, event, model, from_state, to_state,
  object, kind, downloaded_bytes, total_bytes, percent, duration_ms, error, raw, source_hash
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8,
  $9, $10, $11, $12, $13, $14, $15, $16::jsonb, $17
) ON CONFLICT DO NOTHING`,
		s.gatewayID, event.ReceivedAt.UTC(), event.WorkerID, event.Time.UTC(), event.Event, event.Model, event.FromState, event.ToState,
		event.Object, event.Kind, event.DownloadedBytes, event.TotalBytes, event.Percent, event.DurationMS, event.Error, string(raw), strings.TrimSpace(sourceHash),
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
  retry_count, upstream_url, request_headers
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
	var headers []byte
	err := rows.Scan(
		&entry.RequestID, &entry.Time, &entry.Model, &entry.WorkerID, &entry.Tag, &entry.StatusCode, &entry.DurationMS,
		&entry.Stream, &entry.RequestBytes, &entry.ResponseBytes, &entry.MessageCount, &entry.ImageCount, &entry.VideoCount, &entry.AudioCount,
		&entry.MaxTokens, &temperature, &topP, &topK, &entry.PromptTokens, &entry.CompletionTokens, &entry.TotalTokens,
		&entry.CacheTokens, &entry.ReasoningTokens, &entry.FinishReason, &entry.ErrorType, &entry.ErrorCode, &entry.ErrorMessage,
		&entry.RetryCount, &entry.UpstreamURL, &headers,
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
	return entry, nil
}

func (s *PostgresRecordsStore) context(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.timeout)
}
