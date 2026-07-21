# Config Ops Model Modal and Runtime Selector Design

Date: 2026-07-21

## Goal

Make Config Ops model creation and copying easier to use without changing the
gateway configuration workflow: use one reusable modal form for both actions,
compact the disabled control, and make supported runtime selection explicit.

## Scope

This design changes only the Config Ops UI and its frontend tests/build output.
Model persistence remains the existing local draft followed by Dry run and
Apply. No model-specific API, gateway routing behavior, Agent behavior, or
worker-local lifecycle operation is added.

## Reusable Create and Copy Modal

`New model` and `Copy` open the same centered modal. Both flows reuse the
existing model field editor instead of maintaining separate create and edit
forms.

Blank creation initializes:

- `runtime: vllm`;
- `disabled: true`;
- `min_loaded: 0`;
- no selected Tags.

Copy initializes from the selected concrete model, including its configured
runtime and selected Tag memberships, then resets `disabled: true` and
`min_loaded: 0`. The operator must provide a new immutable canonical name and
may adjust all exposed fields and Tags before saving.

Save changes only the in-memory configuration draft and synchronizes selected
Tags through the existing lifecycle helper. It never calls Dry run, Apply,
warm, unload, or Alias operations. After a successful save, Config Ops enables
the disabled-model filter and selects the newly created model.

Cancel, backdrop click, and Escape close the modal. If any modal field differs
from its initial values, the UI asks the operator to confirm that the draft
form should be discarded. No gateway configuration is changed by discard.

Existing canonical models continue to be edited in the page card. Their names
remain display-only and cannot be renamed.

## Runtime Selector and Raw Commands

The runtime field is a select with exactly these values:

- `vllm`;
- `sglang`;
- `llamacpp`.

This matches the existing gateway configuration validation. Blank model
creation defaults to `vllm`; copied models retain their source runtime. The UI
does not add Ollama support.

Existing models that use a raw `run` command without a runtime stay compatible.
They display a read-only `Custom command` state and retain their raw command on
draft serialization. This feature intentionally does not add creation or
editing of raw command text.

## Compact Disabled Control

The editor removes the full-width `Disabled` row from the field grid. Disabled
state moves to a compact control in the model card header alongside existing
readiness/status information. The model picker retains its small Disabled
pill, while the `Show disabled` filter remains available so newly created
disabled models can be found without increasing form height.

## Error Handling and Testing

Existing client-side model-name validation, Tag synchronization, and server-side
Dry run/Apply validation remain authoritative. A modal validation error keeps
the modal open and preserves operator input. Closing a dirty modal requires
explicit confirmation.

Frontend coverage verifies runtime option values, the vLLM default, copied
runtime retention, modal create/cancel behavior, selected disabled-model
visibility after saving, and the compact disabled-control rendering contract.
The final verification runs frontend tests and build, focused configuration
tests if shared validation code changes, and the repository Go test suite.
