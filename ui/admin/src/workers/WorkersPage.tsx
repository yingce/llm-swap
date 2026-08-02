import { useMemo, useState } from "react";

import type { StatusResponse } from "../api";
import { drainWorker, undrainWorker } from "../api";
import { ConfirmDialog, EmptyState } from "../components/primitives";
import { buildWorkerFilters, buildWorkerRows, type WorkerRow } from "./workerView";

export function WorkersPage({
  status,
  onAction
}: {
  status: StatusResponse | null;
  onAction: (action: () => Promise<{ action: string; worker_id?: string; model?: string }>) => Promise<void>;
}) {
  const [query, setQuery] = useState("");
  const [tag, setTag] = useState("");
  const [model, setModel] = useState("");
  const [drainTarget, setDrainTarget] = useState<string | null>(null);
  const filters = useMemo(() => buildWorkerFilters(status), [status]);
  const rows = useMemo(() => buildWorkerRows(status, { query, tag, model }), [status, query, tag, model]);

  return (
    <div className="workers-workspace">
      <section className="worker-toolbar">
        <div>
          <h2>Workers</h2>
          <p>Flat GPU ledger: load, model residency, request pressure, queue headroom, and agent build.</p>
        </div>
        <label className="model-search">
          <span>Search</span>
          <input value={query} placeholder="id, tag, model" onChange={(event) => setQuery(event.target.value)} />
        </label>
        <label className="model-search">
          <span>Tag</span>
          <select value={tag} onChange={(event) => setTag(event.target.value)}>
            <option value="">All tags</option>
            {filters.tags.map((tagName) => <option key={tagName} value={tagName}>{tagName}</option>)}
          </select>
        </label>
        <label className="model-search">
          <span>Model</span>
          <select value={model} onChange={(event) => setModel(event.target.value)}>
            <option value="">All loaded models</option>
            {filters.models.map((modelName) => <option key={modelName} value={modelName}>{modelName}</option>)}
          </select>
        </label>
      </section>

      {rows.length === 0 ? (
        <EmptyState title="No workers match" body="Adjust tag, model, or text filters." />
      ) : (
        <div className="worker-tile-grid">
          {rows.map((row) => (
            <WorkerTile
              key={row.id}
              row={row}
              onDrain={() => setDrainTarget(row.id)}
              onUndrain={() => void onAction(() => undrainWorker(row.id))}
            />
          ))}
        </div>
      )}

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

function WorkerTile({ row, onDrain, onUndrain }: { row: WorkerRow; onDrain: () => void; onUndrain: () => void }) {
  const runningLabel = row.loaded_models.length === 0 ? "no loaded model" : row.loaded_models.map((modelName) => modelName.replace(/:ready$/, "")).join(" · ");
  const hasSecondaryState = row.cooldowns.length > 0 || row.worker.needs_restart || row.worker.last_error;
  const diagnostics = `${row.diagnostics}\ncommit ${row.worker.agent_build.commit || "unknown"}\nheartbeat ${row.heartbeat}`;
  return (
    <article className={`worker-tile ${row.health === "healthy" ? "healthy" : "degraded"}`}>
      <header className="worker-tile-head">
        <div>
          <h3>{row.id}</h3>
          <p>{row.tags.join(" · ") || "untagged"}</p>
          <small className="worker-url" title={row.worker.llama_swap_url}>{row.worker.llama_swap_url}</small>
        </div>
        <div className="worker-head-actions">
          <span className="worker-diagnostics" tabIndex={0} title={diagnostics} aria-label={row.diagnostics}>···</span>
          {row.state === "draining" ? <button className="compact-action" onClick={onUndrain}>Undrain</button> : <button className="danger-ghost compact-action" onClick={onDrain}>Drain</button>}
        </div>
      </header>

      <div className="worker-request-strip" aria-label={`${row.active_requests} active requests, ${row.queued_requests} queued requests`}>
        <div title="Requests currently executing on this worker">
          <span>REQ</span>
          <strong>{row.active_requests}</strong>
        </div>
        <div title="Requests currently queued for this worker and model">
          <span>QUEUE</span>
          <strong>{row.queued_requests}</strong>
        </div>
      </div>

      <div className="worker-gpu-deck">
        {row.gpu_devices.map((gpu) => (
          <div className="worker-gpu-mini" key={`${row.id}-${gpu.index}-${gpu.name}`}>
            <div>
              <strong>GPU {gpu.index}</strong>
              <span className="worker-gpu-name" title={gpu.name.replace(/^NVIDIA\s+/i, "")}>{gpu.name.replace(/^NVIDIA\s+/i, "")}</span>
              <small>{gpu.utilization} · {gpu.temperature}</small>
            </div>
            <div className="worker-gpu-load">
              <div className="worker-gpu-bar" title={gpu.memory} style={{ ["--gpu-used" as string]: `${gpu.memory_percent}%` }} />
              <small>{gpu.memory}</small>
            </div>
          </div>
        ))}
        {row.gpu_devices.length === 0 ? <div className="worker-gpu-mini empty-gpu">no GPU metrics</div> : null}
      </div>

      <div className="worker-model-board">
        <strong>model</strong>
        <span title={runningLabel}>{runningLabel}</span>
      </div>

      {hasSecondaryState ? (
        <div className="worker-resource-sections">
          <ResourceStrip title="Cooldowns" items={row.cooldowns} />
          {row.worker.needs_restart ? <span className="worker-alert-chip">restart needed</span> : null}
          {row.worker.last_error ? <span className="worker-alert-chip" title={row.worker.last_error}>last error</span> : null}
        </div>
      ) : null}
    </article>
  );
}

function ResourceStrip({ title, items }: { title: string; items: string[] }) {
  if (items.length === 0) {
    return null;
  }
  return (
    <div className="worker-resource-strip">
      <strong>{title}</strong>
      <div className="worker-model-ledger">
        {items.map((item) => <span key={item}>{item}</span>)}
      </div>
    </div>
  );
}
