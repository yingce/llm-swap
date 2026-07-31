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
  const queueRatio = row.worker.capacity.max_queue > 0 ? Math.min(1, row.active_requests / row.worker.capacity.max_queue) : 0;
  const runningLabel = row.loaded_models.length === 0 ? "no loaded model" : row.loaded_models.map((modelName) => modelName.replace(/:ready$/, "")).join(" · ");
  const hasSecondaryState = row.artifact_states.length > 0 || row.cooldowns.length > 0 || row.worker.needs_restart || row.worker.last_error;
  const buildState = row.worker.agent_version_status === "current" ? "latest" : "old";
  return (
    <article className={`worker-tile ${row.health === "healthy" ? "healthy" : "degraded"} ${row.active_requests > 0 ? "busy" : "idle"}`}>
      <header className="worker-tile-head">
        <div>
          <h3>{row.id}</h3>
          <p>{row.tags.join(" · ") || "untagged"}</p>
        </div>
        <div className="worker-head-actions">
          <StatusIndicator tone={row.health === "healthy" ? "good" : "bad"} label={row.state} />
          {row.state === "draining" ? <button className="compact-action" onClick={onUndrain}>Undrain</button> : <button className="danger-ghost compact-action" onClick={onDrain}>Drain</button>}
        </div>
      </header>

      <div className="worker-signal-strip" aria-label="worker load signals">
        <Meter value={activeRatio} label="CONC" valueLabel={`${row.active_requests}/${row.worker.capacity.max_concurrency}`} tone="teal" />
        <Meter value={queueRatio} label="Q CAP" valueLabel={`${row.worker.capacity.max_queue}`} tone="amber" />
        <SignalChip label="GPU" value={`${row.gpu_count}×`} />
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
        <strong>model</strong>
        <span title={runningLabel}>{runningLabel}</span>
      </div>

      <div className="worker-tile-detail">
        <div>
          <span>build</span>
          <strong title={row.agent_version}>{buildState}</strong>
        </div>
        <div>
          <span>heartbeat</span>
          <strong title={row.heartbeat}>{row.heartbeat.replace(/^.* · /, "")}</strong>
        </div>
        <div>
          <span>scrape</span>
          <strong>{row.worker.scrape_failures}</strong>
        </div>
      </div>

      {hasSecondaryState ? (
        <div className="worker-resource-sections">
          <ResourceStrip title="Artifacts" items={row.artifact_states} />
          <ResourceStrip title="Cooldowns" items={row.cooldowns} />
          {row.worker.needs_restart ? <span className="worker-alert-chip">restart needed</span> : null}
          {row.worker.last_error ? <span className="worker-alert-chip" title={row.worker.last_error}>last error</span> : null}
        </div>
      ) : null}
    </article>
  );
}

function Meter({ value, label, valueLabel, tone }: { value: number; label: string; valueLabel: string; tone: "teal" | "amber" }) {
  return (
    <div className={`worker-meter ${tone}`} style={{ ["--meter" as string]: `${Math.round(value * 100)}%` }}>
      <span>{label}</span>
      <strong>{valueLabel}</strong>
    </div>
  );
}

function SignalChip({ label, value }: { label: string; value: string }) {
  return (
    <div className="worker-signal-chip">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
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
