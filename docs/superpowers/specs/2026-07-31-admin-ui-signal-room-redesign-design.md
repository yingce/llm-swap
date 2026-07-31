# LLM Swap Admin UI Signal Room Redesign

Status: approved through interactive design review on 2026-07-31.

## Context

The current React admin UI exposes the required gateway capabilities, but it
presents most information at equal visual weight. Six global summary cards
remain visible on every route, the navigation is a flat list, model and worker
state are distributed across tables and cards, and the large
`ui/admin/src/main.tsx` file mixes application state, page composition,
configuration editing, operational actions, and formatting helpers.

The redesign serves one internal technical audience whose members routinely
move from fleet observation through incident diagnosis, model rollout, and
configuration. Their first question on entry is whether the serving plane is
healthy and what needs attention.

## Goals

- Make fleet health and actionable exceptions understandable before aggregate
  traffic or configuration detail.
- Keep observation, diagnosis, and action connected around the same Tag,
  Worker, GPU, Model, Request, or change.
- Preserve all existing UI routes, gateway API contracts, authentication, and
  model/configuration behavior.
- Make the UI useful for sustained desktop operation while retaining a usable
  responsive fallback.
- Separate page composition, derived state, and shared interaction primitives
  so future UI changes do not deepen the current single-file structure.
- Degrade optional records and metrics capabilities honestly without implying
  that the serving plane is unavailable.

## Non-goals

- Adding new Gateway or Agent APIs in the first redesign implementation.
- Moving scheduling, queueing, replica policy, or request ownership out of the
  Gateway.
- Exposing FRP as a first-class product resource.
- Adding user roles, a new authentication system, or external UI dependencies.
- Replacing existing model creation, alias, Tag Policy, dry-run, or apply
  semantics.

## Confirmed Product Decisions

- The primary audience is one internal technical team that performs the full
  configuration-to-observation workflow.
- The landing page prioritizes fleet health and problems requiring action.
- Low-risk common actions may execute directly. Higher-risk actions expose
  their scope and require confirmation.
- The selected information architecture is the exception-first approach.
- The selected visual world is `Signal Room Console`.
- The selected landing-page composition is `Exception Rail` (composition A).

## Direction Contract

**THESIS:** An exception-first signal room that makes serving relationships and
operator consequences legible; it refuses the equal-weight metric-card
dashboard.

**OWN-WORLD:** Matte pale task surfaces, a slate navigation rail, orthogonal
relationship lines, compact annunciator strips, Route Teal selection, and
semantic green, amber, and red signals.

**STORY:** The operator learns whether serving is healthy, selects what needs
attention, traces the affected resources, and performs or verifies the next
safe action.

**FIRST VIEWPORT:** Navigation at left; fleet conclusion and freshness at top;
the highest-priority exception spans the workspace; its relationship view and
supporting fleet values follow; resource rows continue below.

**FORM:** Exception Rail, the selected first-ranked operational composition;
the attention strip controls the current relationship trace while dense tables
remain conventional. Direction seed: `e697a267`.

## Application Shell and Navigation

The application uses a persistent desktop navigation rail and a fluid working
area. The navigation is grouped by operator intent:

1. `Overview`
2. Resources: `Models`, `Workers`
3. Observe: `Requests`, `Activity`, `Billing`
4. Change: `Configuration`, `Advanced`

Labels change without changing current route contracts:

| Label | Existing route |
| --- | --- |
| Overview | `/ui` |
| Models | `/ui/models` |
| Workers | `/ui/workers` |
| Billing | `/ui/billing` |
| Activity | `/ui/event-log` |
| Requests | `/ui/request-log` |
| Configuration | `/ui/config` |
| Advanced | `/ui/advanced` |

Refresh, direct route loads, back/forward navigation, and active navigation
state retain the current History API behavior. The shell header contains only
Gateway connectivity, the last successful state time, refresh progress, and a
manual refresh action. The current six global summary cards move into
`Overview`; they do not remain above every route.

## Overview: Exception Rail

### Fleet conclusion

The first line states one conclusion in plain language, for example:

- `Serving plane operational`
- `Serving plane degraded · 2 items need attention`
- `Gateway state unavailable · showing data from 14:32:05`

The conclusion combines text and a semantic state indicator. It is not derived
from the availability of optional Billing or historical metrics services.

### Attention queue

Attention items are derived from existing status, model, event, request, and
configuration data. Initial categories are:

1. Worker stale or unhealthy.
2. Worker reports a health problem, last error, or required restart.
3. Model has no ready replica, an artifact error, or is below a positive
   `min_loaded` target.
4. Agent version is outdated or legacy.
5. Recent request or worker events contain an error.
6. A configuration change was saved but requires a Gateway restart.

