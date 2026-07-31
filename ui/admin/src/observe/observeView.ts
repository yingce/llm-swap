import type { RequestLogEntry, WorkerEvent } from "../api";

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
