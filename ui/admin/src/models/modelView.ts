import type { ConfigResponse, ModelConfig, ModelStatus, StatusResponse } from "../api";

export type ModelRow = {
  name: string;
  aliases: string[];
  runtime: string;
  disabled: boolean;
  priority: number;
  available: boolean;
  ready_workers: number;
  running_workers: number;
  min_loaded: number;
  max_loaded: number;
  capacity: {
    active: number;
    max_active: number;
    queued: number;
    max_queue: number;
  };
  artifact: string;
  status: ModelStatus | null;
  config: ModelConfig | null;
};

export function buildModelRows(
  status: StatusResponse | null,
  configResponse: ConfigResponse | null,
  options: { query: string; includeDisabled: boolean }
): ModelRow[] {
  const statusByName = new Map((status?.models ?? []).map((model) => [model.name, model]));
  const configByName = new Map(Object.entries(configResponse?.config.models ?? {}));
  const aliasesByTarget = groupAliasesByTarget(configResponse?.config.model_aliases ?? {});
  const names = new Set([...statusByName.keys(), ...configByName.keys()]);
  const query = options.query.trim().toLowerCase();

  return [...names]
    .map((name) => {
      const live = statusByName.get(name) ?? null;
      const config = configByName.get(name) ?? null;
      const aliases = aliasesByTarget.get(name) ?? [];
      const row: ModelRow = {
        name,
        aliases,
        runtime: config?.runtime || (config?.run ? "custom run" : "runtime unset"),
        disabled: Boolean(config?.disabled),
        priority: live?.priority ?? config?.priority ?? 0,
        available: Boolean(live?.available),
        ready_workers: live?.ready_workers ?? 0,
        running_workers: live?.running_workers ?? 0,
        min_loaded: live?.min_loaded ?? config?.min_loaded ?? 0,
        max_loaded: live?.max_loaded ?? config?.max_loaded ?? 0,
        capacity: {
          active: live?.active_requests ?? 0,
          max_active: live?.max_concurrency ?? 0,
          queued: live?.queued_requests ?? 0,
          max_queue: live?.max_queue ?? 0
        },
        artifact: live ? `${live.artifact.kind} · ${live.artifact.object}` : config ? `${config.artifact.kind} · ${config.artifact.object}` : "",
        status: live,
        config
      };
      return row;
    })
    .filter((row) => options.includeDisabled || !row.disabled)
    .filter((row) => matchesQuery(row, query))
    .sort((left, right) => {
      if (left.disabled !== right.disabled) {
        return left.disabled ? 1 : -1;
      }
      return right.priority - left.priority || left.name.localeCompare(right.name);
    });
}

export function modelCapacityLabel(row: ModelRow): string {
  return `${row.capacity.active} active of ${row.capacity.max_active}; ${row.capacity.queued} queued of ${row.capacity.max_queue}`;
}

export function modelTrafficLabel(row: ModelRow): string {
  const traffic = row.status?.traffic;
  if (!traffic) {
    return "no traffic";
  }
  return `${formatCompactNumber(traffic.requests)} requests; ${formatCompactNumber(traffic.total_tokens)} tokens; ${formatCompactNumber(traffic.cache_tokens)} cache tokens; ${traffic.avg_duration_ms}ms average latency; ${formatCompactNumber(traffic.status_4xx)} 4xx; ${formatCompactNumber(traffic.status_5xx)} 5xx`;
}

export function formatCompactNumber(value: number): string {
  const absolute = Math.abs(value);
  if (absolute >= 1_000_000_000) {
    return formatUnit(value / 1_000_000_000, "B");
  }
  if (absolute >= 1_000_000) {
    return formatUnit(value / 1_000_000, "M");
  }
  if (absolute >= 1_000) {
    return formatUnit(value / 1_000, "K");
  }
  return String(value);
}

function formatUnit(value: number, suffix: string): string {
  return `${value.toFixed(1).replace(/\.0$/, "")}${suffix}`;
}

function groupAliasesByTarget(aliases: Record<string, string>): Map<string, string[]> {
  const grouped = new Map<string, string[]>();
  for (const [alias, target] of Object.entries(aliases)) {
    grouped.set(target, [...(grouped.get(target) ?? []), alias].sort());
  }
  return grouped;
}

function matchesQuery(row: ModelRow, query: string): boolean {
  if (!query) {
    return true;
  }
  return [row.name, row.runtime, row.artifact, ...row.aliases].some((value) => value.toLowerCase().includes(query));
}
