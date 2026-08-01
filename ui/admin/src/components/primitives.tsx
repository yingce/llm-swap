import type React from "react";

import type { AttentionItem } from "../domain/attention";

export type StatusTone = "good" | "warn" | "bad" | "neutral";

export function StatusIndicator({
  tone,
  label,
  detail
}: {
  tone: StatusTone;
  label: string;
  detail?: string;
}) {
  return (
    <span className={`status-indicator ${tone}`} title={detail} aria-label={detail ? `${label}: ${detail}` : label}>
      <span className="status-indicator-dot" aria-hidden="true" />
      <span>{label}</span>
    </span>
  );
}

export function EmptyState({
  title,
  body,
  action
}: {
  title: string;
  body?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="empty-state">
      <strong>{title}</strong>
      {body ? <p>{body}</p> : null}
      {action ? <div className="empty-state-action">{action}</div> : null}
    </div>
  );
}

export function DetailPanel({
  title,
  subtitle,
  meta,
  children
}: {
  title: string;
  subtitle?: string;
  meta?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="detail-panel">
      <header className="detail-panel-header">
        <div>
          <h3>{title}</h3>
          {subtitle ? <p>{subtitle}</p> : null}
        </div>
        {meta ? <div className="detail-panel-meta">{meta}</div> : null}
      </header>
      <div className="detail-panel-body">{children}</div>
    </section>
  );
}

export function ResourceList<T>({
  items,
  getKey,
  renderItem,
  empty
}: {
  items: T[];
  getKey: (item: T) => string;
  renderItem: (item: T) => React.ReactNode;
  empty?: React.ReactNode;
}) {
  if (items.length === 0) {
    return <>{empty ?? <EmptyState title="No resources" body="Nothing matches the current view." />}</>;
  }

  return (
    <div className="resource-list">
      {items.map((item) => (
        <div className="resource-list-row" key={getKey(item)}>
          {renderItem(item)}
        </div>
      ))}
    </div>
  );
}

export function AttentionList({
  items,
  emptyMessage = "No active incidents in the current status snapshot."
}: {
  items: AttentionItem[];
  emptyMessage?: string;
}) {
  if (items.length === 0) {
    return <EmptyState title="All clear" body={emptyMessage} />;
  }

  return (
    <div className="attention-list">
      {items.map((item) => (
        <article className={`attention-item ${item.severity}`} key={item.id}>
          <div>
            <StatusIndicator tone={attentionTone(item.severity)} label={item.severity} />
          </div>
          <div>
            <strong>{item.title}</strong>
            <p>{item.detail}</p>
          </div>
        </article>
      ))}
    </div>
  );
}

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  cancelLabel = "Cancel",
  destructive = false,
  onConfirm,
  onCancel
}: {
  open: boolean;
  title: string;
  body: React.ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  destructive?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  if (!open) {
    return null;
  }

  return (
    <div className="confirm-dialog" role="dialog" aria-modal={true} aria-labelledby="confirm-dialog-title">
      <div className="confirm-dialog-scrim" onClick={onCancel} aria-hidden="true" />
      <section className="confirm-dialog-panel">
        <h2 id="confirm-dialog-title">{title}</h2>
        <div className="confirm-dialog-body">{body}</div>
        <div className="confirm-dialog-actions">
          <button type="button" onClick={onCancel}>{cancelLabel}</button>
          <button type="button" className={destructive ? "danger" : "primary"} onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </section>
    </div>
  );
}

function attentionTone(severity: AttentionItem["severity"]): StatusTone {
  if (severity === "critical") {
    return "bad";
  }
  if (severity === "warning") {
    return "warn";
  }
  return "neutral";
}
