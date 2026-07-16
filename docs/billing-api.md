# Billing API

`GET /api/billing` returns model occupancy cost, app/request allocation, and
model capacity-based token prices from Postgres records.

Authentication uses the gateway agent/UI token.

## Query Parameters

- `start`: optional range start.
- `end`: optional range end.
- `day`: optional natural day, format `YYYY-MM-DD`.
- `date`: alias for `day`.
- `hour`: optional natural hour, format `YYYY-MM-DDTHH` or `YYYY-MM-DD HH`.
- `worker_day_cost_rmb`: optional worker cost per 24 hours. Defaults to `55`.
- `include_requests`: optional boolean. `1`, `true`, `yes`, and `on` include
  per-request cost rows.
- `persist`: optional boolean. When true, writes calculated request costs back
  to `request_records`.

`start` and `end` override `day`/`date`/`hour` when they are provided.
`hour` overrides `day`/`date`.

## Time Parsing

Timezone-aware values keep their own timezone:

```text
start=2026-07-16T00:00:00+08:00
end=2026-07-17T00:00:00+08:00
```

Values without timezone are interpreted as UTC+8:

```text
start=2026-07-16 00:00:00
end=2026-07-17 00:00:00
```

Supported no-timezone layouts:

```text
YYYY-MM-DD
YYYY-MM-DD HH
YYYY-MM-DDTHH
YYYY-MM-DD HH:mm
YYYY-MM-DDTHH:mm
YYYY-MM-DD HH:mm:ss
YYYY-MM-DDTHH:mm:ss
```

Natural day and hour are also interpreted in UTC+8:

```text
/api/billing?day=2026-07-16
/api/billing?hour=2026-07-16T13
```

The response `start` and `end` are serialized in UTC. For example,
`day=2026-07-16` returns `2026-07-15T16:00:00Z` to
`2026-07-16T16:00:00Z`.

## Cost Semantics

`ready_seconds` is based on `worker_model_ready_intervals`.

The gateway updates intervals incrementally from each worker heartbeat:

- ready running models open or refresh an interval for that worker/model;
- missing ready models close open intervals for that worker;
- open intervals are billed only until their latest `last_seen_at`;
- if a worker stops reporting, time after the last heartbeat is not counted.

`billable_worker_seconds` splits time evenly when the same worker reports
multiple ready models.

`model_cost_rmb` is actual ready occupancy cost:

```text
model_cost_rmb = billable_worker_seconds / 86400 * worker_day_cost_rmb
```

`cost_per_request_rmb` and `cost_per_million_tokens_rmb` allocate that actual
occupancy cost across requests observed in the selected time range.

`capacity_90` is a separate capacity price estimate. It does not allocate actual
usage. For successful requests with positive `duration_ms`, the gateway computes
observed throughput per model:

```text
observed_tps = tokens / sum(duration_ms / 1000)
daily_capacity = observed_tps * 86400 * 0.90
token_unit_price_rmb = worker_day_cost_rmb / daily_capacity
cost_per_million_tokens_rmb = token_unit_price_rmb * 1000000
```

Input, output, and cache tokens are calculated separately:

- `input_cost_per_million_tokens_rmb`
- `output_cost_per_million_tokens_rmb`
- `cache_cost_per_million_tokens_rmb`

## Examples

Natural day in UTC+8:

```bash
curl -H "Authorization: Bearer $AGENT_TOKEN" \
  "http://gateway:8080/api/billing?day=2026-07-16"
```

Natural hour in UTC+8:

```bash
curl -H "Authorization: Bearer $AGENT_TOKEN" \
  "http://gateway:8080/api/billing?hour=2026-07-16T13"
```

Explicit timezone-aware range:

```bash
curl -H "Authorization: Bearer $AGENT_TOKEN" \
  "http://gateway:8080/api/billing?start=2026-07-16T00:00:00%2B08:00&end=2026-07-17T00:00:00%2B08:00"
```

Local UTC+8 range without timezone, including per-request rows:

```bash
curl -H "Authorization: Bearer $AGENT_TOKEN" \
  "http://gateway:8080/api/billing?start=2026-07-16%2000:00:00&end=2026-07-17%2000:00:00&include_requests=1"
```

Override daily worker cost:

```bash
curl -H "Authorization: Bearer $AGENT_TOKEN" \
  "http://gateway:8080/api/billing?day=2026-07-16&worker_day_cost_rmb=60"
```
