import { useMemo, useState } from "react";

import type { StatusResponse } from "../api";
import { drainWorker, undrainWorker } from "../api";
import { ConfirmDialog, DetailPanel, EmptyState, ResourceList, StatusIndicator } from "../components/primitives";
import { buildWorkerRows, type WorkerRow } from "./workerView";

export function WorkersPage({
  status,
  onAction
}: {
  status: StatusResponse | null;
  onAction: (action: () => Promise<{ action: string; worker_id?: string; model?: string }>) => Promise<void>;
}) {
  const [query, setQuery] = useState("");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [drainTarget, setDrainTarget] = useState<string | null>(null);
  const rows = useMemo(() => buildWorkerRows(status, { query }), [status, query]);
  const selected = rows.find((row) => row.id === selectedId) ?? rows[0] ?? null;

  return (
    <div className="workers-workspace">
      <section className="worker-toolbar">
        <div>
          <h2>Workers</h2>
          <p>GPU inventory, loaded models, and current agent connectivity.</p>
        </div>
        <label className="model-search">
          <span>Search</span>
          <input value={query} placeholder="id, tag, model" onChange={(event) => setQuery(event.target.value)} />
        </label>
      </section>

      <div className="workers-master-detail">
        <ResourceList
          items={rows}
          getKey={(row) => row.id}
          empty={<EmptyState title="No workers match" body="Adjust search or wait for worker heartbeats." />}
          renderItem={(row) => (
            <button className={`worker-ledger-row ${selected?.id === row.id ? "selected" : ""}`} onClick={() => setSelectedId(row.id)}>
              <span className="worker-ledger-identity">
                <strong>{row.id}</strong>
                <small>{row.tags.join(", ") || "untagged"}</small>
              </span>
              <span className="worker-ledger-metrics">
                <small>{row.request_capacity}</small>
                <small>{row.gpu_count} GPU · {row.gpu_memory}</small>
                <small>{row.agent_version}</small>
              </span>
              <StatusIndicator tone={row.health === "healthy" ? "good" : "bad"} label={row.state} />
            </button>
          )}
        />

        {selected ? (
          <WorkerDetail
            row={selected}
            onDrain={() => setDrainTarget(selected.id)}
            onUndrain={() => void onAction(() => undrainWorker(selected.id))}
          />
        ) : (
          <EmptyState title="No worker selected" body="Select a worker to inspect GPUs and loaded models." />
        )}
      </div>

      <ConfirmDialog
        open={Boolean(drainTarget)}
        title="Drain worker?"
        body={drainTarget ? `${drainTarget} will stop receiving new requests. Active requests can finish.` : ""}
        confirmLabel="Drain"
        destructive
        onCancel={() => setDrainTarget(null)}
        onConfirm={() => {
          const target = drainTarget;
          setDrainTarget(null);
          if (target) {
            void onAction(() => drainWorker(target));
          }
        }}
      />
    </div>
  );
}

function WorkerDetail({ row, onDrain, onUndrain }: { row: WorkerRow; onDrain: () => void; onUndrain: () => void }) {
  return (
    <DetailPanel
      title={row.id}
      subtitle={row.connectivity}
      meta={<StatusIndicator tone={row.health === "healthy" ? "good" : "bad"} label={row.health} />}
    >
      <div className="worker-detail-grid">
        <div>
          <strong>GPU</strong>
          <span>{row.gpu_count} devices · {row.gpu_memory}</span>
        </div>
        <div>
          <strong>Requests</strong>
          <span>{row.request_capacity}</span>
        </div>
        <div>
          <strong>Agent</strong>
          <span>{row.agent_version}{row.worker.agent_build.commit ? ` · ${row.worker.agent_build.commit.slice(0, 12)}` : ""}</span>
        </div>
        <div>
          <strong>Heartbeat</strong>
          <span>{row.heartbeat}</span>
        </div>
        <div>
          <strong>llama-swap</strong>
          <span>{row.worker.llama_swap_url}</span>
        </div>
        <div>
          <strong>Scrape</strong>
          <span>{row.worker.scrape_failures} failures{row.worker.scrape_backoff_seconds ? ` · backoff ${row.worker.scrape_backoff_seconds}s` : ""}</span>
        </div>
      </div>
      <div className="worker-gpu-list">
        {row.gpu_devices.map((gpu) => (
          <div className="worker-gpu-row" key={`${row.id}-${gpu.index}-${gpu.name}`}>
            <strong>{gpu.index}: {gpu.name}</strong>
            <span>{gpu.utilization} util · {gpu.temperature} · {gpu.memory}</span>
          </div>
        ))}
        {row.gpu_devices.length === 0 ? <EmptyState title="No GPU metrics" body="The worker did not report GPU devices." /> : null}
      </div>
      <div className="worker-resource-sections">
        <ResourceStrip title="Running models" items={row.loaded_models} empty="no loaded models" />
        <ResourceStrip title="Allowed models" items={row.allowed_models} empty="no allowed models" />
        <ResourceStrip title="Artifacts" items={row.artifact_states} empty="no artifact state" />
        <ResourceStrip title="Cooldowns" items={row.cooldowns} empty="no replica cooldowns" />
      </div>
      <div className="model-actions">
        {row.state === "draining" ? <button onClick={onUndrain}>Undrain</button> : <button className="danger" onClick={onDrain}>Drain</button>}
      </div>
    </DetailPanel>
  );
}

function ResourceStrip({ title, items, empty }: { title: string; items: string[]; empty: string }) {
  return (
    <div className="worker-resource-strip">
      <strong>{title}</strong>
      <div className="worker-model-ledger">
        {items.map((item) => <span key={item}>{item}</span>)}
        {items.length === 0 ? <span className="muted">{empty}</span> : null}
      </div>
    </div>
  );
}
