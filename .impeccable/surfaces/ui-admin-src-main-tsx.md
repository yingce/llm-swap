---
version: 1
slug: "ui-admin-src-main-tsx"
primary_target: "ui/admin/src/main.tsx"
related_targets: ["ui/admin/src/styles.css","ui/admin/src/routes.ts"]
---

Scope: the complete embedded admin UI rooted at `ui/admin/src/main.tsx` and its
standalone `/ui/*` routes. Visitor mode: Operate.

Audience and job: one internal technical audience moves from fleet observation
through model rollout, configuration, and incident diagnosis. The first job is
to decide whether the serving plane is healthy and what needs attention.

Task and content: lead with a plain-language fleet conclusion and a ranked
attention queue. Preserve models, workers, billing, events, requests,
configuration operations, advanced YAML, standalone routes, and all current API
contracts. Connect observation, evidence, and action around the same resource.

Constraints: optional records and metrics stores must degrade quietly; canonical
model identities stay immutable; low-risk actions may execute directly while
high-risk actions expose scope and require confirmation. Desktop is the primary
dense operating surface, with usable responsive fallback.

Chosen direction: Signal Room Console. A light matte control panel, dark slate
navigation rail, orthogonal relationship diagrams, compact annunciator strips,
and semantic signal colors. The memorable moment is selecting an exception and
seeing its full Gateway-to-model impact chain highlight across the workspace.

Unresolved: final token values, font families, and the exact topology fallback
for fleets large enough to exceed one viewport will be settled during the build.
