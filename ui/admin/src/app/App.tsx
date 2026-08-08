import React, { useEffect, useMemo, useReducer, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  applyConfig,
  BillingSummary,
  ConfigChange,
  ConfigImpact,
  ConfigResponse,
  getConfig,
  getBilling,
  getEvents,
  getStatus,
  getRequests,
  getServiceNameEligibility,
  ModelBillingConfig,
  ModelStatus,
  ServiceNamePromotion,
  ServiceNameTargetEligibility,
  RequestLogEntry,
  StatusResponse,
  TagPolicyConfig,
  unloadModel,
  warmModel,
  WorkerEvent,
  dryRunConfig,
  promoteServiceName,
  rollbackServiceName
} from "../api";
import { removeAlias, setAliasTarget, validateAliasDraft } from "../modelAliases";
import {
  MODEL_RUNTIME_OPTIONS,
  emptyEditableModel,
  isModelCreateDraftDirty,
  modelTagCapacity,
  setModelTagCapacity,
  setModelTagEnabled,
  setModelTagMembership,
  validateNewModelName,
  type EditableModelConfig,
  type ModelCreateDraft
} from "../modelLifecycle";
import { AppShell } from "./AppShell";
import { STATUS_REFRESH_INTERVAL_MS, reduceLiveStatus } from "./liveStatus";
import { pathForTab, shouldPushTabPath, tabFromPath, type Tab } from "./navigation";
import { appendRoutePage, routeDataKeysForTab } from "./routeData";
import { OverviewPage } from "../overview/OverviewPage";
import { ModelsPage } from "../models/ModelsPage";
import { WorkersPage } from "../workers/WorkersPage";
import { ActivityPage, BillingPage, RequestsPage } from "../observe/ObservePages";
import { reduceBillingView } from "../observe/observeView";
import {
  cloneEditableConfig as cloneConfigDraft,
  hasLegacyCapacityCeiling,
  renderDraftYAML as renderConfigDraftYAML,
  toEditableConfig as toEditableGatewayConfig
} from "../config/configDraft";
import { tokenizeYAML } from "../config/yamlHighlight";

type EditableGatewayConfig = {
  models: Record<string, EditableModelConfig>;
  model_aliases: Record<string, string>;
  tag_policies: Record<string, TagPolicyConfig>;
};

type ModelBillingPriceField = keyof ModelBillingConfig;
type BillingInputTokenMode = "total" | "billable";
const secondsPerDay = 86400;

export function App() {
  const appContentRef = useRef<HTMLElement>(null);
  const [tab, setTab] = useState<Tab>(() => tabFromPath(window.location.pathname));
  const [liveStatus, setLiveStatus] = useState<{ status: StatusResponse | null; error: string }>({
    status: null,
    error: ""
  });
  const [events, setEvents] = useState<WorkerEvent[]>([]);
  const [eventOffset, setEventOffset] = useState(0);
  const [hasMoreEvents, setHasMoreEvents] = useState(false);
  const [requests, setRequests] = useState<RequestLogEntry[]>([]);
  const [requestOffset, setRequestOffset] = useState(0);
  const [hasMoreRequests, setHasMoreRequests] = useState(false);
  const [billingView, dispatchBillingView] = useReducer(reduceBillingView, {
    billing: null,
    rangeHours: 24,
    groupBy: "canonical",
    loading: false,
    error: "",
    requestID: 0
  });
  const billingRequestID = useRef(0);
  const [notice, setNotice] = useState("");

  const [configResponse, setConfigResponse] = useState<ConfigResponse | null>(null);
  const [configDraft, setConfigDraft] = useState<EditableGatewayConfig | null>(null);
  const [configChanges, setConfigChanges] = useState<ConfigChange[]>([]);
  const [configImpacts, setConfigImpacts] = useState<ConfigImpact[]>([]);
  const [configApplyMode, setConfigApplyMode] = useState("");
  const [configMessage, setConfigMessage] = useState("");
  const [configError, setConfigError] = useState("");

  async function refresh() {
    try {
      const next = await getStatus();
      setLiveStatus((current) => reduceLiveStatus(current, { type: "success", status: next }));
    } catch (err) {
      setLiveStatus((current) =>
        reduceLiveStatus(current, { type: "failure", error: err instanceof Error ? err.message : String(err) })
      );
    }
  }

  async function refreshConfig() {
    try {
      const next = await getConfig();
      setConfigResponse(next);
      setConfigDraft(toEditableGatewayConfig(next));
      setConfigChanges([]);
      setConfigImpacts([]);
      setConfigApplyMode("");
      setConfigMessage("");
      setConfigError("");
    } catch (err) {
      setConfigError(err instanceof Error ? err.message : String(err));
    }
  }

  async function loadEvents(offset = 0) {
    const page = await getEvents(offset, 50);
    setEvents((current) => appendRoutePage(current, page.events, offset));
    setEventOffset(page.next_offset);
    setHasMoreEvents(page.has_more);
  }

  async function loadRequests(offset = 0) {
    const page = await getRequests(offset, 50);
    setRequests((current) => appendRoutePage(current, page.requests, offset));
    setRequestOffset(page.next_offset);
    setHasMoreRequests(page.has_more);
  }

  async function loadBilling(rangeHours = billingView.rangeHours, groupBy = billingView.groupBy) {
    const requestID = ++billingRequestID.current;
    dispatchBillingView({ type: "start", requestID, rangeHours, groupBy });
    try {
      const next = await getBilling(rangeHours, true, groupBy);
      dispatchBillingView({ type: "success", requestID, billing: next });
    } catch (err) {
      dispatchBillingView({ type: "failure", requestID, error: err instanceof Error ? err.message : String(err) });
    }
  }

  async function runAction(action: () => Promise<{ action: string; worker_id?: string; model?: string }>) {
    try {
      const result = await action();
      setNotice(`${result.action} done${result.model ? ` · ${result.model}` : ""}${result.worker_id ? ` · ${result.worker_id}` : ""}`);
      await refresh();
      if (routeDataKeysForTab(tab).includes("events")) {
        await loadEvents(0);
      }
    } catch (err) {
      setLiveStatus((current) =>
        reduceLiveStatus(current, { type: "failure", error: err instanceof Error ? err.message : String(err) })
      );
    }
  }

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(refresh, STATUS_REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    const initialTab = tabFromPath(window.location.pathname);
    if (pathForTab(initialTab) !== window.location.pathname) {
      window.history.replaceState(null, "", pathForTab(initialTab));
    }

    const handlePopState = () => setTab(tabFromPath(window.location.pathname));
    window.addEventListener("popstate", handlePopState);
    return () => window.removeEventListener("popstate", handlePopState);
  }, []);

  function selectTab(next: Tab) {
    if (shouldPushTabPath(window.location.pathname, next)) {
      window.history.pushState(null, "", pathForTab(next));
    }
    setTab(next);
  }

  useEffect(() => {
    const keys = routeDataKeysForTab(tab);
    if (keys.includes("config") && !configResponse) {
      void refreshConfig();
    }
    if (keys.includes("events") && events.length === 0 && eventOffset === 0) {
      void loadEvents(0);
    }
    if (keys.includes("requests") && requests.length === 0 && requestOffset === 0) {
      void loadRequests(0);
    }
    if (keys.includes("billing") && !billingView.billing && !billingView.loading) {
      void loadBilling(24);
    }
  }, [tab]);

  const status = liveStatus.status;
  const error = liveStatus.error;
  const summary = status?.summary;
  const renderedConfigYaml = useMemo(() => {
    if (!configResponse || !configDraft) {
      return "";
    }
    return renderConfigDraftYAML(configResponse.yaml, configDraft);
  }, [configResponse, configDraft]);
  const configDirty = Boolean(configResponse && renderedConfigYaml !== configResponse.yaml);

  function updateDraft(mutator: (draft: EditableGatewayConfig) => void) {
    setConfigDraft((current) => {
      if (!current) {
        return current;
      }
      const next = cloneConfigDraft(current);
      mutator(next);
      return next;
    });
    setConfigChanges([]);
    setConfigImpacts([]);
    setConfigApplyMode("");
    setConfigMessage("");
    setConfigError("");
  }

  function replaceModel(modelName: string, nextModel: EditableModelConfig) {
    updateDraft((draft) => {
      draft.models[modelName] = nextModel;
    });
  }

  function createModel(modelName: string, model: EditableModelConfig, selectedTags: string[]) {
    updateDraft((draft) => {
      draft.models[modelName] = model;
      draft.tag_policies = setModelTagMembership(draft.tag_policies, modelName, selectedTags);
    });
  }

  function replaceModelTags(modelName: string, nextModel: EditableModelConfig, selectedTags: string[]) {
    updateDraft((draft) => {
      draft.models[modelName] = nextModel;
      draft.tag_policies = setModelTagMembership(draft.tag_policies, modelName, selectedTags);
    });
  }

  function replaceModelAliases(nextAliases: Record<string, string>) {
    updateDraft((draft) => {
      draft.model_aliases = { ...nextAliases };
    });
  }

  function updateModelBillingPrice(modelName: string, field: ModelBillingPriceField, value: number | undefined) {
    updateDraft((draft) => {
      const model = draft.models[modelName];
      if (!model) {
        return;
      }
      const billing: ModelBillingConfig = { ...(model.billing ?? {}) };
      if (value === undefined) {
        delete billing[field];
      } else {
        billing[field] = value;
      }
      model.billing = hasBillingValues(billing) ? billing : undefined;
    });
  }

  function resetDraft() {
    if (!configResponse) {
      return;
    }
    setConfigDraft(toEditableGatewayConfig(configResponse));
    setConfigChanges([]);
    setConfigImpacts([]);
    setConfigApplyMode("");
    setConfigMessage("Draft reset to the current gateway config.");
    setConfigError("");
  }

  async function dryRunDraft() {
    if (!configResponse || !configDraft) {
      return;
    }
    try {
      const result = await dryRunConfig(renderedConfigYaml);
      setConfigChanges(result.changes ?? []);
      setConfigImpacts(result.impacts ?? []);
      setConfigApplyMode(result.apply_mode);
      setConfigMessage(
        result.requires_gateway_restart
          ? "Valid. Apply will only save this config. Restart gateway to activate process-level changes."
          : "Valid. This change set can be hot-applied."
      );
      setConfigError("");
    } catch (err) {
      setConfigError(err instanceof Error ? err.message : String(err));
    }
  }

  async function applyDraft() {
    if (!configResponse || !configDraft) {
      return;
    }
    try {
      const result = await applyConfig(renderedConfigYaml);
      setConfigChanges(result.changes ?? []);
      setConfigImpacts(result.impacts ?? []);
      setConfigApplyMode(result.apply_mode);
      setConfigMessage(
        result.requires_gateway_restart
          ? "Saved. Restart gateway to activate process-level changes."
          : `Applied version ${result.version}.`
      );
      setConfigError("");
      await refreshConfig();
      await refresh();
      if (routeDataKeysForTab(tab).includes("events")) {
        await loadEvents(0);
      }
    } catch (err) {
      setConfigError(err instanceof Error ? err.message : String(err));
    }
  }

  async function applyPricingDraft() {
    await applyDraft();
    await loadBilling(billingView.rangeHours, billingView.groupBy);
  }

  async function copyAdvancedYAML() {
    try {
      await navigator.clipboard.writeText(renderedConfigYaml);
      setConfigMessage("Advanced YAML copied.");
      setConfigError("");
    } catch (err) {
      setConfigError(err instanceof Error ? err.message : String(err));
    }
  }

  async function promoteCanonicalServiceName(serviceName: string, targetModel: string) {
    const promotion = await promoteServiceName(serviceName, targetModel);
    await refreshConfig();
    await refresh();
    await loadEvents(0);
    return promotion;
  }

  async function rollbackCanonicalServiceName(promotion: ServiceNamePromotion) {
    const result = await rollbackServiceName(promotion);
    await refreshConfig();
    await refresh();
    await loadEvents(0);
    return result;
  }

  return (
    <AppShell
      appContentRef={appContentRef}
      tab={tab}
      summary={summary}
      statusUpdatedAt={status?.generated_at}
      error={error}
      notice={notice}
      onRefresh={() => void refresh()}
      onSelectTab={selectTab}
    >
          {tab === "dashboard" && (
            <OverviewPage status={status} />
          )}
          {tab === "models" && <ModelsPage status={status} configResponse={configResponse} onAction={runAction} />}
          {tab === "workers" && <WorkersPage status={status} onAction={runAction} />}
          {tab === "billing" && (
            <BillingPage
              billing={billingView.billing}
              groupBy={billingView.groupBy}
              loading={billingView.loading}
              rangeHours={billingView.rangeHours}
              error={billingView.error}
              pricingDraft={configDraft}
              pricingDirty={configDirty}
              pricingMessage={configMessage}
              pricingError={configError}
              onRangeChange={(hours) => void loadBilling(hours, billingView.groupBy)}
              onGroupByChange={(groupBy) => void loadBilling(billingView.rangeHours, groupBy)}
              onPriceChange={updateModelBillingPrice}
              onSavePricing={() => void applyPricingDraft()}
            />
          )}
          {tab === "events" && (
            <ActivityPage events={events} hasMore={hasMoreEvents} onMore={() => void loadEvents(eventOffset)} />
          )}
          {tab === "requests" && (
            <RequestsPage requests={requests} hasMore={hasMoreRequests} onMore={() => void loadRequests(requestOffset)} />
          )}
          {tab === "configOps" && (
            <ConfigOps
              status={status}
              configResponse={configResponse}
              draft={configDraft}
              dirty={configDirty}
              changes={configChanges}
              impacts={configImpacts}
              applyMode={configApplyMode}
              message={configMessage}
              error={configError}
              onReset={resetDraft}
              onDryRun={() => void dryRunDraft()}
              onApply={() => void applyDraft()}
              onModelChange={replaceModel}
              onModelTagsChange={replaceModelTags}
              onCreateModel={createModel}
              onAliasesChange={replaceModelAliases}
              onPromoteServiceName={promoteCanonicalServiceName}
              onRollbackServiceName={rollbackCanonicalServiceName}
              appContentRef={appContentRef}
            />
          )}
          {tab === "advanced" && (
            <AdvancedConfig
              version={configResponse?.version ?? null}
              yaml={renderedConfigYaml}
              dirty={configDirty}
              message={configMessage}
              error={configError}
              onCopy={() => void copyAdvancedYAML()}
            />
          )}
    </AppShell>
  );
}

