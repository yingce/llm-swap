import type { BillingGroupBy, BillingModelSummary, BillingSummary, ModelBillingConfig, RequestLogEntry, WorkerEvent } from "../api";
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
  groupBy,
  loading,
  rangeHours,
  error,
  pricingDraft,
  pricingDirty,
  pricingMessage,
  pricingError,
  onRangeChange,
  onGroupByChange,
  onPriceChange,
  onSavePricing
}: {
  billing: BillingSummary | null;
  groupBy: BillingGroupBy;
  loading: boolean;
  rangeHours: number;
  error: string;
  pricingDraft: { models: Record<string, EditableModelConfig> } | null;
  pricingDirty: boolean;
  pricingMessage: string;
  pricingError: string;
  onRangeChange: (hours: number) => void;
  onGroupByChange: (groupBy: BillingGroupBy) => void;
  onPriceChange: (modelName: string, field: keyof ModelBillingConfig, value: number | undefined) => void;
  onSavePricing: () => void;
}) {
  const notice = classifyBillingError(error);
  const pricingModels = Object.entries(pricingDraft?.models ?? {}).sort(([left], [right]) => left.localeCompare(right));
  const isCanonicalBilling = (billing?.group_by ?? groupBy ?? "canonical") === "canonical";
  const billingByModel = new Map((billing && isCanonicalBilling ? billing.models : []).map((model) => [model.model, model]));
  const occupancyGapHint = groupBy === "alias"
    ? "Allocated occupancy gap is allocated occupancy cost minus configured usage revenue. Negative values mean configured usage revenue exceeds occupancy cost."
    : "Uncovered occupancy is actual occupancy cost minus configured usage revenue. Negative values mean configured usage revenue exceeds occupancy cost.";
  return (
    <div className="observe-page">
      <section className="observe-heading">
        <div>
          <h2>Billing</h2>
          <p>Cost and usage records. Disabled local storage is informational; auth and network errors remain visible.</p>
        </div>
        <div className="billing-controls">
          <div className="billing-view-tabs" aria-label="Billing grouping">
            <button type="button" className={groupBy === "canonical" ? "active" : ""} aria-pressed={groupBy === "canonical"} onClick={() => onGroupByChange("canonical")}>Actual models</button>
            <button type="button" className={groupBy === "alias" ? "active" : ""} aria-pressed={groupBy === "alias"} onClick={() => onGroupByChange("alias")}>Service aliases</button>
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
        </div>
      </section>
      {notice ? <div className={notice.tone === "bad" ? "alert" : "notice"}><strong>{notice.title}</strong> · {notice.detail}</div> : null}
      {loading ? <EmptyState title="Billing is loading" body={`Loading ${groupBy === "alias" ? "service alias allocations" : "actual model costs"} for the selected range.`} /> : billing && billing.models.length > 0 ? (
        <div className="billing-ledger">
          <div className="traffic-summary">
            <Metric label="Requests" value={formatNumber(billing.totals.requests)} />
            <Metric label="Tokens" value={formatNumber(billing.totals.total_tokens)} />
            <Metric label="Configured usage cost" value={formatMoney(billing.totals.model_used_cost)} />
            <Metric label={groupBy === "alias" ? "Allocated model cost" : "Actual model cost"} value={formatMoney(billing.totals.model_cost)} />
            <Metric label={groupBy === "alias" ? "Allocated occupancy gap" : "Uncovered occupancy"} value={formatMoney(billing.totals.model_idle_cost)} hint={occupancyGapHint} />
          </div>
          <p className="billing-basis-note">
            {groupBy === "alias" ? "Occupancy cost is allocated by request share for this period; it is not the runtime accounting ledger. " : ""}
            {occupancyGapHint}
          </p>
          <div className="observe-table-wrap">
            <table className="observe-table">
              <thead><tr><th>{groupBy === "alias" ? "Service identity" : "Actual model"}</th><th>Requests</th><th>Tokens</th><th>Configured usage</th><th>{groupBy === "alias" ? "Allocated model cost" : "Actual model cost"}</th></tr></thead>
              <tbody>{billing.models.map((model) => <BillingModelRow key={`${model.group_kind ?? "canonical"}:${model.model}`} model={model} groupBy={groupBy} />)}</tbody>
            </table>
          </div>
        </div>
      ) : billing && !notice ? <EmptyState title="No billing activity" body={`No ${groupBy === "alias" ? "service alias or direct request" : "actual model"} costs were recorded for the selected range.`} /> : !notice ? <EmptyState title="Billing is loading" body="Waiting for the selected range." /> : null}
      {pricingDraft ? (
        <section className="billing-pricing-panel">
          <div className="table-heading">
            <div>
              <h3>Manual model pricing</h3>
              <p>{groupBy === "alias" ? "Configure USD pricing. Switch to Actual models for compute-cost pricing suggestions." : "Configure USD pricing. Each suggestion independently recovers compute cost from the selected range."}</p>
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

function BillingModelRow({ model, groupBy }: { model: BillingModelSummary; groupBy: BillingGroupBy }) {
  const isUnattributed = groupBy === "alias" && model.group_kind === "unattributed";
  const versions = model.canonical_versions ?? [];
  return (
    <tr>
      <td className="billing-identity-cell">
        <strong>{isUnattributed ? "Direct / historic traffic" : model.model}</strong>
        {isUnattributed ? <span className="billing-kind-label">Unattributed</span> : null}
        {groupBy === "alias" && model.group_kind === "alias" ? <span className="billing-kind-label">Service alias</span> : null}
        {groupBy === "alias" && versions.length > 0 ? (
          <details className="billing-version-breakdown">
            <summary>{versions.length} canonical {versions.length === 1 ? "version" : "versions"}</summary>
            <ul>
              {versions.map((version) => (
                <li key={version.canonical_model}>
                  <span>{version.canonical_model}</span>
                  <small>{formatNumber(version.requests)} req · {formatNumber(version.total_tokens)} tokens · {formatMoney(version.model_used_cost)} configured usage · {formatMoney(version.allocated_model_cost)} allocated</small>
                </li>
              ))}
            </ul>
          </details>
        ) : null}
      </td>
      <td>{formatNumber(model.requests)}</td>
      <td>{formatNumber(model.total_tokens)}</td>
      <td>{formatMoney(model.model_used_cost)}</td>
      <td>{formatMoney(model.model_cost)}</td>
    </tr>
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

function Metric({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return <div className="metric" title={hint}><strong>{value}</strong><span>{label}</span></div>;
}

function formatTime(value: string) {
  return value ? new Date(value).toLocaleTimeString() : "-";
}

function formatNumber(value: number | undefined) {
  return String(Math.round(Number(value ?? 0)));
}

function formatMoney(value: number | undefined) {
  const amount = Number(value ?? 0);
  return amount < 0 ? `-$${Math.abs(amount).toFixed(2)}` : `$${amount.toFixed(2)}`;
}

function formatPricingValue(value: number) {
  return `$${value.toFixed(6).replace(/0+$/, "").replace(/\.$/, "")}`;
}

function formatRangeLabel(hours: number) {
  return hours < 24 ? `${hours}h` : `${hours / 24}d`;
}
