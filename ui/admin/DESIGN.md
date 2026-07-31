# Admin UI signal room design

The admin console is organized as an internal operator workspace:

- Overview is exception-first and uses `/ui/status` only. It avoids request/event lazy data so a refresh on `/ui` stays fast and stable.
- Fleet pages use master-detail layouts. Models join live status with gateway config for aliases, runtime, and disabled drafts. Workers show GPU inventory, loaded models, and existing connectivity fields only.
- Observe pages are route-loaded. Request log, activity, and billing do not load until their route is selected.
- Configuration keeps YAML omission semantics. In particular, omitted `max_loaded` remains the auto form, and empty `model_aliases` is omitted from rendered YAML.

FRP is transport rather than topology in this UI. Cluster topology is shown as Gateway → Tag → Worker → Loaded model.
