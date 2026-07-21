# Config Ops Model Actions Simplification Design

Date: 2026-07-22

## Goal

Simplify Config Ops model management by keeping model creation and existing
model editing, while removing the Model Copy and Delete features that add
unnecessary lifecycle choices to the UI.

## Scope

The Models card retains `New model`. Its modal continues to create a blank
draft with `runtime: vllm`, `disabled: true`, `min_loaded: 0`, and no selected
Tags. Existing canonical models remain editable in place, including their
runtime, local directory, policy, artifact, Tag configuration, and compact
Disabled control.

The UI removes:

- every Model Copy button and copy-mode modal path;
- model deletion buttons, confirmation panels, and deletion blockers;
- draft callbacks, lifecycle helpers, tests, and documentation that exist only
  for copying or deleting models.

Advanced page `Copy YAML` remains available. Model aliases, Tag policies, Dry
run, Apply, and the existing YAML configuration API remain unchanged.

## Behavior and Compatibility

No Gateway or Agent code changes. The configuration format and existing model
entries remain compatible. Removing Model Copy/Delete changes only which
configuration mutations the Config Ops UI exposes; direct configuration API
validation remains as it is.

The New model modal is creation-only: no source model or inherited Tags are
available. Closing a dirty modal still requires discard confirmation and never
changes the gateway configuration. Raw `run` models remain read-only compatible
in existing-model editing, and Runtime selection continues to offer only
`vllm`, `sglang`, and `llamacpp`.

## Testing

Frontend tests remove copy/delete-specific fixtures and assertions. They retain
coverage for blank model defaults, runtime options, name validation, New model
modal cancellation, dirty-discard behavior, compact Disabled rendering, and
Advanced Copy YAML. Final verification runs frontend tests/build, Go tests, and
the whitespace check.
