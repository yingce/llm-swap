import { describe, expect, it } from "vitest";
import { buildTagFleetSummaries } from "./fleet";
import { createStatusFixture } from "./testFixtures";

describe("buildTagFleetSummaries", () => {
  it("derives per-tag model readiness from the tag's workers instead of global model availability", () => {
    const summaries = buildTagFleetSummaries(createStatusFixture());

    expect(summaries.map((summary) => summary.tag)).toEqual(["gpu-4090", "self-4090"]);

    const gpu = summaries.find((summary) => summary.tag === "gpu-4090");
    expect(gpu).toMatchObject({
      worker_count: 2,
      healthy_workers: 2,
      draining_workers: 1,
      active_requests: 3,
      gpu_count: 2
    });
    expect(gpu?.models.find((model) => model.name === "joyfox-model-latest")).toMatchObject({
      eligible_workers: 2,
      ready_replicas: 1,
      required_replicas: 2,
      shortfall: 1,
      state: "short"
    });

    const self = summaries.find((summary) => summary.tag === "self-4090");
    expect(self?.models.find((model) => model.name === "joyfox-model-latest")).toMatchObject({
      eligible_workers: 1,
      ready_replicas: 0,
      required_replicas: 1,
      shortfall: 1,
      state: "short"
    });
  });
});
