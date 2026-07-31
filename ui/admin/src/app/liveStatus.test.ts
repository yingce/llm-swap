import { describe, expect, it } from "vitest";

import { STATUS_REFRESH_INTERVAL_MS, reduceLiveStatus } from "./liveStatus";
import { createStatusFixture } from "../domain/testFixtures";

describe("live status state", () => {
  it("replaces status and clears the banner on successful refresh", () => {
    const next = createStatusFixture();

    expect(reduceLiveStatus({ status: null, error: "old" }, { type: "success", status: next })).toEqual({
      status: next,
      error: ""
    });
  });

  it("retains stale status data when a refresh fails", () => {
    const previous = createStatusFixture();

    expect(reduceLiveStatus({ status: previous, error: "" }, { type: "failure", error: "network down" })).toEqual({
      status: previous,
      error: "network down"
    });
  });

  it("keeps the existing five second polling cadence", () => {
    expect(STATUS_REFRESH_INTERVAL_MS).toBe(5000);
  });
});
