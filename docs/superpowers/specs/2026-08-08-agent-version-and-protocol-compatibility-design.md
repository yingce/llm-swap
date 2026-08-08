# Agent Version and Protocol Compatibility

## Goal

Make the Workers page show a useful Agent release version without coupling
Agent freshness to unrelated Gateway releases. Compatibility is determined by
the heartbeat protocol, not by equality between release-version strings or Git
commits.

## Display model

- The normal Worker diagnostics display one field: **Agent version**.
- The Git commit is not displayed in the normal UI. It remains in
  `BuildInfo.commit` for logs, API diagnostics, and exact source tracing.
- A compatible worker receives no `latest` or `old` release label. Gateway does
  not know whether a different compatible Agent release is operationally
  preferred.
- An upgrade warning is shown only when the reported heartbeat protocol is
  outside the Gateway-supported range or absent:
  - missing protocol: legacy Agent;
  - below the minimum: upgrade Agent;
  - above the maximum: upgrade Gateway.

## Version ownership

`BuildInfo.version` is a human release identifier such as `2026.08.08.1`.
Agent release automation sets it independently from `BuildInfo.commit`.
Gateway-only changes do not change the Agent release version and do not require
an Agent rollout.

The source fallback `buildinfo.AgentVersion` is bumped only when publishing a
new Agent release. Build tooling must not populate `LLMSWAP_BUILD_VERSION` with
the Git SHA; the SHA belongs only in `LLMSWAP_BUILD_COMMIT`.

## Compatibility contract

Gateway owns an explicit inclusive supported Agent protocol range. The current
Agent reports one `protocol_version` in every heartbeat.

Compatibility status is derived as follows:

| Agent protocol | Status | Operator action |
| --- | --- | --- |
| absent/zero | `legacy` | Upgrade Agent |
| below Gateway minimum | `upgrade_agent` | Upgrade Agent |
| within supported range | `compatible` | None |
| above Gateway maximum | `upgrade_gateway` | Upgrade Gateway |

Release versions and commits never participate in this decision.

Protocol constants change only for Gateway/Agent communication behavior that
requires coordination. A coordinated rollout first deploys a Gateway that
supports both old and new protocol versions, then rolls Agents, and only later
raises the Gateway minimum. Pure Gateway behavior or UI changes do not change
the protocol.

## API compatibility

The heartbeat `BuildInfo` payload retains `version`, `commit`, `build_time`, and
`protocol_version`. The UI status response retains `agent_build` so existing
diagnostic consumers do not lose data. `agent_version_status` changes from an
exact release comparison to the protocol-derived status values above.

## Tests

- A matching protocol with a different Agent release version is compatible.
- A lower protocol requests an Agent upgrade.
- A higher protocol requests a Gateway upgrade.
- A heartbeat without build/protocol data is legacy.
- Workers UI displays Agent version, omits Commit and latest/old labels, and
  shows upgrade guidance only for non-compatible states.
- Production UI assets are rebuilt after source tests pass.

## Non-goals

- Do not build a general package-update service.
- Do not infer compatibility from semantic/date version ordering.
- Do not remove commit/build-time metadata from heartbeat or APIs.
- Do not require Agent publication for Gateway-only changes.
