import { describe, expect, it, vi } from "vitest";

import { emptyEditableModel } from "../modelLifecycle";
import { BillingPage } from "./ObservePages";

describe("BillingPage", () => {
  it("renders editable manual pricing for every configured model", () => {
    const element = BillingPage({
      billing: {
        start: "2026-08-01T00:00:00Z",
        end: "2026-08-02T00:00:00Z",
        currency: "USD",
        exchange_rate_cny_to_usd: 0.14,
        exchange_rate_stale: false,
        worker_day_cost_rmb: 55,
        worker_day_cost_usd: 7.7,
        totals: {
          ready_seconds: 3600,
          billable_worker_seconds: 3600,
          request_duration_seconds: 20,
          model_cost: 1,
          model_used_cost: 0.5,
          model_idle_cost: 0.5,
          requests: 2,
          input_tokens: 100,
          output_tokens: 20,
          cached_input_tokens: 10,
          total_tokens: 120
        },
        models: [],
        apps: []
      },
      rangeHours: 24,
      error: "",
      pricingDraft: {
        models: {
          "joyfox-model": {
            ...emptyEditableModel(),
            disabled: false,
            billing: {
              per_request_usd: 0.123456,
              input_per_million_usd: 0.35,
              output_per_million_usd: 1.5,
              cached_input_per_million_usd: 0.08
            }
          }
        }
      },
      pricingDirty: false,
      pricingMessage: "",
      pricingError: "",
      onRangeChange: vi.fn(),
      onPriceChange: vi.fn(),
      onSavePricing: vi.fn()
    } as any);

    const rendered = JSON.stringify(element);
    expect(rendered).toContain("Manual model pricing");
    expect(rendered).toContain("Per request");
    expect(rendered).toContain("Input / 1M");
    expect(rendered).toContain("0.123456");
    expect(rendered).toContain("joyfox-model");
  });
});
