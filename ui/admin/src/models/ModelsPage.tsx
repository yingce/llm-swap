import { useMemo, useState } from "react";

import type { ConfigResponse, StatusResponse } from "../api";
import { unloadModel, warmModel } from "../api";
import { ConfirmDialog, DetailPanel, EmptyState, StatusIndicator } from "../components/primitives";
import { buildModelRows, formatCompactNumber, modelCapacityLabel, modelTrafficLabel, type ModelRow } from "./modelView";

export function ModelsPage({
  status,
  configResponse,
  onAction
}: {
  status: StatusResponse | null;
  configResponse: ConfigResponse | null;
  onAction: (action: () => Promise<{ action: string; worker_id?: string; model?: string }>) => Promise<void>;
}) {
  const [query, setQuery] = useState("");
  const [includeDisabled, setIncludeDisabled] = useState(false);
  const [selectedName, setSelectedName] = useState<string | null>(null);
  const [confirmUnload, setConfirmUnload] = useState<{ model: string; workerId: string } | null>(null);
  const rows = useMemo(
    () => buildModelRows(status, configResponse, { query, includeDisabled }),
    [status, configResponse, query, includeDisabled]
  );
  const selected = rows.find((row) => row.name === selectedName) ?? rows[0] ?? null;

  return (
    <div className="models-workspace">
      <section className="model-toolbar">
        <div>
          <h2>Models</h2>
          <p>Live replicas joined with config runtime, aliases, and disabled drafts.</p>
        </div>
        <label className="model-search">
          <span>Search</span>
          <input value={query} placeholder="name, alias, runtime" onChange={(event) => setQuery(event.target.value)} />
        </label>
        <label className="switch-control compact-switch">
          <input type="checkbox" checked={includeDisabled} onChange={(event) => setIncludeDisabled(event.target.checked)} />
          <span className="switch-track" aria-hidden="true" />
          <span>Show disabled</span>
        </label>
      </section>

      {rows.length === 0 ? (
        <EmptyState title="No models match" body="Adjust search or include disabled config drafts." />
      ) : (
        <section className="model-ledger" aria-label="Model capacity and traffic">
          <div className="model-ledger-head" aria-hidden="true">
            <span>Model</span>
            <span>Replicas</span>
            <span>Capacity</span>
            <span>Traffic</span>
          </div>
          {rows.map((row) => (
            <article
              className={`model-ledger-row ${selected?.name === row.name ? "selected" : ""} ${row.disabled ? "disabled" : ""}`}
              key={row.name}
            >
              <button className="model-ledger-identity" onClick={() => setSelectedName(row.name)} aria-pressed={selected?.name === row.name}>
                <span className={`model-health-mark ${row.disabled ? "disabled" : row.available ? "ready" : "cold"}`} aria-hidden="true" />
                <span>
                  <strong>{row.name}</strong>
                  <small>{row.aliases.length > 0 ? `alias ${row.aliases.join(", ")}` : row.runtime}</small>
                </span>
              </button>
              <div className="model-ledger-replicas" aria-label={`${row.ready_workers} ready artifacts; ${row.running_workers} running replicas`}>
                <strong>{row.ready_workers}</strong><span>ready</span>
                <strong>{row.running_workers}</strong><span>running</span>
              </div>
              <div className="model-ledger-capacity" aria-label={modelCapacityLabel(row)}>
                <CapacitySignal label="active" value={row.capacity.active} max={row.capacity.max_active} />
                <CapacitySignal label="queued" value={row.capacity.queued} max={row.capacity.max_queue} />
              </div>
              <ModelTraffic row={row} />
            </article>
          ))}
        </section>
      )}

      {selected ? (
        <div className="model-ledger-detail">
          <ModelDetail
            row={selected}
            onWarm={(workerId) => void onAction(() => warmModel(selected.name, workerId))}
            onUnload={(workerId) => setConfirmUnload({ model: selected.name, workerId })}
          />
        </div>
      ) : null}

      <ConfirmDialog
        open={Boolean(confirmUnload)}
        title="Unload model replica?"
        body={confirmUnload ? `${confirmUnload.model} on ${confirmUnload.workerId} will be unloaded from llama-swap.` : ""}
        confirmLabel="Unload"
        destructive
        onCancel={() => setConfirmUnload(null)}
        onConfirm={() => {
          const target = confirmUnload;
          setConfirmUnload(null);
          if (target) {
            void onAction(() => unloadModel(target.model, target.workerId));
          }
        }}
      />
    </div>
  );
}

