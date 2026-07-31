import type { StatusResponse } from "../api";
import { buildAttentionItems, type AttentionItem } from "../domain/attention";
import { buildTagFleetSummaries, type TagFleetSummary } from "../domain/fleet";
import { buildRelationshipView, type RelationshipView } from "../domain/relationships";

export type OverviewConclusion = {
  tone: "good" | "warn" | "bad" | "neutral";
  title: string;
  detail: string;
};

export type OverviewView = {
  conclusion: OverviewConclusion;
  traffic: OverviewTrafficSummary;
  attentionItems: AttentionItem[];
  tagSummaries: TagFleetSummary[];
  relationship: RelationshipView;
};

export type OverviewTrafficSummary = {
  requests: number;
  totalTokens: number;
  cacheTokens: number;
  non200: number;
  avgLatencyMs: number;
};

export function buildOverviewView(status: StatusResponse): OverviewView {
  const attentionItems = buildAttentionItems(status);
  return {
    conclusion: buildConclusion(status, attentionItems),
    traffic: buildTrafficSummary(status),
    attentionItems,
    tagSummaries: buildTagFleetSummaries(status),
    relationship: buildRelationshipView(status)
  };
}

function buildTrafficSummary(status: StatusResponse): OverviewTrafficSummary {
  const totals = status.models.reduce(
    (acc, model) => {
      const traffic = model.traffic;
      const requests = Number(traffic.requests || 0);
      acc.requests += requests;
      acc.totalTokens += Number(traffic.total_tokens || 0);
      acc.cacheTokens += Number(traffic.cache_tokens || 0);
      acc.non200 += Number(traffic.status_4xx || 0) + Number(traffic.status_5xx || 0);
      acc.durationWeighted += Number(traffic.avg_duration_ms || 0) * requests;
      return acc;
    },
    { requests: 0, totalTokens: 0, cacheTokens: 0, non200: 0, durationWeighted: 0 }
  );
  return {
    requests: totals.requests,
    totalTokens: totals.totalTokens,
    cacheTokens: totals.cacheTokens,
    non200: totals.non200,
    avgLatencyMs: totals.requests > 0 ? Math.round(totals.durationWeighted / totals.requests) : 0
  };
}

function buildConclusion(status: StatusResponse, attentionItems: AttentionItem[]): OverviewConclusion {
  const critical = attentionItems.filter((item) => item.severity === "critical").length;
  const warning = attentionItems.filter((item) => item.severity === "warning").length;
  if (critical > 0) {
    return {
      tone: "bad",
      title: `${critical} critical exception${critical === 1 ? "" : "s"}`,
      detail: `${status.summary.healthy_workers}/${status.summary.total_workers} workers healthy · ${status.summary.available_models}/${status.summary.configured_models} models available`
    };
  }
  if (warning > 0) {
    return {
      tone: "warn",
      title: `${warning} warning${warning === 1 ? "" : "s"} need review`,
      detail: `${status.summary.active_requests} active requests · generated ${new Date(status.generated_at).toLocaleTimeString()}`
    };
  }
  return {
    tone: "good",
    title: "All serving lanes are clear",
    detail: `${status.summary.healthy_workers} healthy workers · ${status.summary.available_models} available models`
  };
}
