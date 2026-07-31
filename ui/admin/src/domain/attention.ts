import type { ReplicaCooldown, StatusResponse, WorkerEvent, WorkerStatus } from "../api";

export type AttentionItem = {
  id: string;
  type:
    | "model_shortfall"
    | "worker_draining"
    | "worker_stale"
    | "worker_error"
    | "worker_restart_needed"
    | "scrape_backoff"
    | "replica_cooldown"
    | "event_error";
  severity: "critical" | "warning" | "info";
  title: string;
  detail: string;
  worker_id?: string;
  model?: string;
};

export function buildAttentionItems(status: StatusResponse): AttentionItem[] {
  return [
    ...buildModelShortfalls(status),
    ...status.workers.flatMap(buildWorkerAttentionItems),
    ...status.workers.flatMap((worker) => worker.replica_cooldowns.map((cooldown) => buildCooldownItem(cooldown))),
    ...status.events.filter(isErrorEvent).map(buildEventErrorItem)
  ];
}

function buildModelShortfalls(status: StatusResponse): AttentionItem[] {
  return status.models
    .filter((model) => model.min_loaded > 0 && model.ready_workers < model.min_loaded)
    .map((model) => ({
      id: `model-shortfall:${model.name}`,
      type: "model_shortfall",
      severity: "critical",
      title: `${model.name} is below min replicas`,
      detail: `${model.ready_workers}/${model.min_loaded} ready replicas`,
      model: model.name
    }));
}

function buildWorkerAttentionItems(worker: WorkerStatus): AttentionItem[] {
  const items: AttentionItem[] = [];
  if (worker.health === "stale" || worker.state === "stale" || Number(worker.last_heartbeat_age_ms ?? 0) >= 120000) {
    items.push({
      id: `worker-stale:${worker.id}`,
      type: "worker_stale",
      severity: "critical",
      title: `${worker.id} is stale`,
      detail: worker.last_heartbeat ? `last heartbeat ${worker.last_heartbeat}` : "heartbeat is stale",
      worker_id: worker.id
    });
  }
  if (worker.state === "draining") {
    items.push({
      id: `worker-draining:${worker.id}`,
      type: "worker_draining",
      severity: "info",
      title: `${worker.id} is draining`,
      detail: `${worker.active_requests} active requests`,
      worker_id: worker.id
    });
  }
  if (worker.last_error) {
    items.push({
      id: `worker-error:${worker.id}`,
      type: "worker_error",
      severity: "warning",
      title: `${worker.id} reported an error`,
      detail: worker.last_error,
      worker_id: worker.id
    });
  }
  if (worker.needs_restart) {
    items.push({
      id: `worker-restart:${worker.id}`,
      type: "worker_restart_needed",
      severity: "warning",
      title: `${worker.id} needs restart`,
      detail: "agent reported pending local changes",
      worker_id: worker.id
    });
  }
  if (worker.scrape_backoff_until || Number(worker.scrape_backoff_seconds ?? 0) > 0) {
    items.push({
      id: `scrape-backoff:${worker.id}`,
      type: "scrape_backoff",
      severity: "warning",
      title: `${worker.id} scrape is backing off`,
      detail: worker.scrape_backoff_until ?? `${worker.scrape_backoff_seconds}s remaining`,
      worker_id: worker.id
    });
  }
  return items;
}

function buildCooldownItem(cooldown: ReplicaCooldown): AttentionItem {
  return {
    id: `replica-cooldown:${cooldown.worker_id}:${cooldown.model}`,
    type: "replica_cooldown",
    severity: "warning",
    title: `${cooldown.model} replica cooldown`,
    detail: `${cooldown.worker_id}: ${cooldown.reason} (${cooldown.remaining_seconds}s)`,
    worker_id: cooldown.worker_id,
    model: cooldown.model
  };
}

function isErrorEvent(event: WorkerEvent): boolean {
  return Boolean(event.error || event.event.toLowerCase().includes("error"));
}

function buildEventErrorItem(event: WorkerEvent): AttentionItem {
  return {
    id: `event-error:${event.worker_id}:${event.received_at}:${event.event}`,
    type: "event_error",
    severity: "warning",
    title: `${event.worker_id} event error`,
    detail: event.error ?? event.event,
    worker_id: event.worker_id,
    model: event.model
  };
}