function Metric({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="metric">
      <strong>{value}</strong>
      <span>{label}</span>
    </div>
  );
}

function PriceField({
  value,
  disabled = false,
  onChange
}: {
  value: number | undefined;
  disabled?: boolean;
  onChange: (value: number | undefined) => void;
}) {
  return (
    <input
      className="price-input"
      type="number"
      min="0"
      step="0.000001"
      inputMode="decimal"
      value={value ?? ""}
      disabled={disabled}
      onChange={(event) => onChange(parseOptionalPrice(event.target.value))}
    />
  );
}

function PricingCell({
  value,
  recommended,
  disabled = false,
  onChange
}: {
  value: number | undefined;
  recommended: number | undefined;
  disabled?: boolean;
  onChange: (value: number | undefined) => void;
}) {
  return (
    <div className="price-cell">
      <PriceField value={value} disabled={disabled} onChange={onChange} />
      {recommended !== undefined ? (
        <button type="button" className="price-recommendation" disabled={disabled} onClick={() => onChange(recommended)}>
          Use {formatPricingValue(recommended)}
        </button>
      ) : (
        <span className="price-recommendation empty-recommendation">No range data</span>
      )}
    </div>
  );
}

function Dashboard({
  status,
  events,
  requests,
  onAction
}: {
  status: StatusResponse | null;
  events: WorkerEvent[];
  requests: RequestLogEntry[];
  onAction: (action: () => Promise<{ action: string; worker_id?: string; model?: string }>) => Promise<void>;
}) {
  const topModels = useMemo(() => {
    return [...(status?.models ?? [])].sort((a, b) => b.traffic.requests - a.traffic.requests).slice(0, 5);
  }, [status]);
  const traffic = useMemo(() => aggregateTraffic(status?.models ?? []), [status]);
  return (
    <div className="stack">
      <h2>Overview</h2>
      <div className="traffic-summary">
        <Metric label="Requests" value={compactNumber(traffic.requests)} />
        <Metric label="Total tokens" value={compactNumber(traffic.totalTokens)} />
        <Metric label="Cache tokens" value={compactNumber(traffic.cacheTokens)} />
        <Metric label="Avg latency" value={`${traffic.avgDurationMS}ms`} />
        <Metric label="5xx responses" value={compactNumber(traffic.status5xx)} />
      </div>
      <div className="split">
        <div>
          <h3>Traffic leaders</h3>
          <Models models={topModels} compact onAction={onAction} />
        </div>
        <div className="stack">
          <div>
            <h3>Recent worker events</h3>
            <EventList events={events} compact />
          </div>
          <div>
            <h3>Recent requests</h3>
            <RequestList requests={requests} compact />
          </div>
        </div>
      </div>
    </div>
  );
}

