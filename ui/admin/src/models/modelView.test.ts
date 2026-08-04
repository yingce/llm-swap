// @ts-expect-error Vitest runs this source-contract test in Node; the admin app ships without Node types.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import type { ConfigResponse } from "../api";
import { modelArtifactTone, modelRuntimeLabel, modelRuntimeTone } from "../domain/modelRuntime";
import { createStatusFixture } from "../domain/testFixtures";
import { buildModelRows, findUnloadWorker, findWarmWorker, formatCompactNumber, modelCapacityLabel, modelTrafficLabel } from "./modelView";

const modelsPageSource = readFileSync(new URL("./ModelsPage.tsx", import.meta.url), "utf8");

const configResponse: ConfigResponse = {
  version: 7,
  yaml: "models: {}\n",
  config: {
    models: {
      "joyfox-model-latest": {
        priority: 10,
        min_loaded: 2,
        max_loaded: 3,
        max_concurrency: 8,
        max_queue: 16,
        queue_timeout_ms: 2000,
        ttl: 300,
        artifact: { object: "joyfox/latest.tar.gz", kind: "tar_gz", crc64ecma: "" },
        run: "python -m vllm.entrypoints.openai.api_server",
        runtime: "vllm",
        runtime_args: ["--served-model-name", "joyfox"]
      },
      "disabled-draft": {
        disabled: true,
        priority: 0,
        min_loaded: 0,
        max_loaded: 1,
        max_concurrency: 1,
        max_queue: 1,
        queue_timeout_ms: 1000,
        ttl: 60,
        artifact: { object: "disabled/model.gguf", kind: "file", crc64ecma: "" },
        run: "./server",
        runtime: "llamacpp",
        runtime_args: []
      }
    },
    model_aliases: {
      latest: "joyfox-model-latest"
    },
    tag_policies: {}
  }
};

describe("buildModelRows", () => {
  it("combines live status with config aliases and runtime without showing disabled drafts by default", () => {
    const rows = buildModelRows(createStatusFixture(), configResponse, { query: "", includeDisabled: false });

    expect(rows.map((row) => row.name)).toEqual(["joyfox-model-latest", "embedding-idle"]);
    expect(rows[0]).toMatchObject({
      name: "joyfox-model-latest",
      aliases: ["latest"],
      runtime: "vllm",
      disabled: false,
      ready_workers: 1
    });
  });

  it("can explicitly include disabled config-only models and search by alias or runtime", () => {
    expect(buildModelRows(createStatusFixture(), configResponse, { query: "", includeDisabled: true }).map((row) => row.name)).toContain("disabled-draft");
    expect(buildModelRows(createStatusFixture(), configResponse, { query: "latest", includeDisabled: false }).map((row) => row.name)).toEqual(["joyfox-model-latest"]);
    expect(buildModelRows(createStatusFixture(), configResponse, { query: "llamacpp", includeDisabled: true }).map((row) => row.name)).toEqual(["disabled-draft"]);
  });

  it("distinguishes ready artifacts from running model runtimes", () => {
    expect(modelArtifactTone("ready")).toBe("neutral");
    expect(modelRuntimeLabel("ready")).toBe("running");
    expect(modelRuntimeTone("ready")).toBe("good");
    expect(modelRuntimeLabel("loading")).toBe("loading");
    expect(modelRuntimeTone("loading")).toBe("warn");
    expect(modelRuntimeLabel(undefined)).toBe("not running");
    expect(modelRuntimeTone(undefined)).toBe("neutral");
    expect(modelsPageSource).toContain('className="model-replica-signals"');
    expect(modelsPageSource).toContain("artifact ${worker.artifact_status}");
    expect(modelsPageSource).toContain("runtime ${modelRuntimeLabel(worker.running_state)}");
    expect(modelsPageSource).toContain('worker.health !== "healthy"');
  });

  it("exposes compact live capacity and traffic labels", () => {
    const [row] = buildModelRows(createStatusFixture(), configResponse, { query: "joyfox", includeDisabled: false });

    expect(row.capacity).toEqual({ active: 3, max_active: 8, queued: 1, max_queue: 16 });
    expect(modelCapacityLabel(row)).toBe("3 active of 8; 1 queued of 16");
    expect(modelTrafficLabel(row)).toContain("12 requests");
    expect(modelTrafficLabel(row)).toContain("1.8K tokens");
    expect(formatCompactNumber(18_700_000)).toBe("18.7M");
  });

  it("warms only an idle ready artifact and unloads only a ready replica", () => {
    const workers = [
      { worker_id: "ready", artifact_status: "ready", running_state: "ready", health: "healthy", cooldown_active: false },
      { worker_id: "ready-idle", artifact_status: "ready", running_state: "ready", health: "healthy", cooldown_active: false },
      { worker_id: "starting", artifact_status: "ready", running_state: "starting", health: "healthy", cooldown_active: false },
      { worker_id: "idle", artifact_status: "ready", health: "healthy", cooldown_active: false },
      { worker_id: "stale-idle", artifact_status: "ready", health: "stale", cooldown_active: false }
    ];

    expect(findWarmWorker(workers)?.worker_id).toBe("idle");
    expect(findUnloadWorker(workers, { ready: 2, "ready-idle": 0 })?.worker_id).toBe("ready-idle");
  });
});
