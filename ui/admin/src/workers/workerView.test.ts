import { describe, expect, it } from "vitest";

import { createStatusFixture } from "../domain/testFixtures";
import { buildWorkerRows } from "./workerView";

describe("buildWorkerRows", () => {
  it("summarizes worker GPU, model, and connectivity fields without FRP details", () => {
    const rows = buildWorkerRows(createStatusFixture(), { query: "" });

    expect(rows.map((row) => row.id)).toEqual(["worker-a", "worker-b", "worker-c"]);
    expect(rows[0]).toMatchObject({
      id: "worker-a",
      tags: ["gpu-4090"],
      gpu_count: 1,
      loaded_models: ["joyfox-model-latest:ready"],
      connectivity: "healthy · active · scrape ok"
    });
    expect(JSON.stringify(rows).toLowerCase()).not.toContain("frp");
  });

  it("searches workers by id, tag, and loaded model", () => {
    expect(buildWorkerRows(createStatusFixture(), { query: "self-4090" }).map((row) => row.id)).toEqual(["worker-b", "worker-c"]);
    expect(buildWorkerRows(createStatusFixture(), { query: "joyfox" }).map((row) => row.id)).toEqual(["worker-a", "worker-b"]);
  });
});
