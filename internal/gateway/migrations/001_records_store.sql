CREATE TABLE IF NOT EXISTS request_records (
  id BIGSERIAL PRIMARY KEY,
  request_id TEXT NOT NULL,
  gateway_id TEXT NOT NULL DEFAULT '',
  event_time TIMESTAMPTZ NOT NULL,
  model TEXT NOT NULL,
  worker_id TEXT NOT NULL DEFAULT '',
  tag TEXT NOT NULL DEFAULT '',
  status_code INTEGER NOT NULL DEFAULT 0,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  stream BOOLEAN NOT NULL DEFAULT FALSE,
  request_bytes BIGINT NOT NULL DEFAULT 0,
  response_bytes BIGINT NOT NULL DEFAULT 0,
  message_count INTEGER NOT NULL DEFAULT 0,
  image_count INTEGER NOT NULL DEFAULT 0,
  video_count INTEGER NOT NULL DEFAULT 0,
  audio_count INTEGER NOT NULL DEFAULT 0,
  max_tokens INTEGER NOT NULL DEFAULT 0,
  temperature DOUBLE PRECISION,
  top_p DOUBLE PRECISION,
  top_k DOUBLE PRECISION,
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  cache_tokens INTEGER NOT NULL DEFAULT 0,
  reasoning_tokens INTEGER NOT NULL DEFAULT 0,
  finish_reason TEXT NOT NULL DEFAULT '',
  error_type TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  retry_count INTEGER NOT NULL DEFAULT 0,
  upstream_url TEXT NOT NULL DEFAULT '',
  request_headers JSONB NOT NULL DEFAULT '{}'::jsonb,
  app_id TEXT NOT NULL DEFAULT '',
  source_hash TEXT NOT NULL DEFAULT '',
  model_used_cost_usd NUMERIC(18, 6) NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- statement-breakpoint
ALTER TABLE request_records ADD COLUMN IF NOT EXISTS source_hash TEXT NOT NULL DEFAULT '';
-- statement-breakpoint
ALTER TABLE request_records ADD COLUMN IF NOT EXISTS cost_by_token_rmb NUMERIC(18, 6) NOT NULL DEFAULT 0;
-- statement-breakpoint
ALTER TABLE request_records ADD COLUMN IF NOT EXISTS cost_by_request_rmb NUMERIC(18, 6) NOT NULL DEFAULT 0;
-- statement-breakpoint
ALTER TABLE request_records ADD COLUMN IF NOT EXISTS model_used_cost_usd NUMERIC(18, 6) NOT NULL DEFAULT 0;
-- statement-breakpoint
ALTER TABLE request_records ADD COLUMN IF NOT EXISTS cost_calculated_at TIMESTAMPTZ;
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS request_records_event_time_idx ON request_records (event_time DESC);
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS request_records_model_time_idx ON request_records (model, event_time DESC);
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS request_records_worker_time_idx ON request_records (worker_id, event_time DESC);
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS request_records_app_time_idx ON request_records (app_id, event_time DESC) WHERE app_id <> '';
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS request_records_request_id_idx ON request_records (request_id);
-- statement-breakpoint
CREATE UNIQUE INDEX IF NOT EXISTS request_records_source_hash_idx ON request_records (source_hash) WHERE source_hash <> '';
-- statement-breakpoint
CREATE TABLE IF NOT EXISTS worker_events (
  id BIGSERIAL PRIMARY KEY,
  gateway_id TEXT NOT NULL DEFAULT '',
  received_at TIMESTAMPTZ NOT NULL,
  worker_id TEXT NOT NULL,
  event_time TIMESTAMPTZ NOT NULL,
  event TEXT NOT NULL,
  model TEXT NOT NULL DEFAULT '',
  from_state TEXT NOT NULL DEFAULT '',
  to_state TEXT NOT NULL DEFAULT '',
  object TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT '',
  downloaded_bytes BIGINT NOT NULL DEFAULT 0,
  total_bytes BIGINT NOT NULL DEFAULT 0,
  percent DOUBLE PRECISION NOT NULL DEFAULT 0,
  duration_ms BIGINT NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  raw JSONB NOT NULL DEFAULT '{}'::jsonb,
  source_hash TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- statement-breakpoint
ALTER TABLE worker_events ADD COLUMN IF NOT EXISTS source_hash TEXT NOT NULL DEFAULT '';
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS worker_events_received_at_idx ON worker_events (received_at DESC);
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS worker_events_worker_time_idx ON worker_events (worker_id, received_at DESC);
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS worker_events_model_time_idx ON worker_events (model, received_at DESC) WHERE model <> '';
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS worker_events_event_time_idx ON worker_events (event, received_at DESC);
-- statement-breakpoint
CREATE UNIQUE INDEX IF NOT EXISTS worker_events_source_hash_idx ON worker_events (source_hash) WHERE source_hash <> '';
-- statement-breakpoint
CREATE TABLE IF NOT EXISTS worker_model_ready_intervals (
  id BIGSERIAL PRIMARY KEY,
  gateway_id TEXT NOT NULL DEFAULT '',
  worker_id TEXT NOT NULL,
  model TEXT NOT NULL,
  started_at TIMESTAMPTZ NOT NULL,
  ended_at TIMESTAMPTZ,
  share_ratio NUMERIC(12, 6) NOT NULL DEFAULT 1,
  source_event_id BIGINT REFERENCES worker_events(id),
  close_event_id BIGINT REFERENCES worker_events(id),
  last_seen_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (ended_at IS NULL OR ended_at >= started_at)
);
-- statement-breakpoint
ALTER TABLE worker_model_ready_intervals ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS worker_model_ready_intervals_model_time_idx ON worker_model_ready_intervals (model, started_at, ended_at);
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS worker_model_ready_intervals_worker_time_idx ON worker_model_ready_intervals (worker_id, started_at, ended_at);
-- statement-breakpoint
CREATE INDEX IF NOT EXISTS worker_model_ready_intervals_open_idx ON worker_model_ready_intervals (worker_id, model) WHERE ended_at IS NULL;
