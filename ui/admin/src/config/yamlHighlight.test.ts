import { describe, expect, it } from "vitest";

import { tokenizeYAML } from "./yamlHighlight";

describe("tokenizeYAML", () => {
  it("classifies yaml keys, strings, comments, and plain values", () => {
    expect(tokenizeYAML('models:\n  qwen: "ready" # note')).toEqual([
      [
        { text: "models:", kind: "key" }
      ],
      [
        { text: "  ", kind: "space" },
        { text: "qwen:", kind: "key" },
        { text: " ", kind: "space" },
        { text: '"ready"', kind: "string" },
        { text: " ", kind: "space" },
        { text: "# note", kind: "comment" }
      ]
    ]);
  });
});
