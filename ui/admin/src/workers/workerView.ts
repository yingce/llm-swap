import type { GPUDevice, StatusResponse, WorkerStatus } from "../api";

export type WorkerGpuRow = {
  index: number;
  name: string;
  memory: string;
  utilization: string;
  temperature: string;
};

export type WorkerRow = {
  id: string;
  tags: string[];
  health: string;
  state: string;
  active_requests: number;
  gpu_count: number;
  gpu_memory: string;
  gpu_devices: WorkerGpuRow[];
  loaded_models: string[];
  allowed_models: string[];
  artifact_states: string[];
  cooldowns: string[];
  connectivity: string;
  heartbeat: string;
  request_capacity: string;
  agent_version: string;
  worker: WorkerStatus;
};

export function buildWorkerRows(status: StatusResponse | null, options: { query: string }): WorkerRow[] {
  const query = options.query.trim().toLowerCase();
  return (status?.workers ?? [])
    .map((worker) => {
      const loadedModels = worker.running_models.map((model) => `${model.model}:${model.state || "ready"}`);
      return {
        id: worker.id,
        tags: worker.tags,
        health: worker.health,
        state: worker.state,
        active_requests: worker.active_requests,
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
        heartbeat: worker.last_heartbeat ? `${worker.last_heartbeat} · ${formatAge(worker.last_heartbeat_age_ms)}` : "heartbeat unavailable",
        request_capacity: `${worker.active_requests} active · concurrency ${worker.capacity.max_concurrency} · queue ${worker.capacity.max_queue}`,
        agent_version: `${worker.agent_build.version || "unknown"} · ${worker.agent_version_status}`,
        worker
      };
    })
    .filter((row) => matchesQuery(row, query))
    .sort((left, right) => left.id.localeCompare(right.id));
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
  return {
    index: gpu.index,
    name: gpu.name,
    memory: `${formatMiB(gpu.memory_used_mib)} / ${formatMiB(gpu.memory_total_mib)}`,
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
