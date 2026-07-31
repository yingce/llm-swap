import { describe, expect, it } from "vitest";

import { buildOverviewView } from "./overviewView";
import { createStatusFixture } from "../domain/testFixtures";

describe("buildOverviewView", () => {
  it("builds an exception-first status summary from /ui/status only", () => {
    const view = buildOverviewView(createStatusFixture());

    expect(view.conclusion.tone).toBe("bad");
    expect(view.conclusion.title).toContain("critical");
    expect(view.attentionItems.map((item) => item.type)).toContain("model_shortfall");
    expect(view.tagSummaries.map((tag) => tag.tag)).toEqual(["gpu-4090", "self-4090"]);
    expect(view.relationship.tags[0].workers[0].gpu_count).toBe(1);
    expect(view.relationship.tags[0].workers[0].loaded_models).toEqual([{ name: "joyfox-model-latest", state: "ready" }]);
    expect(view.relationship.nodes.some((node) => node.id.toLowerCase().includes("frp"))).toBe(false);
    expect(view.relationship.edges.some((edge) => String(edge.type) === "gpu-model")).toBe(false);
  });

  it("does not treat cold min_loaded=0 models as overview incidents", () => {
    const view = buildOverviewView(createStatusFixture());

    expect(view.attentionItems.some((item) => item.model === "embedding-idle")).toBe(false);
  });
});
