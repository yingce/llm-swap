import type React from "react";

import type { Summary } from "../api";
import { NAV_GROUPS, type Tab } from "./navigation";

export function AppShell({
  appContentRef,
  tab,
  summary,
  statusUpdatedAt,
  error,
  notice,
  onRefresh,
  onSelectTab,
  children
}: {
  appContentRef: React.RefObject<HTMLElement | null>;
  tab: Tab;
  summary: Summary | undefined;
  statusUpdatedAt: string | undefined;
  error: string;
  notice: string;
  onRefresh: () => void;
  onSelectTab: (tab: Tab) => void;
  children: React.ReactNode;
}) {
  return (
    <main ref={appContentRef} className="app">
      <header className="topbar">
        <div>
          <h1>LLM Swap Admin</h1>
          <p>{statusUpdatedAt ? `Updated ${new Date(statusUpdatedAt).toLocaleTimeString()}` : "Loading gateway state"}</p>
        </div>
        <button className="primary" onClick={onRefresh}>Refresh</button>
      </header>

      {error ? <div className="alert">Failed to load state: {error}</div> : null}
      {notice ? <div className="notice">{notice}</div> : null}

      <section className="summary-grid">
        <ShellMetric label="Healthy workers" value={summary ? `${summary.healthy_workers}/${summary.total_workers}` : "-"} />
        <ShellMetric label="Available models" value={summary ? `${summary.available_models}/${summary.configured_models}` : "-"} />
        <ShellMetric label="Active requests" value={summary?.active_requests ?? "-"} />
        <ShellMetric label="Draining workers" value={summary?.draining_workers ?? "-"} />
        <ShellMetric label="Stale workers" value={summary?.stale_workers ?? "-"} />
        <ShellMetric label="Recent errors" value={summary ? summary.recent_error_events + summary.workers_with_errors : "-"} />
      </section>

      <div className="shell">
        <nav className="tabs" aria-label="Admin sections">
          {NAV_GROUPS.map((group) => (
            <div className="tab-group" key={group.label}>
              <span className="tab-group-label">{group.label}</span>
              {group.items.map((item) => (
                <button key={item.id} className={tab === item.id ? "active" : ""} onClick={() => onSelectTab(item.id)}>
                  {item.label}
                </button>
              ))}
            </div>
          ))}
        </nav>

        <section className="panel">{children}</section>
      </div>
    </main>
  );
}

function ShellMetric({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="metric">
      <strong>{value}</strong>
      <span>{label}</span>
    </div>
  );
}
