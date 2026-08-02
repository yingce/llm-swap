// @ts-expect-error Vitest runs this source-contract test in Node; the admin app ships without Node types.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./app/App.tsx", import.meta.url), "utf8");
const shellSource = readFileSync(new URL("./app/AppShell.tsx", import.meta.url), "utf8");
const stylesSource = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
const normalizedStylesSource = stylesSource.replace(/\r\n/g, "\n");

describe("Config Ops model creation modal", () => {
  it("uses a reusable modal with constrained runtime and header disabled controls", () => {
    expect(source).toContain("function ModelCreateModal({");
    expect(source).toContain('role="dialog"');
    expect(source).toContain('aria-modal="true"');
    expect(source).toContain("isModelCreateDraftDirty(initialDraft, draft)");
    expect(source).toContain("MODEL_RUNTIME_OPTIONS.map");
    expect(source).toContain('className="model-disabled-toggle switch-control"');
  });

  it("manages focus inside a portaled modal while the complete app background is inert", () => {
    expect(source).toContain("canonicalNameInputRef.current?.focus()");
    expect(source).toContain("discardChoiceRef.current?.focus()");
    expect(source).toContain('event.key !== "Tab"');
    expect(source).toContain("modalFocusableElements(dialog)");
    expect(source).toContain("onKeyDown={handleKeyDown}");
    expect(source).toContain('import { createPortal } from "react-dom"');
    expect(source).toContain("appContentRef={appContentRef}");
    expect(shellSource).toContain('ref={appContentRef}');
    expect(source).toContain("appContentRef.current");
    expect(source).toContain("app.inert = Boolean(createDraft)");
    expect(source).toContain("createPortal(");
    expect(source).toContain("document.body");
    expect(source).toContain("createTriggerRef.current");
    expect(source).not.toContain('data-model-create-trigger="copy"');
    expect(source).not.toContain("Delete model");
    expect(source).not.toContain("onDeleteModel");
    expect(source).toContain('data-model-create-trigger="new"');
    expect(source).toContain("Copy YAML");
  });

  it("uses independent model config columns with a capacity rail and titled alias targets", () => {
    expect(source.match(/className="model-config-layout"/g) ?? []).toHaveLength(1);
    expect(source.match(/className="model-config-base"/g) ?? []).toHaveLength(1);
    expect(source.match(/className="model-config-column"/g) ?? []).toHaveLength(2);
    expect(source.match(/className="model-capacity-rail"/g) ?? []).toHaveLength(1);
    expect(source).toContain("title={aliasTarget}");
    expect(source).toContain("title={target}");
    expect(source).toContain('aria-label={`Target for alias ${alias}`}');
    expect(source).toContain('aria-label={`Remove alias ${alias}`}');
  });

  it("pins alias row order and readable worker pressure text in the stylesheet", () => {
    expect(normalizedStylesSource).toContain('grid-template-areas:\n    "alias alias"\n    "target target"\n    "status action";');
    expect(normalizedStylesSource).toContain('grid-template-areas:\n      "alias"\n      "target"\n      "status"\n      "action";');
    expect(normalizedStylesSource).toContain(".alias-row > strong {\n  grid-area: alias;");
    expect(normalizedStylesSource).toContain(".alias-row > select {\n  grid-area: target;");
    expect(normalizedStylesSource).toContain(".alias-row > button {\n  grid-area: action;");
    expect(normalizedStylesSource).toContain(".alias-status {\n  grid-area: status;");
    expect(normalizedStylesSource.match(/\.worker-pressure-meter (?:span|small) \{[^}]*color: var\(--muted\)/gs) ?? []).toHaveLength(2);
  });
});
