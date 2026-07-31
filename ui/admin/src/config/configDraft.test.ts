import { describe, expect, it } from "vitest";

import type { ConfigResponse } from "../api";
import { renderDraftYAML, toEditableConfig } from "./configDraft";

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
});
