import type { StatusResponse, WorkerStatus } from "../api";

export type RelationshipNode = {
  id: string;
  type: "gateway" | "tag" | "worker" | "model";
  label: string;
};

export type RelationshipEdge = {
  from: string;
  to: string;
  type: "gateway-tag" | "tag-worker" | "worker-model";
};

export type RelationshipTag = {
  id: string;
  tag: string;
  workers: RelationshipWorker[];
};

export type RelationshipWorker = {
  id: string;
  health: string;
  state: string;
  gpu_count: number;
  loaded_models: { name: string; state: string }[];
};

export type RelationshipView = {
  gateway: RelationshipNode;
  tags: RelationshipTag[];
  nodes: RelationshipNode[];
  edges: RelationshipEdge[];
};

export function buildRelationshipView(status: StatusResponse): RelationshipView {
  const gateway: RelationshipNode = { id: "gateway", type: "gateway", label: "Gateway" };
  const tags = groupWorkersByTag(status.workers);
  const tagViews = [...tags.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([tag, workers]) => ({
      id: tagNodeId(tag),
      tag,
      workers: workers
        .slice()
        .sort((left, right) => left.id.localeCompare(right.id))
        .map((worker) => ({
          id: worker.id,
          health: worker.health,
          state: worker.state,
          gpu_count: worker.gpu_devices.length,
          loaded_models: worker.running_models.map((model) => ({
            name: model.model,
            state: model.state || "ready"
          }))
        }))
    }));

  const nodes: RelationshipNode[] = [
    gateway,
    ...tagViews.map((tag) => ({ id: tag.id, type: "tag" as const, label: tag.tag })),
    ...uniqueNodes(
      tagViews.flatMap((tag) =>
        tag.workers.flatMap((worker) => [
          { id: workerNodeId(worker.id), type: "worker" as const, label: worker.id },
          ...worker.loaded_models.map((model) => ({
            id: modelNodeId(worker.id, model.name),
            type: "model" as const,
            label: model.name
          }))
        ])
      )
    )
  ];

  const edges = uniqueEdges([
    ...tagViews.map((tag) => ({ from: gateway.id, to: tag.id, type: "gateway-tag" as const })),
    ...tagViews.flatMap((tag) =>
      tag.workers.flatMap((worker) => [
        { from: tag.id, to: workerNodeId(worker.id), type: "tag-worker" as const },
        ...worker.loaded_models.map((model) => ({
          from: workerNodeId(worker.id),
          to: modelNodeId(worker.id, model.name),
          type: "worker-model" as const
        }))
      ])
    )
  ]);

  return { gateway, tags: tagViews, nodes, edges };
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

function uniqueNodes(nodes: RelationshipNode[]): RelationshipNode[] {
  return [...new Map(nodes.map((node) => [node.id, node])).values()];
}

function uniqueEdges(edges: RelationshipEdge[]): RelationshipEdge[] {
  return [...new Map(edges.map((edge) => [`${edge.from}->${edge.to}:${edge.type}`, edge])).values()];
}

function tagNodeId(tag: string): string {
  return `tag:${tag}`;
}

function workerNodeId(workerId: string): string {
  return `worker:${workerId}`;
}

function modelNodeId(workerId: string, model: string): string {
  return `model:${workerId}:${model}`;
}
