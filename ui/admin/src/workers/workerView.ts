import type { GPUDevice, StatusResponse, WorkerStatus } from "../api";

export type WorkerGpuRow = {
  index: number;
  name: string;
  memory: string;
  memory_percent: number;
  utilization: string;
  temperature: string;
};

export type WorkerRow = {
  id: string;
  tags: string[];
  health: string;
  state: string;
  active_requests: number;
  live_capacity_available: boolean;
  max_concurrency: number;
  queued_requests: number;
  max_queue: number;
  gpu_count: number;
  gpu_memory: string;
  gpu_devices: WorkerGpuRow[];
  loaded_models: string[];
  allowed_models: string[];
  artifact_states: string[];
  cooldowns: string[];
  connectivity: string;
  heartbeat: string;
  agent_version: string;
  diagnostics: string;
  worker: WorkerStatus;
};

export type WorkerFilters = {
  tags: string[];
  models: string[];
};

export type WorkerRowOptions = {
  query: string;
  tag?: string;
  model?: string;
};

export function buildWorkerFilters(status: StatusResponse | null): WorkerFilters {
  return {
    tags: [...new Set((status?.workers ?? []).flatMap((worker) => worker.tags))].sort(),
    models: [...new Set((status?.workers ?? []).flatMap((worker) => worker.running_models.map((model) => model.model)))].sort()
  };
}

export function buildWorkerRows(status: StatusResponse | null, options: WorkerRowOptions): WorkerRow[] {
  const query = options.query.trim().toLowerCase();
  return (status?.workers ?? [])
    .map((worker) => {
      const loadedModels = worker.running_models.map((model) => `${model.model}:${model.state || "ready"}`);
      const heartbeatAge = formatAge(worker.last_heartbeat_age_ms);
      const buildState = worker.agent_version_status === "current" ? "latest" : "old";
      return {
        id: worker.id,
        tags: worker.tags,
        health: worker.health,
        state: worker.state,
        active_requests: worker.active_requests,
        live_capacity_available: worker.live_capacity_available ?? false,
        max_concurrency: Math.max(0, Number(worker.max_concurrency ?? 0)),
        queued_requests: worker.queued_requests,
        max_queue: Math.max(0, Number(worker.max_queue ?? 0)),
        gpu_count: worker.gpu_devices.length,
        gpu_memory: summarizeGpuMemory(worker),
        gpu_devices: worker.gpu_devices.map(formatGpuDevice),
        loaded_models: loadedModels,
        allowed_models: [...worker.allowed_models].sort(),
        artifact_states: Object.entries(worker.artifacts ?? {})
          .map(([model, state]) => `${model}:${state}`)
          .sort(),
        cooldowns: worker.replica_cooldowns.map((cooldown) => `${cooldown.model}:${cooldown.reason}:${cooldown.remaining_seconds}s`),
        connectivity: summarizeConnectivity(worker),
        heartbeat: worker.last_heartbeat ? `${worker.last_heartbeat} · ${heartbeatAge}` : "heartbeat unavailable",
        agent_version: `${worker.agent_build.version || "unknown"} · ${worker.agent_version_status}`,
        diagnostics: `build ${worker.agent_build.version || "unknown"} (${buildState}) · heartbeat ${heartbeatAge} · scrape failures ${worker.scrape_failures}`,
        worker
      };
    })
    .filter((row) => !options.tag || row.tags.includes(options.tag))
    .filter((row) => !options.model || row.worker.running_models.some((model) => model.model === options.model))
    .filter((row) => matchesQuery(row, query))
    .sort((left, right) => left.id.localeCompare(right.id));
}

export type WorkerPressurePresentation = {
  limitText: string;
  maximumText: string;
  title: string;
};

export function formatWorkerPressure(
  label: string,
  current: number,
  max: number,
  available: boolean
): WorkerPressurePresentation {
  const normalizedMax = Math.max(0, Number(max) || 0);
  const limitText = available ? String(normalizedMax) : "—";
  const maximumText = available ? `maximum ${normalizedMax}` : "maximum unavailable (—)";
  return {
    limitText,
    maximumText,
    title: `${label}: ${current} current requests; ${maximumText}`
  };
}

function summarizeConnectivity(worker: WorkerStatus): string {
  const scrape = worker.scrape_backoff_until || Number(worker.scrape_backoff_seconds ?? 0) > 0 ? "scrape backoff" : "scrape ok";
  return `${worker.health} · ${worker.state} · ${scrape}`;
}

function summarizeGpuMemory(worker: WorkerStatus): string {
  const used = worker.gpu_devices.reduce((total, gpu) => total + gpu.memory_used_mib, 0);
  const total = worker.gpu_devices.reduce((sum, gpu) => sum + gpu.memory_total_mib, 0);
  return total > 0 ? `${formatMiB(used)} / ${formatMiB(total)}` : "no GPU metrics";
}

function matchesQuery(row: WorkerRow, query: string): boolean {
  if (!query) {
    return true;
  }
  return [row.id, row.health, row.state, row.connectivity, ...row.tags, ...row.loaded_models].some((value) =>
    value.toLowerCase().includes(query)
  );
}

function formatMiB(value: number): string {
  return value >= 1024 ? `${(value / 1024).toFixed(1).replace(/\.0$/, "")}GiB` : `${Math.round(value)}MiB`;
}

function formatGpuDevice(gpu: GPUDevice): WorkerGpuRow {
  const memoryPercent = gpu.memory_total_mib > 0 ? Math.round((gpu.memory_used_mib / gpu.memory_total_mib) * 100) : 0;
  return {
    index: gpu.index,
    name: gpu.name,
    memory: `${formatMiB(gpu.memory_used_mib)} / ${formatMiB(gpu.memory_total_mib)}`,
    memory_percent: memoryPercent,
    utilization: `${gpu.utilization_percent}%`,
    temperature: `${gpu.temperature_celsius}°C`
  };
}

function formatAge(ageMs: number | undefined): string {
  const seconds = Math.round(Number(ageMs ?? 0) / 1000);
  if (seconds <= 0) {
    return "fresh";
  }
  if (seconds < 60) {
    return `${seconds}s ago`;
  }
  return `${Math.round(seconds / 60)}m ago`;
}
