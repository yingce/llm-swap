export type Summary = {
  total_workers: number;
  healthy_workers: number;
  draining_workers: number;
  available_models: number;
  configured_models: number;
  underprovisioned_models: number;
  active_requests: number;
  stale_workers: number;
  workers_with_errors: number;
  recent_error_events: number;
};

export type ModelStatus = {
  name: string;
  priority: number;
  min_loaded: number;
  max_loaded: number;
  max_concurrency: number;
  max_queue: number;
  available: boolean;
  ready_workers: number;
  running_workers: number;
  artifact: { object: string; kind: string };
  availability_note: string;
  traffic: {
    last_access?: string;
    requests: number;
    status_2xx: number;
    status_4xx: number;
    status_5xx: number;
    prompt_tokens: number;
    completion_tokens: number;
    total_tokens: number;
    cache_tokens: number;
    reasoning_tokens: number;
    avg_duration_ms: number;
    max_duration_ms: number;
  };
  worker_statuses: {
    worker_id: string;
    artifact_status: string;
    running_state?: string;
    health: string;
    cooldown_active: boolean;
  }[];
};

export type WorkerStatus = {
  id: string;
  tags: string[];
  health: string;
  state: string;
  llama_swap_url: string;
  last_heartbeat?: string;
  last_heartbeat_age_ms?: number;
  active_requests: number;
  running_models: { model: string; state: string }[];
  gpu_devices: GPUDevice[];
  allowed_models: string[];
  artifacts?: Record<string, string>;
  capacity: WorkerDefaultsConfig;
  needs_restart?: boolean;
  last_error?: string;
  scrape_failures: number;
  scrape_backoff_seconds?: number;
  health_problem?: string;
  agent_build: BuildInfo;
  agent_version_status: "current" | "outdated" | "legacy";
};

export type BuildInfo = {
  version?: string;
  commit?: string;
  build_time?: string;
  protocol_version?: number;
};

export type GPUDevice = {
  index: number;
  name: string;
  uuid?: string;
  memory_total_mib: number;
  memory_used_mib: number;
  memory_free_mib: number;
  utilization_percent: number;
  temperature_celsius: number;
};

export type WorkerEvent = {
  received_at: string;
  worker_id: string;
  event: string;
  model?: string;
  from_state?: string;
  to_state?: string;
  object?: string;
  error?: string;
  downloaded_bytes?: number;
  total_bytes?: number;
  percent?: number;
  duration_ms?: number;
};

export type RequestLogEntry = {
  time: string;
  request_id: string;
  model: string;
  worker_id?: string;
  tag?: string;
  status_code: number;
  duration_ms: number;
  stream: boolean;
  request_bytes: number;
  response_bytes: number;
  message_count?: number;
  image_count?: number;
  video_count?: number;
  audio_count?: number;
  max_tokens?: number;
  temperature?: number;
  top_p?: number;
  top_k?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  cache_tokens?: number;
  reasoning_tokens?: number;
  finish_reason?: string;
  error_type?: string;
  error_code?: string;
  error_message?: string;
  retry_count?: number;
  upstream_url?: string;
  cost_by_token_rmb?: number;
  cost_by_request_rmb?: number;
  cost_calculated_at?: string;
};

export type StatusResponse = {
  generated_at: string;
  summary: Summary;
  models: ModelStatus[];
  workers: WorkerStatus[];
  events: WorkerEvent[];
};

export type EventsResponse = {
  events: WorkerEvent[];
  next_offset: number;
  has_more: boolean;
};

export type RequestsResponse = {
  requests: RequestLogEntry[];
  next_offset: number;
  has_more: boolean;
};

export type ConfigChange = {
  path: string;
  type: string;
  model?: string;
  requires_worker_restart: boolean;
  requires_gateway_restart: boolean;
  detail?: string;
};

export type ConfigImpact = {
  model: string;
  worker_id: string;
  running_state?: string;
  loaded: boolean;
  requires_worker_restart: boolean;
  reason?: string;
};

export type ArtifactConfig = {
  object: string;
  kind: string;
  crc64ecma: string;
};

export type ModelConfig = {
  priority: number;
  min_loaded: number;
  max_loaded: number;
  max_concurrency: number;
  max_queue: number;
  queue_timeout_ms: number;
  ttl: number;
  artifact: ArtifactConfig;
  run: string;
  runtime?: string;
  runtime_args?: string[];
  cmd_stop?: string;
  check_endpoint?: string;
};

export type WorkerDefaultsConfig = {
  max_concurrency: number;
  max_queue: number;
};

export type TagPolicyConfig = {
  max_concurrency: number;
  max_queue: number;
  worker_defaults: WorkerDefaultsConfig;
  allowed_models: string[];
  warm_when_idle: string;
};

export type GatewayConfigView = {
  models: Record<string, ModelConfig>;
  tag_policies: Record<string, TagPolicyConfig>;
};

export type ConfigResponse = {
  version: number;
  yaml: string;
  config: GatewayConfigView;
};

export type ConfigDryRunResponse = {
  valid: boolean;
  version: number;
  changes: ConfigChange[];
  impacts: ConfigImpact[];
  apply_mode: "hot_apply" | "save_requires_gateway_restart";
  requires_gateway_restart: boolean;
  error?: string;
};

export type MetricsPoint = {
  t: number;
  v: number;
};

export type MetricsSeries = {
  name: string;
  metric: Record<string, string>;
  points: MetricsPoint[];
};

