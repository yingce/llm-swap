import { describe, expect, it } from "vitest";

import { NAV_GROUPS, pathForTab, shouldPushTabPath, tabFromPath, type Tab } from "./navigation";

describe("admin navigation model", () => {
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

  it.each(routes)("preserves the %s route at %s", (tab, path) => {
    expect(pathForTab(tab)).toBe(path);
    expect(tabFromPath(path)).toBe(tab);
  });

  it("keeps route groups separate from stable tab ids", () => {
    expect(NAV_GROUPS.map((group) => group.label)).toEqual(["Signal", "Fleet", "Observe", "Configure"]);
    expect(NAV_GROUPS.flatMap((group) => group.items.map((item) => item.id))).toEqual([
      "dashboard",
      "models",
      "workers",
      "billing",
      "events",
      "requests",
      "configOps",
      "advanced"
    ]);
  });

  it("keeps a refreshed deep route selected instead of forcing the dashboard", () => {
    expect(tabFromPath("/ui/models")).toBe("models");
    expect(shouldPushTabPath("/ui/models", "models")).toBe(false);
  });
});
