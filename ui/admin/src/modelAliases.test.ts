import { describe, expect, it } from "vitest";
import { removeAlias, setAliasTarget, validateAliasDraft } from "./modelAliases";

describe("model aliases", () => {
  it("retargets immutably", () => {
    const source = { latest: "v1" };
    expect(setAliasTarget(source, "latest", "v2")).toEqual({ latest: "v2" });
    expect(source).toEqual({ latest: "v1" });
  });
  it("removes an alias", () => {
    expect(removeAlias({ latest: "v1", stable: "v1" }, "latest")).toEqual({ stable: "v1" });
  });
  it("validates names and targets", () => {
    expect(validateAliasDraft("", "v1", ["v1"], {})).toContain("required");
    expect(validateAliasDraft("v1", "v1", ["v1"], {})).toContain("collides");
    expect(validateAliasDraft("latest", "missing", ["v1"], {})).toContain("target");
  });
});
