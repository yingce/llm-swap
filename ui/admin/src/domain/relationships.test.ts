import { describe, expect, it } from "vitest";
import { buildRelationshipView } from "./relationships";
import { createStatusFixture } from "./testFixtures";

describe("buildRelationshipView", () => {
  it("models gateway to tag to worker to loaded model topology without FRP or GPU-model edges", () => {
    const view = buildRelationshipView(createStatusFixture());

    expect(view.gateway.id).toBe("gateway");
    expect(view.tags.map((tag) => tag.tag)).toEqual(["gpu-4090", "self-4090"]);
    expect(view.tags.find((tag) => tag.tag === "gpu-4090")?.workers).toEqual([
      {
        id: "worker-a",
        health: "healthy",
        state: "active",
        gpu_count: 1,
        loaded_models: [{ name: "joyfox-model-latest", state: "ready" }]
      },
      {
        id: "worker-b",
        health: "healthy",
        state: "draining",
        gpu_count: 1,
        loaded_models: [{ name: "joyfox-model-latest", state: "installing" }]
      }
    ]);

    expect(view.nodes.some((node) => String(node.type) === "frp" || node.id.toLowerCase().includes("frp"))).toBe(false);
    expect(view.edges.some((edge) => String(edge.type) === "gpu-model")).toBe(false);
    expect(new Set(view.edges.map((edge) => `${edge.from}->${edge.to}:${edge.type}`)).size).toBe(view.edges.length);
  });
});
