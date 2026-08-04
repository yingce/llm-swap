import type { BillingSummary, ModelBillingConfig, RequestLogEntry, WorkerEvent } from "../api";
import type { EditableModelConfig } from "../modelLifecycle";
import { EmptyState, StatusIndicator } from "../components/primitives";
import { buildEventRows, buildRequestRows, classifyBillingError, recommendedBillingPricing } from "./observeView";

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
  onPriceChange,
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
  onPriceChange: (modelName: string, field: keyof ModelBillingConfig, value: number | undefined) => void;
  onSavePricing: () => void;
}) {
  const notice = classifyBillingError(error);
  const pricingModels = Object.entries(pricingDraft?.models ?? {}).sort(([left], [right]) => left.localeCompare(right));
  const billingByModel = new Map((billing?.models ?? []).map((model) => [model.model, model]));
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
        <section className="billing-pricing-panel">
          <div className="table-heading">
            <div>
              <h3>Manual model pricing</h3>
              <p>Configure USD pricing. Each suggestion independently recovers compute cost from the selected range.</p>
            </div>
            <div className="pricing-save-row">
              <StatusIndicator tone={pricingDirty ? "warn" : "good"} label={pricingDirty ? "pricing draft" : "in sync"} />
              <button disabled={!pricingDirty} onClick={onSavePricing}>Save pricing</button>
            </div>
          </div>
          {pricingMessage ? <div className="notice">{pricingMessage}</div> : null}
          {pricingError ? <div className="alert">{pricingError}</div> : null}
          <div className="observe-table-wrap">
            <table className="observe-table pricing-table">
              <thead>
                <tr>
                  <th>Model</th>
                  <th>Per request</th>
                  <th>Input / 1M</th>
                  <th>Output / 1M</th>
                  <th>Cached / 1M</th>
                </tr>
              </thead>
              <tbody>
                {pricingModels.map(([modelName, model]) => {
                  const pricing = model.billing ?? {};
                  const recommended = recommendedBillingPricing(billingByModel.get(modelName), billing?.worker_day_cost_usd);
                  return (
                    <tr key={modelName}>
                      <td>
                        <strong>{modelName}</strong>
                        {model.disabled ? <small className="muted-cell"> disabled</small> : null}
                      </td>
                      <td><PricingCell model={modelName} label="Per request" value={pricing.per_request_usd} recommended={recommended.per_request_usd} onChange={(value) => onPriceChange(modelName, "per_request_usd", value)} /></td>
                      <td><PricingCell model={modelName} label="Input per million" value={pricing.input_per_million_usd} recommended={recommended.input_per_million_usd} onChange={(value) => onPriceChange(modelName, "input_per_million_usd", value)} /></td>
                      <td><PricingCell model={modelName} label="Output per million" value={pricing.output_per_million_usd} recommended={recommended.output_per_million_usd} onChange={(value) => onPriceChange(modelName, "output_per_million_usd", value)} /></td>
                      <td><PricingCell model={modelName} label="Cached input per million" value={pricing.cached_input_per_million_usd} recommended={recommended.cached_input_per_million_usd} onChange={(value) => onPriceChange(modelName, "cached_input_per_million_usd", value)} /></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      ) : null}
    </div>
  );
}

function PricingCell({
  model,
  label,
  value,
  recommended,
  onChange
}: {
  model: string;
  label: string;
  value: number | undefined;
  recommended: number | undefined;
  onChange: (value: number | undefined) => void;
}) {
  return (
    <div className="price-cell">
      <input
        className="price-input"
        type="number"
        min="0"
        step="0.000001"
        inputMode="decimal"
        aria-label={`${model} ${label}`}
        value={value ?? ""}
        onChange={(event) => onChange(parseOptionalPrice(event.target.value))}
      />
      {typeof recommended === "number" ? (
        <button type="button" className="price-recommendation" onClick={() => onChange(recommended)}>
          Use {formatPricingValue(recommended)}
        </button>
      ) : <span className="price-recommendation empty-recommendation">No range data</span>}
    </div>
  );
}

function parseOptionalPrice(raw: string) {
  const value = raw.trim();
  if (!value) {
    return undefined;
  }
  const parsed = Number(value);
  return Number.isFinite(parsed) ? Math.max(0, parsed) : undefined;
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

function formatPricingValue(value: number) {
  return `$${value.toFixed(6).replace(/0+$/, "").replace(/\.$/, "")}`;
}

function formatRangeLabel(hours: number) {
  return hours < 24 ? `${hours}h` : `${hours / 24}d`;
}
