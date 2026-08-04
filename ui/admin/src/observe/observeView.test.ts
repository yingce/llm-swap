import { describe, expect, it } from "vitest";

import type { RequestLogEntry, WorkerEvent } from "../api";
import { buildEventRows, buildRequestRows, classifyBillingError, recommendedBillingPricing } from "./observeView";

describe("observe view model", () => {
  it("classifies disabled billing storage as neutral without hiding auth or network failures", () => {
    expect(classifyBillingError("Billing unavailable: records store is not enabled")).toEqual({
      tone: "neutral",
      title: "Billing records are disabled",
      detail: "records store is not enabled"
    });
    expect(classifyBillingError("unauthorized")).toMatchObject({ tone: "bad" });
    expect(classifyBillingError("fetch failed")).toMatchObject({ tone: "bad" });
  });

  it("normalizes request and event rows for dense observe tables", () => {
    const request: RequestLogEntry = {
      time: "2026-07-31T09:00:00Z",
      request_id: "req-1",
      model: "qwen",
      worker_id: "worker-a",
      tag: "gpu-4090",
      status_code: 200,
      duration_ms: 1200,
      stream: true,
      request_bytes: 10,
      response_bytes: 20,
      prompt_tokens: 100,
      completion_tokens: 50,
      total_tokens: 150
    };
    const event: WorkerEvent = {
      received_at: "2026-07-31T09:00:01Z",
      worker_id: "worker-a",
      event: "artifact_state",
      model: "qwen",
      from_state: "missing",
      to_state: "ready"
    };

    expect(buildRequestRows([request])[0]).toMatchObject({
      id: "req-1",
      route: "qwen · worker-a · gpu-4090",
      tokens: "150"
    });
    expect(buildEventRows([event])[0]).toMatchObject({
      id: "worker-a:2026-07-31T09:00:01Z:artifact_state",
      subject: "qwen · worker-a",
      detail: "missing -> ready"
    });
  });

  it("derives alternative pricing recommendations from selected-range runtime and token usage", () => {
    const recommended = recommendedBillingPricing({
      model: "qwen",
      ready_seconds: 3600,
      billable_worker_seconds: 3600,
      request_duration_seconds: 3600,
      ready_share: 1,
      cost_share: 1,
      model_cost: 1,
      model_used_cost: 1,
      model_idle_cost: 0,
      requests: 2,
      input_tokens: 100,
      output_tokens: 20,
      cached_input_tokens: 20,
      total_tokens: 120
    }, 24);

    expect(recommended).toEqual({
      per_request_usd: 0.5,
      input_per_million_usd: 12500,
      output_per_million_usd: 50000,
      cached_input_per_million_usd: 50000
    });
  });
});