function Models({
  models,
  compact = false,
  onAction
}: {
  models: ModelStatus[];
  compact?: boolean;
  onAction: (action: () => Promise<{ action: string; worker_id?: string; model?: string }>) => Promise<void>;
}) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Model</th>
            <th>Availability</th>
            <th>Replicas</th>
            <th>Traffic</th>
            {!compact ? <th>Artifact</th> : null}
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {models.map((model) => (
            <tr key={model.name}>
              <td>
                <strong>{model.name}</strong>
                <small>priority {model.priority}</small>
              </td>
              <td><Badge tone={model.available ? "good" : "bad"}>{model.available ? "ready" : "blocked"}</Badge></td>
              <td>{model.ready_workers} ready / {model.running_workers} running</td>
              <td>
                <TrafficCell traffic={model.traffic} compact={compact} />
              </td>
              {!compact ? <td className="mono">{model.artifact.kind} · {model.artifact.object}</td> : null}
              <td>
                <ModelActions model={model} onAction={onAction} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ModelActions({
  model,
  onAction
}: {
  model: ModelStatus;
  onAction: (action: () => Promise<{ action: string; worker_id?: string; model?: string }>) => Promise<void>;
}) {
  const warmWorker = model.worker_statuses.find((worker) => worker.artifact_status === "ready" && worker.health === "healthy");
  const loadedWorker = model.worker_statuses.find((worker) => worker.running_state === "ready" && worker.health === "healthy");
  return (
    <div className="actions">
      <button disabled={!warmWorker} onClick={() => warmWorker && void onAction(() => warmModel(model.name, warmWorker.worker_id))}>
        Warm
      </button>
      <button disabled={!loadedWorker} onClick={() => loadedWorker && void onAction(() => unloadModel(model.name, loadedWorker.worker_id))}>
        Unload
      </button>
    </div>
  );
}

function Events({ events, hasMore, onMore }: { events: WorkerEvent[]; hasMore: boolean; onMore: () => void }) {
  return (
    <div className="stack">
      <EventList events={events} />
      {hasMore ? <button onClick={onMore}>Load more</button> : null}
    </div>
  );
}

function Requests({
  requests,
  hasMore,
  onMore
}: {
  requests: RequestLogEntry[];
  hasMore: boolean;
  onMore: () => void;
}) {
  return (
    <div className="stack">
      <RequestList requests={requests} />
      {hasMore ? <button onClick={onMore}>Load more</button> : null}
    </div>
  );
}

function Billing({
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
  pricingDraft: EditableGatewayConfig | null;
  pricingDirty: boolean;
  pricingMessage: string;
  pricingError: string;
  onRangeChange: (hours: number) => void;
  onPriceChange: (modelName: string, field: ModelBillingPriceField, value: number | undefined) => void;
  onSavePricing: () => void;
}) {
  const [showDisabledPricing, setShowDisabledPricing] = useState(false);
  const [inputTokenMode, setInputTokenMode] = useState<BillingInputTokenMode>("total");
  const recentRequestCosts = billing?.request_costs?.slice(-20).reverse() ?? [];
  const billingModelMap = useMemo(() => new Map((billing?.models ?? []).map((model) => [model.model, model])), [billing]);
  const inputTokenHeading = inputTokenMode === "billable" ? "Billable input" : "Input total";
  const pricingModelNames = useMemo(() => {
    const names = new Set<string>();
    for (const modelName of Object.keys(pricingDraft?.models ?? {})) {
      names.add(modelName);
    }
    for (const model of billing?.models ?? []) {
      names.add(model.model);
    }
    return Array.from(names)
      .filter((modelName) => showDisabledPricing || !pricingDraft?.models[modelName]?.disabled)
      .sort((a, b) => a.localeCompare(b));
  }, [billing, pricingDraft, showDisabledPricing]);
  return (
    <div className="stack">
      <div className="config-toolbar">
        <div>
          <h2>Billing</h2>
          <p className="toolbar-sub">
            {billing ? `${new Date(billing.start).toLocaleString()} - ${new Date(billing.end).toLocaleString()}` : "Loading cost records"}
          </p>
        </div>
        <div className="config-toolbar-actions">
          <div className="segmented" aria-label="Input token display">
            <button
              className={inputTokenMode === "total" ? "active" : ""}
              title="Prompt tokens, including cached input."
              onClick={() => setInputTokenMode("total")}
            >
              Input total
            </button>
            <button
              className={inputTokenMode === "billable" ? "active" : ""}
              title="Prompt tokens minus cached input."
              onClick={() => setInputTokenMode("billable")}
            >
              Billable input
            </button>
          </div>
          <div className="segmented" aria-label="Billing time range">
            {[6, 24, 72, 168].map((hours) => (
              <button key={hours} className={rangeHours === hours ? "active" : ""} onClick={() => onRangeChange(hours)}>
                {hours < 24 ? `${hours}h` : `${hours / 24}d`}
              </button>
            ))}
          </div>
        </div>
      </div>

      {error ? <div className="alert">Billing unavailable: {error}</div> : null}
      {billing?.exchange_rate_stale ? <div className="notice">Using stale or fallback CNY/USD exchange rate.</div> : null}
      {pricingError ? <div className="alert">Pricing config unavailable: {pricingError}</div> : null}
      {pricingMessage ? <div className="notice">{pricingMessage}</div> : null}

      <div className="traffic-summary">
        <Metric label="Model cost" value={formatMoney(billing?.totals.model_cost)} />
        <Metric label="Used cost" value={formatMoney(billing?.totals.model_used_cost)} />
        <Metric label="Idle cost" value={formatMoney(billing?.totals.model_idle_cost)} />
        <Metric label="Billable hours" value={formatHours(billing?.totals.billable_worker_seconds)} />
        <Metric label="Requests" value={compactNumber(billing?.totals.requests)} />
        <Metric label="Total tokens" value={compactNumber(billing?.totals.total_tokens)} />
        <Metric label="Worker day price" value={formatMoney(billing?.worker_day_cost_usd)} />
        <Metric label="CNY/USD" value={formatRate(billing?.exchange_rate_cny_to_usd)} />
      </div>

      <div className="table-wrap">
        <div className="table-heading">
          <h3>Manual model pricing</h3>
          <div className="config-toolbar-actions">
            <label className="switch-control compact-switch">
              <input
                type="checkbox"
                checked={showDisabledPricing}
                onChange={(event) => setShowDisabledPricing(event.target.checked)}
              />
              <span className="switch-track" aria-hidden="true" />
              <span>Show disabled</span>
            </label>
            <Badge tone={pricingDirty ? "warn" : "good"}>{pricingDirty ? "draft changed" : "in sync"}</Badge>
            <button className="primary" disabled={!pricingDirty} onClick={onSavePricing}>Save pricing</button>
          </div>
        </div>
        <table className="pricing-table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Requests</th>
              <th>Cost</th>
              <th>Used cost</th>
              <th>Per request</th>
              <th>Input / 1M</th>
              <th>Output / 1M</th>
              <th>Cached / 1M</th>
            </tr>
          </thead>
          <tbody>
            {pricingModelNames.map((modelName) => {
              const model = pricingDraft?.models[modelName];
              const billingRow = billingModelMap.get(modelName);
              const pricing = model?.billing ?? {};
              const recommended = recommendedBillingPricing(billingRow, billing?.worker_day_cost_usd);
              return (
                <tr key={modelName}>
                  <td>
                    <strong>{modelName}</strong>
                    {model?.disabled ? <Badge tone="warn">disabled</Badge> : null}
                  </td>
                  <td>{compactNumber(billingRow?.requests)}</td>
                  <td>{formatMoney(billingRow?.model_cost)}</td>
                  <td>{formatMoney(billingRow?.model_used_cost)}</td>
                  <td>
                    <PricingCell
                      value={pricing.per_request_usd}
                      recommended={recommended.per_request_usd}
                      onChange={(value) => onPriceChange(modelName, "per_request_usd", value)}
                      disabled={!model}
                    />
                  </td>
                  <td>
                    <PricingCell
                      value={pricing.input_per_million_usd}
                      recommended={recommended.input_per_million_usd}
                      onChange={(value) => onPriceChange(modelName, "input_per_million_usd", value)}
                      disabled={!model}
                    />
                  </td>
                  <td>
                    <PricingCell
                      value={pricing.output_per_million_usd}
                      recommended={recommended.output_per_million_usd}
                      onChange={(value) => onPriceChange(modelName, "output_per_million_usd", value)}
                      disabled={!model}
                    />
                  </td>
                  <td>
                    <PricingCell
                      value={pricing.cached_input_per_million_usd}
                      recommended={recommended.cached_input_per_million_usd}
                      onChange={(value) => onPriceChange(modelName, "cached_input_per_million_usd", value)}
                      disabled={!model}
                    />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <div className="table-wrap">
        <h3>Model cost</h3>
        <table>
          <thead>
            <tr>
              <th>Model</th>
              <th>Cost</th>
              <th>Used</th>
              <th>Idle</th>
              <th>Billable time</th>
              <th>Requests</th>
              <th>{inputTokenHeading}</th>
              <th>Output</th>
              <th>Cached</th>
              <th>Total tokens</th>
            </tr>
          </thead>
          <tbody>
            {(billing?.models ?? []).map((model) => (
              <tr key={model.model}>
                <td><strong>{model.model}</strong></td>
                <td>{formatMoney(model.model_cost)}</td>
                <td>{formatMoney(model.model_used_cost)}</td>
                <td>{formatMoney(model.model_idle_cost)}</td>
                <td>{formatHours(model.billable_worker_seconds)}</td>
                <td>{compactNumber(model.requests)}</td>
                <td>{compactNumber(displayInputTokens(model, inputTokenMode))}</td>
                <td>{compactNumber(model.output_tokens)}</td>
                <td>{compactNumber(model.cached_input_tokens)}</td>
                <td>{compactNumber(model.total_tokens)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="table-wrap">
        <h3>App cost</h3>
        <table>
          <thead>
            <tr>
              <th>App ID</th>
              <th>Requests</th>
              <th>{inputTokenHeading}</th>
              <th>Output</th>
              <th>Cached</th>
              <th>Total tokens</th>
              <th>Cost</th>
              <th>Used cost</th>
              <th>Idle cost</th>
            </tr>
          </thead>
          <tbody>
            {(billing?.apps ?? []).map((app) => (
              <tr key={app.app_id}>
                <td><strong>{app.app_id}</strong></td>
                <td>{compactNumber(app.requests)}</td>
                <td>{compactNumber(displayInputTokens(app, inputTokenMode))}</td>
                <td>{compactNumber(app.output_tokens)}</td>
                <td>{compactNumber(app.cached_input_tokens)}</td>
                <td>{compactNumber(app.total_tokens)}</td>
                <td>{formatMoney(app.model_cost)}</td>
                <td>{formatMoney(app.model_used_cost)}</td>
                <td>{formatMoney(app.model_idle_cost)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="table-wrap">
        <h3>Recent allocated requests</h3>
        <table>
          <thead>
            <tr>
              <th>Time</th>
              <th>Model</th>
              <th>App</th>
              <th>{inputTokenHeading}</th>
              <th>Output</th>
              <th>Cached</th>
              <th>Total tokens</th>
              <th>Used cost</th>
            </tr>
          </thead>
          <tbody>
            {recentRequestCosts.map((request) => (
              <tr key={`${request.time}-${request.request_id}`}>
                <td>{new Date(request.time).toLocaleTimeString()}</td>
                <td>{request.model}</td>
                <td>{request.app_id || "-"}</td>
                <td>{compactNumber(displayInputTokens(request, inputTokenMode))}</td>
                <td>{compactNumber(request.output_tokens)}</td>
                <td>{compactNumber(request.cached_input_tokens)}</td>
                <td>{compactNumber(request.total_tokens)}</td>
                <td>{formatMoney(request.model_used_cost)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function EventList({ events, compact = false }: { events: WorkerEvent[]; compact?: boolean }) {
  if (events.length === 0) {
    return <div className="empty">No worker events yet.</div>;
  }
  if (compact) {
    return (
      <div className="event-list compact-events">
        {events.map((event, index) => (
          <div className="event-card" key={`${event.received_at}-${event.worker_id}-${index}`}>
            <div className="event-card-head">
              <span>{new Date(event.received_at).toLocaleTimeString()}</span>
              <strong>{event.event}</strong>
            </div>
            <div className="event-card-meta">
              {[event.worker_id, event.model, eventDetail(event)].filter(Boolean).join(" · ")}
            </div>
          </div>
        ))}
      </div>
    );
  }
  return (
    <div className="event-list full-events">
      <div className="event event-head">
        <strong>Received</strong>
        <strong>Event</strong>
        <strong>Worker</strong>
        <strong>Model</strong>
        <strong>Detail</strong>
      </div>
      {events.map((event, index) => (
        <div className="event" key={`${event.received_at}-${event.worker_id}-${index}`}>
          <span>{new Date(event.received_at).toLocaleTimeString()}</span>
          <strong>{event.event}</strong>
          <span>{event.worker_id}</span>
          <span>{event.model || "-"}</span>
          <span>{eventDetail(event) || "-"}</span>
        </div>
      ))}
    </div>
  );
}

function RequestList({ requests, compact = false }: { requests: RequestLogEntry[]; compact?: boolean }) {
  if (requests.length === 0) {
    return <div className="empty">No request logs yet.</div>;
  }
  if (compact) {
    return (
      <div className="event-list compact-events">
        {requests.map((request, index) => (
          <div className="event-card" key={`${request.time}-${request.request_id}-${index}`}>
            <div className="event-card-head">
              <span>{new Date(request.time).toLocaleTimeString()}</span>
              <strong>{request.model}</strong>
            </div>
            <div className="event-card-meta">
              {[request.worker_id, `status ${request.status_code}`, requestDetail(request)].filter(Boolean).join(" · ")}
            </div>
          </div>
        ))}
      </div>
    );
  }
  return (
    <div className="event-list full-events">
      <div className="event event-head request-head">
        <strong>Received</strong>
        <strong>Model</strong>
        <strong>Worker</strong>
        <strong>Status</strong>
        <strong>Parts</strong>
        <strong>Tokens</strong>
        <strong>Cost</strong>
        <strong>Detail</strong>
      </div>
      {requests.map((request, index) => (
        <div className="event request-row" key={`${request.time}-${request.request_id}-${index}`}>
          <span>{new Date(request.time).toLocaleTimeString()}</span>
          <strong>{request.model}</strong>
          <span>{request.worker_id || "-"}</span>
          <span>{request.status_code}</span>
          <span>{requestParts(request) || "-"}</span>
          <span>{requestTokens(request) || "-"}</span>
          <span>{requestCost(request) || "-"}</span>
          <span>{requestDetail(request) || "-"}</span>
        </div>
      ))}
    </div>
  );
}

function TrafficCell({
  traffic,
  compact = false
}: {
  traffic: ModelStatus["traffic"];
  compact?: boolean;
}) {
  if (compact) {
    return (
      <span>{compactNumber(traffic.requests)} req · {traffic.avg_duration_ms}ms avg</span>
    );
  }
  return (
    <div className="traffic-cell">
      <span>req={compactNumber(traffic.requests)}</span>
      <span>tok={compactNumber(traffic.total_tokens)}</span>
      <span>cache={compactNumber(traffic.cache_tokens)}</span>
      <span>avg={traffic.avg_duration_ms}ms</span>
      <span>max={traffic.max_duration_ms}ms</span>
      <span>2xx={compactNumber(traffic.status_2xx)}</span>
      <span>4xx={compactNumber(traffic.status_4xx)}</span>
      <span>5xx={compactNumber(traffic.status_5xx)}</span>
      <span>last={formatTime(traffic.last_access)}</span>
    </div>
  );
}

function ConfigOps({
  status,
  configResponse,
  draft,
  dirty,
  changes,
  impacts,
  applyMode,
  message,
  error,
  onReset,
  onDryRun,
  onApply,
  onModelChange,
  onModelTagsChange,
  onCreateModel,
  onAliasesChange,
  onPromoteServiceName,
  onRollbackServiceName,
  appContentRef
}: {
  status: StatusResponse | null;
  configResponse: ConfigResponse | null;
  draft: EditableGatewayConfig | null;
  dirty: boolean;
  changes: ConfigChange[];
  impacts: ConfigImpact[];
  applyMode: string;
  message: string;
  error: string;
  onReset: () => void;
  onDryRun: () => void;
  onApply: () => void;
  onModelChange: (modelName: string, nextModel: EditableModelConfig) => void;
  onModelTagsChange: (modelName: string, nextModel: EditableModelConfig, selectedTags: string[]) => void;
  onCreateModel: (modelName: string, model: EditableModelConfig, selectedTags: string[]) => void;
  onAliasesChange: (nextAliases: Record<string, string>) => void;
  onPromoteServiceName: (serviceName: string, targetModel: string) => Promise<ServiceNamePromotion>;
  onRollbackServiceName: (promotion: ServiceNamePromotion) => Promise<ServiceNamePromotion>;
  appContentRef: React.RefObject<HTMLElement | null>;
}) {
  const modelNames = useMemo(() => sortedKeys(draft?.models), [draft]);
  const [showDisabledModels, setShowDisabledModels] = useState(false);
  const visibleModelNames = useMemo(
    () => modelNames.filter((modelName) => showDisabledModels || !draft?.models[modelName]?.disabled),
    [draft, modelNames, showDisabledModels]
  );
  const tagNames = useMemo(() => sortedKeys(draft?.tag_policies), [draft]);
  const [selectedModel, setSelectedModel] = useState("");
  const [createDraft, setCreateDraft] = useState<ModelCreateDraft | null>(null);
  const [createInitialDraft, setCreateInitialDraft] = useState<ModelCreateDraft | null>(null);
  const [discardCreateConfirm, setDiscardCreateConfirm] = useState(false);
  const [createError, setCreateError] = useState("");
  const createTriggerRef = useRef<HTMLButtonElement>(null);
  const [promotionTarget, setPromotionTarget] = useState("");
  const [confirmPromotion, setConfirmPromotion] = useState(false);
  const [promotion, setPromotion] = useState<ServiceNamePromotion | null>(null);
  const [promotionError, setPromotionError] = useState("");
  const [promotionEligibility, setPromotionEligibility] = useState<ServiceNameTargetEligibility[]>([]);

  useEffect(() => {
    if (!selectedModel || !visibleModelNames.includes(selectedModel)) {
      setSelectedModel(visibleModelNames[0] ?? "");
    }
  }, [selectedModel, visibleModelNames]);

  useEffect(() => {
    const app = appContentRef.current;
    if (!app) return;
    app.inert = Boolean(createDraft);
    return () => {
      app.inert = false;
    };
  }, [appContentRef, createDraft]);

  useEffect(() => {
    if (!selectedModel || dirty || !draft?.models[selectedModel]?.disabled) {
      setPromotionEligibility([]);
      return;
    }
    let current = true;
    void getServiceNameEligibility(selectedModel)
      .then((response) => { if (current) setPromotionEligibility(response.targets ?? []); })
      .catch((error) => { if (current) { setPromotionEligibility([]); setPromotionError(error instanceof Error ? error.message : String(error)); } });
    return () => { current = false; };
  }, [selectedModel, dirty, draft]);

  useEffect(() => {
    if (createDraft !== null) return;
    const trigger = createTriggerRef.current;
    if (trigger?.isConnected) {
      trigger.focus();
    }
    createTriggerRef.current = null;
  }, [createDraft]);

  if (!configResponse || !draft) {
    return <div className="empty">Loading config workspace...</div>;
  }

  const currentDraft = draft;
  const selectedModelConfig = currentDraft.models[selectedModel];
  const selectedModelTags = tagNames.filter((tagName) => currentDraft.tag_policies[tagName].allowed_models.includes(selectedModel));
  const liveModelMap = new Map((status?.models ?? []).map((model) => [model.name, model]));
  const promotionTargets = promotionEligibility.filter((target) => target.eligible && target.model !== selectedModel && !currentDraft.models[target.model]?.disabled);

  function startCreate(trigger: HTMLButtonElement) {
    const nextDraft: ModelCreateDraft = {
      name: "",
      model: emptyEditableModel(),
      tags: []
    };
    setCreateDraft(nextDraft);
    setCreateInitialDraft(cloneModelCreateDraft(nextDraft));
    setDiscardCreateConfirm(false);
    setCreateError("");
    createTriggerRef.current = trigger;
  }

  function clearCreateModal() {
    setCreateDraft(null);
    setCreateInitialDraft(null);
    setDiscardCreateConfirm(false);
    setCreateError("");
  }

  function requestCloseCreateModal() {
    if (createDraft && createInitialDraft && isModelCreateDraftDirty(createInitialDraft, createDraft)) {
      setDiscardCreateConfirm(true);
      return;
    }
    clearCreateModal();
  }

  function saveCreatedModel() {
    if (!createDraft) return;
    const validationError = validateNewModelName(createDraft.name, currentDraft.models, currentDraft.model_aliases);
    setCreateError(validationError);
    if (validationError) return;
    const modelName = createDraft.name.trim();
    onCreateModel(modelName, createDraft.model, createDraft.tags);
    setShowDisabledModels(true);
    setSelectedModel(modelName);
    clearCreateModal();
  }

  return (
    <div className="config-ops">
      <div className="config-toolbar">
        <div>
          <h2>Config Ops</h2>
          <p className="toolbar-sub">Version {configResponse.version} · models, aliases, placement, and capacity</p>
        </div>
        <div className="config-toolbar-actions">
          <Badge tone={dirty ? "warn" : "good"}>{dirty ? "draft changed" : "in sync"}</Badge>
          <button onClick={onReset} disabled={!dirty}>Reset</button>
          <button onClick={onDryRun}>Dry run</button>
          <button className="primary" onClick={onApply}>Apply</button>
        </div>
      </div>

      {error ? <div className="alert">{error}</div> : null}
      {message ? <div className="notice">{message}</div> : null}

      <div className="config-grid">
        <div className="config-stack">
          <ConfigListCard
            title="Models"
            subtitle="Select a model to edit runtime, placement, capacity, and artifacts."
            actions={
              <div className="model-card-actions">
                <button type="button" data-model-create-trigger="new" onClick={(event) => startCreate(event.currentTarget)}>New model</button>
              </div>
            }
          >
            <label className="switch-control compact-switch">
              <input
                type="checkbox"
                checked={showDisabledModels}
                onChange={(event) => setShowDisabledModels(event.target.checked)}
              />
              <span className="switch-track" aria-hidden="true" />
              <span>Show disabled</span>
            </label>
            {visibleModelNames.map((modelName) => {
              const model = draft.models[modelName];
              const live = liveModelMap.get(modelName);
              return (
                <button
                  key={modelName}
                  className={`picker-item ${model.disabled ? "model-disabled" : ""} ${selectedModel === modelName ? "selected" : ""}`}
                  onClick={() => setSelectedModel(modelName)}
                >
                  <div>
                    <strong>{modelName}</strong>
                    <small>{model.runtime || (model.run ? "raw run" : "runtime unset")}</small>
                  </div>
                  <div className="picker-meta">
                    {model.disabled ? (
                      <span className="disabled-pill">Disabled</span>
                    ) : (
                      <Badge tone={live?.available ? "good" : "warn"}>{live?.available ? "ready" : "draft"}</Badge>
                    )}
                    <span>{model.min_loaded}/{model.max_loaded_auto ? "auto" : model.max_loaded}</span>
                  </div>
                </button>
              );
            })}
          </ConfigListCard>

          <ModelAliasesEditor
            aliases={draft.model_aliases}
            modelNames={modelNames}
            modelStatuses={status?.models ?? []}
            onChange={onAliasesChange}
          />
        </div>

        <div className="config-stack">
          {selectedModelConfig ? (
            <ModelEditor
              key={selectedModel}
              name={selectedModel}
              model={selectedModelConfig}
              liveStatus={liveModelMap.get(selectedModel)}
              tagNames={tagNames}
              selectedTags={selectedModelTags}
              tagPolicies={currentDraft.tag_policies}
              onChange={(nextModel) => onModelChange(selectedModel, nextModel)}
              onTagsChange={(nextModel, nextTags) => onModelTagsChange(selectedModel, nextModel, nextTags)}
            />
          ) : null}

          {promotion ? (
            <div className="config-card service-name-promotion">
              <div className="config-card-head"><div><h3>Promotion active</h3><p>{promotion.service_name} → {promotion.target_model}</p></div></div>
              <div className="service-name-promotion-body">
                <p className="muted mono">Archive {promotion.archive_id}</p>
                <p>Rollback removes the service alias and restores the archived canonical definition. It is refused if the alias or touched placement policy changes later.</p>
                <button type="button" onClick={async () => { try { await onRollbackServiceName(promotion); setPromotion(null); setPromotionTarget(""); } catch (error) { setPromotionError(error instanceof Error ? error.message : String(error)); } }}>Rollback promotion</button>
              </div>
            </div>
          ) : selectedModelConfig?.disabled ? (
            <div className="config-card service-name-promotion">
              <div className="config-card-head">
                <div><h3>Promote service name</h3><p>Convert this disabled canonical name into a stable public alias.</p></div>
              </div>
              <div className="service-name-promotion-body">
                <p>This archives the disabled canonical model <strong>{selectedModel}</strong> and removes it from active placement policies.</p>
                {dirty ? <p className="alert">Apply or reset the current draft before promoting a service name.</p> : null}
                <label><span>Ready target</span><select value={promotionTarget} onChange={(event) => setPromotionTarget(event.target.value)}>
                  <option value="">Select a ready canonical model</option>
                  {promotionTargets.map((target) => <option key={target.model} value={target.model}>{target.model} · ready {target.routable_ready}/{target.ready_floor}</option>)}
                </select></label>
                {promotionTarget ? <p className="notice">The target is already ready: <strong>{promotionTarget}</strong>. New requests for {selectedModel} will route there immediately.</p> : null}
                <p className="muted">Rollback removes the service alias and restores the archived canonical definition. It is refused if the alias or touched placement policy changes later.</p>
                {promotionError ? <div className="alert">{promotionError}</div> : null}
                <button type="button" className="danger" disabled={dirty || !promotionTarget} onClick={() => setConfirmPromotion(true)}>Promote service name</button>
              </div>
              {confirmPromotion ? <div className="modal-backdrop"><section className="model-create-modal" role="dialog" aria-modal="true" aria-label="Confirm service-name promotion">
                <header><h2>Confirm service-name promotion</h2><p>This is an audited namespace change.</p></header>
                <div className="model-create-modal-body"><p><strong>{selectedModel}</strong> will stop being a canonical model and become an alias for ready target <strong>{promotionTarget}</strong>.</p><p>Rollback removes the service alias and restores the archived canonical definition.</p><div className="model-card-actions"><button type="button" onClick={() => setConfirmPromotion(false)}>Cancel</button><button type="button" className="danger" onClick={async () => { try { const result = await onPromoteServiceName(selectedModel, promotionTarget); setPromotion(result); setConfirmPromotion(false); setPromotionError(""); } catch (error) { setPromotionError(error instanceof Error ? error.message : String(error)); setConfirmPromotion(false); } }}>Confirm promotion</button></div></div>
              </section></div> : null}
            </div>
          ) : null}

          <ImpactSummary applyMode={applyMode} impacts={impacts} changes={changes} />
          <ChangeList changes={changes} />
        </div>
      </div>

      {createDraft && createInitialDraft ? (
        <ModelCreateModal
          draft={createDraft}
          initialDraft={createInitialDraft}
          tagNames={tagNames}
          tagPolicies={currentDraft.tag_policies}
          nameError={createError}
          discardConfirm={discardCreateConfirm}
          onChange={(nextDraft) => {
            setCreateDraft(nextDraft);
            if (createError) {
              setCreateError(validateNewModelName(nextDraft.name, currentDraft.models, currentDraft.model_aliases));
            }
          }}
          onSave={saveCreatedModel}
          onRequestClose={requestCloseCreateModal}
          onKeepEditing={() => setDiscardCreateConfirm(false)}
          onDiscard={clearCreateModal}
        />
      ) : null}
    </div>
  );
}

function AdvancedConfig({
  version,
  yaml,
  dirty,
  message,
  error,
  onCopy
}: {
  version: number | null;
  yaml: string;
  dirty: boolean;
  message: string;
  error: string;
  onCopy: () => void;
}) {
  return (
    <div className="config-ops">
      <div className="config-toolbar">
        <div>
          <h2>Advanced</h2>
          <p className="toolbar-sub">Version {version ?? "-"} · read-only full YAML</p>
        </div>
        <div className="config-toolbar-actions">
          <Badge tone={dirty ? "warn" : "good"}>{dirty ? "shows current draft" : "shows saved config"}</Badge>
          <button onClick={onCopy}>Copy YAML</button>
        </div>
      </div>
      {error ? <div className="alert">{error}</div> : null}
      {message ? <div className="notice">{message}</div> : null}
      <div className="advanced-readonly yaml-shell" aria-label="Read-only YAML">
        <pre className="yaml-highlight">
          {tokenizeYAML(yaml).map((line, lineIndex) => (
            <span className="yaml-line" key={lineIndex}>
              <span className="yaml-line-number">{lineIndex + 1}</span>
              <span className="yaml-line-code">
                {line.map((token, tokenIndex) => (
                  <span className={`yaml-token ${token.kind}`} key={`${lineIndex}-${tokenIndex}`}>{token.text}</span>
                ))}
              </span>
            </span>
          ))}
        </pre>
      </div>
    </div>
  );
}

function ConfigListCard({
  title,
  subtitle,
  actions,
  children
}: {
  title: string;
  subtitle: string;
  actions?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="config-card">
      <div className="config-card-head">
        <div>
          <h3>{title}</h3>
          <p>{subtitle}</p>
        </div>
        {actions}
      </div>
      <div className="picker-list">{children}</div>
    </div>
  );
}

function cloneModelCreateDraft(draft: ModelCreateDraft): ModelCreateDraft {
  return {
    name: draft.name,
    model: {
      ...draft.model,
      artifact: { ...draft.model.artifact },
      runtime_args: [...draft.model.runtime_args],
      tag_capacity: Object.fromEntries(
        Object.entries(draft.model.tag_capacity ?? {}).map(([tag, capacity]) => [tag, { ...capacity }])
      ),
      billing: draft.model.billing ? { ...draft.model.billing } : undefined
    },
    tags: [...draft.tags]
  };
}

function ModelCreateModal({
  draft,
  initialDraft,
  tagNames,
  tagPolicies,
  nameError,
  discardConfirm,
  onChange,
  onSave,
  onRequestClose,
  onKeepEditing,
  onDiscard
}: {
  draft: ModelCreateDraft;
  initialDraft: ModelCreateDraft;
  tagNames: string[];
  tagPolicies: Record<string, TagPolicyConfig>;
  nameError: string;
  discardConfirm: boolean;
  onChange: (next: ModelCreateDraft) => void;
  onSave: () => void;
  onRequestClose: () => void;
  onKeepEditing: () => void;
  onDiscard: () => void;
}) {
  const hasUnsavedChanges = isModelCreateDraftDirty(initialDraft, draft);
  const dialogRef = useRef<HTMLElement>(null);
  const canonicalNameInputRef = useRef<HTMLInputElement>(null);
  const discardChoiceRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    canonicalNameInputRef.current?.focus();
  }, []);

  useEffect(() => {
    if (discardConfirm && hasUnsavedChanges) {
      discardChoiceRef.current?.focus();
    }
  }, [discardConfirm, hasUnsavedChanges]);

  function handleKeyDown(event: React.KeyboardEvent<HTMLElement>) {
    if (event.key === "Escape") {
      onRequestClose();
      return;
    }
    if (event.key !== "Tab") return;

    const dialog = dialogRef.current;
    const focusable = dialog ? modalFocusableElements(dialog) : [];
    if (focusable.length === 0) {
      event.preventDefault();
      dialog?.focus();
      return;
    }
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    const focusIsOutsideDialog = !dialog?.contains(document.activeElement);
    if (event.shiftKey && (document.activeElement === first || focusIsOutsideDialog)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (document.activeElement === last || focusIsOutsideDialog)) {
      event.preventDefault();
      first.focus();
    }
  }

  return createPortal(
    <div
      className="modal-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) {
          onRequestClose();
        }
      }}
    >
      <section
        ref={dialogRef}
        className="model-create-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="model-create-modal-title"
        tabIndex={-1}
        onKeyDown={handleKeyDown}
      >
        <header>
          <h2 id="model-create-modal-title">New model</h2>
          <p>Create a disabled model draft.</p>
        </header>
        <div className="model-create-modal-body">
          <ModelEditor
            name="New model"
            model={draft.model}
            editableName={{
              value: draft.name,
              onChange: (name) => onChange({ ...draft, name }),
              error: nameError,
              inputRef: canonicalNameInputRef
            }}
            tagNames={tagNames}
            selectedTags={draft.tags}
            tagPolicies={tagPolicies}
            onChange={(model) => onChange({ ...draft, model })}
            onTagsChange={(model, tags) => onChange({ ...draft, model, tags })}
          >
            <div className="model-card-actions">
              <button type="button" className="primary" onClick={onSave}>Save to draft</button>
              <button type="button" onClick={onRequestClose}>Cancel</button>
            </div>
            {discardConfirm && hasUnsavedChanges ? (
              <div className="modal-discard-confirm">
                <strong>Discard unsaved model changes?</strong>
                <p>This model has not been added to the config draft yet.</p>
                <div className="model-card-actions">
                  <button ref={discardChoiceRef} type="button" onClick={onKeepEditing}>Keep editing</button>
                  <button type="button" className="danger" onClick={onDiscard}>Discard changes</button>
                </div>
              </div>
            ) : null}
          </ModelEditor>
        </div>
      </section>
    </div>,
    document.body
  );
}

function modalFocusableElements(dialog: HTMLElement): HTMLElement[] {
  return Array.from(dialog.querySelectorAll<HTMLElement>([
    "a[href]",
    "button:not([disabled])",
    "input:not([disabled])",
    "select:not([disabled])",
    "textarea:not([disabled])",
    '[tabindex]:not([tabindex="-1"])'
  ].join(", "))).filter((element) => element.tabIndex >= 0 && !element.hasAttribute("inert"));
}

function ModelEditor({
  name,
  model,
  liveStatus,
  editableName,
  tagNames = [],
  selectedTags = [],
  tagPolicies = {},
  children,
  onChange,
  onTagsChange
}: {
  name: string;
  model: EditableModelConfig;
  liveStatus?: ModelStatus;
  editableName?: { value: string; onChange: (value: string) => void; error: string; inputRef?: React.Ref<HTMLInputElement> };
  tagNames?: string[];
  selectedTags?: string[];
  tagPolicies?: Record<string, TagPolicyConfig>;
  children?: React.ReactNode;
  onChange: (nextModel: EditableModelConfig) => void;
  onTagsChange?: (nextModel: EditableModelConfig, selectedTags: string[]) => void;
}) {
  const isRawRunModel = Boolean(model.run && !model.runtime);
  const runtimeArgsValue = model.runtime_args.join("\n");
  const canonicalName = editableName?.value.trim() || name;
  const [runtimeArgsText, setRuntimeArgsText] = useState(runtimeArgsValue);
  const lastRuntimeArgsValue = useRef(runtimeArgsValue);

  useEffect(() => {
    if (runtimeArgsValue !== lastRuntimeArgsValue.current) {
      setRuntimeArgsText(runtimeArgsValue);
      lastRuntimeArgsValue.current = runtimeArgsValue;
    }
  }, [name, runtimeArgsValue]);

  return (
    <div className="config-card">
      <div className="config-card-head">
        <div>
          <h3>{name}</h3>
          <p>Runtime, replicas, runnable tags, and artifacts.</p>
        </div>
        <label className="model-disabled-toggle switch-control">
          <input
            type="checkbox"
            checked={Boolean(model.disabled)}
            onChange={(event) => onChange({ ...model, disabled: event.target.checked || undefined })}
          />
          <span className="switch-track" aria-hidden="true" />
          <span>Disabled</span>
        </label>
        {liveStatus ? <div className="config-card-state">
          <Badge tone={liveStatus?.available ? "good" : "warn"}>{liveStatus?.available ? "ready" : "draft only"}</Badge>
          <span>{`${liveStatus.ready_workers} ready / ${liveStatus.running_workers} running`}</span>
        </div> : null}
      </div>

      {isRawRunModel ? <div className="notice">This model uses a raw `run` command. Runtime command text stays read-only in Ops.</div> : null}
      <div className="model-config-layout">
        <div className="model-config-base">
          <div className="model-config-column">
            <fieldset className="config-field-group">
              <legend>Identity</legend>
              <div className="detail-grid">
                {editableName ? (
                  <label>
                    <span>Canonical model name</span>
                    <input
                      ref={editableName.inputRef}
                      value={editableName.value}
                      required
                      onChange={(event) => editableName.onChange(event.target.value)}
                    />
                    <small>Required. Existing model names are immutable.</small>
                    {editableName.error ? <span className="field-error">{editableName.error}</span> : null}
                  </label>
                ) : null}
                <label>
                  <span>Model directory</span>
                  <input
                    value={model.model_dir ?? ""}
                    placeholder={canonicalName}
                    onChange={(event) => onChange({ ...model, model_dir: event.target.value })}
                  />
                  <small>Empty uses {canonicalName}.</small>
                </label>
              </div>
            </fieldset>

            <fieldset className="config-field-group">
              <legend>Replica policy</legend>
              <div className="detail-grid">
                <NumberField label="Min loaded" value={model.min_loaded} onChange={(value) => onChange({ ...model, min_loaded: value })} />
                <label>
                  <span>Max loaded mode</span>
                  <select
                    value={model.max_loaded_auto ? "auto" : "fixed"}
                    onChange={(event) => onChange({ ...model, max_loaded_auto: event.target.value === "auto" })}
                  >
                    <option value="auto">Auto</option>
                    <option value="fixed">Fixed</option>
                  </select>
                </label>
                <NumberField
                  label="Max loaded"
                  value={model.max_loaded_auto ? "" : model.max_loaded}
                  disabled={model.max_loaded_auto}
                  onChange={(value) => onChange({ ...model, max_loaded: value })}
                />
                <NumberField label="Queue timeout ms" value={model.queue_timeout_ms} onChange={(value) => onChange({ ...model, queue_timeout_ms: value })} />
                <NumberField label="TTL seconds" value={model.ttl} onChange={(value) => onChange({ ...model, ttl: value })} />
                <NumberField label="Priority" value={model.priority} onChange={(value) => onChange({ ...model, priority: value })} />
              </div>
              {hasLegacyCapacityCeiling(model.max_concurrency, model.max_queue) ? (
                <p className="compatibility-note">Legacy model capacity ceilings are preserved. Edit them in Advanced YAML only.</p>
              ) : null}
            </fieldset>

          </div>

          <div className="model-config-column">
            <fieldset className="config-field-group">
              <legend>Runtime</legend>
              <div className="detail-grid">
                {isRawRunModel ? (
                  <label>
                    <span>Runtime</span>
                    <input value="Custom command" readOnly />
                  </label>
                ) : (
                  <label>
                    <span>Runtime</span>
                    <select value={model.runtime ?? "vllm"} onChange={(event) => onChange({ ...model, runtime: event.target.value })}>
                      {MODEL_RUNTIME_OPTIONS.map((runtime) => <option key={runtime} value={runtime}>{runtime}</option>)}
                    </select>
                  </label>
                )}
                <label>
                  <span>Check endpoint</span>
                  <input value={model.check_endpoint ?? ""} onChange={(event) => onChange({ ...model, check_endpoint: event.target.value })} />
                </label>
                <label className="field-span wide-field">
                  <span>Runtime args</span>
                  <textarea
                    className="mini-textarea"
                    spellCheck={false}
                    value={runtimeArgsText}
                    disabled={isRawRunModel}
                    onChange={(event) => {
                      const nextText = event.target.value;
                      const nextRuntimeArgs = splitLines(nextText);
                      setRuntimeArgsText(nextText);
                      lastRuntimeArgsValue.current = nextRuntimeArgs.join("\n");
                      onChange({ ...model, runtime_args: nextRuntimeArgs });
                    }}
                  />
                </label>
              </div>
            </fieldset>

            <fieldset className="config-field-group">
              <legend>Artifact</legend>
              <div className="detail-grid artifact-fields">
                <label className="field-span wide-field">
                  <span>Artifact object</span>
                  <input
                    value={model.artifact.object}
                    onChange={(event) => onChange({ ...model, artifact: { ...model.artifact, object: event.target.value } })}
                  />
                </label>
                <label className="artifact-kind-field">
                  <span>Artifact kind</span>
                  <select
                    value={model.artifact.kind}
                    onChange={(event) => onChange({ ...model, artifact: { ...model.artifact, kind: event.target.value } })}
                  >
                    <option value="tar_gz">tar_gz</option>
                    <option value="file">file</option>
                  </select>
                </label>
                <label className="artifact-checksum-field">
                  <span>CRC64 ECMA</span>
                  <input
                    value={model.artifact.crc64ecma}
                    onChange={(event) => onChange({ ...model, artifact: { ...model.artifact, crc64ecma: event.target.value } })}
                  />
                </label>
              </div>
            </fieldset>
          </div>
        </div>

        <aside className="model-capacity-rail">
          <fieldset className="config-field-group model-tag-capacity-group">
            <legend>Runnable tags &amp; capacity</legend>
            <p className="field-group-help">Limits apply to this model on each selected GPU worker.</p>
            <div className="model-tag-capacity-list">
              {tagNames.map((tagName) => {
                const checked = selectedTags.includes(tagName);
                const capacity = modelTagCapacity(model, tagName, tagPolicies[tagName]);
                return (
                  <div className={`model-tag-capacity-row ${checked ? "selected" : ""}`} key={tagName}>
                    <label className="model-tag-choice">
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() => {
                          const nextModel = setModelTagEnabled(model, tagName, !checked);
                          const nextTags = checked
                            ? selectedTags.filter((tag) => tag !== tagName)
                            : [...selectedTags, tagName].sort();
                          onTagsChange?.(nextModel, nextTags);
                        }}
                      />
                      <strong>{tagName}</strong>
                    </label>
                    {checked ? (
                      <div className="model-tag-limit-fields">
                        <CompactNumberField
                          label="Concurrent"
                          value={capacity.max_concurrency}
                          min={1}
                          onChange={(value) => onTagsChange?.(
                            setModelTagCapacity(model, tagName, { ...capacity, max_concurrency: value }),
                            selectedTags
                          )}
                        />
                        <CompactNumberField
                          label="Queue"
                          value={capacity.max_queue}
                          min={0}
                          onChange={(value) => onTagsChange?.(
                            setModelTagCapacity(model, tagName, { ...capacity, max_queue: value }),
                            selectedTags
                          )}
                        />
                      </div>
                    ) : <span className="model-tag-disabled">not selected</span>}
                  </div>
                );
              })}
              {tagNames.length === 0 ? <div className="empty">No worker tags are configured.</div> : null}
            </div>
          </fieldset>
        </aside>
      </div>
      {children}
    </div>
  );
}

function ModelAliasesEditor({
  aliases,
  modelNames,
  modelStatuses,
  onChange
}: {
  aliases: Record<string, string>;
  modelNames: string[];
  modelStatuses: ModelStatus[];
  onChange: (nextAliases: Record<string, string>) => void;
}) {
  const [aliasName, setAliasName] = useState("");
  const [target, setTarget] = useState(modelNames[0] ?? "");
  const [validationMessage, setValidationMessage] = useState("");
  const statusByModel = new Map(modelStatuses.map((model) => [model.name, model]));

  useEffect(() => {
    if (!modelNames.includes(target)) {
      setTarget(modelNames[0] ?? "");
    }
  }, [modelNames, target]);

  function addAlias() {
    const nextValidationMessage = validateAliasDraft(aliasName, target, modelNames, aliases);
    setValidationMessage(nextValidationMessage);
    if (nextValidationMessage) {
      return;
    }
    onChange(setAliasTarget(aliases, aliasName, target));
    setAliasName("");
  }

  return (
    <div className="config-card">
      <div className="config-card-head">
        <div>
          <h3>Model aliases</h3>
          <p>Route a stable public name to a concrete model.</p>
        </div>
      </div>

      <div className="alias-list">
        {sortedKeys(aliases).map((alias) => {
          const aliasTarget = aliases[alias];
          const targetStatus = statusByModel.get(aliasTarget);
          const readyWorkers = targetStatus?.ready_workers ?? 0;
          const runningWorkers = targetStatus?.running_workers ?? 0;
          return (
            <div className="alias-row" key={alias}>
              <strong>{alias}</strong>
              <select aria-label={`Target for alias ${alias}`} title={aliasTarget} value={aliasTarget} onChange={(event) => onChange(setAliasTarget(aliases, alias, event.target.value))}>
                {modelNames.map((modelName) => <option key={modelName} value={modelName}>{modelName}</option>)}
              </select>
              <div className="alias-status">
                <Badge tone={readyWorkers > 0 ? "good" : "warn"}>{readyWorkers > 0 ? "ready" : "zero ready"}</Badge>
                <span>{readyWorkers} ready / {runningWorkers} running</span>
              </div>
              <button type="button" aria-label={`Remove alias ${alias}`} onClick={() => onChange(removeAlias(aliases, alias))}>Remove</button>
            </div>
          );
        })}
        {Object.keys(aliases).length === 0 ? <div className="empty">No model aliases configured.</div> : null}
      </div>

      <div className="alias-add">
        <label>
          <span>Alias name</span>
          <input
            value={aliasName}
            placeholder="latest"
            onChange={(event) => {
              setAliasName(event.target.value);
              if (validationMessage) {
                setValidationMessage(validateAliasDraft(event.target.value, target, modelNames, aliases));
              }
            }}
          />
        </label>
        <label>
          <span>Concrete target</span>
          <select
            title={target}
            value={target}
            disabled={modelNames.length === 0}
            onChange={(event) => {
              setTarget(event.target.value);
              if (validationMessage) {
                setValidationMessage(validateAliasDraft(aliasName, event.target.value, modelNames, aliases));
              }
            }}
          >
            {modelNames.map((modelName) => <option key={modelName} value={modelName}>{modelName}</option>)}
          </select>
        </label>
        <button type="button" className="primary" onClick={addAlias}>Add alias</button>
        {validationMessage ? <span className="alias-validation">{validationMessage}</span> : null}
      </div>
    </div>
  );
}

function ImpactSummary({
  applyMode,
  impacts,
  changes
}: {
  applyMode: string;
  impacts: ConfigImpact[];
  changes: ConfigChange[];
}) {
  if (!applyMode && changes.length === 0) {
    return null;
  }
  const workerReloads = impacts.filter((impact) => impact.requires_worker_restart);
  return (
    <div className="impact-summary">
      <div>
        <strong>Apply mode</strong>
        <Badge tone={applyMode === "save_requires_gateway_restart" ? "bad" : "good"}>
          {applyMode === "save_requires_gateway_restart" ? "save + gateway restart" : "hot apply"}
        </Badge>
      </div>
      <div>
        <strong>Changed paths</strong>
        <span>{changes.length}</span>
      </div>
      <div>
        <strong>Worker reload impact</strong>
        <span>{workerReloads.length}</span>
      </div>
      {workerReloads.length > 0 ? (
        <div className="impact-list">
          {workerReloads.map((impact) => (
            <span key={`${impact.model}-${impact.worker_id}`}>
              {impact.model} on {impact.worker_id} · {impact.running_state || "loaded"}
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function ChangeList({ changes }: { changes: ConfigChange[] }) {
  if (changes.length === 0) {
    return <div className="empty">No pending changes.</div>;
  }
  return (
    <div className="changes">
      {changes.map((change) => (
        <div className="change" key={`${change.path}-${change.type}`}>
          <strong>{change.path}</strong>
          <Badge tone={change.requires_gateway_restart ? "bad" : change.requires_worker_restart ? "warn" : "good"}>
            {change.requires_gateway_restart ? "gateway restart" : change.requires_worker_restart ? "worker reload" : "hot"}
          </Badge>
          <span>{change.type}{change.detail ? ` · ${change.detail}` : ""}</span>
        </div>
      ))}
    </div>
  );
}

function NumberField({
  label,
  value,
  disabled = false,
  onChange
}: {
  label: string;
  value: number | "";
  disabled?: boolean;
  onChange: (value: number) => void;
}) {
  return (
    <label>
      <span>{label}</span>
      <input
        type="number"
        value={value === "" ? "" : Number.isFinite(value) ? value : 0}
        disabled={disabled}
        onChange={(event) => onChange(Number(event.target.value || 0))}
      />
    </label>
  );
}

function CompactNumberField({
  label,
  value,
  min,
  onChange
}: {
  label: string;
  value: number;
  min: number;
  onChange: (value: number) => void;
}) {
  return (
    <label className="compact-number-field">
      <span>{label}</span>
      <input
        type="number"
        min={min}
        value={Number.isFinite(value) ? value : min}
        onChange={(event) => onChange(Math.max(min, Number(event.target.value || min)))}
      />
    </label>
  );
}

function Badge({ children, tone }: { children: React.ReactNode; tone: "good" | "warn" | "bad" }) {
  return <span className={`badge ${tone}`}>{children}</span>;
}

function aggregateTraffic(models: ModelStatus[]) {
  const totals = models.reduce(
    (acc, model) => {
      const traffic = model.traffic;
      acc.requests += traffic.requests;
      acc.totalTokens += traffic.total_tokens;
      acc.cacheTokens += traffic.cache_tokens;
      acc.status5xx += traffic.status_5xx;
      acc.durationWeighted += traffic.avg_duration_ms * traffic.requests;
      return acc;
    },
    { requests: 0, totalTokens: 0, cacheTokens: 0, status5xx: 0, durationWeighted: 0 }
  );
  return {
    ...totals,
    avgDurationMS: totals.requests > 0 ? Math.round(totals.durationWeighted / totals.requests) : 0
  };
}

function compactNumber(value: number | bigint | undefined) {
  const numberValue = Number(value ?? 0);
  if (!Number.isFinite(numberValue)) {
    return "0";
  }
  if (Math.abs(numberValue) >= 1_000_000) {
    return `${(numberValue / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
  }
  if (Math.abs(numberValue) >= 1_000) {
    return `${(numberValue / 1_000).toFixed(1).replace(/\.0$/, "")}K`;
  }
  return String(Math.round(numberValue));
}

function formatMoney(value: number | undefined) {
  const numberValue = Number(value ?? 0);
  if (!Number.isFinite(numberValue)) {
    return "$0.00";
  }
  return `$${numberValue.toFixed(numberValue >= 100 ? 1 : 2)}`;
}

function formatPricingValue(value: number | undefined) {
  const numberValue = Number(value ?? 0);
  if (!Number.isFinite(numberValue) || numberValue <= 0) {
    return "$0";
  }
  return `$${numberValue.toFixed(6).replace(/0+$/, "").replace(/\.$/, "")}`;
}

function recommendedBillingPricing(model: BillingSummary["models"][number] | undefined, workerDayCostUSD: number | undefined): ModelBillingConfig {
  const dayCost = Number(workerDayCostUSD ?? 0);
  const durationSeconds = Number(model?.request_duration_seconds ?? 0);
  if (!model || dayCost <= 0 || durationSeconds <= 0) {
    return {};
  }
  const uncachedInputTokens = billableInputTokens(model.input_tokens, model.cached_input_tokens);
  return {
    per_request_usd: model.requests > 0 ? roundPricingValue(capacityUnitPrice(dayCost, durationSeconds, model.requests, 1)) : undefined,
    input_per_million_usd: uncachedInputTokens > 0 ? roundPricingValue(capacityUnitPrice(dayCost, durationSeconds, uncachedInputTokens, 1_000_000)) : undefined,
    output_per_million_usd: model.output_tokens > 0 ? roundPricingValue(capacityUnitPrice(dayCost, durationSeconds, model.output_tokens, 1_000_000)) : undefined,
    cached_input_per_million_usd: model.cached_input_tokens > 0
      ? roundPricingValue(capacityUnitPrice(dayCost, durationSeconds, model.cached_input_tokens, 1_000_000))
      : undefined
  };
}

function billableInputTokens(inputTokens: number | undefined, cachedInputTokens: number | undefined) {
  return Math.max(Number(inputTokens ?? 0) - Number(cachedInputTokens ?? 0), 0);
}

function displayInputTokens(
  row: { input_tokens: number | undefined; cached_input_tokens: number | undefined },
  mode: BillingInputTokenMode
) {
  return mode === "billable" ? billableInputTokens(row.input_tokens, row.cached_input_tokens) : row.input_tokens;
}

function capacityUnitPrice(workerDayCostUSD: number, durationSeconds: number, units: number, multiplier: number) {
  if (!Number.isFinite(workerDayCostUSD) || !Number.isFinite(durationSeconds) || !Number.isFinite(units) || units <= 0) {
    return undefined;
  }
  return workerDayCostUSD * durationSeconds * multiplier / (units * secondsPerDay);
}

function roundPricingValue(value: number | undefined) {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) {
    return undefined;
  }
  return Math.round(value * 1_000_000) / 1_000_000;
}

function parseOptionalPrice(raw: string) {
  const value = raw.trim();
  if (value === "") {
    return undefined;
  }
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    return undefined;
  }
  return Math.max(0, parsed);
}

function hasBillingValues(billing: ModelBillingConfig) {
  return Object.values(billing).some((value) => typeof value === "number" && Number.isFinite(value));
}

function formatRate(value: number | undefined) {
  const numberValue = Number(value ?? 0);
  if (!Number.isFinite(numberValue) || numberValue <= 0) {
    return "-";
  }
  return numberValue.toFixed(6).replace(/0+$/, "").replace(/\.$/, "");
}

function formatHours(seconds: number | undefined) {
  const numberValue = Number(seconds ?? 0);
  if (!Number.isFinite(numberValue) || numberValue <= 0) {
    return "0h";
  }
  return `${(numberValue / 3600).toFixed(2)}h`;
}

function formatTime(value?: string) {
  if (!value || value.startsWith("0001-")) {
    return "-";
  }
  return new Date(value).toLocaleTimeString();
}

function eventDetail(event: WorkerEvent) {
  if (event.error) {
    return event.error;
  }
  if (typeof event.percent === "number") {
    return `${event.percent.toFixed(1)}% ${formatBytes(event.downloaded_bytes)}/${formatBytes(event.total_bytes)}`;
  }
  if (event.downloaded_bytes && event.total_bytes) {
    return `${formatBytes(event.downloaded_bytes)}/${formatBytes(event.total_bytes)}`;
  }
  if (event.from_state || event.to_state) {
    return `${event.from_state || "-"} -> ${event.to_state || "-"}`;
  }
  if (event.duration_ms) {
    return `duration ${Math.round(event.duration_ms / 1000)}s`;
  }
  return event.object || "";
}

function requestParts(request: RequestLogEntry) {
  const parts = [
    request.message_count ? `messages=${request.message_count}` : "",
    request.image_count ? `images=${request.image_count}` : "",
    request.video_count ? `videos=${request.video_count}` : "",
    request.audio_count ? `audios=${request.audio_count}` : ""
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : "";
}

function requestTokens(request: RequestLogEntry) {
  const parts = [
    request.prompt_tokens ? `p=${compactNumber(request.prompt_tokens)}` : "",
    request.completion_tokens ? `c=${compactNumber(request.completion_tokens)}` : "",
    request.total_tokens ? `t=${compactNumber(request.total_tokens)}` : "",
    request.cache_tokens ? `cache=${compactNumber(request.cache_tokens)}` : "",
    request.reasoning_tokens ? `reason=${compactNumber(request.reasoning_tokens)}` : ""
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" ") : "";
}

function requestCost(request: RequestLogEntry) {
  const modelUsedCost = request.model_used_cost_usd ?? 0;
  if (modelUsedCost > 0 || request.cost_calculated_at) {
    const pricing = requestPricingSnapshot(request);
    return [`used ${formatMoney(modelUsedCost)}`, pricing].filter(Boolean).join(" · ");
  }
  const tokenCost = request.cost_by_token_rmb ?? 0;
  const requestCostValue = request.cost_by_request_rmb ?? 0;
  if (!tokenCost && !requestCostValue) {
    return "";
  }
  return `tok ${formatMoney(tokenCost)} · req ${formatMoney(requestCostValue)}`;
}

function requestPricingSnapshot(request: RequestLogEntry) {
  const perRequest = request.billing_per_request_usd ?? 0;
  if (perRequest > 0) {
    return `req ${formatPricingValue(perRequest)}`;
  }
  const pieces = [
    request.billing_input_per_million_usd ? `in ${formatPricingValue(request.billing_input_per_million_usd)}/M` : "",
    request.billing_output_per_million_usd ? `out ${formatPricingValue(request.billing_output_per_million_usd)}/M` : "",
    request.billing_cached_input_per_million_usd ? `cache ${formatPricingValue(request.billing_cached_input_per_million_usd)}/M` : ""
  ].filter(Boolean);
  return pieces.join(" ");
}

function requestDetail(request: RequestLogEntry) {
  if (request.error_message) {
    return request.error_message;
  }
  const pieces = [];
  if (request.duration_ms > 0) {
    pieces.push(`duration ${Math.round(request.duration_ms / 1000)}s`);
  }
  if (request.stream) {
    pieces.push("stream");
  }
  if (request.finish_reason) {
    pieces.push(`finish ${request.finish_reason}`);
  }
  if (request.image_count || request.video_count || request.audio_count) {
    pieces.push(requestParts(request));
  }
  return pieces.join(" · ");
}

function formatBytes(value?: number) {
  if (!value) {
    return "0B";
  }
  const units = ["B", "KiB", "MiB", "GiB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit++;
  }
  return `${size.toFixed(unit === 0 ? 0 : 1)}${units[unit]}`;
}

function sortedKeys(record?: Record<string, unknown>) {
  return Object.keys(record ?? {}).sort((a, b) => a.localeCompare(b));
}

function splitLines(value: string) {
  return value
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);
}