The order above is the default severity order. Within a category, newer
evidence sorts first, then stable resource identity provides deterministic
ordering. A disabled model and a deliberately zero-warm model are not problems
solely because they have zero ready replicas. Optional records or metrics
storage being disabled creates a capability notice, not an attention item.

Each item includes the affected resource, concise reason, evidence age, and one
`Inspect` action. The queue never invents diagnostic conclusions unsupported by
the existing API.

### Relationship view

The Overview relationship view is not a literal whole-cluster graph.

- Default state: summarize the whole cluster by Tag. Each Tag group reports
  healthy/total Workers, GPU capacity, available/configured Models, active
  requests, and attention count.
- Selected Tag: expand Workers and relevant Models for that Tag while keeping
  the Tag summary visible.
- Selected attention item: show only the concrete affected relationships
  supported by current data. The normal shape is `Tag → Worker`, branching to
  that Worker's reported GPU devices and affected or running Models. The UI
  must not link one Model to one specific GPU unless a future API reports that
  mapping.

FRP is not a persistent node. The default relationship is logical Gateway to
Worker connectivity. A transport-related failure may mark that connection as
degraded, while low-level FRP details remain absent until an explicit
diagnostic API exists. This prevents the product model from coupling to one
transport implementation.

Relationship lines must represent real data. Decorative network lines, guessed
dependencies, and hidden unlabeled nodes are prohibited.

### Supporting state

Compact supporting values include healthy Workers, available Models, active
requests, and aggregate GPU memory. They explain scale but remain subordinate
to the conclusion and attention queue. A concise model/resource table follows
the first viewport and links to the relevant resource page.

## Models Page

Models uses a searchable master-detail layout.

The resource list displays canonical identity, availability, ready/running
replicas, stable aliases, runtime, and traffic. Disabled Models can be included
through an explicit filter and remain visually quiet rather than consuming a
full-width disabled treatment.

The selected Model detail keeps its identity visible while presenting:

- availability and replica state;
- aliases targeting the canonical Model;
- eligible Worker state and artifact readiness;
- traffic and request evidence;
- lifecycle actions;
- configuration entry point.

Canonical names remain immutable. Alias retargeting and model creation retain
their existing configuration-draft semantics.

## Workers Page

Workers reuses the dense resource-ledger strengths of composition C without
making the whole product Worker-centric.

The resource list displays health, Tags, GPU state, active requests, running
Models, restart state, and Agent version. The selected Worker detail presents:

- heartbeat time and health reason;
- GPU devices and memory/utilization/temperature;
- running and allowed Models;
- artifact states and replica cooldowns;
- Agent build and version compatibility;
- llama-swap connectivity data already present in the API;
- recent Worker events and applicable actions.

FRP lease, renewal, or assigned-port diagnostics are not shown because the
current status API does not expose them. A future diagnostic API may add those
facts to the connectivity section without changing the Overview topology.

## Requests, Activity, and Billing

Requests and Activity use high-density tables with fixed headers, a stable
resource identity column, filters, and a detail inspector. Existing pagination
remains `Load more` unless a later API adds cursor or server-side filtering.

Billing remains a normal Observe route. When the records store is disabled, the
page displays an unavailable capability explanation and states that core model
serving remains operational. It does not produce a red global alert. When
enabled, existing ranges, pricing edits, totals, model rows, application rows,
and request cost evidence remain available.

## Configuration and Advanced

Configuration retains structured model, alias, and Tag Policy editing. The
page uses a workspace layout:

- resource selector and structured editor;
- persistent draft/saved status;
- dry-run result and loaded-resource impacts;
- explicit reset, dry run, and apply actions;
- model creation through the existing accessible Modal Form.

The structured workspace continues to preserve original YAML omission
semantics, including omitted `max_loaded`. Advanced remains a read-only view of
the current draft YAML with copy support.

## Action Risk and Confirmation

| Action | Interaction |
| --- | --- |
| Refresh, inspect, filter, copy identifier | Immediate |
| Warm a selected Model on an explicit eligible Worker | Immediate with inline progress and result |
| Unload Model | Confirmation naming Model and Worker |
| Drain Worker | Confirmation naming Worker and routing effect |
| Alias target change | Confirm as part of configuration impact review |
| Configuration Apply | Require visible dry-run result and impact review |
| Restart-required configuration | State clearly that Apply saves but does not activate the process-level change |

After a state-changing operation, feedback appears next to the originating
control and links to the corresponding Activity evidence when available.

## Visual System

The durable visual rules live in `DESIGN.md`. The admin UI uses:

- a restrained neutral palette and stable slate navigation rail;
- Route Teal only for current selection, focus, and traced relationships;
- green, amber, and red only for named semantic states;
- a system sans stack for interface text and a system mono stack for machine
  identifiers, timestamps, ports, versions, and operational labels;
