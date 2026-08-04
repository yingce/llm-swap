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
  attentionItems: AttentionItem[];
  tagSummaries: TagFleetSummary[];
  relationship: RelationshipView;
};

export type OverviewTrafficRange = "1h" | "6h" | "24h" | "3d" | "7d";

const rangeDurationMS: Record<OverviewTrafficRange, number> = {
  "1h": 60 * 60 * 1000,
  "6h": 6 * 60 * 60 * 1000,
  "24h": 24 * 60 * 60 * 1000,
  "3d": 3 * 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000
};

export function buildOverviewView(status: StatusResponse, trafficRange: OverviewTrafficRange = "24h"): OverviewView {
  const rangeEnd = Date.parse(status.generated_at);
  const attentionItems = buildAttentionItems(status, Number.isFinite(rangeEnd) ? {
    event_start: new Date(rangeEnd - rangeDurationMS[trafficRange]).toISOString(),
    event_end: new Date(rangeEnd).toISOString()
  } : {});
  return {
    conclusion: buildConclusion(status, attentionItems),
    attentionItems,
    tagSummaries: buildTagFleetSummaries(status),
    relationship: buildRelationshipView(status)
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
