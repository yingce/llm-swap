---
name: LLM Swap
description: A signal-room control console for operating model serving infrastructure.
---

<!-- SEED: established with the user before implementation; re-run $impeccable document once there's code to capture the actual tokens and components. -->

# Design System: LLM Swap

## Overview

**Creative North Star: "The Signal Room Console"**

LLM Swap should feel like a modern railway signal room translated into a web
operator console: a quiet, durable working surface where routes, dependencies,
exceptions, and operator interventions stay legible under sustained use. The
visual world combines a pale matte control panel, dark slate framing, precise
line diagrams, compact status strips, and restrained signal colors.

Expression must remain subordinate to operation. The signature is not a themed
decoration but a functional relationship language: selecting an exception or
resource reveals the connected gateway, transport, worker, GPU, model, request,
and control-action evidence. Dense tables, forms, and logs remain familiar
technical interfaces inside the same system.

**Key Characteristics:**

- Light, low-glare working surfaces with a stable dark navigation rail.
- Orthogonal relationship lines and compact annunciator strips.
- High information density with clear exception-first hierarchy.
- Sparse, semantic signal color rather than ambient decoration.
- Operational actions remain conventional, explicit, and verifiable.

## Colors

Use a restrained strategy: neutral panel and ink colors own almost the entire
surface, with one route accent and three signal colors reserved for real state.
Exact values will be resolved against the implemented UI and accessibility
checks.

- **Panel White:** the primary task surface, matte rather than luminous.
- **Slate Frame:** navigation, strong headings, and structural anchors.
- **Route Teal:** selected paths, active navigation, focus, and deliberate
  operator intent.
- **Signal Green:** confirmed healthy or completed state only.
- **Signal Amber:** degraded, pending, loading, or review-required state.
- **Signal Red:** unavailable, failed, destructive, or urgent state only.
- **Graphite and Steel:** body text, secondary text, dividers, and inactive
  topology.

**The Signal Has Meaning Rule.** Green, amber, and red never decorate a neutral
screen. Each occurrence must communicate a state that can be named in text.

**The One Active Route Rule.** Route Teal identifies the current location,
selection, or traced relationship; it is not scattered across unrelated cards.

## Typography

Use a workhorse interface sans for navigation, content, controls, and tables,
paired with a compact monospaced face for identifiers, timestamps, ports,
versions, metric labels, and topology annotations. Exact families and the final
scale will be resolved during implementation.

Headlines are concise and sentence case. Small operational labels may use
uppercase with modest tracking, but body copy and action labels do not. Numeric
values align for fast vertical comparison, and long identifiers truncate only
when the full value remains available on demand.

**The Label Is Not Decoration Rule.** Monospaced uppercase text is reserved for
machine-oriented labels and coordinates, never used as a general stylistic
texture.

## Layout

The desktop shell uses a stable left navigation rail and a fluid working area.
Pages organize information in this order: current conclusion, actionable
exceptions, related system state, then historical evidence. Resource pages use
a master-detail pattern so models and workers can be scanned without losing the
selected resource context.

The topology grammar is orthogonal and layered. Lines show real relationships,
arrows show real direction, and highlighted routes always begin and end at
labeled resources. Tables and forms use a compact spacing rhythm; larger gaps
separate operational sections, not every card.

On narrower screens the navigation becomes a compact horizontal or drawer
surface, topology becomes a linear dependency trail, and master-detail panels
stack without hiding actions or state. Wide tables may scroll horizontally but
their resource identity and primary state remain sticky.

## Elevation & Depth

The system is flat by default. Borders, tonal fields, inset rails, and line
weight establish hierarchy. Shadows are limited to transient layers such as
dialogs, command confirmation sheets, and floating detail inspectors.

**The Panel Stays Put Rule.** Persistent dashboard surfaces never float as a
field of unrelated cards. Elevation means temporary interaction or immediate
operator focus.

## Shapes

Use compact, gently squared corners: controls and small status strips are
slightly rounded, while large working regions are defined primarily by borders
and alignment. Pills are reserved for short state or tag tokens and never used
for full-width settings, disabled model rows, or general containers.

Topology nodes may use notched or terminal-like details when they improve
relationship reading, but forms, tables, and dialogs keep conventional web
geometry.

## Do's and Don'ts

### Do:

- **Do** lead each operational page with a plain-language health or state
  conclusion.
- **Do** pair color, text, and shape for every important state.
- **Do** keep resource identity visible while inspecting events, traffic, or
  actions.
- **Do** use relationship lines only for relationships represented by real
  gateway data.
- **Do** keep high-risk confirmations explicit about resource and impact.

### Don't:

- **Don't** reproduce the category-standard dark neon monitoring dashboard.
- **Don't** turn every metric into an equal-weight card or circular gauge.
- **Don't** use gradients, glass effects, bloom, or decorative network lines.
- **Don't** hide unavailable optional services behind alarming failure states.
- **Don't** replace familiar tables, inputs, and buttons with themed controls
  that slow technical operators down.