export type MetricsResponse = {
  range: string;
  step: string;
  series: MetricsSeries[];
};

export type BillingSummary = {
  start: string;
  end: string;
  worker_day_cost_rmb: number;
  totals: {
    ready_seconds: number;
    billable_worker_seconds: number;
    model_cost_rmb: number;
    request_cost_by_token_rmb: number;
    request_cost_by_request_rmb: number;
    requests: number;
    total_tokens: number;
  };
  models: BillingModelSummary[];
  apps: BillingAppSummary[];
  request_costs?: BillingRequestCost[];
};

export type BillingModelSummary = {
  model: string;
  ready_seconds: number;
  billable_worker_seconds: number;
  ready_share: number;
  cost_share: number;
  model_cost_rmb: number;
  requests: number;
  total_tokens: number;
  cost_per_request_rmb: number;
  cost_per_million_tokens_rmb: number;
};

export type BillingAppSummary = {
  app_id: string;
  requests: number;
  total_tokens: number;
  request_cost_by_token_rmb: number;
  request_cost_by_request_rmb: number;
};

export type BillingRequestCost = {
  request_id: string;
  time: string;
  model: string;
  app_id?: string;
  worker_id?: string;
  total_tokens: number;
  request_cost_by_token_rmb: number;
  request_cost_by_request_rmb: number;
};

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: "same-origin",
    ...init
  });
  if (!response.ok) {
    const message = await response.text();
    throw new Error(message || response.statusText);
  }
  return response.json() as Promise<T>;
}

function normalizeWorker(worker: WorkerStatus): WorkerStatus {
  return {
    ...worker,
    tags: worker.tags ?? [],
    running_models: worker.running_models ?? [],
    gpu_devices: worker.gpu_devices ?? [],
    capacity: worker.capacity ?? { max_concurrency: 0, max_queue: 0 },
    allowed_models: worker.allowed_models ?? [],
    agent_build: worker.agent_build ?? {},
    agent_version_status: worker.agent_version_status ?? "legacy"
  };
}

function normalizeStatus(status: StatusResponse): StatusResponse {
  return {
    ...status,
    models: (status.models ?? []).map((model) => ({
      ...model,
      worker_statuses: model.worker_statuses ?? []
    })),
    workers: (status.workers ?? []).map(normalizeWorker),
    events: status.events ?? []
  };
}

export async function getStatus(): Promise<StatusResponse> {
  return normalizeStatus(await request<StatusResponse>("/ui/status"));
}

export async function getEvents(offset = 0, limit = 50): Promise<EventsResponse> {
  const response = await request<EventsResponse>(`/ui/events?offset=${offset}&limit=${limit}`);
  return {
    ...response,
    events: response.events ?? []
  };
}

export async function getRequests(offset = 0, limit = 50): Promise<RequestsResponse> {
  const response = await request<RequestsResponse>(`/ui/requests?offset=${offset}&limit=${limit}`);
  return {
    ...response,
    requests: response.requests ?? []
  };
}

export function getConfig(): Promise<ConfigResponse> {
  return request<ConfigResponse>("/ui/api/config");
}

export function dryRunConfig(yaml: string): Promise<ConfigDryRunResponse> {
  return request<ConfigDryRunResponse>("/ui/api/config/dry-run", {
    method: "POST",
    body: yaml
  });
}

export function applyConfig(yaml: string): Promise<ConfigDryRunResponse> {
  return request<ConfigDryRunResponse>("/ui/api/config/apply", {
    method: "POST",
    body: yaml
  });
}

export function getSummaryMetrics(range: string): Promise<MetricsResponse> {
  return request<MetricsResponse>(`/ui/metrics/summary?range=${encodeURIComponent(range)}&step=1m`);
}

export function getBilling(rangeHours = 24, includeRequests = false): Promise<BillingSummary> {
  const end = new Date();
  const start = new Date(end.getTime() - rangeHours * 60 * 60 * 1000);
  const params = new URLSearchParams({
    start: start.toISOString(),
    end: end.toISOString(),
    worker_day_cost_rmb: "55"
  });
  if (includeRequests) {
    params.set("include_requests", "1");
  }
  return request<BillingSummary>(`/ui/api/billing?${params.toString()}`);
}

export type AdminActionResponse = {
  action: string;
  result: string;
  worker_id?: string;
  model?: string;
  error?: string;
};

export function drainWorker(workerId: string): Promise<AdminActionResponse> {
  return request<AdminActionResponse>(`/ui/api/workers/${encodeURIComponent(workerId)}/drain`, {
    method: "POST",
    body: "{}"
  });
}

export function undrainWorker(workerId: string): Promise<AdminActionResponse> {
  return request<AdminActionResponse>(`/ui/api/workers/${encodeURIComponent(workerId)}/undrain`, {
    method: "POST",
    body: "{}"
  });
}

export function warmModel(model: string, workerId: string): Promise<AdminActionResponse> {
  return request<AdminActionResponse>(`/ui/api/models/${encodeURIComponent(model)}/warm`, {
    method: "POST",
    body: JSON.stringify({ worker_id: workerId })
  });
}

export function unloadModel(model: string, workerId: string): Promise<AdminActionResponse> {
  return request<AdminActionResponse>(`/ui/api/models/${encodeURIComponent(model)}/unload`, {
    method: "POST",
    body: JSON.stringify({ worker_id: workerId })
  });
}
