# Worker Diagnostics Hover Design

## Goal

Make the Worker diagnostics question mark behave like a transient tooltip and
quietly signal an outdated Agent build without competing with Worker health.

## Interaction

- Replace the persistent `details` disclosure with a non-toggling tooltip
  trigger.
- Show diagnostics while the trigger area is hovered.
- Hide diagnostics as soon as the pointer leaves the trigger and tooltip area.
- Show the same tooltip when the trigger receives keyboard-visible focus, and
  hide it when focus moves away.
- Keep the existing Build, Commit, Heartbeat, and Scrape failures content.

## Version Signal

- A `current` Agent keeps the existing quiet gray-blue question mark.
- An `outdated` or `legacy` Agent uses a muted light brown-yellow background
  and darker brown-yellow glyph.
- Version signaling affects only the diagnostics trigger. Worker health remains
  represented by the card's green or red top edge.

## Implementation Shape

- Use a semantic button inside a positioned wrapper.
- Use CSS `:hover` and the button's `:focus-visible` sibling state to reveal the
  tooltip; do not store open state in React.
- Preserve the tooltip role and an accessible trigger label.
- Remove `details`, `summary`, and `[open]` styling so mouse clicks cannot leave
  the tooltip pinned open.

## Verification

- Add source-contract tests that fail while the persistent `details` behavior
  remains.
- Cover transient hover/focus selectors and the conditional version-warning
  class.
- Run the focused Worker UI tests, the complete admin UI tests, and the admin UI
  production build.
