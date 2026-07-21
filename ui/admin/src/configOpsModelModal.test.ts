// @ts-expect-error Vitest runs this source-contract test in Node; the admin app ships without Node types.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./main.tsx", import.meta.url), "utf8");

describe("Config Ops model creation modal", () => {
  it("uses a reusable modal with constrained runtime and header disabled controls", () => {
    expect(source).toContain("function ModelCreateModal({");
    expect(source).toContain('role="dialog"');
    expect(source).toContain('aria-modal="true"');
    expect(source).toContain("isModelCreateDraftDirty(initialDraft, draft)");
    expect(source).toContain("MODEL_RUNTIME_OPTIONS.map");
    expect(source).toContain('className="model-disabled-toggle"');
  });

  it("manages focus inside the modal and keeps Config Ops inert while it is open", () => {
    expect(source).toContain("canonicalNameInputRef.current?.focus()");
    expect(source).toContain("discardChoiceRef.current?.focus()");
    expect(source).toContain('event.key !== "Tab"');
    expect(source).toContain("modalFocusableElements(dialog)");
    expect(source).toContain("onKeyDown={handleKeyDown}");
    expect(source).toContain("configOpsContentRef.current");
    expect(source).toContain("content.inert = Boolean(createMode)");
    expect(source).toContain("createTriggerRef.current");
    expect(source).toContain("data-model-create-trigger");
  });
});
