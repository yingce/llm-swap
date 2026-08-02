import { describe, expect, it } from "vitest";

import type { ConfigResponse } from "../api";
import { hasLegacyCapacityCeiling, renderDraftYAML, toEditableConfig } from "./configDraft";

const response: ConfigResponse = {
  version: 1,
  yaml: [
    "models:",
    "  qwen:",
    "    priority: 1",
    "    min_loaded: 0",
    "    max_concurrency: 2",
    "    max_queue: 4",
    "    queue_timeout_ms: 1000",
    "    ttl: 60",
    "    artifact:",
    "      object: qwen.gguf",
    "      kind: file",
    "      crc64ecma: \"\"",
    "    run: ./server",
    "tag_policies: {}",
    ""
  ].join("\n"),
  config: {
    models: {
      qwen: {
        priority: 1,
        min_loaded: 0,
        max_loaded: 0,
        max_concurrency: 2,
        max_queue: 4,
        queue_timeout_ms: 1000,
        ttl: 60,
        artifact: { object: "qwen.gguf", kind: "file", crc64ecma: "" },
        run: "./server",
        runtime_args: []
      }
    },
    model_aliases: {},
    tag_policies: {}
  }
};

describe("config draft YAML rendering", () => {
  it("preserves omitted max_loaded as auto and omits empty model_aliases", () => {
    const draft = toEditableConfig(response);
    const rendered = renderDraftYAML(response.yaml, draft);

    expect(draft.models.qwen.max_loaded_auto).toBe(true);
    expect(rendered).not.toContain("max_loaded:");
    expect(rendered).not.toContain("model_aliases:");
  });

  it("recognizes legacy global capacity ceilings without treating worker defaults as legacy", () => {
    expect(hasLegacyCapacityCeiling(2, 0)).toBe(true);
    expect(hasLegacyCapacityCeiling(0, 4)).toBe(true);
    expect(hasLegacyCapacityCeiling(0, 0)).toBe(false);
  });

  it("round-trips model tag capacity without emitting an empty map", () => {
    const withCapacity = structuredClone(response);
    withCapacity.config.models.qwen.tag_capacity = {
      "gpu-4090": { max_concurrency: 2, max_queue: 3 }
    };
    withCapacity.config.tag_policies = {
      "gpu-4090": {
        max_concurrency: 0,
        max_queue: 0,
        worker_defaults: { max_concurrency: 0, max_queue: 0 },
        allowed_models: ["qwen"],
        warm_when_idle: ""
      }
    };
    const draft = toEditableConfig(withCapacity);
    const rendered = renderDraftYAML(withCapacity.yaml, draft);

    expect(rendered).toContain("tag_capacity:");
    expect(rendered).toContain("gpu-4090:");
    expect(rendered).toContain("max_concurrency: 2");
    expect(renderDraftYAML(response.yaml, toEditableConfig(response))).not.toContain("tag_capacity:");
  });
});
