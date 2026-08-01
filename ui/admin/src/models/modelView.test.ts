import { describe, expect, it } from "vitest";

import type { ConfigResponse } from "../api";
import { createStatusFixture } from "../domain/testFixtures";
import { buildModelRows, formatCompactNumber, modelCapacityLabel, modelTrafficLabel } from "./modelView";

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

  it("exposes compact live capacity and traffic labels", () => {
    const [row] = buildModelRows(createStatusFixture(), configResponse, { query: "joyfox", includeDisabled: false });

    expect(row.capacity).toEqual({ active: 3, max_active: 8, queued: 1, max_queue: 16 });
    expect(modelCapacityLabel(row)).toBe("3 active of 8; 1 queued of 16");
    expect(modelTrafficLabel(row)).toContain("12 requests");
    expect(modelTrafficLabel(row)).toContain("1.8K tokens");
    expect(formatCompactNumber(18_700_000)).toBe("18.7M");
  });
});
