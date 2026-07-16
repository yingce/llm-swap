import React, { useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import YAML from "yaml";
import {
  applyConfig,
  BillingSummary,
  ConfigChange,
  ConfigImpact,
  ConfigResponse,
  drainWorker,
  GatewayConfigView,
  getConfig,
  getBilling,
  getEvents,
  getStatus,
  getRequests,
  ModelConfig,
  ModelStatus,
  RequestLogEntry,
  StatusResponse,
  TagPolicyConfig,
  unloadModel,
  undrainWorker,
  warmModel,
  WorkerEvent,
  WorkerStatus,
  dryRunConfig
} from "./api";
import "./styles.css";

type Tab = "dashboard" | "models" | "workers" | "billing" | "events" | "requests" | "configOps" | "advanced";

type EditableModelConfig = Omit<ModelConfig, "runtime_args"> & {
  runtime_args: string[];
  max_loaded_auto: boolean;
};

type EditableGatewayConfig = {
  models: Record<string, EditableModelConfig>;
  tag_policies: Record<string, TagPolicyConfig>;
};

const tabs: Array<{ id: Tab; label: string }> = [
  { id: "dashboard", label: "Dashboard" },
  { id: "models", label: "Models" },
  { id: "workers", label: "Workers" },
  { id: "billing", label: "Billing" },
  { id: "events", label: "Events" },
  { id: "requests", label: "Requests" },
  { id: "configOps", label: "Config Ops" },
  { id: "advanced", label: "Advanced" }
];

function App() {
  const [tab, setTab] = useState<Tab>("dashboard");
  const [status, setStatus] = useState<StatusResponse | null>(null);
  const [events, setEvents] = useState<WorkerEvent[]>([]);
  const [eventOffset, setEventOffset] = useState(0);
  const [hasMoreEvents, setHasMoreEvents] = useState(false);
  const [requests, setRequests] = useState<RequestLogEntry[]>([]);
  const [requestOffset, setRequestOffset] = useState(0);
  const [hasMoreRequests, setHasMoreRequests] = useState(false);
  const [billing, setBilling] = useState<BillingSummary | null>(null);
  const [billingRangeHours, setBillingRangeHours] = useState(24);
  const [billingError, setBillingError] = useState("");
  const [error, setError] = useState("");
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
      setStatus(next);
      setError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function refreshConfig() {
    try {
      const next = await getConfig();
      setConfigResponse(next);
      setConfigDraft(toEditableConfig(next));
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
    setEvents((current) => (offset === 0 ? page.events : [...current, ...page.events]));
    setEventOffset(page.next_offset);
    setHasMoreEvents(page.has_more);
  }

  async function loadRequests(offset = 0) {
    const page = await getRequests(offset, 50);
    setRequests((current) => (offset === 0 ? page.requests : [...current, ...page.requests]));
    setRequestOffset(page.next_offset);
    setHasMoreRequests(page.has_more);
  }

  async function loadBilling(rangeHours = billingRangeHours) {
    try {
      const next = await getBilling(rangeHours, true);
      setBilling(next);
      setBillingRangeHours(rangeHours);
      setBillingError("");
    } catch (err) {
      setBillingError(err instanceof Error ? err.message : String(err));
    }
  }

  async function runAction(action: () => Promise<{ action: string; worker_id?: string; model?: string }>) {
    try {
      const result = await action();
      setNotice(`${result.action} done${result.model ? ` · ${result.model}` : ""}${result.worker_id ? ` · ${result.worker_id}` : ""}`);
      await refresh();
      await loadEvents(0);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  useEffect(() => {
    void refresh();
    void refreshConfig();
    void loadEvents(0);
    void loadRequests(0);
    void loadBilling(24);
    const timer = window.setInterval(refresh, 5000);
    return () => window.clearInterval(timer);
  }, []);

  const summary = status?.summary;
  const renderedConfigYaml = useMemo(() => {
    if (!configResponse || !configDraft) {
      return "";
    }
    return renderDraftYAML(configResponse.yaml, configDraft);
  }, [configResponse, configDraft]);
  const configDirty = Boolean(configResponse && renderedConfigYaml !== configResponse.yaml);

  function updateDraft(mutator: (draft: EditableGatewayConfig) => void) {
    setConfigDraft((current) => {
      if (!current) {
        return current;
      }
      const next = cloneEditableConfig(current);
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

  function replaceTagPolicy(tagName: string, nextPolicy: TagPolicyConfig) {
    updateDraft((draft) => {
      draft.tag_policies[tagName] = normalizeTagPolicy(nextPolicy);
    });
  }

  function resetDraft() {
    if (!configResponse) {
      return;
    }
    setConfigDraft(toEditableConfig(configResponse));
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
      await loadEvents(0);
    } catch (err) {
      setConfigError(err instanceof Error ? err.message : String(err));
    }
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

  return (
    <main className="app">
      <header className="topbar">
        <div>
          <h1>LLM Swap Admin</h1>
          <p>{status ? `Updated ${new Date(status.generated_at).toLocaleTimeString()}` : "Loading gateway state"}</p>
        </div>
        <button className="primary" onClick={() => void refresh()}>Refresh</button>
      </header>

      {error ? <div className="alert">Failed to load state: {error}</div> : null}
      {notice ? <div className="notice">{notice}</div> : null}

      <section className="summary-grid">
        <Metric label="Healthy workers" value={summary ? `${summary.healthy_workers}/${summary.total_workers}` : "-"} />
        <Metric label="Available models" value={summary ? `${summary.available_models}/${summary.configured_models}` : "-"} />
        <Metric label="Active requests" value={summary?.active_requests ?? "-"} />
        <Metric label="Draining workers" value={summary?.draining_workers ?? "-"} />
        <Metric label="Stale workers" value={summary?.stale_workers ?? "-"} />
        <Metric label="Recent errors" value={summary ? summary.recent_error_events + summary.workers_with_errors : "-"} />
      </section>

      <div className="shell">
        <nav className="tabs" aria-label="Admin sections">
          {tabs.map((item) => (
            <button key={item.id} className={tab === item.id ? "active" : ""} onClick={() => setTab(item.id)}>
              {item.label}
            </button>
          ))}
        </nav>

        <section className="panel">
          {tab === "dashboard" && (
            <Dashboard status={status} events={events.slice(0, 5)} requests={requests.slice(0, 5)} onAction={runAction} />
          )}
          {tab === "models" && <Models models={status?.models ?? []} onAction={runAction} />}
          {tab === "workers" && <Workers workers={status?.workers ?? []} onAction={runAction} />}
          {tab === "billing" && (
            <Billing billing={billing} rangeHours={billingRangeHours} error={billingError} onRangeChange={(hours) => void loadBilling(hours)} />
          )}
          {tab === "events" && (
            <Events events={events} hasMore={hasMoreEvents} onMore={() => void loadEvents(eventOffset)} />
          )}
          {tab === "requests" && (
            <Requests requests={requests} hasMore={hasMoreRequests} onMore={() => void loadRequests(requestOffset)} />
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
              onTagChange={replaceTagPolicy}
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
        </section>
      </div>
    </main>
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

function Workers({
  workers,
  onAction
}: {
  workers: WorkerStatus[];
  onAction: (action: () => Promise<{ action: string; worker_id?: string; model?: string }>) => Promise<void>;
}) {
  return (
    <div className="worker-grid">
      {workers.map((worker) => {
        const runningModels = worker.running_models ?? [];
        const tags = worker.tags ?? [];
        const gpuDevices = worker.gpu_devices ?? [];
        const agentCommit = shortCommit(worker.agent_build?.commit);
        const agentVersion = worker.agent_build?.version || "unknown";
        return (
          <article className="worker" key={worker.id}>
            <div className="worker-head">
              <strong>{worker.id}</strong>
              <Badge tone={worker.health === "healthy" ? "good" : "bad"}>{worker.health}</Badge>
            </div>
            <p className="mono break">{worker.llama_swap_url}</p>
            <p>{worker.active_requests} active · {runningModels.length} running · {worker.scrape_failures} scrape failures</p>
            <p>
              agent {agentVersion}{agentCommit ? ` · ${agentCommit}` : ""}{" "}
              <Badge tone={agentVersionTone(worker.agent_version_status)}>{worker.agent_version_status}</Badge>
            </p>
            <div className="worker-models">
              <strong>Current models</strong>
              <div className="chips">
                {runningModels.length > 0 ? (
                  runningModels.map((model) => (
                    <span key={`${worker.id}-${model.model}`} className={`model-pill ${model.state || "ready"}`}>
                      {model.model}
                      {model.state ? ` · ${model.state}` : ""}
                    </span>
                  ))
                ) : (
                  <span className="muted">none</span>
                )}
              </div>
            </div>
            <div className="worker-models">
              <strong>GPU</strong>
              {gpuDevices.length > 0 ? (
                <div className="gpu-list">
                  {gpuDevices.map((gpu) => <GPUDeviceView key={`${worker.id}-${gpu.index}-${gpu.uuid || gpu.name}`} gpu={gpu} />)}
                </div>
              ) : (
                <span className="muted">no GPU metrics reported</span>
              )}
            </div>
            <div className="actions">
              {worker.state === "draining" ? (
                <button onClick={() => void onAction(() => undrainWorker(worker.id))}>Undrain</button>
              ) : (
                <button onClick={() => void onAction(() => drainWorker(worker.id))}>Drain</button>
              )}
            </div>
            <div className="chips">
              {tags.map((tag) => <span key={tag}>{tag}</span>)}
            </div>
            {worker.last_error ? <div className="alert compact">{worker.last_error}</div> : null}
          </article>
        );
      })}
    </div>
  );
}

function GPUDeviceView({ gpu }: { gpu: WorkerStatus["gpu_devices"][number] }) {
  const usedPercent = gpu.memory_total_mib > 0 ? Math.min(100, Math.max(0, (gpu.memory_used_mib / gpu.memory_total_mib) * 100)) : 0;
  return (
    <div className="gpu-card">
      <div className="gpu-card-head">
        <strong>{gpu.index}: {gpu.name}</strong>
        <span>{Math.round(gpu.utilization_percent)}%</span>
      </div>
      <div className="gpu-bar" aria-label={`GPU ${gpu.index} memory ${Math.round(usedPercent)} percent used`}>
        <span style={{ width: `${usedPercent}%` }} />
      </div>
      <div className="gpu-meta">
        <span>{formatMiB(gpu.memory_used_mib)} / {formatMiB(gpu.memory_total_mib)}</span>
        <span>{Math.round(gpu.temperature_celsius)}C</span>
      </div>
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
  onRangeChange
}: {
  billing: BillingSummary | null;
  rangeHours: number;
  error: string;
  onRangeChange: (hours: number) => void;
}) {
  const recentRequestCosts = billing?.request_costs?.slice(-20).reverse() ?? [];
  return (
    <div className="stack">
      <div className="config-toolbar">
        <div>
          <h2>Billing</h2>
          <p className="toolbar-sub">
            {billing ? `${new Date(billing.start).toLocaleString()} - ${new Date(billing.end).toLocaleString()}` : "Loading cost records"}
          </p>
        </div>
        <div className="segmented">
          {[6, 24, 72, 168].map((hours) => (
            <button key={hours} className={rangeHours === hours ? "active" : ""} onClick={() => onRangeChange(hours)}>
              {hours < 24 ? `${hours}h` : `${hours / 24}d`}
            </button>
          ))}
        </div>
      </div>

      {error ? <div className="alert">Billing unavailable: {error}</div> : null}

      <div className="traffic-summary">
        <Metric label="Model cost" value={formatMoney(billing?.totals.model_cost_rmb)} />
        <Metric label="Billable hours" value={formatHours(billing?.totals.billable_worker_seconds)} />
        <Metric label="Requests" value={compactNumber(billing?.totals.requests)} />
        <Metric label="Total tokens" value={compactNumber(billing?.totals.total_tokens)} />
        <Metric label="Worker day price" value={formatMoney(billing?.worker_day_cost_rmb)} />
      </div>

      <div className="table-wrap">
        <h3>Model cost</h3>
        <table>
          <thead>
            <tr>
              <th>Model</th>
              <th>Cost</th>
              <th>Billable time</th>
              <th>Requests</th>
              <th>Tokens</th>
              <th>Per request</th>
              <th>Per 1M tokens</th>
            </tr>
          </thead>
          <tbody>
            {(billing?.models ?? []).map((model) => (
              <tr key={model.model}>
                <td><strong>{model.model}</strong></td>
                <td>{formatMoney(model.model_cost_rmb)}</td>
                <td>{formatHours(model.billable_worker_seconds)}</td>
                <td>{compactNumber(model.requests)}</td>
                <td>{compactNumber(model.total_tokens)}</td>
                <td>{formatMoney(model.cost_per_request_rmb)}</td>
                <td>{formatMoney(model.cost_per_million_tokens_rmb)}</td>
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
              <th>Tokens</th>
              <th>Token cost</th>
              <th>Request allocation</th>
            </tr>
          </thead>
          <tbody>
            {(billing?.apps ?? []).map((app) => (
              <tr key={app.app_id}>
                <td><strong>{app.app_id}</strong></td>
                <td>{compactNumber(app.requests)}</td>
                <td>{compactNumber(app.total_tokens)}</td>
                <td>{formatMoney(app.cost_by_token_rmb)}</td>
                <td>{formatMoney(app.request_cost_by_request_rmb)}</td>
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
              <th>Tokens</th>
              <th>Token unit</th>
              <th>Token cost</th>
              <th>Request cost</th>
            </tr>
          </thead>
          <tbody>
            {recentRequestCosts.map((request) => (
              <tr key={`${request.time}-${request.request_id}`}>
                <td>{new Date(request.time).toLocaleTimeString()}</td>
                <td>{request.model}</td>
                <td>{request.app_id || "-"}</td>
                <td>{compactNumber(request.total_tokens)}</td>
                <td>{formatUnitPrice(request.token_unit_price_rmb)}</td>
                <td>{formatMoney(request.cost_by_token_rmb)}</td>
                <td>{formatMoney(request.request_cost_by_request_rmb)}</td>
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
  onTagChange
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
  onTagChange: (tagName: string, nextPolicy: TagPolicyConfig) => void;
}) {
  const modelNames = useMemo(() => sortedKeys(draft?.models), [draft]);
  const tagNames = useMemo(() => sortedKeys(draft?.tag_policies), [draft]);
  const [selectedModel, setSelectedModel] = useState("");
  const [selectedTag, setSelectedTag] = useState("");

  useEffect(() => {
    if (!selectedModel || !draft?.models[selectedModel]) {
      setSelectedModel(modelNames[0] ?? "");
    }
  }, [draft, modelNames, selectedModel]);

  useEffect(() => {
    if (!selectedTag || !draft?.tag_policies[selectedTag]) {
      setSelectedTag(tagNames[0] ?? "");
    }
  }, [draft, selectedTag, tagNames]);

  if (!configResponse || !draft) {
    return <div className="empty">Loading config workspace...</div>;
  }

  const selectedModelConfig = draft.models[selectedModel];
  const selectedTagPolicy = draft.tag_policies[selectedTag];
  const liveModelMap = new Map((status?.models ?? []).map((model) => [model.name, model]));

  return (
    <div className="config-ops">
      <div className="config-toolbar">
        <div>
          <h2>Config Ops</h2>
          <p className="toolbar-sub">Version {configResponse.version} · model + tag changes only</p>
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
          <ConfigListCard title="Models" subtitle="Select a model to edit push and replica policy.">
            {modelNames.map((modelName) => {
              const model = draft.models[modelName];
              const live = liveModelMap.get(modelName);
              return (
                <button
                  key={modelName}
                  className={`picker-item ${selectedModel === modelName ? "selected" : ""}`}
                  onClick={() => setSelectedModel(modelName)}
                >
                  <div>
                    <strong>{modelName}</strong>
                    <small>{model.runtime || (model.run ? "raw run" : "runtime unset")}</small>
                  </div>
                  <div className="picker-meta">
                    <Badge tone={live?.available ? "good" : "warn"}>{live?.available ? "ready" : "draft"}</Badge>
                    <span>{model.min_loaded}/{model.max_loaded_auto ? "auto" : model.max_loaded}</span>
                  </div>
                </button>
              );
            })}
          </ConfigListCard>

          {selectedModelConfig ? (
            <ModelEditor
              key={selectedModel}
              name={selectedModel}
              model={selectedModelConfig}
              liveStatus={liveModelMap.get(selectedModel)}
              onChange={(nextModel) => onModelChange(selectedModel, nextModel)}
            />
          ) : null}
        </div>

        <div className="config-stack">
          <ConfigListCard title="Tag Policies" subtitle="Edit routing and concurrency for each worker tag.">
            {tagNames.map((tagName) => {
              const policy = draft.tag_policies[tagName];
              return (
                <button
                  key={tagName}
                  className={`picker-item ${selectedTag === tagName ? "selected" : ""}`}
                  onClick={() => setSelectedTag(tagName)}
                >
                  <div>
                    <strong>{tagName}</strong>
                    <small>{policy.allowed_models.length} models allowed</small>
                  </div>
                  <div className="picker-meta">
                    <span>tag {policy.max_concurrency || 0}</span>
                    <span>worker {policy.worker_defaults.max_concurrency || 0}</span>
                  </div>
                </button>
              );
            })}
          </ConfigListCard>

          {selectedTagPolicy ? (
            <TagPolicyEditor
              name={selectedTag}
              policy={selectedTagPolicy}
              modelNames={modelNames}
              onChange={(nextPolicy) => onTagChange(selectedTag, nextPolicy)}
            />
          ) : null}

          <ImpactSummary applyMode={applyMode} impacts={impacts} changes={changes} />
          <ChangeList changes={changes} />
        </div>
      </div>
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
      <div className="advanced-readonly">
        <textarea spellCheck={false} readOnly value={yaml} />
      </div>
    </div>
  );
}

function ConfigListCard({
  title,
  subtitle,
  children
}: {
  title: string;
  subtitle: string;
  children: React.ReactNode;
}) {
  return (
    <div className="config-card">
      <div className="config-card-head">
        <div>
          <h3>{title}</h3>
          <p>{subtitle}</p>
        </div>
      </div>
      <div className="picker-list">{children}</div>
    </div>
  );
}

function ModelEditor({
  name,
  model,
  liveStatus,
  onChange
}: {
  name: string;
  model: EditableModelConfig;
  liveStatus?: ModelStatus;
  onChange: (nextModel: EditableModelConfig) => void;
}) {
  const isRawRunModel = Boolean(model.run && !model.runtime);
  const runtimeArgsValue = model.runtime_args.join("\n");
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
          <p>Push policy, runtime, and artifact settings.</p>
        </div>
        <div className="config-card-state">
          <Badge tone={liveStatus?.available ? "good" : "warn"}>{liveStatus?.available ? "ready" : "draft only"}</Badge>
          <span>{liveStatus ? `${liveStatus.ready_workers} ready / ${liveStatus.running_workers} running` : "not active"}</span>
        </div>
      </div>

      {isRawRunModel ? <div className="notice">This model uses a raw `run` command. Runtime command text stays read-only in Ops.</div> : null}

      <div className="detail-grid">
        <NumberField label="Priority" value={model.priority} onChange={(value) => onChange({ ...model, priority: value })} />
        <NumberField label="Min loaded" value={model.min_loaded} onChange={(value) => onChange({ ...model, min_loaded: value })} />
        <NumberField label="Max concurrency" value={model.max_concurrency} onChange={(value) => onChange({ ...model, max_concurrency: value })} />
        <NumberField label="Max queue" value={model.max_queue} onChange={(value) => onChange({ ...model, max_queue: value })} />
        <NumberField label="Queue timeout ms" value={model.queue_timeout_ms} onChange={(value) => onChange({ ...model, queue_timeout_ms: value })} />
        <NumberField label="TTL seconds" value={model.ttl} onChange={(value) => onChange({ ...model, ttl: value })} />

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

        <label>
          <span>Runtime</span>
          <input
            value={model.runtime ?? ""}
            disabled={isRawRunModel}
            onChange={(event) => onChange({ ...model, runtime: event.target.value })}
          />
        </label>
        <label>
          <span>Check endpoint</span>
          <input value={model.check_endpoint ?? ""} onChange={(event) => onChange({ ...model, check_endpoint: event.target.value })} />
        </label>

        <label className="field-span">
          <span>Artifact object</span>
          <input
            value={model.artifact.object}
            onChange={(event) => onChange({ ...model, artifact: { ...model.artifact, object: event.target.value } })}
          />
        </label>
        <label>
          <span>Artifact kind</span>
          <select
            value={model.artifact.kind}
            onChange={(event) => onChange({ ...model, artifact: { ...model.artifact, kind: event.target.value } })}
          >
            <option value="tar_gz">tar_gz</option>
            <option value="file">file</option>
          </select>
        </label>
        <label>
          <span>CRC64 ECMA</span>
          <input
            value={model.artifact.crc64ecma}
            onChange={(event) => onChange({ ...model, artifact: { ...model.artifact, crc64ecma: event.target.value } })}
          />
        </label>

        <label className="field-span">
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
    </div>
  );
}

function TagPolicyEditor({
  name,
  policy,
  modelNames,
  onChange
}: {
  name: string;
  policy: TagPolicyConfig;
  modelNames: string[];
  onChange: (nextPolicy: TagPolicyConfig) => void;
}) {
  return (
    <div className="config-card">
      <div className="config-card-head">
        <div>
          <h3>{name}</h3>
          <p>Routing, queueing, and allowed models for this worker tag.</p>
        </div>
      </div>

      <div className="detail-grid">
        <NumberField
          label="Tag max concurrency"
          value={policy.max_concurrency}
          onChange={(value) => onChange({ ...policy, max_concurrency: value })}
        />
        <NumberField
          label="Tag max queue"
          value={policy.max_queue}
          onChange={(value) => onChange({ ...policy, max_queue: value })}
        />
        <NumberField
          label="Worker max concurrency"
          value={policy.worker_defaults.max_concurrency}
          onChange={(value) => onChange({ ...policy, worker_defaults: { ...policy.worker_defaults, max_concurrency: value } })}
        />
        <NumberField
          label="Worker max queue"
          value={policy.worker_defaults.max_queue}
          onChange={(value) => onChange({ ...policy, worker_defaults: { ...policy.worker_defaults, max_queue: value } })}
        />
        <label className="field-span">
          <span>Warm when idle</span>
          <input value={policy.warm_when_idle ?? ""} onChange={(event) => onChange({ ...policy, warm_when_idle: event.target.value })} />
        </label>
      </div>

      <div className="checkbox-block">
        <strong>Allowed models</strong>
        <div className="checkbox-list">
          {modelNames.map((modelName) => {
            const checked = policy.allowed_models.includes(modelName);
            return (
              <label key={modelName} className="checkbox-item">
                <input
                  type="checkbox"
                  checked={checked}
                  onChange={() => {
                    const nextAllowed = checked
                      ? policy.allowed_models.filter((item) => item !== modelName)
                      : [...policy.allowed_models, modelName];
                    onChange({ ...policy, allowed_models: nextAllowed.sort() });
                  }}
                />
                <span>{modelName}</span>
              </label>
            );
          })}
        </div>
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
    return "¥0.00";
  }
  return `¥${numberValue.toFixed(numberValue >= 100 ? 1 : 2)}`;
}

function formatUnitPrice(value: number | undefined) {
  const numberValue = Number(value ?? 0);
  if (!Number.isFinite(numberValue) || numberValue <= 0) {
    return "¥0/token";
  }
  if (numberValue < 0.000001) {
    return `¥${numberValue.toExponential(2)}/token`;
  }
  return `¥${numberValue.toFixed(6)}/token`;
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
  const tokenCost = request.cost_by_token_rmb ?? 0;
  const requestCostValue = request.cost_by_request_rmb ?? 0;
  if (!tokenCost && !requestCostValue) {
    return "";
  }
  return `tok ${formatMoney(tokenCost)} · req ${formatMoney(requestCostValue)}`;
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

function formatMiB(value?: number) {
  const mib = Number(value ?? 0);
  if (!Number.isFinite(mib) || mib <= 0) {
    return "0MiB";
  }
  if (mib >= 1024) {
    return `${(mib / 1024).toFixed(1).replace(/\.0$/, "")}GiB`;
  }
  return `${Math.round(mib)}MiB`;
}

function shortCommit(commit?: string) {
  return commit ? commit.slice(0, 12) : "";
}

function agentVersionTone(status?: string): "good" | "warn" | "bad" {
  if (status === "current") {
    return "good";
  }
  if (status === "outdated") {
    return "bad";
  }
  return "warn";
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

function normalizeTagPolicy(policy: TagPolicyConfig): TagPolicyConfig {
  return {
    ...policy,
    worker_defaults: {
      max_concurrency: policy.worker_defaults?.max_concurrency ?? 0,
      max_queue: policy.worker_defaults?.max_queue ?? 0
    },
    allowed_models: [...(policy.allowed_models ?? [])].sort()
  };
}

function cloneEditableConfig(config: EditableGatewayConfig): EditableGatewayConfig {
  return {
    models: Object.fromEntries(
      Object.entries(config.models).map(([name, model]) => [
        name,
        {
          ...model,
          artifact: { ...model.artifact },
          runtime_args: [...model.runtime_args]
        }
      ])
    ),
    tag_policies: Object.fromEntries(
      Object.entries(config.tag_policies).map(([name, policy]) => [
        name,
        {
          ...policy,
          worker_defaults: { ...policy.worker_defaults },
          allowed_models: [...policy.allowed_models]
        }
      ])
    )
  };
}

function toEditableConfig(configResponse: ConfigResponse): EditableGatewayConfig {
  const parsed = YAML.parseDocument(configResponse.yaml);
  const editableModels = Object.fromEntries(
    Object.entries(configResponse.config.models ?? {}).map(([name, model]) => [
      name,
      {
        ...model,
        artifact: { ...model.artifact },
        runtime_args: [...(model.runtime_args ?? [])],
        max_loaded_auto: !yamlModelHasKey(parsed, name, "max_loaded") && model.max_loaded === 0
      }
    ])
  );
  const editableTagPolicies = Object.fromEntries(
    Object.entries(configResponse.config.tag_policies ?? {}).map(([name, policy]) => [name, normalizeTagPolicy(policy)])
  );
  return {
    models: editableModels,
    tag_policies: editableTagPolicies
  };
}

function yamlModelHasKey(document: YAML.Document.Parsed, modelName: string, field: string) {
  const modelsNode = document.get("models", true) as any;
  const modelNode = modelsNode?.items?.find((item: any) => item?.key?.value === modelName)?.value;
  return Boolean(modelNode?.items?.some((item: any) => item?.key?.value === field));
}

function toGatewayConfigView(draft: EditableGatewayConfig): GatewayConfigView {
  return {
    models: Object.fromEntries(
      Object.entries(draft.models).map(([name, model]) => {
        const nextModel: ModelConfig = {
          priority: model.priority,
          min_loaded: model.min_loaded,
          max_loaded: model.max_loaded_auto ? 0 : model.max_loaded,
          max_concurrency: model.max_concurrency,
          max_queue: model.max_queue,
          queue_timeout_ms: model.queue_timeout_ms,
          ttl: model.ttl,
          artifact: { ...model.artifact },
          run: model.run,
          runtime: model.runtime,
          runtime_args: [...model.runtime_args],
          cmd_stop: model.cmd_stop,
          check_endpoint: model.check_endpoint
        };
        return [name, nextModel];
      })
    ),
    tag_policies: Object.fromEntries(
      Object.entries(draft.tag_policies).map(([name, policy]) => [
        name,
        {
          ...policy,
          worker_defaults: { ...policy.worker_defaults },
          allowed_models: [...policy.allowed_models].sort()
        }
      ])
    )
  };
}

function renderDraftYAML(baseYaml: string, draft: EditableGatewayConfig) {
  const document = YAML.parseDocument(baseYaml);
  const rendered = toGatewayConfigView(draft);
  document.set("models", createYamlModelsMap(rendered.models, draft.models));
  document.set("tag_policies", rendered.tag_policies);
  return String(document);
}

function createYamlModelsMap(
  models: Record<string, ModelConfig>,
  editableModels: Record<string, EditableModelConfig>
) {
  return Object.fromEntries(
    Object.entries(models).map(([name, model]) => {
      const editable = editableModels[name];
      const nextModel: Record<string, unknown> = {
        priority: model.priority,
        min_loaded: model.min_loaded,
        max_concurrency: model.max_concurrency,
        max_queue: model.max_queue,
        queue_timeout_ms: model.queue_timeout_ms,
        ttl: model.ttl,
        artifact: { ...model.artifact }
      };
      if (!editable.max_loaded_auto) {
        nextModel.max_loaded = model.max_loaded;
      }
      if (model.run) {
        nextModel.run = model.run;
      }
      if (model.runtime) {
        nextModel.runtime = model.runtime;
      }
      if (model.runtime_args && model.runtime_args.length > 0) {
        nextModel.runtime_args = model.runtime_args;
      }
      if (model.cmd_stop) {
        nextModel.cmd_stop = model.cmd_stop;
      }
      if (model.check_endpoint) {
        nextModel.check_endpoint = model.check_endpoint;
      }
      return [name, nextModel];
    })
  );
}

createRoot(document.getElementById("llmswap-admin-root")!).render(<App />);
