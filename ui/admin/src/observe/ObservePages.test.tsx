import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";

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
        group_by: "canonical",
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
        models: [{
          model: "joyfox-model",
          cost_basis: "actual",
          ready_seconds: 3600,
          billable_worker_seconds: 3600,
          request_duration_seconds: 20,
          ready_share: 1,
          cost_share: 1,
          model_cost: 0.1,
          model_used_cost: 0.1,
          model_idle_cost: 0,
          requests: 2,
          input_tokens: 100,
          output_tokens: 20,
          cached_input_tokens: 10,
          total_tokens: 120
        }],
        apps: []
      },
      groupBy: "canonical",
      loading: false,
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
      onGroupByChange: vi.fn(),
      onRangeChange: vi.fn(),
      onPriceChange: vi.fn(),
      onSavePricing: vi.fn()
    } as any);

    const rendered = renderToStaticMarkup(element);
    expect(rendered).toContain("Manual model pricing");
    expect(rendered).toContain("Per request");
    expect(rendered).toContain("Input / 1M");
    expect(rendered).toContain("0.123456");
    expect(rendered).toContain("joyfox-model");
    expect(rendered).toContain("Use $");
    expect(rendered).toContain("selected range");
    expect(rendered).toContain("Actual model cost");
    expect(rendered).toContain("Uncovered occupancy");
    expect(rendered).toContain("Negative values mean configured usage revenue exceeds occupancy cost");
    expect(rendered).not.toContain("Allocated model cost");
    expect(rendered).not.toContain("Actual idle cost");
  });

  it("presents alias allocations without treating unattributed traffic as an alias", () => {
    const element = BillingPage({
      billing: {
        start: "2026-08-01T00:00:00Z",
        end: "2026-08-02T00:00:00Z",
        currency: "USD",
        exchange_rate_cny_to_usd: 0.14,
        exchange_rate_stale: false,
        worker_day_cost_rmb: 55,
        worker_day_cost_usd: 7.7,
        group_by: "alias",
        totals: {
          ready_seconds: 3600,
          billable_worker_seconds: 3600,
          request_duration_seconds: 20,
          model_cost: 1,
          model_used_cost: 1.5,
          model_idle_cost: -0.5,
          requests: 3,
          input_tokens: 150,
          output_tokens: 30,
          cached_input_tokens: 10,
          total_tokens: 180
        },
        models: [
          {
            model: "A-Pro",
            group_kind: "alias",
            cost_basis: "allocated",
            ready_seconds: 2700,
            billable_worker_seconds: 2700,
            request_duration_seconds: 15,
            ready_share: 0.75,
            cost_share: 0.75,
            model_cost: 0.75,
            model_used_cost: 1.1,
            model_idle_cost: -0.35,
            requests: 2,
            input_tokens: 100,
            output_tokens: 20,
            cached_input_tokens: 10,
            total_tokens: 120,
            canonical_versions: [
              { canonical_model: "A-Pro-0808", requests: 1, input_tokens: 40, output_tokens: 10, cached_input_tokens: 0, total_tokens: 50, model_used_cost: 0.3, allocated_model_cost: 0.25 },
              { canonical_model: "A-Pro-0901", requests: 1, input_tokens: 60, output_tokens: 10, cached_input_tokens: 10, total_tokens: 70, model_used_cost: 0.8, allocated_model_cost: 0.5 }
            ]
          },
          {
            model: "",
            group_kind: "unattributed",
            cost_basis: "allocated",
            ready_seconds: 900,
            billable_worker_seconds: 900,
            request_duration_seconds: 5,
            ready_share: 0.25,
            cost_share: 0.25,
            model_cost: 0.25,
            model_used_cost: 0.4,
            model_idle_cost: -0.15,
            requests: 1,
            input_tokens: 50,
            output_tokens: 10,
            cached_input_tokens: 0,
            total_tokens: 60,
            canonical_versions: [{ canonical_model: "A-Pro-0901", requests: 1, input_tokens: 50, output_tokens: 10, cached_input_tokens: 0, total_tokens: 60, model_used_cost: 0.4, allocated_model_cost: 0.25 }]
          }
        ],
        apps: []
      },
      groupBy: "alias",
      loading: false,
      rangeHours: 24,
      error: "",
      pricingDraft: {
        models: {
          "A-Pro": {
            ...emptyEditableModel(),
            disabled: false,
            billing: { per_request_usd: 0.1 }
          }
        }
      },
      pricingDirty: false,
      pricingMessage: "",
      pricingError: "",
      onGroupByChange: vi.fn(),
      onRangeChange: vi.fn(),
      onPriceChange: vi.fn(),
      onSavePricing: vi.fn()
    } as any);

    const rendered = renderToStaticMarkup(element);
    expect(rendered).toContain("Actual models");
    expect(rendered).toContain("Service aliases");
    expect(rendered).toContain("Allocated model cost");
    expect(rendered).toContain("Allocated occupancy gap");
    expect(rendered).toContain("-$0.50");
    expect(rendered).toContain("Negative values mean configured usage revenue exceeds occupancy cost");
    expect(rendered).not.toContain("Allocated idle cost");
    expect(rendered).toContain("A-Pro-0808");
    expect(rendered).toContain("A-Pro-0901");
    expect(rendered).toContain("Direct / historic traffic");
    expect(rendered).toContain("Unattributed");
    expect(rendered).not.toContain("Alias: Direct / historic traffic");
    expect(rendered).toContain("Switch to Actual models for compute-cost pricing suggestions");
    expect(rendered).not.toContain("Use $");
  });

  it("keeps the selected alias identity visible while its data loads", () => {
    const rendered = renderToStaticMarkup(BillingPage({
      billing: null,
      groupBy: "alias",
      loading: true,
      rangeHours: 24,
      error: "",
      pricingDraft: null,
      pricingDirty: false,
      pricingMessage: "",
      pricingError: "",
      onGroupByChange: vi.fn(),
      onRangeChange: vi.fn(),
      onPriceChange: vi.fn(),
      onSavePricing: vi.fn()
    }));

    expect(rendered).toContain("Loading service alias allocations");
    expect(rendered).toContain('aria-pressed="true">Service aliases');
    expect(rendered).not.toContain("Actual model cost");
  });

  it("distinguishes a failed billing load from an empty selected range", () => {
    const base = {
      groupBy: "alias" as const,
      loading: false,
      rangeHours: 24,
      pricingDraft: null,
      pricingDirty: false,
      pricingMessage: "",
      pricingError: "",
      onGroupByChange: vi.fn(),
      onRangeChange: vi.fn(),
      onPriceChange: vi.fn(),
      onSavePricing: vi.fn()
    };
    const failed = renderToStaticMarkup(BillingPage({ ...base, billing: null, error: "fetch failed" }));
    const empty = renderToStaticMarkup(BillingPage({
      ...base,
      error: "",
      billing: { group_by: "alias", models: [], apps: [] } as any
    }));

    expect(failed).toContain("Billing could not load");
    expect(failed).toContain("fetch failed");
    expect(failed).not.toContain("No billing activity");
    expect(empty).toContain("No billing activity");
    expect(empty).toContain("No service alias or direct request costs were recorded");
  });
});