- flat persistent surfaces separated by borders, tonal fields, and alignment;
- shadows only for transient overlays and confirmation surfaces;
- compact squared corners, with pills limited to short Tags and states.

The implementation does not load remote fonts and does not use gradients,
glass, glow, decorative topology, circular metric gauges, or a dark-neon
monitoring-dashboard treatment.

## Responsive and Accessibility Behavior

Desktop at `1280px` and wider is the primary dense operating surface.

- At intermediate widths, navigation narrows and master-detail panels may
  collapse while retaining the selected resource identity.
- At compact widths, navigation becomes a compact horizontal or drawer
  surface, master-detail stacks vertically, and relationship diagrams become
  labeled dependency trails.
- Wide tables retain horizontal scrolling, sticky headers, and a sticky primary
  identity column where practical.

Every important state combines text with shape/color. All interactive controls
have visible keyboard focus. Dialogs retain focus trapping, Escape handling,
and focus restoration. Motion is limited to relationship-selection changes,
drawers, and operation feedback and respects `prefers-reduced-motion`.

## Frontend Architecture

The current single UI entry file is decomposed along page and shared-domain
boundaries. The planned structure is:

```text
ui/admin/src/
  app/
    App.tsx
    AppShell.tsx
    navigation.ts
  pages/
    OverviewPage.tsx
    ModelsPage.tsx
    WorkersPage.tsx
    RequestsPage.tsx
    ActivityPage.tsx
    BillingPage.tsx
    ConfigurationPage.tsx
    AdvancedPage.tsx
  components/
    AttentionList.tsx
    ConfirmDialog.tsx
    DetailPanel.tsx
    EmptyState.tsx
    ResourceList.tsx
    StatusIndicator.tsx
  domain/
    attention.ts
    fleet.ts
    relationships.ts
  api.ts
  routes.ts
  styles.css
```

This is a target organization, not a requirement to rename stable existing
domain helpers that already have clear ownership. Model lifecycle, alias, YAML
round-trip, and route logic retain their focused modules and tests.

`domain/attention.ts`, `fleet.ts`, and `relationships.ts` contain pure derived
state functions. React components consume their results and do not repeat
severity, Tag aggregation, or chain-building logic.

## Data Loading and Failure Isolation

- `/ui/status` remains the single five-second live-state poll.
- The manual refresh control exposes refresh-in-progress and the last successful
  data time while leaving the previous successful state visible.
- Requests, Activity, Billing, and Configuration load when their routes become
  active rather than all loading during initial application mount.
- Failure of one optional or route-specific endpoint affects only its page.
- Initial app-shell failure presents an authentication/connection error without
  replacing it with an unrelated empty state.
- Empty, unavailable, error, stale, and loading are separate UI states.

## Compatibility

- All existing `/ui/*` routes remain independently refreshable.
- Agent-token UI authentication and the HttpOnly cookie flow remain unchanged.
- Gateway JSON contracts and operational action endpoints remain unchanged.
- Existing canonical model, alias, disabled model, runtime, tag policy, and
  configuration-apply semantics remain unchanged.
- The UI remains embedded by the existing Gateway Docker build path.
- No optional database or metrics service becomes required for the UI shell or
  core serving state.

## Testing and Acceptance

### Unit tests

- Tag aggregation across Workers with one or multiple Tags.
- Attention derivation, exclusions, severity ordering, and deterministic ties.
- Relationship derivation for a Worker and its reported GPU/Model branches,
  including the absence of a Model-to-specific-GPU mapping.
- Optional records/metrics capability notices do not become fleet incidents.
- Existing route mapping, popstate handling, configuration draft, model
  lifecycle, and alias behavior.

### Gateway tests

- Existing UI page routes, authentication, status, action, records, Billing,
  and configuration API tests remain passing.
- No new backend endpoint is required by the first implementation.

### Browser acceptance

- Desktop overview clearly communicates healthy versus degraded state before
  aggregate traffic.
- Tag aggregation, Tag expansion, and attention-item selection display only
  supported relationships.
- FRP is absent from the default Overview topology.
- Models and Workers preserve selection while operators inspect related state.
- Direct route refresh, back, and forward navigation restore the correct page.
- Keyboard-only navigation, dialog focus, Escape, focus restoration, and
  reduced motion work.
- Billing and historical metrics disabled states are neutral and local.
- Model creation, dry run, Apply, Warm, Unload, Drain, and Undrain continue to
  use the current endpoints and semantics.

### Build verification

```bash
cd ui/admin && npm test
cd ui/admin && npm run build
go test ./internal/gateway -count=1
go test ./...
```

## Implementation Boundary

The first implementation changes the embedded React UI, its tests, and only the
minimum Gateway UI route/static-asset tests required by the refactor. Any new
FRP diagnostic API, server-side request filtering, role system, or persistence
feature requires a separate design and is not implied by this redesign.
