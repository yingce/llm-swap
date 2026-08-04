// @ts-expect-error Vitest runs this source-contract test in Node; the admin app ships without Node types.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { createStatusFixture } from "../domain/testFixtures";
import { buildWorkerFilters, buildWorkerRows, workerHasDiagnosticWarning } from "./workerView";
import * as workerView from "./workerView";

const workersPageSource = readFileSync(new URL("./WorkersPage.tsx", import.meta.url), "utf8");
const stylesSource = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

describe("buildWorkerRows", () => {
  it("summarizes worker GPU, model, and connectivity fields without FRP details", () => {
    const rows = buildWorkerRows(createStatusFixture(), { query: "" });

    expect(rows.map((row) => row.id)).toEqual(["worker-a", "worker-b", "worker-c"]);
    expect(rows[0]).toMatchObject({
      id: "worker-a",
      tags: ["gpu-4090"],
      gpu_count: 1,
      loaded_models: ["joyfox-model-latest:ready"],
      running_models: [{ model: "joyfox-model-latest", state: "ready" }],
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
    expect(workersPageSource).not.toContain("worker-pressure-bar");
  });
});

describe("Worker diagnostics", () => {
  it("uses a transient button tooltip instead of a persistent disclosure", () => {
    expect(workersPageSource).toContain('className="worker-diagnostics-trigger"');
    expect(workersPageSource).toContain('type="button"');
    expect(workersPageSource).toContain('className="worker-diagnostics-popover"');
    expect(workersPageSource).toContain('role="tooltip"');
    expect(workersPageSource).toContain("Build");
    expect(workersPageSource).toContain("Commit");
    expect(workersPageSource).toContain("Heartbeat");
    expect(workersPageSource).toContain("Scrape failures");
    expect(workersPageSource).not.toContain("<details");
    expect(workersPageSource).not.toContain("<summary");
    expect(workersPageSource).not.toContain('className="worker-diagnostics" tabIndex={0} title={diagnostics}');
  });

  it("reveals diagnostics only for pointer hover or keyboard-visible focus", () => {
    expect(stylesSource).toContain(".worker-diagnostics:hover .worker-diagnostics-popover");
    expect(stylesSource).toContain(".worker-diagnostics-trigger:focus-visible + .worker-diagnostics-popover");
    expect(stylesSource).not.toContain(".worker-diagnostics[open]");
  });

  it("adds a quiet warning treatment when the agent build is not current", () => {
    expect(workersPageSource).toContain('diagnosticsWarning ? " diagnostic-warning" : ""');
    expect(stylesSource).toContain(".worker-diagnostics.diagnostic-warning .worker-diagnostics-trigger");
    expect(stylesSource).toMatch(/\.worker-diagnostics\.diagnostic-warning \.worker-diagnostics-trigger \{[^}]*background: transparent;/s);
  });

  it("warns for stale builds or reported diagnostics without changing healthy status color", () => {
    const status = createStatusFixture();
    expect(workerHasDiagnosticWarning(status.workers[0])).toBe(false);
    expect(workerHasDiagnosticWarning({ ...status.workers[0], last_error: "scrape failed" })).toBe(true);
    expect(workerHasDiagnosticWarning(status.workers[2])).toBe(true);
  });

  it("keeps request pressure and model state in separate compact rows", () => {
    const pressureIndex = workersPageSource.indexOf('className="worker-request-strip"');
    const modelIndex = workersPageSource.indexOf('className="worker-model-board"');
    const gpuIndex = workersPageSource.indexOf('className="worker-gpu-deck"');
    expect(pressureIndex).toBeGreaterThan(-1);
    expect(modelIndex).toBeGreaterThan(pressureIndex);
    expect(gpuIndex).toBeGreaterThan(modelIndex);
    expect(workersPageSource.slice(pressureIndex, modelIndex)).not.toContain("worker-model-state-list");
    expect(workersPageSource.slice(modelIndex, gpuIndex)).toContain("worker-model-state-list");
    expect(workersPageSource).toContain("model-state-${modelStateTone(model.state)}");
    expect(stylesSource).toContain("grid-template-columns: repeat(2, minmax(0, 1fr));");
  });
});
