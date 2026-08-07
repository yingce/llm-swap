// @ts-expect-error Vitest runs this source-contract test in Node; the admin app ships without Node types.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const source = readFileSync(new URL("./app/App.tsx", import.meta.url), "utf8");
const api = readFileSync(new URL("./api.ts", import.meta.url), "utf8");

describe("Promote service name workflow", () => {
  it("uses the dedicated audited endpoints instead of editing ordinary aliases", () => {
    expect(api).toContain("/ui/api/service-names/promote");
    expect(api).toContain("/ui/api/service-names/rollback");
    expect(source).toContain("Promote service name");
    expect(source).toContain("This archives the disabled canonical model");
    expect(source).toContain("The target is already ready");
    expect(source).toContain("Rollback removes the service alias and restores the archived canonical definition");
  });

  it("requires an explicit confirmation and keeps rollback tied to the archive identity", () => {
    expect(source).toContain('aria-label="Confirm service-name promotion"');
    expect(source).toContain("promotion.archive_id");
    expect(api).toContain("target_model: promotion.target_model");
  });
});
