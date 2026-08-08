import { useMemo, useState } from "react";

import type { AgentCompatibilityStatus, StatusResponse } from "../api";
import { drainWorker, undrainWorker } from "../api";
import { ConfirmDialog, EmptyState } from "../components/primitives";
import { modelRuntimeLabel } from "../domain/modelRuntime";
import { buildWorkerFilters, buildWorkerRows, formatWorkerPressure, modelStateTone, workerHasDiagnosticWarning, type WorkerRow } from "./workerView";

export function agentCompatibilityGuidance(status: AgentCompatibilityStatus): string | null {
  switch (status) {
    case "legacy":
      return "Legacy Agent; upgrade Agent";
    case "upgrade_agent":
      return "Upgrade Agent";
    case "upgrade_gateway":
      return "Upgrade Gateway";
    case "compatible":
      return null;
  }
}

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
          <p>Flat GPU ledger: load, model residency, request pressure, queue headroom, and agent version.</p>
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
  const hasSecondaryState = row.cooldowns.length > 0 || row.worker.needs_restart || row.worker.last_error;
  const compatibilityGuidance = agentCompatibilityGuidance(row.worker.agent_version_status);
  const diagnosticsWarning = workerHasDiagnosticWarning(row.worker);
  const requestPressure = formatWorkerPressure("REQ", row.active_requests, row.max_concurrency, row.live_capacity_available);
  const queuePressure = formatWorkerPressure("QUEUE", row.queued_requests, row.max_queue, row.live_capacity_available);
  const modelNames = row.running_models.map((model) => model.model).join(" · ");
  const modelStates = row.running_models.map((model) => `${model.model} ${modelRuntimeLabel(model.state)}`).join(", ") || "idle";
  return (
    <article className={`worker-tile ${row.health === "healthy" ? "healthy" : "degraded"}`}>
      <header className="worker-tile-head">
        <div>
          <h3>{row.id}</h3>
          <p>{row.tags.join(" · ") || "untagged"}</p>
          <small className="worker-url" title={row.worker.llama_swap_url}>{row.worker.llama_swap_url}</small>
        </div>
        <div className="worker-head-actions">
          <div className={`worker-diagnostics${diagnosticsWarning ? " diagnostic-warning" : ""}`}>
            <button
              type="button"
              className="worker-diagnostics-trigger"
              aria-label={`Show diagnostics for ${row.id}`}
              aria-describedby={`worker-diagnostics-${row.id}`}
            >?</button>
            <dl id={`worker-diagnostics-${row.id}`} className="worker-diagnostics-popover" role="tooltip">
              <div><dt>Agent version</dt><dd>{row.agent_version}</dd></div>
              {compatibilityGuidance ? <div><dt>Compatibility</dt><dd>{compatibilityGuidance}</dd></div> : null}
              <div><dt>Heartbeat</dt><dd>{row.heartbeat}</dd></div>
              <div><dt>Scrape failures</dt><dd>{row.worker.scrape_failures}</dd></div>
            </dl>
          </div>
          {row.state === "draining" ? <button className="compact-action" onClick={onUndrain}>Undrain</button> : <button className="danger-ghost compact-action" onClick={onDrain}>Drain</button>}
        </div>
      </header>

      <div
        className="worker-request-strip"
        role="group"
        aria-label={`Executing: ${row.active_requests} current, ${requestPressure.maximumText}; queued: ${row.queued_requests} current, ${queuePressure.maximumText}; runtime: ${modelStates}`}
      >
        <PressureMeter label="REQ" current={row.active_requests} max={row.max_concurrency} available={row.live_capacity_available} />
        <PressureMeter label="QUEUE" current={row.queued_requests} max={row.max_queue} available={row.live_capacity_available} />
        <div className="worker-model-state-list" aria-label={row.running_models.length > 0 ? "Running model states" : "No running model"}>
          {row.running_models.length > 0 ? row.running_models.map((model) => (
            <span className={`worker-model-state model-state-${modelStateTone(model.state)}`} title={`${model.model}: ${modelRuntimeLabel(model.state)}`} key={`${model.model}:${model.state}`}>
              <i aria-hidden="true" />
              <small>{modelRuntimeLabel(model.state)}</small>
            </span>
          )) : <span className="worker-model-state model-state-neutral"><small>idle</small></span>}
        </div>
      </div>

      <div className="worker-model-board" aria-label={modelNames ? `Models: ${modelNames}` : "No loaded model"}>
        <strong>Model</strong>
        <span className="worker-model-names" title={modelNames || "No loaded model"}>{modelNames || "none"}</span>
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

function PressureMeter({ label, current, max, available }: { label: string; current: number; max: number; available: boolean }) {
  const pressure = formatWorkerPressure(label, current, max, available);

  return (
    <div className="worker-pressure-meter" title={pressure.title} aria-label={pressure.title}>
      <span>{label}</span>
      <strong>{current}<small>max {pressure.limitText}</small></strong>
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
