import type { StatusResponse, WorkerStatus } from "../api";

export type WorkerRow = {
  id: string;
  tags: string[];
  health: string;
  state: string;
  active_requests: number;
  gpu_count: number;
  gpu_memory: string;
  loaded_models: string[];
  connectivity: string;
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
        loaded_models: loadedModels,
        connectivity: summarizeConnectivity(worker),
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
