# Billing API

`GET /api/billing` returns model occupancy cost, configured usage cost, idle
cost, app usage, and per-request usage cost from Postgres records.

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
  to `request_records.model_used_cost_usd` and updates `cost_calculated_at`.

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

All returned cost fields use USD unless the field name explicitly says `rmb`.
The response includes:

- `currency`: currently always `USD`.
- `exchange_rate_cny_to_usd`: CNY to USD rate used for this response.
- `exchange_rate_time`: rate date/time when available.
- `exchange_rate_stale`: true when the gateway used a stale cached rate or the
  fallback rate.
- `worker_day_cost_rmb`: the configured machine cost input.
- `worker_day_cost_usd`: `worker_day_cost_rmb * exchange_rate_cny_to_usd`.

The gateway refreshes CNY/USD from
`https://api.frankfurter.dev/v2/rates?base=CNY&quotes=USD` at most once every
10 minutes. If the refresh fails, it uses the last successful rate. If there is
no successful rate yet, it falls back to `1 CNY = 0.14 USD` and marks
`exchange_rate_stale=true`.

`ready_seconds` is based on `worker_model_ready_intervals`.

The gateway updates intervals incrementally from each worker heartbeat:

- ready running models open or refresh an interval for that worker/model;
- missing ready models close open intervals for that worker;
- open intervals are billed only until their latest `last_seen_at`;
- if a worker stops reporting, time after the last heartbeat is not counted.

`billable_worker_seconds` splits time evenly when the same worker reports
multiple ready models.

`model_cost` is actual ready occupancy cost:

```text
model_cost = billable_worker_seconds / 86400 * worker_day_cost_usd
```

`model_used_cost` is calculated from the configured per-model customer pricing,
not from machine idle cost. Configure it under each model:

```yaml
models:
  Qwen:
    billing:
      per_request_usd: 0.001
      input_per_million_usd: 0.2
      output_per_million_usd: 0.8
      cached_input_per_million_usd: 0.05
```

Per-request configured usage cost:

```text
model_used_cost =
  per_request_usd
  + input_tokens / 1000000 * input_per_million_usd
  + output_tokens / 1000000 * output_per_million_usd
  + cached_input_tokens / 1000000 * cached_input_per_million_usd
```

`model_idle_cost` is the unbilled/over-covered part of occupancy cost:

```text
model_idle_cost = model_cost - model_used_cost
```

This value can be negative when configured customer usage cost exceeds machine
occupancy cost.

`models[]`, `apps[]`, and `request_costs[]` report token counts separately:

- `input_tokens`
- `output_tokens`
- `cached_input_tokens`
- `total_tokens`

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
