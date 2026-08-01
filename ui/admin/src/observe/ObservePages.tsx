import type { BillingSummary, RequestLogEntry, WorkerEvent } from "../api";
import type { EditableModelConfig } from "../modelLifecycle";
import { EmptyState, StatusIndicator } from "../components/primitives";
import { buildEventRows, buildRequestRows, classifyBillingError } from "./observeView";

export function RequestsPage({
  requests,
  hasMore,
  onMore
}: {
  requests: RequestLogEntry[];
  hasMore: boolean;
  onMore: () => void;
}) {
  const rows = buildRequestRows(requests);
  return (
    <ObserveTableShell title="Request log" subtitle="Dense request records from the gateway records store.">
      {rows.length === 0 ? <EmptyState title="No request records" body="No requests are available for this range." /> : (
        <table className="observe-table">
          <thead><tr><th>Time</th><th>Route</th><th>Status</th><th>Latency</th><th>Tokens</th><th>Detail</th></tr></thead>
          <tbody>{rows.map((row) => <tr key={row.id}><td>{formatTime(row.time)}</td><td>{row.route}</td><td>{row.status}</td><td>{row.latency}</td><td>{row.tokens}</td><td>{row.detail}</td></tr>)}</tbody>
        </table>
      )}
      {hasMore ? <button onClick={onMore}>Load more</button> : null}
    </ObserveTableShell>
  );
}

export function ActivityPage({
  events,
  hasMore,
  onMore
}: {
  events: WorkerEvent[];
  hasMore: boolean;
  onMore: () => void;
}) {
  const rows = buildEventRows(events);
  return (
    <ObserveTableShell title="Activity" subtitle="Worker events reported through gateway status and event history.">
      {rows.length === 0 ? <EmptyState title="No activity records" body="No worker events are available." /> : (
        <table className="observe-table">
          <thead><tr><th>Time</th><th>Subject</th><th>Event</th><th>Detail</th></tr></thead>
          <tbody>{rows.map((row) => <tr key={row.id}><td>{formatTime(row.time)}</td><td>{row.subject}</td><td>{row.event}</td><td>{row.detail}</td></tr>)}</tbody>
        </table>
      )}
      {hasMore ? <button onClick={onMore}>Load more</button> : null}
    </ObserveTableShell>
  );
}

export function BillingPage({
  billing,
  rangeHours,
  error,
  pricingDraft,
  pricingDirty,
  pricingMessage,
  pricingError,
  onRangeChange,
  onSavePricing
}: {
  billing: BillingSummary | null;
  rangeHours: number;
  error: string;
  pricingDraft: { models: Record<string, EditableModelConfig> } | null;
  pricingDirty: boolean;
  pricingMessage: string;
  pricingError: string;
  onRangeChange: (hours: number) => void;
  onSavePricing: () => void;
}) {
  const notice = classifyBillingError(error);
  return (
    <div className="observe-page">
      <section className="observe-heading">
        <div>
          <h2>Billing</h2>
          <p>Cost and usage records. Disabled local storage is informational; auth and network errors remain visible.</p>
        </div>
        <div className="billing-range-tabs" aria-label="Billing time range">
          {[1, 6, 24, 72, 168].map((hours) => (
            <button
              type="button"
              key={hours}
              className={rangeHours === hours ? "active" : ""}
              onClick={() => onRangeChange(hours)}
            >
              {formatRangeLabel(hours)}
            </button>
          ))}
        </div>
      </section>
      {notice ? <div className={notice.tone === "bad" ? "alert" : "notice"}><strong>{notice.title}</strong> · {notice.detail}</div> : null}
      {billing ? (
        <div className="billing-ledger">
          <div className="traffic-summary">
            <Metric label="Requests" value={formatNumber(billing.totals.requests)} />
            <Metric label="Tokens" value={formatNumber(billing.totals.total_tokens)} />
            <Metric label="Used cost" value={formatMoney(billing.totals.model_used_cost)} />
            <Metric label="Idle cost" value={formatMoney(billing.totals.model_idle_cost)} />
          </div>
          <div className="observe-table-wrap">
            <table className="observe-table">
              <thead><tr><th>Model</th><th>Requests</th><th>Tokens</th><th>Used</th></tr></thead>
              <tbody>{billing.models.map((model) => <tr key={model.model}><td>{model.model}</td><td>{formatNumber(model.requests)}</td><td>{formatNumber(model.total_tokens)}</td><td>{formatMoney(model.model_used_cost)}</td></tr>)}</tbody>
            </table>
          </div>
        </div>
      ) : !notice ? <EmptyState title="Billing is loading" body="Waiting for the selected range." /> : null}
      {pricingDraft ? (
        <div className="pricing-save-row">
          <StatusIndicator tone={pricingDirty ? "warn" : "good"} label={pricingDirty ? "pricing draft" : "in sync"} />
          {pricingMessage ? <span>{pricingMessage}</span> : null}
          {pricingError ? <span className="danger-text">{pricingError}</span> : null}
          <button disabled={!pricingDirty} onClick={onSavePricing}>Save pricing</button>
        </div>
      ) : null}
    </div>
  );
}

function ObserveTableShell({ title, subtitle, children }: { title: string; subtitle: string; children: React.ReactNode }) {
  return (
    <div className="observe-page">
      <section className="observe-heading">
        <div>
          <h2>{title}</h2>
          <p>{subtitle}</p>
        </div>
      </section>
      <div className="observe-table-wrap">{children}</div>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div className="metric"><strong>{value}</strong><span>{label}</span></div>;
}

function formatTime(value: string) {
  return value ? new Date(value).toLocaleTimeString() : "-";
}

function formatNumber(value: number | undefined) {
  return String(Math.round(Number(value ?? 0)));
}

function formatMoney(value: number | undefined) {
  return `$${Number(value ?? 0).toFixed(2)}`;
}

function formatRangeLabel(hours: number) {
  return hours < 24 ? `${hours}h` : `${hours / 24}d`;
}
