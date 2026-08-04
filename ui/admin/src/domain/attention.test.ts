import { describe, expect, it } from "vitest";
import { buildAttentionItems } from "./attention";
import { createStatusFixture } from "./testFixtures";

describe("buildAttentionItems", () => {
  it("surfaces actionable status exceptions while excluding intentionally cold min_loaded=0 models", () => {
    const items = buildAttentionItems(createStatusFixture());
    const types = items.map((item) => item.type);

    expect(types).toContain("model_shortfall");
    expect(types).toContain("worker_draining");
    expect(types).toContain("worker_stale");
    expect(types).toContain("worker_error");
    expect(types).toContain("worker_restart_needed");
    expect(types).toContain("scrape_backoff");
    expect(types).toContain("replica_cooldown");
    expect(types).toContain("event_error");

    expect(items.some((item) => item.model === "embedding-idle")).toBe(false);
    expect(types).not.toContain("gateway_restart");
  });

  it("limits historical event errors to the selected overview window", () => {
    const status = createStatusFixture();
    status.events.unshift({
      received_at: "2026-07-31T04:00:00Z",
      worker_id: "worker-a",
      event: "model_error",
      model: "joyfox-model-latest",
      error: "old failure"
    });

    const items = buildAttentionItems(status, {
      event_start: "2026-07-31T07:00:00Z",
      event_end: "2026-07-31T08:00:00Z"
    });

    expect(items.some((item) => item.detail === "upstream returned 503")).toBe(true);
    expect(items.some((item) => item.detail === "old failure")).toBe(false);
    expect(items.some((item) => item.type === "worker_stale")).toBe(true);
  });
});
