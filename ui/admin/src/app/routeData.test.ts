import { describe, expect, it } from "vitest";

import { appendRoutePage, routeDataKeysForTab } from "./routeData";

describe("route data policy", () => {
  it("keeps the overview status-only and lazy-loads route-specific data", () => {
    expect(routeDataKeysForTab("dashboard")).toEqual([]);
    expect(routeDataKeysForTab("models")).toEqual([]);
    expect(routeDataKeysForTab("workers")).toEqual([]);
    expect(routeDataKeysForTab("events")).toEqual(["events"]);
    expect(routeDataKeysForTab("requests")).toEqual(["requests"]);
    expect(routeDataKeysForTab("billing")).toEqual(["billing", "config"]);
    expect(routeDataKeysForTab("configOps")).toEqual(["config"]);
    expect(routeDataKeysForTab("advanced")).toEqual(["config"]);
  });

  it("retains loaded data while appending paged route data", () => {
    expect(appendRoutePage(["old"], ["next"], 10)).toEqual(["old", "next"]);
    expect(appendRoutePage(["old"], ["fresh"], 0)).toEqual(["fresh"]);
  });
});
