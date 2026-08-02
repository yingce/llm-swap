// @ts-expect-error Vitest runs this source-contract test in Node; the admin app ships without Node types.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { createStatusFixture } from "../domain/testFixtures";
import { buildWorkerFilters, buildWorkerRows } from "./workerView";
import * as workerView from "./workerView";

const workersPageSource = readFileSync(new URL("./WorkersPage.tsx", import.meta.url), "utf8");

describe("buildWorkerRows", () => {
  it("summarizes worker GPU, model, and connectivity fields without FRP details", () => {
    const rows = buildWorkerRows(createStatusFixture(), { query: "" });

    expect(rows.map((row) => row.id)).toEqual(["worker-a", "worker-b", "worker-c"]);
    expect(rows[0]).toMatchObject({
      id: "worker-a",
      tags: ["gpu-4090"],
      gpu_count: 1,
      loaded_models: ["joyfox-model-latest:ready"],
      active_requests: 2,
      live_capacity_available: true,
      max_concurrency: 4,
      queued_requests: 1,
      max_queue: 6,
      connectivity: "healthy · active · scrape ok",
      agent_version: "1.0.0 · current",
      diagnostics: "build 1.0.0 (latest) · heartbeat 2s ago · scrape failures 0"
    });
    expect(rows[0].gpu_devices[0]).toMatchObject({
      index: 0,
      memory: "15.6GiB / 24GiB",
      memory_percent: 65,
      utilization: "80%",
      temperature: "70°C"
    });
    expect(JSON.stringify(rows).toLowerCase()).not.toContain("frp");
  });

  it("uses zero limits when an older gateway omits live pressure capacity", () => {
    const status = createStatusFixture();
    delete status.workers[0].max_concurrency;
    delete status.workers[0].max_queue;
    delete status.workers[0].live_capacity_available;

    const [row] = buildWorkerRows(status, { query: "" });

    expect(row).toMatchObject({ live_capacity_available: false, max_concurrency: 0, max_queue: 0 });
  });

  it("searches workers by id, tag, and loaded model", () => {
    expect(buildWorkerRows(createStatusFixture(), { query: "self-4090" }).map((row) => row.id)).toEqual(["worker-b", "worker-c"]);
    expect(buildWorkerRows(createStatusFixture(), { query: "joyfox" }).map((row) => row.id)).toEqual(["worker-a", "worker-b"]);
  });

  it("filters flat worker cards by tag and running model", () => {
    const status = createStatusFixture();

    expect(buildWorkerFilters(status)).toEqual({
      tags: ["gpu-4090", "self-4090"],
      models: ["joyfox-model-latest"]
    });
    expect(buildWorkerRows(status, { query: "", tag: "self-4090", model: "" }).map((row) => row.id)).toEqual(["worker-b", "worker-c"]);
    expect(buildWorkerRows(status, { query: "", tag: "", model: "joyfox-model-latest" }).map((row) => row.id)).toEqual(["worker-a", "worker-b"]);
  });
});

describe("Worker request pressure", () => {
  it("groups the labelled pressure meters semantically", () => {
    expect(workersPageSource).toContain('className="worker-request-strip"\n        role="group"\n        aria-label=');
  });

  it("formats zero capacity separately from unavailable capacity", () => {
    const formatWorkerPressure = (workerView as unknown as {
      formatWorkerPressure?: (label: string, current: number, max: number, available: boolean) => unknown;
    }).formatWorkerPressure;

    expect(formatWorkerPressure?.("QUEUE", 3, 0, true)).toEqual({
      limitText: "0",
      maximumText: "maximum 0",
      title: "QUEUE: 3 current requests; maximum 0"
    });
    expect(formatWorkerPressure?.("QUEUE", 3, 0, false)).toEqual({
      limitText: "—",
      maximumText: "maximum unavailable (—)",
      title: "QUEUE: 3 current requests; maximum unavailable (—)"
    });
  });

  it("passes explicit availability to both pressure meters", () => {
    expect(workersPageSource.match(/available=\{row\.live_capacity_available\}/g) ?? []).toHaveLength(2);
  });

  it("renders each maximum inline as quiet supporting text without a pressure bar", () => {
    expect(workersPageSource).toContain("<small>max {pressure.limitText}</small>");
    expect(workersPageSource).not.toContain('<i aria-hidden="true"');
  });
});
