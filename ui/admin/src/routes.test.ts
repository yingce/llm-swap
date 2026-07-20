import { describe, expect, it } from "vitest";

import { pathForTab, shouldPushTabPath, tabFromPath, type Tab } from "./routes";

describe("admin routes", () => {
  const routes: Array<[Tab, string]> = [
    ["dashboard", "/ui"],
    ["models", "/ui/models"],
    ["workers", "/ui/workers"],
    ["billing", "/ui/billing"],
    ["events", "/ui/event-log"],
    ["requests", "/ui/request-log"],
    ["configOps", "/ui/config"],
    ["advanced", "/ui/advanced"]
  ];

  it.each(routes)("maps %s to %s", (tab, path) => {
    expect(pathForTab(tab)).toBe(path);
  });

  it.each(routes)("maps %s back from %s", (tab, path) => {
    expect(tabFromPath(path)).toBe(tab);
  });

  it("maps an unknown path to the dashboard", () => {
    expect(tabFromPath("/ui/not-a-page")).toBe("dashboard");
  });

  it("only pushes history when the selected tab changes the path", () => {
    expect(shouldPushTabPath("/ui/models", "models")).toBe(false);
    expect(shouldPushTabPath("/ui", "models")).toBe(true);
  });
});
