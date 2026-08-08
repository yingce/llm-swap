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

`BuildInfo.version` is a human release identifier. The fencing-capable Agent
release uses `2026.08.08.1`.
Agent release automation sets it independently from `BuildInfo.commit`.
Gateway-only changes do not change the Agent release version and do not require
an Agent rollout.

The source fallback `buildinfo.AgentVersion` is bumped only when publishing a
new Agent release. Build tooling must not populate `LLMSWAP_BUILD_VERSION` with
the Git SHA; the SHA belongs only in `LLMSWAP_BUILD_COMMIT`.

## Compatibility contract

Gateway owns an explicit inclusive supported Agent protocol range. The current
Agent reports protocol v3 in every heartbeat, and the current Gateway's safe
range is exactly v3 through v3. Protocol v2 predates `config_revision` and
shared-directory artifact fencing, so a v2 heartbeat may still be accepted by
the HTTP endpoint during cutover but must be reported as `upgrade_agent`,
never `compatible`.

Compatibility status is derived as follows:

| Agent protocol | Status | Operator action |
| --- | --- | --- |
| absent/zero | `legacy` | Upgrade Agent |
| below Gateway minimum | `upgrade_agent` | Upgrade Agent |
| within supported range | `compatible` | None |
| above Gateway maximum | `upgrade_gateway` | Upgrade Gateway |

Release versions and commits never participate in this decision.

Protocol constants change only for Gateway/Agent communication behavior that
requires coordination. The first fencing release is a coordinated v3
Gateway+Agent batch because v2 cannot safely participate in the fencing
contract; HTTP acceptance of v2 exists for rollout visibility, not a supported
mixed serving state. After the fleet is on v3, pure Gateway behavior or UI
changes that preserve the protocol contract do not change the protocol and do
not require a new Agent release. A later overlap-compatible protocol change may
use Gateway old+new support, then Agent rollout, then a minimum-version raise;
never infer that overlap from release strings or commits.

## API compatibility

The heartbeat `BuildInfo` payload retains `version`, `commit`, `build_time`, and
`protocol_version`. The UI status response retains `agent_build` so existing
diagnostic consumers do not lose data. `agent_version_status` changes from an
exact release comparison to the protocol-derived status values above.

## Tests

- A matching protocol with a different Agent release version is compatible.
- A lower protocol requests an Agent upgrade.
- A v2 heartbeat is accepted but requests an Agent upgrade under the v3 safety
  minimum.
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
