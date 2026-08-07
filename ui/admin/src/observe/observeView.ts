import type { BillingGroupBy, BillingModelSummary, BillingSummary, ModelBillingConfig, RequestLogEntry, WorkerEvent } from "../api";

export type BillingViewState = {
  billing: BillingSummary | null;
  rangeHours: number;
  groupBy: BillingGroupBy;
  loading: boolean;
  error: string;
  requestID: number;
};

export type BillingViewAction =
  | { type: "start"; requestID: number; rangeHours: number; groupBy: BillingGroupBy }
  | { type: "success"; requestID: number; billing: BillingSummary }
  | { type: "failure"; requestID: number; error: string };

export function reduceBillingView(state: BillingViewState, action: BillingViewAction): BillingViewState {
  if (action.type === "start") {
    return {
      billing: null,
      rangeHours: action.rangeHours,
      groupBy: action.groupBy,
      loading: true,
      error: "",
      requestID: action.requestID
    };
  }
  if (action.requestID !== state.requestID) {
    return state;
  }
  if (action.type === "success") {
    return { ...state, billing: action.billing, loading: false, error: "" };
  }
  return { ...state, billing: null, loading: false, error: action.error };
}

export type ObserveNotice = {
  tone: "neutral" | "bad";
  title: string;
  detail: string;
};

export type RequestRow = {
  id: string;
  time: string;
  route: string;
  status: number;
  latency: string;
  tokens: string;
  detail: string;
};

export type EventRow = {
  id: string;
  time: string;
  subject: string;
  event: string;
  detail: string;
};

export function classifyBillingError(error: string): ObserveNotice | null {
  const normalized = error.trim().toLowerCase();
  if (!normalized) {
    return null;
  }
  if (normalized.includes("records store is not enabled")) {
    return {
      tone: "neutral",
      title: "Billing records are disabled",
      detail: "records store is not enabled"
    };
  }
  return {
    tone: "bad",
    title: "Billing could not load",
    detail: error
  };
}

export function buildRequestRows(requests: RequestLogEntry[]): RequestRow[] {
  return requests.map((request) => ({
    id: request.request_id,
    time: request.time,
    route: [request.model, request.worker_id, request.tag].filter(Boolean).join(" · "),
    status: request.status_code,
    latency: `${request.duration_ms}ms`,
    tokens: compactNumber(request.total_tokens),
    detail: request.error_message || request.finish_reason || (request.stream ? "stream" : "standard")
  }));
}

export function buildEventRows(events: WorkerEvent[]): EventRow[] {
  return events.map((event) => ({
    id: `${event.worker_id}:${event.received_at}:${event.event}`,
    time: event.time || event.received_at,
    subject: [event.model, event.worker_id].filter(Boolean).join(" · "),
    event: event.event,
    detail: event.error || (event.from_state || event.to_state ? `${event.from_state || "-"} -> ${event.to_state || "-"}` : event.object || "")
  }));
}

export function recommendedBillingPricing(
  model: BillingModelSummary | undefined,
  workerDayCostUSD: number | undefined
): ModelBillingConfig {
  const dayCost = Number(workerDayCostUSD ?? 0);
  const durationSeconds = Number(model?.request_duration_seconds ?? 0);
  if (!model || dayCost <= 0 || durationSeconds <= 0) {
    return {};
  }
  const uncachedInputTokens = Math.max(Number(model.input_tokens ?? 0) - Number(model.cached_input_tokens ?? 0), 0);
  return {
    per_request_usd: model.requests > 0 ? roundPricingValue(capacityUnitPrice(dayCost, durationSeconds, model.requests, 1)) : undefined,
    input_per_million_usd: uncachedInputTokens > 0
      ? roundPricingValue(capacityUnitPrice(dayCost, durationSeconds, uncachedInputTokens, 1_000_000))
      : undefined,
    output_per_million_usd: model.output_tokens > 0
      ? roundPricingValue(capacityUnitPrice(dayCost, durationSeconds, model.output_tokens, 1_000_000))
      : undefined,
    cached_input_per_million_usd: model.cached_input_tokens > 0
      ? roundPricingValue(capacityUnitPrice(dayCost, durationSeconds, model.cached_input_tokens, 1_000_000))
      : undefined
  };
}

function capacityUnitPrice(workerDayCostUSD: number, durationSeconds: number, units: number, multiplier: number) {
  if (!Number.isFinite(workerDayCostUSD) || !Number.isFinite(durationSeconds) || !Number.isFinite(units) || units <= 0) {
    return undefined;
  }
  return workerDayCostUSD * durationSeconds * multiplier / (units * 86400);
}

function roundPricingValue(value: number | undefined) {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) {
    return undefined;
  }
  return Math.round(value * 1_000_000) / 1_000_000;
}

function compactNumber(value: number | undefined): string {
  const numberValue = Number(value ?? 0);
  if (!Number.isFinite(numberValue)) {
    return "0";
  }
  if (Math.abs(numberValue) >= 1000) {
    return `${(numberValue / 1000).toFixed(1).replace(/\.0$/, "")}K`;
  }
  return String(Math.round(numberValue));
}
