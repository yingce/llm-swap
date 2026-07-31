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
});
