import type { ModelStatus, StatusResponse, WorkerStatus } from "../api";

export type TagModelReadiness = {
  name: string;
  eligible_workers: number;
  ready_replicas: number;
  required_replicas: number;
  shortfall: number;
  state: "ready" | "short" | "idle";
};

export type TagFleetSummary = {
  tag: string;
  workers: string[];
  worker_count: number;
  healthy_workers: number;
  draining_workers: number;
  active_requests: number;
  gpu_count: number;
  models: TagModelReadiness[];
};

export function buildTagFleetSummaries(status: StatusResponse): TagFleetSummary[] {
  const workersByTag = groupWorkersByTag(status.workers);

  return [...workersByTag.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([tag, workers]) => {
      const workerIds = new Set(workers.map((worker) => worker.id));
      return {
        tag,
        workers: workers.map((worker) => worker.id).sort(),
        worker_count: workers.length,
        healthy_workers: workers.filter((worker) => worker.health === "healthy").length,
        draining_workers: workers.filter((worker) => worker.state === "draining").length,
        active_requests: workers.reduce((total, worker) => total + Number(worker.active_requests || 0), 0),
        gpu_count: workers.reduce((total, worker) => total + (worker.gpu_devices?.length ?? 0), 0),
        models: status.models.flatMap((model) => buildTagModelReadiness(model, workerIds))
      };
    });
}

function groupWorkersByTag(workers: WorkerStatus[]): Map<string, WorkerStatus[]> {
  const groups = new Map<string, WorkerStatus[]>();
  for (const worker of workers) {
    const tags = worker.tags.length > 0 ? worker.tags : ["untagged"];
    for (const tag of tags) {
      groups.set(tag, [...(groups.get(tag) ?? []), worker]);
    }
  }
  return groups;
}

function buildTagModelReadiness(model: ModelStatus, workerIds: Set<string>): TagModelReadiness[] {
  const workerStatuses = model.worker_statuses.filter((workerStatus) => workerIds.has(workerStatus.worker_id));
  if (workerStatuses.length === 0) {
    return [];
  }

  const readyReplicas = workerStatuses.filter(
    (workerStatus) => workerStatus.running_state === "ready" && workerStatus.health === "healthy"
  ).length;
  const requiredReplicas = Math.min(Math.max(model.min_loaded, 0), workerStatuses.length);
  const shortfall = Math.max(requiredReplicas - readyReplicas, 0);
  const state: TagModelReadiness["state"] =
    requiredReplicas === 0 ? "idle" : shortfall > 0 ? "short" : "ready";

  return [
    {
      name: model.name,
      eligible_workers: workerStatuses.length,
      ready_replicas: readyReplicas,
      required_replicas: requiredReplicas,
      shortfall,
      state
    }
  ];
}
