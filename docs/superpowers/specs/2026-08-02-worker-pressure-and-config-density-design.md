# Worker Pressure and Config Density Design

## Goal

Make the worker fleet denser without hiding operational limits, and remove the
large empty gaps in Config Ops while keeping model-specific capacity ownership
clear.

## Worker cards

- Limit desktop worker cards to roughly 240-280 px and keep a bounded grid so a
  small worker count does not stretch cards across the page.
- Show request pressure as `current / max` for both executing requests and the
  queue. Use compact labels and a low-contrast fill indicator instead of large
  metric blocks.
- Derive the displayed limits from every ready model replica on the worker and
  that model's capacity for the worker's selected tag. Sum limits only when a
  worker reports multiple distinct ready models.
- Do not use the heartbeat's legacy machine-level `capacity` as the displayed
  limit. It remains a compatibility field, while serving limits belong to the
  model and tag pair.
- If a worker has no ready model, expose a zero live limit and render an em dash
  rather than suggesting that the worker can currently accept requests.

## Config Ops layout

- Widen the left navigation column to roughly 360 px.
- Give alias targets a full row in the alias card. Keep controls shrink-safe and
  expose the full selected target through the native title tooltip.
- Replace the shared two-column fieldset grid with independent column flows:
  one base column for identity, replica policy, and placement; another for
  runtime and artifact configuration. This prevents a tall runtime fieldset
  from creating an empty grid row beneath the shorter identity fieldset.
- Place Runnable Tags and Capacity in a compact right rail, approximately
  250-280 px wide. Each selected tag keeps only its checkbox and the two
  per-worker model limits.
- Collapse the capacity rail below the base configuration on narrower screens;
  retain the existing single-column mobile form behavior.

## API and compatibility

The worker UI response gains `max_concurrency` and `max_queue`. Existing fields
remain unchanged. The frontend treats absent fields as zero so a newer UI can
still render during a rolling gateway upgrade.

## Verification

- Add a Go regression test proving worker limits use model/tag capacity instead
  of legacy worker capacity and aggregate distinct ready models correctly.
- Add frontend view-model tests for the new fields and older API responses.
- Run gateway tests, frontend tests, the production frontend build, and the full
  Go test suite before considering the change complete.
