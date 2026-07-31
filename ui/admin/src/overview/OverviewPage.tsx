import { useMemo, useState } from "react";

import type { StatusResponse } from "../api";
import { AttentionList, EmptyState, StatusIndicator } from "../components/primitives";
import { buildOverviewView } from "./overviewView";

type TopologyMode = "tag" | "model";

type ModelTopologyLane = {
  name: string;
  summary: string;
  workers: {
    id: string;
    health: string;
    state: string;
  }[];
};

export function OverviewPage({ status }: { status: StatusResponse | null }) {
  const [topologyMode, setTopologyMode] = useState<TopologyMode>("tag");
  const modelTopology = useMemo(() => (status ? buildModelTopology(status) : []), [status]);

  if (!status) {
    return <EmptyState title="Waiting for gateway status" body="The live status poll has not returned yet." />;
  }

  const view = buildOverviewView(status);
  return (
    <div className="overview-page">
      <section className={`overview-conclusion ${view.conclusion.tone}`}>
        <div>
          <StatusIndicator tone={view.conclusion.tone} label={view.conclusion.tone === "bad" ? "Action" : view.conclusion.tone} />
          <h2>{view.conclusion.title}</h2>
          <p>{view.conclusion.detail}</p>
        </div>
      </section>

      <section className="overview-grid">
        <div className="overview-main">
          <div className="section-heading">
            <h3>Tag fleet</h3>
            <p>Readiness is computed per tag from the workers inside that tag.</p>
          </div>
          <div className="tag-summary-grid">
            {view.tagSummaries.map((tag) => (
              <article className="tag-summary" key={tag.tag}>
                <header>
                  <strong>{tag.tag}</strong>
                  <span>{tag.worker_count} workers</span>
                </header>
                <div className="tag-summary-metrics">
                  <span>{tag.healthy_workers} healthy</span>
                  <span>{tag.draining_workers} draining</span>
                  <span>{tag.active_requests} active</span>
                  <span>{tag.gpu_count} GPU</span>
                </div>
                <div className="tag-models">
                  {tag.models.map((model) => (
                    <div className="tag-model-row" key={model.name}>
                      <span>{model.name}</span>
                      <StatusIndicator
                        tone={model.state === "ready" ? "good" : model.state === "short" ? "bad" : "neutral"}
                        label={`${model.ready_replicas}/${model.required_replicas}`}
                        detail={`${model.eligible_workers} eligible workers`}
                      />
                    </div>
                  ))}
                </div>
              </article>
            ))}
          </div>

          <div className="section-heading topology-heading">
            <div>
              <h3>Cluster topology</h3>
              <p>{topologyMode === "tag" ? "Tag policy is the scheduling entry point." : "Model view shows where configured replicas live."}</p>
            </div>
            <div className="topology-toggle" role="tablist" aria-label="Topology view">
              <button
                type="button"
                className={topologyMode === "tag" ? "active" : ""}
                aria-selected={topologyMode === "tag"}
                role="tab"
                onClick={() => setTopologyMode("tag")}
              >
                By tag
              </button>
              <button
                type="button"
                className={topologyMode === "model" ? "active" : ""}
                aria-selected={topologyMode === "model"}
                role="tab"
                onClick={() => setTopologyMode("model")}
              >
                By model
              </button>
            </div>
          </div>
          <div className="topology-map" aria-label={topologyMode === "tag" ? "Gateway to tag to worker topology" : "Gateway to model to worker topology"}>
            <div className="topology-gateway">Gateway</div>
            <div className="topology-tag-lanes">
              {topologyMode === "tag" ? (
                view.relationship.tags.map((tag) => (
                  <article className="topology-lane" key={tag.tag}>
                    <header>
                      <strong>{tag.tag}</strong>
                      <span>{tag.workers.length} workers</span>
                    </header>
                    <div className="topology-worker-strip">
                      {tag.workers.map((worker) => (
                        <div className={`topology-worker-node ${worker.health === "healthy" ? "healthy" : "degraded"}`} key={worker.id}>
                          <div>
                            <span>{worker.id}</span>
                            <small>{worker.state}</small>
                          </div>
                          <p>
                            {worker.gpu_count} GPU · {worker.loaded_models.length > 0
                              ? worker.loaded_models.map((model) => `${model.name}:${model.state}`).join(" · ")
                              : "no loaded models"}
                          </p>
                        </div>
                      ))}
                    </div>
                  </article>
                ))
              ) : (
                modelTopology.map((model) => (
                  <article className="topology-lane model-lane" key={model.name}>
                    <header>
                      <strong title={model.name}>{model.name}</strong>
                      <span>{model.summary}</span>
                    </header>
                    <div className="topology-worker-strip">
                      {model.workers.length > 0 ? model.workers.map((worker) => (
                        <div className={`topology-worker-node ${worker.health === "healthy" ? "healthy" : "degraded"}`} key={`${model.name}-${worker.id}`}>
                          <div>
                            <span>{worker.id}</span>
                            <small>{worker.state}</small>
                          </div>
                        </div>
                      )) : <div className="topology-worker-node quiet-node">No worker state</div>}
                    </div>
                  </article>
                ))
              )}
            </div>
          </div>
        </div>

        <aside className="overview-rail">
          <div className="section-heading">
            <h3>Attention</h3>
            <p>Only actionable exceptions from the current status snapshot.</p>
          </div>
          <AttentionList items={view.attentionItems} />
        </aside>
      </section>
    </div>
  );
}

function buildModelTopology(status: StatusResponse): ModelTopologyLane[] {
  return status.models
    .map((model) => ({
      name: model.name,
      summary: `${model.ready_workers}/${model.min_loaded} ready · ${model.running_workers} running`,
      workers: model.worker_statuses
        .map((worker) => ({
          id: worker.worker_id,
          health: worker.health,
          state: worker.running_state || worker.artifact_status || (worker.cooldown_active ? "cooldown" : "configured")
        }))
        .sort((left, right) => left.id.localeCompare(right.id))
    }))
    .sort((left, right) => left.name.localeCompare(right.name));
}
