import { describe, expect, it } from "vitest";
import type { EditableModelConfig } from "./modelLifecycle";
import {
  copyEditableModel,
  emptyEditableModel,
  modelDeleteBlockers,
  setModelTagMembership,
  validateNewModelName
} from "./modelLifecycle";

const sourceModel: EditableModelConfig = {
  disabled: false,
  model_dir: "/opt/llmswap/models/v1",
  priority: 7,
  min_loaded: 2,
  max_loaded: 4,
  max_loaded_auto: false,
  max_concurrency: 3,
  max_queue: 9,
  queue_timeout_ms: 1200,
  ttl: 3600,
  artifact: { object: "v1.tar.gz", kind: "tar_gz", crc64ecma: "abc123" },
  run: "llama-server",
  runtime: "llamacpp",
  runtime_args: ["--ctx-size", "8192"],
  cmd_stop: "killall llama-server",
  check_endpoint: "/health",
  billing: { per_request_usd: 0.1, input_per_million_usd: 1.2, output_per_million_usd: 2.3, cached_input_per_million_usd: 0.4 }
};

const tags = {
  "gpu-a": {
    max_concurrency: 4,
    max_queue: 8,
    worker_defaults: { max_concurrency: 2, max_queue: 4 },
    allowed_models: ["v1"],
    warm_when_idle: "v1"
  },
  "gpu-b": {
    max_concurrency: 2,
    max_queue: 4,
    worker_defaults: { max_concurrency: 1, max_queue: 2 },
    allowed_models: ["v3", "v2", "v2"],
    warm_when_idle: ""
  }
};

const workers = [{
  id: "worker-a",
  tags: ["gpu-a"],
  health: "healthy",
  state: "ready",
  llama_swap_url: "http://worker-a:6006",
  active_requests: 0,
  running_models: [{ model: "v1", state: "ready" }, { model: "v2", state: "loading" }],
  gpu_devices: [],
  allowed_models: ["v1", "v2"],
  capacity: { max_concurrency: 2, max_queue: 4 },
  scrape_failures: 0,
  agent_build: {},
  agent_version_status: "current" as const
}];

describe("model lifecycle drafts", () => {
  it("creates blank drafts with safe defaults", () => {
    expect(emptyEditableModel()).toMatchObject({
      disabled: true,
      min_loaded: 0,
      max_loaded_auto: true,
      artifact: { object: "", kind: "tar_gz", crc64ecma: "" },
      runtime_args: []
    });
  });

  it("copies model drafts without mutating nested source values", () => {
    const copied = copyEditableModel(sourceModel);
    expect(copied).toMatchObject({ disabled: true, min_loaded: 0, model_dir: sourceModel.model_dir });
    copied.artifact.object = "other.tar.gz";
    copied.runtime_args.push("--new");
    expect(sourceModel.artifact.object).not.toBe("other.tar.gz");
    expect(sourceModel.runtime_args).not.toContain("--new");
  });

  it("validates name collisions after trimming", () => {
    expect(validateNewModelName(" latest ", { v1: sourceModel }, { latest: "v1" })).toContain("alias");
    expect(validateNewModelName("v1", { v1: sourceModel }, {})).toContain("exists");
  });

  it("synchronizes model tag membership immutably", () => {
    const updated = setModelTagMembership(tags, "v2", ["gpu-a"]);
    expect(updated["gpu-a"].allowed_models).toEqual(["v1", "v2"]);
    expect(updated["gpu-b"].allowed_models).toEqual(["v3"]);
    expect(tags["gpu-b"].allowed_models).toEqual(["v3", "v2", "v2"]);
  });

  it("lists all blockers before deleting a model", () => {
    expect(modelDeleteBlockers("v1", { latest: "v1" }, tags, workers)).toEqual({
      aliases: ["latest"], tags: ["gpu-a"], running: [{ workerID: "worker-a", state: "ready" }]
    });
  });
});
