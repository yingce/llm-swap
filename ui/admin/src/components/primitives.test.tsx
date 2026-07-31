import { describe, expect, it, vi } from "vitest";

import { AttentionList, ConfirmDialog, DetailPanel, EmptyState, ResourceList, StatusIndicator } from "./primitives";

describe("signal room primitives", () => {
  it("keeps confirm dialog absent until a protected action is active", () => {
    expect(
      ConfirmDialog({
        open: false,
        title: "Unload model?",
        body: "This removes the loaded replica.",
        confirmLabel: "Unload",
        onConfirm: vi.fn(),
        onCancel: vi.fn()
      })
    ).toBeNull();
  });

  it("renders an accessible confirm dialog with explicit confirm and cancel actions", () => {
    const dialog = ConfirmDialog({
      open: true,
      title: "Drain worker?",
      body: "Existing requests continue; new requests stop routing here.",
      confirmLabel: "Drain",
      cancelLabel: "Keep active",
      onConfirm: vi.fn(),
      onCancel: vi.fn(),
      destructive: true
    }) as any;

    expect(dialog.props.role).toBe("dialog");
    expect(dialog.props["aria-modal"]).toBe(true);
    expect(JSON.stringify(dialog)).toContain("Drain worker?");
    expect(JSON.stringify(dialog)).toContain("Keep active");
  });

  it("renders status, empty, detail, resource, and attention structures without route data assumptions", () => {
    expect((StatusIndicator({ tone: "good", label: "Ready" }) as any).props.className).toContain("status-indicator");
    expect(JSON.stringify(EmptyState({ title: "No workers", body: "Register an agent to begin." }))).toContain("No workers");
    expect(JSON.stringify(DetailPanel({ title: "worker-a", subtitle: "gpu-4090", children: "ready" }))).toContain("worker-a");
    expect(
      JSON.stringify(
        ResourceList({
          items: ["worker-a"],
          getKey: (item) => item,
          renderItem: (item) => item
        })
      )
    ).toContain("worker-a");
    expect(
      JSON.stringify(
        AttentionList({
          items: [
            {
              id: "model-shortfall:qwen",
              type: "model_shortfall",
              severity: "critical",
              title: "qwen is below min replicas",
              detail: "0/1 ready",
              model: "qwen"
            }
          ]
        })
      )
    ).toContain("qwen is below min replicas");
  });
});
