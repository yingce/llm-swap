import type { StatusResponse } from "../api";
import { AttentionList, EmptyState, StatusIndicator } from "../components/primitives";
import { buildOverviewView } from "./overviewView";

export function OverviewPage({ status }: { status: StatusResponse | null }) {
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
            <h3>Cluster topology</h3>
            <p>Gateway routes through tag policy to workers and their loaded models.</p>
          </div>
          <div className="topology-map" aria-label="Gateway to tag to worker topology">
            <div className="topology-gateway">Gateway</div>
            <div className="topology-tag-lanes">
              {view.relationship.tags.map((tag) => (
                <article className="topology-lane" key={tag.tag}>
                  <header>
                    <strong>{tag.tag}</strong>
                    <span>{tag.workers.length} workers</span>
                  </header>
                  <div className="topology-worker-strip">
                    {tag.workers.map((worker) => (
                      <div className="topology-worker-node" key={worker.id}>
                        <div>
                          <span>{worker.id}</span>
                          <StatusIndicator tone={worker.health === "healthy" ? "good" : "bad"} label={worker.state} />
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
              ))}
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
