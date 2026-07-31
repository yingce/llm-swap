import { useMemo, useState } from "react";

import type { StatusResponse } from "../api";
import { drainWorker, undrainWorker } from "../api";
import { ConfirmDialog, EmptyState, StatusIndicator } from "../components/primitives";
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
  const activeRatio = row.worker.capacity.max_concurrency > 0 ? Math.min(1, row.active_requests / row.worker.capacity.max_concurrency) : 0;
  const queueRatio = row.worker.capacity.max_queue > 0 ? Math.min(1, row.worker.active_requests / row.worker.capacity.max_queue) : 0;
  return (
    <article className={`worker-tile ${row.health === "healthy" ? "healthy" : "degraded"}`}>
      <header className="worker-tile-head">
        <div>
          <h3>{row.id}</h3>
          <p>{row.tags.join(" · ") || "untagged"}</p>
        </div>
        <StatusIndicator tone={row.health === "healthy" ? "good" : "bad"} label={row.state} />
      </header>

      <div className="worker-signal-strip" aria-label="worker load signals">
        <Meter value={activeRatio} label={`${row.active_requests}/${row.worker.capacity.max_concurrency}`} tone="teal" />
        <Meter value={queueRatio} label={`${row.worker.active_requests}/${row.worker.capacity.max_queue}`} tone="amber" />
        <div className="worker-signal-chip">{row.gpu_count}×</div>
        <div className="worker-signal-chip">{row.loaded_models.length}</div>
      </div>

      <div className="worker-gpu-deck">
        {row.gpu_devices.map((gpu) => (
          <div className="worker-gpu-mini" key={`${row.id}-${gpu.index}-${gpu.name}`}>
            <div>
              <strong>{gpu.index}</strong>
              <span>{gpu.name.replace(/^NVIDIA\s+/i, "")}</span>
            </div>
            <div className="worker-gpu-bar" style={{ ["--gpu-used" as string]: `${gpu.memory_percent}%` }} />
            <small>{gpu.utilization} · {gpu.temperature} · {gpu.memory}</small>
          </div>
        ))}
        {row.gpu_devices.length === 0 ? <div className="worker-gpu-mini empty-gpu">no GPU metrics</div> : null}
      </div>

      <div className="worker-model-board">
        {row.loaded_models.map((modelName) => <span key={modelName}>{modelName}</span>)}
        {row.loaded_models.length === 0 ? <span className="muted">no loaded models</span> : null}
      </div>

      <div className="worker-tile-detail">
        <div>
          <strong>{row.agent_version}</strong>
          <span>{row.worker.agent_build.commit ? row.worker.agent_build.commit.slice(0, 12) : "no commit"}</span>
        </div>
        <div>
          <strong>{row.heartbeat}</strong>
          <span>{row.connectivity}</span>
        </div>
        <div>
          <strong>{row.worker.llama_swap_url}</strong>
          <span>{row.worker.scrape_failures} scrape failures</span>
        </div>
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
    </article>
  );
}

function Meter({ value, label, tone }: { value: number; label: string; tone: "teal" | "amber" }) {
  return (
    <div className={`worker-meter ${tone}`} style={{ ["--meter" as string]: `${Math.round(value * 100)}%` }}>
      <span>{label}</span>
    </div>
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