function CapacitySignal({ label, value, max }: { label: string; value: number; max: number }) {
  return (
    <span className="model-capacity-signal">
      <strong>{value}<i>/</i>{max}</strong>
      <small>{label}</small>
    </span>
  );
}

function ModelTraffic({ row }: { row: ModelRow }) {
  const traffic = row.status?.traffic;
  if (!traffic) {
    return <div className="model-ledger-traffic muted" aria-label="no traffic">No traffic</div>;
  }
  return (
    <div className="model-ledger-traffic" aria-label={modelTrafficLabel(row)}>
      <TrafficMetric label="requests" value={formatCompactNumber(traffic.requests)} />
      <TrafficMetric label="tokens" value={formatCompactNumber(traffic.total_tokens)} detail={`${formatCompactNumber(traffic.cache_tokens)} cache`} />
      <TrafficMetric label="avg" value={`${traffic.avg_duration_ms}ms`} />
      <TrafficMetric label="errors" value={`${formatCompactNumber(traffic.status_4xx)} / ${formatCompactNumber(traffic.status_5xx)}`} detail="4xx / 5xx" warn={traffic.status_4xx + traffic.status_5xx > 0} />
    </div>
  );
}

function TrafficMetric({ label, value, detail, warn = false }: { label: string; value: string; detail?: string; warn?: boolean }) {
  return (
    <span className={`model-traffic-metric ${warn ? "warn" : ""}`}>
      <small>{label}</small>
      <strong>{value}</strong>
      {detail ? <i>{detail}</i> : null}
    </span>
  );
}

function ModelDetail({
  row,
  onWarm,
  onUnload
}: {
  row: ModelRow;
  onWarm: (workerId: string) => void;
  onUnload: (workerId: string) => void;
}) {
  const warmWorker = row.status?.worker_statuses.find((worker) => worker.artifact_status === "ready" && worker.health === "healthy");
  const loadedWorker = row.status?.worker_statuses.find((worker) => worker.running_state === "ready" && worker.health === "healthy");
  return (
    <DetailPanel
      title={row.name}
      subtitle={row.aliases.length > 0 ? `aliases: ${row.aliases.join(", ")}` : "no aliases configured"}
      meta={<StatusIndicator tone={row.disabled ? "neutral" : row.available ? "good" : "warn"} label={row.disabled ? "disabled" : row.available ? "ready" : "not ready"} />}
    >
      <div className="model-detail-grid">
        <div>
          <strong>Runtime</strong>
          <span>{row.runtime}</span>
        </div>
        <div>
          <strong>Replica policy</strong>
          <span>{row.ready_workers}/{row.min_loaded} ready · max {row.max_loaded}</span>
        </div>
        <div>
          <strong>Artifact</strong>
          <span>{row.artifact || "not configured"}</span>
        </div>
      </div>
      <div className="model-replica-list">
        {(row.status?.worker_statuses ?? []).map((worker) => (
          <div className="model-replica-row" key={worker.worker_id}>
            <span>{worker.worker_id}</span>
            <StatusIndicator tone={worker.health === "healthy" ? "good" : "bad"} label={worker.running_state || worker.artifact_status} />
            {worker.cooldown_active ? <StatusIndicator tone="warn" label="cooldown" detail={worker.cooldown_reason} /> : null}
          </div>
        ))}
      </div>
      <div className="model-actions">
        <button disabled={!warmWorker || row.disabled} onClick={() => warmWorker && onWarm(warmWorker.worker_id)}>Warm</button>
        <button disabled={!loadedWorker || row.disabled} onClick={() => loadedWorker && onUnload(loadedWorker.worker_id)}>Unload</button>
      </div>
    </DetailPanel>
  );
}
