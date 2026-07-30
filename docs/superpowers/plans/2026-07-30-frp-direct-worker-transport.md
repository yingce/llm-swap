# FRP Direct Worker Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the gateway-to-agent WebSocket tunnel with gateway-controlled, embedded-FRPC TCP transport, then run eight single-GPU worker containers on the RTX 4090 host through a real external FRPS path.

**Architecture:** The gateway remains the sole owner of worker routing, concurrency, queues, retries, and model lifecycle. An agent still polls its config and heartbeats over HTTPS, but receives an encrypted FRP bootstrap payload and acquires a durable TCP slot lease. The agent runs one embedded FRPC client for its local `127.0.0.1:6006` llama-swap service and only heartbeats a direct `http://<frps-address>:<remote-port>` URL after that proxy is ready. Gateway proxying, metrics, warm, and unload use that URL directly; no WebSocket route or compatibility fallback remains.

**Tech Stack:** Go 1.23; official pinned `github.com/fatedier/frp v0.70.0` Go client; AES-256-GCM and HKDF-SHA-256 (`golang.org/x/crypto/hkdf`); YAML gateway configuration; Docker Compose with NVIDIA device reservations; existing React/Vite admin UI only for redacted configuration visibility.

## Global Constraints

- Preserve the gateway ownership boundaries in `docs/agents/project-map.md`; do not move scheduling, queues, active counts, or replica policy into the agent.
- This release is a hard cut: remove all gateway and agent WebSocket tunnel code, routes, dependency references, tests, and documentation. Do not leave a tunnel fallback switch.
- Keep the initial lease store single-gateway and durable on that gateway's local disk. It is intentionally not a Redis or multi-gateway change.
- The static agent configuration contains only normal local runtime settings, `gateway_url`, the agent credential, and tags. It must not contain FRP endpoint/range/token, an advertised worker URL, or a separate llama-swap token.
- Derive agent identity as: explicit `agent.id` / `LLMSWAP_AGENT_ID`; otherwise hostname; otherwise a generated UUID persisted under the agent root. Container services use hostnames `worker-gpu0` through `worker-gpu7`.
- Do not set `NVIDIA_VISIBLE_DEVICES` in the Compose file. Pin each service using the Docker NVIDIA device request for its physical GPU; each container will see its assigned card as CUDA device `0`.
- Never log, include in test snapshots, show in the Admin UI, commit, or put real FRP or agent tokens in Compose examples, docs, or shell output.
- Keep existing non-FRP/Tailscale-capable image paths working unless a test demonstrates that they share removed tunnel code. The FRP deployment build specifically sets `LLMSWAP_INSTALL_TAILSCALE=0`.

---

## File Structure

- Modify: `internal/config/config.go`, `internal/config/load.go`, `internal/config/agent_runtime.go` — gateway transport schema/validation and minimal agent bootstrap identity.
- Modify: `internal/config/config_test.go`, `internal/config/agent_runtime_test.go`, `internal/config/gateway_runtime_test.go` — schema, validation, identity, and no-advertised-URL regression coverage.
- Modify: `internal/protocol/agent.go`, `internal/protocol/agent_test.go` — encrypted transport bootstrap, lease request/response, and heartbeat lease fields.
- Create: `internal/transport/crypto.go`, `internal/transport/crypto_test.go` — authenticated encrypted bootstrap envelope.
- Create: `internal/gateway/transport_lease.go`, `internal/gateway/transport_lease_store.go`, `internal/gateway/transport_lease_test.go` — sticky slot allocation, expiry, persistence, and release.
- Modify: `internal/gateway/server.go`, `internal/gateway/workers_test.go`, `internal/gateway/config_admin.go`, `internal/gateway/config_admin_test.go`, `internal/gateway/config_manager.go` — secure config delivery, lease endpoint, persistence wiring, and UI redaction.
- Modify: `internal/agent/config_client.go`, `internal/agent/config_client_test.go`, `internal/agent/reconcile.go`, `internal/agent/reconcile_test.go` — authenticated config/lease exchange and runtime token propagation.
- Create: `internal/agent/frp_client.go`, `internal/agent/frp_client_test.go`, `internal/agent/transport_manager.go`, `internal/agent/transport_manager_test.go` — lifecycle-owned embedded FRPC adapter and its fully fakeable unit seam.
- Modify: `cmd/agent/main.go`, `cmd/agent/main_test.go`, `scripts/agent-container-entrypoint.sh`, `scripts/agent_container_entrypoint_test.go`, `Dockerfile.agent`, `go.mod`, `go.sum` — agent boot/lifecycle and image changes.
- Modify or remove: `internal/agent/tunnel.go`, `internal/agent/tunnel_test.go`, `internal/gateway/tunnel.go`, `internal/gateway/tunnel_test.go`, `internal/gateway/worker_client.go`, `internal/gateway/proxy.go`, `internal/gateway/proxy_test.go`, `internal/gateway/metrics_scrape.go`, `internal/gateway/metrics_scrape_test.go`, `internal/gateway/llamaswap_client.go`, `internal/gateway/llamaswap_client_test.go` — delete tunnel-only transport and make all worker operations direct HTTP.
- Create: `deploy/worker-frp/compose.yaml`, `deploy/worker-frp/.env.example`, `deploy/worker-frp/gateway.test.yaml.example`, `deploy/worker-frp/README.md`, `deploy/worker-frp/verify.sh` — reproducible 8-GPU worker simulation without secrets.
- Modify: `examples/gateway.yaml`, `examples/agent.yaml`, `docs/agents/project-map.md`, `README.md` (only where transport setup is described) — supported configuration and operations documentation.

## Protocol and Data Contract

### Gateway configuration

Add the following to `config.GatewayConfig`:

```go
type TransportConfig struct {
    Type string             `yaml:"type" json:"type"`
    FRP  FRPTCPConfig       `yaml:"frp" json:"frp"`
}

type FRPTCPConfig struct {
    ServerAddr      string `yaml:"server_addr" json:"server_addr"`
    ServerPort      int    `yaml:"server_port" json:"server_port"`
    AuthToken       string `yaml:"auth_token" json:"auth_token"`
    PortStart       int    `yaml:"port_start" json:"port_start"`
    PortEnd         int    `yaml:"port_end" json:"port_end"`
    LeaseTTLSeconds int    `yaml:"lease_ttl_seconds" json:"lease_ttl_seconds"`
}
```

`transport.type` is either empty (legacy direct URL mode for existing gateway configs) or exactly `frp_tcp`. When it is `frp_tcp`, validate nonempty address/token, server port `1..65535`, range `1..65535` with `port_start <= port_end`, range capacity of at least one, and a positive TTL. Set a default lease TTL of 180 seconds only after the transport type has been selected. Production uses this section in the gateway YAML and is never returned unredacted by `/ui/api/config`.

### Encrypted bootstrap and leases

Extend `protocol.AgentConfigResponse` with:

```go
type EncryptedTransportBootstrap struct {
    Generation uint64 `json:"generation"`
    Nonce      string `json:"nonce"`
    Ciphertext string `json:"ciphertext"`
}

type TransportLeaseRequest struct {
    AgentID      string `json:"agent_id"`
    Generation   uint64 `json:"generation"`
    LeaseID      string `json:"lease_id,omitempty"`
    Release      bool   `json:"release,omitempty"`
    ExcludeSlots []int  `json:"exclude_slots,omitempty"`
}

type TransportLeaseResponse struct {
    LeaseID    string    `json:"lease_id"`
    Slot       int       `json:"slot"`
    RemotePort int       `json:"remote_port"`
    Generation uint64    `json:"generation"`
    ExpiresAt  time.Time `json:"expires_at"`
}
```

The encrypted plaintext is an internal `transport.Bootstrap` containing FRP endpoint, FRP token, range, effective lease TTL, and llama-swap bearer token. Derive a 32-byte encryption key using HKDF-SHA-256 with input key material equal to the existing agent token, salt equal to the big-endian config generation, and info `llmswap/transport-bootstrap/<agent-id>`. Encrypt JSON with AES-256-GCM, a new random 12-byte nonce, and associated data `<agent-id>\n<generation>`. Encode nonce/ciphertext with raw URL-safe base64. The gateway must require `agent_id` on `GET /internal/agent/config`, use it in the encryption binding, and reject empty or malformed identities before sending a payload.

Lease identity is the stable `agent_id`, not a GPU number. A lease's remote port is always `port_start + slot`; slot zero maps to `port_start`. Its stored record holds agent ID, lease ID, slot, config generation, expiry, and last renewal. The gateway allocates the existing valid lease for the same agent/generation first; otherwise the lowest unexpired-free slot. It never reallocates a live slot. An expired record can be reclaimed on allocation or periodic garbage collection. A valid heartbeat/renew extends expiry; a graceful agent release deletes only the exact matching lease ID and generation.

## Tasks

### Task 1: Lock down configuration, agent identity, and redaction contracts

**Files:** `internal/config/config.go`, `internal/config/load.go`, `internal/config/agent_runtime.go`, `internal/config/config_test.go`, `internal/config/agent_runtime_test.go`, `internal/config/gateway_runtime_test.go`, `internal/gateway/config_admin.go`, `internal/gateway/config_admin_test.go`, `examples/gateway.yaml`, `examples/agent.yaml`.

- [ ] **Step 1: Write failing gateway transport validation tests.**

  Add table cases to `internal/config/config_test.go` for: a valid `frp_tcp` block; missing address; missing auth token; invalid FRPS port; reversed port range; an invalid out-of-range port; and zero TTL. Assert the exact configuration field in each error. Add a compatibility case proving a legacy config with no `transport` still loads.

- [ ] **Step 2: Implement schema and validation.**

  Add `Transport` to `GatewayConfig` and validate it from the same `LoadGateway` path that validates tokens and tag policies. Keep `auth_token` in the in-memory config because the gateway must encrypt and send it, but do not marshal it to UI output.

- [ ] **Step 3: Write failing minimal-agent configuration tests.**

  In `internal/config/agent_runtime_test.go`, add tests that `LoadAgentRuntime` accepts no `swap_url`/`llama_swap_url`, defaults a missing ID to the injected hostname, and persists/reuses a generated fallback ID if hostname lookup fails. Add a direct loader test that a provided `llama_swap_token` remains accepted for an explicitly configured non-FRP legacy installation but no longer defaults to the agent token in FRP mode.

- [ ] **Step 4: Implement minimal bootstrap and identity resolution.**

  Stop deriving an external worker URL from Tailscale/local IP in the FRP path. Keep `LocalLlamaSwapURL(swapPort)` for local agent operations. Introduce a small injected `IdentityProvider`/runtime helper so hostname and persistent UUID behavior is deterministic in tests; write the fallback ID to `${LLMSWAP_ROOT}/agent-id` with mode `0600`. Make `gateway_url`, token, tags, model root, llama-swap config, and swap port the only required FRP startup inputs.

- [ ] **Step 5: Redact operational config responses before exposing them.**

  Add `redactedGatewayConfig` (or equivalent response DTO) in `internal/gateway/config_admin.go`. `/ui/api/config` must replace `transport.frp.auth_token` with a non-reusable marker in both structured JSON and rendered YAML. The apply/dry-run request remains authoritative raw YAML and continues to accept a real secret; the UI must preserve a redacted existing token by omitting it from an unchanged form submission rather than writing the marker back.

- [ ] **Step 6: Run focused tests and commit.**

  Run:

  ```powershell
  go test ./internal/config ./internal/gateway -run 'Test(LoadGateway.*Transport|LoadAgentRuntime.*(NoAdvertisedURL|Hostname|PersistentFallback)|UIConfig.*Transport.*Redact)' -count=1
  ```

  Expected: all new validation, compatibility, identity, and redaction tests pass.

  ```powershell
  git add internal/config internal/gateway/config_admin.go internal/gateway/config_admin_test.go examples/gateway.yaml examples/agent.yaml
  git commit -m "feat: add FRP transport configuration"
  ```

### Task 2: Build the encrypted bootstrap and durable gateway lease allocator

**Files:** `internal/protocol/agent.go`, `internal/protocol/agent_test.go`, `internal/transport/crypto.go`, `internal/transport/crypto_test.go`, `internal/gateway/transport_lease.go`, `internal/gateway/transport_lease_store.go`, `internal/gateway/transport_lease_test.go`, `internal/gateway/server.go`, `internal/gateway/workers_test.go`.

- [ ] **Step 1: Add failing crypto tests.**

  Create `internal/transport/crypto_test.go`. Cover encrypt/decrypt round-trip, wrong agent ID, wrong generation, wrong token, modified ciphertext, and modified nonce. Verify ciphertext never contains the plaintext FRP token or llama-swap token.

- [ ] **Step 2: Implement a narrow transport crypto package.**

  Add `transport.SealBootstrap(agentToken, agentID string, generation uint64, payload Bootstrap) (protocol.EncryptedTransportBootstrap, error)` and the inverse `OpenBootstrap`. Use `golang.org/x/crypto/hkdf`, `crypto/aes`, `crypto/cipher`, `crypto/rand`, `encoding/base64`, and `encoding/json`; return errors without including secret input values.

- [ ] **Step 3: Write allocator/store tests before implementation.**

  Create `internal/gateway/transport_lease_test.go` with a controllable clock and temporary JSON state path. Verify: first two agent IDs receive slots 0 then 1; an agent renews the same slot; a config generation change returns a fresh lease generation; live slots are not reassigned; an expired slot is reusable; release requires matching lease ID/generation; state survives store reload; and allocation returns a clear capacity error when every slot is live.

- [ ] **Step 4: Implement an atomic in-process lease manager with disk persistence.**

  Implement `TransportLeaseManager` behind a mutex and a `TransportLeaseStore` that writes atomically (`.tmp`, `fsync`, rename) to `<gateway-config-dir>/transport-leases.json` by default. Pass the resolved config path into server construction, so `cmd/gateway/main.go` needs no second command-line setting. If no config path exists in tests, use an in-memory store. Do not persist FRP credentials in this file.

- [ ] **Step 5: Expose authenticated agent config and lease endpoints.**

  Change `ConfigClient.GetConfigContext` and `GET /internal/agent/config` to carry `agent_id` along with tags. When `transport.type: frp_tcp`, `handleAgentConfig` seals the bootstrap under that agent ID and current config-manager version. Add `POST /internal/agent/transport/lease`, protected by the same agent bearer token, and reject: invalid JSON, missing ID, unsupported transport, generation mismatch, non-matching renewal/release, or unknown configured tag. Extend heartbeats with `transport_lease_id` and `transport_generation`; renew only a valid matching lease during normal heartbeat processing.

- [ ] **Step 6: Write endpoint behavior tests.**

  Extend `internal/gateway/workers_test.go` to assert that a valid config response has an opaque bootstrap but no plaintext FRP token, an agent can decrypt with its token and ID, a different ID cannot decrypt it, a lease request gets `port_start`, and heartbeat renewal does not permit a mismatched lease to mark a worker available.

- [ ] **Step 7: Run focused tests and commit.**

  ```powershell
  go test ./internal/protocol ./internal/transport ./internal/gateway -run 'Test(AgentConfigResponse|.*Bootstrap|TransportLease|AgentConfigEndpoint.*Transport)' -count=1
  git add internal/protocol internal/transport internal/gateway
  git commit -m "feat: add encrypted FRP transport leases"
  ```

### Task 3: Add an embedded FRPC lifecycle under the agent process

**Files:** `go.mod`, `go.sum`, `internal/agent/frp_client.go`, `internal/agent/frp_client_test.go`, `internal/agent/transport_manager.go`, `internal/agent/transport_manager_test.go`, `internal/agent/config_client.go`, `internal/agent/config_client_test.go`, `cmd/agent/main.go`, `cmd/agent/main_test.go`.

- [ ] **Step 1: Pin the official FRP Go client and isolate its API.**

  Add `github.com/fatedier/frp v0.70.0` and `golang.org/x/crypto` using `go get`. Use FRP's official `client.NewService(...)`, `Run(context.Context)`, and `GracefulClose()` APIs behind the repository-owned adapter in the next step. Do not shell out to `frpc`, add a sidecar, or copy FRP source into this repository. Record the pinned versions in `go.mod` and let `go mod tidy` produce `go.sum`.

- [ ] **Step 2: Define a local, fakeable boundary before wiring FRP.**

  In `internal/agent/frp_client.go`, define repository-owned types `FRPProxyConfig` (`ServerAddr`, `ServerPort`, `AuthToken`, `ProxyName`, `LocalAddr`, `RemotePort`) and `FRPClient` with `Run(context.Context) error` and `Close() error`. Keep all upstream FRP types private to the real adapter. `ProxyName` must be deterministic and generation-specific, e.g. `llmswap-<sanitized-agent-id>-g<generation>`.

- [ ] **Step 3: Write failing transport-manager tests.**

  Create fake gateway and fake FRP client factories in `internal/agent/transport_manager_test.go`. Test this sequence: decrypt config; acquire lease; start the client with local address `127.0.0.1:6006`; wait for readiness; advertise exactly `http://server_addr:remote_port`; renew through heartbeats; cancel and release on shutdown. Add cases for invalid encrypted payload, failed registration (request a replacement lease while excluding the failed slot), stale generation (stop old client before reacquiring), and a cancel that never leaves an FRP goroutine running.

- [ ] **Step 4: Implement `TransportManager`.**

  `TransportManager` owns the current decoded bootstrap, lease, context cancellation, and client. It receives no raw secret logging capability. It calls the config client with agent ID, decrypts in memory, obtains/renews a lease, then starts one client. It publishes the advertised URL only after the client reports ready; on failed registration it closes the client, requests a new lease with `ExcludeSlots`, and uses bounded exponential retry. It makes graceful release best-effort with a bounded context. The reconciler sees a current URL/token through a concurrency-safe runtime state object rather than immutable startup fields.

- [ ] **Step 5: Wire main lifecycle without tunnel startup.**

  In `cmd/agent/main.go`, construct the config client once, start `TransportManager` under the signal context, give the reconciler the shared runtime state, and remove all `TunnelClient` construction/logging. A manager failure should keep the agent heartbeat unavailable and log a redacted operational error; it must not advertise a guessed LAN/Tailscale URL.

- [ ] **Step 6: Run tests and commit.**

  ```powershell
  go test ./internal/agent ./cmd/agent -run 'Test(TransportManager|ConfigClient.*Transport|Agent.*Transport)' -count=1
  go mod tidy
  git add go.mod go.sum internal/agent cmd/agent
  git commit -m "feat: run embedded FRPC in worker agent"
  ```

### Task 4: Make agent reconciliation transport-aware and remove static worker secrets

**Files:** `internal/agent/reconcile.go`, `internal/agent/reconcile_test.go`, `internal/agent/render.go`, `internal/agent/render_test.go`, `scripts/agent-container-entrypoint.sh`, `scripts/agent_container_entrypoint_test.go`, `Dockerfile.agent`.

- [ ] **Step 1: Write failing reconciler/render tests.**

  Add test cases proving a reconciler does not send an externally reachable URL until `TransportManager` marks it ready, and that it reads its llama-swap token from the decrypted runtime state. Add a render test that the llama-swap config receives that token but no FRP field is rendered. Existing legacy static-token behavior must retain a dedicated compatibility test only where the configured transport is not FRP.

- [ ] **Step 2: Implement runtime-state use in reconcile and rendering.**

  Replace fixed `Reconciler.LlamaSwapURL` and `Reconciler.LlamaSwapToken` reads with a small accessor. Before transport readiness, heartbeat an empty URL plus transport-not-ready event/state; after readiness, heartbeat the direct FRP URL and lease identifiers. Continue all local `/running`, health, and restart commands through `config.LocalLlamaSwapURL(cfg.Agent.SwapPort)`.

- [ ] **Step 3: Simplify the container entrypoint for FRP workers.**

  Remove Tailscale variables, startup scripts, supervisor programs, and external URL derivation from `scripts/agent-container-entrypoint.sh`. Retain the generic image's existing optional behavior only if required outside the FRP path; the `worker-frp` Compose environment must only use `LLMSWAP_GATEWAY_URL`, `LLMSWAP_AGENT_TOKEN_FILE`, and `LLMSWAP_AGENT_TAGS`. Implement `LLMSWAP_AGENT_TOKEN_FILE` as a strict read-once file input with a clear non-secret error if absent/empty. The entrypoint writes no `llama_swap_token` and no `swap_url` to generated agent YAML.

- [ ] **Step 4: Make the FRP image testable.**

  Keep the Dockerfile runtime option behavior, but document and test build arg `LLMSWAP_INSTALL_TAILSCALE=0`. Ensure the entrypoint unit/shell tests assert no generated Tailscale supervisor config and no token value appears in diagnostic output.

- [ ] **Step 5: Run agent and installer tests; commit.**

  ```powershell
  go test ./internal/agent ./internal/config -count=1
  go test ./scripts -count=1
  git add internal/agent scripts/agent-container-entrypoint.sh scripts/agent_container_entrypoint_test.go Dockerfile.agent
  git commit -m "feat: bootstrap FRP workers from gateway transport"
  ```

### Task 5: Remove the WebSocket tunnel completely and use only direct worker HTTP

**Files:** remove `internal/agent/tunnel.go`, `internal/agent/tunnel_test.go`, `internal/gateway/tunnel.go`, `internal/gateway/tunnel_test.go`, `internal/gateway/worker_client.go`; modify `internal/gateway/server.go`, `internal/gateway/proxy.go`, `internal/gateway/proxy_test.go`, `internal/gateway/metrics_scrape.go`, `internal/gateway/metrics_scrape_test.go`, `internal/gateway/llamaswap_client.go`, `internal/gateway/llamaswap_client_test.go`, `internal/gateway/worker_event_log.go` if it contains reverse-tunnel event names, `docs/agents/project-map.md`, and `go.mod`/`go.sum`.

- [ ] **Step 1: Add direct-HTTP regression cases first.**

  Update or add gateway tests that place a ready worker with an advertised FRP URL and assert proxy requests, activity scraping, performance scraping, warm, and unload all reach the direct HTTP test server with the llama-swap bearer token. Add a route test that `GET /internal/agent/tunnel` returns 404. Add an absence test by running `rg` in the verification step for `gorilla/websocket`, `TunnelClient`, `AgentTunnel`, `proxyAttemptViaTunnel`, and `/internal/agent/tunnel`.

- [ ] **Step 2: Refactor callers to direct clients.**

  Remove `Server.tunnels`, registry creation, route registration, `tunnelForWorker`, and `llamaSwapClientForWorker`. Rename `PullActivityViaTunnel`/`PullPerformanceViaTunnel` to transport-neutral direct methods and remove tunnel parameters. In proxy code, construct the upstream URL only from `worker.LlamaSwapURL`; preserve cancellation, retry, response streaming, request logging, and worker reverse-access failure accounting for ordinary HTTP errors.

- [ ] **Step 3: Delete tunnel implementation and dependency.**

  Delete both tunnel packages/tests and all WS-specific protocol/multiplexing code. Run `go mod tidy`; `github.com/gorilla/websocket` must disappear from `go.mod` and `go.sum`. Update project map wording from tunnel-preferred routing to direct advertised URLs through FRP.

- [ ] **Step 4: Run direct-path package and full Go regression tests; commit.**

  ```powershell
  go test ./internal/gateway -count=1
  go test ./... -count=1
  rg -n 'gorilla/websocket|TunnelClient|AgentTunnel|proxyAttemptViaTunnel|/internal/agent/tunnel' --glob '!docs/superpowers/specs/**' --glob '!docs/superpowers/plans/**'
  ```

  Expected: tests pass; the final `rg` produces no source/dependency hits.

  ```powershell
  git add -A internal/agent internal/gateway internal/protocol go.mod go.sum docs/agents/project-map.md
  git commit -m "refactor: remove websocket worker tunnel"
  ```

### Task 6: Create the reproducible eight-worker FRP Compose deployment

**Files:** `deploy/worker-frp/compose.yaml`, `deploy/worker-frp/.env.example`, `deploy/worker-frp/gateway.test.yaml.example`, `deploy/worker-frp/README.md`, `deploy/worker-frp/verify.sh`, optionally a small `deploy/worker-frp/verify_test.go` only if the project already has a compose validation test convention.

- [ ] **Step 1: Add Compose configuration with one named service per physical GPU.**

  Create services `worker-gpu0` through `worker-gpu7`, each with the same built image, unique `hostname`, `LLMSWAP_AGENT_TAGS=gpu-4090`, and read-only `LLMSWAP_AGENT_TOKEN_FILE=/run/secrets/agent_token`. Use a private network and no published worker port. Pin each using:

  ```yaml
  deploy:
    resources:
      reservations:
        devices:
          - driver: nvidia
            device_ids: ["0"] # increment per service through "7"
            capabilities: [gpu]
  ```

  Build once from `Dockerfile.agent` with `LLMSWAP_RUNTIME=all` and `LLMSWAP_INSTALL_TAILSCALE=0`; reuse the image tag across all eight services. Give every worker a separate `/opt/llmswap` state/log bind mount and a shared models bind mount. The Compose file must contain no FRP address, port, token, llama-swap token, or explicit agent ID.

- [ ] **Step 2: Add a test gateway template.**

  `gateway.test.yaml.example` defines `transport.type: frp_tcp`, placeholders for FRPS address/credentials, range `2000..2007` for this 8-worker simulation, `lease_ttl_seconds: 180`, and a `gpu-4090` tag policy. It must contain no real values. The README states that a real existing model/OSS configuration must be supplied from an authorized source before inference validation; do not invent model artifacts or production credentials.

- [ ] **Step 3: Add offline deployment verification.**

  `verify.sh` must run `docker compose config`, verify eight distinct hostnames and device IDs 0–7, assert no `NVIDIA_VISIBLE_DEVICES`, `LLMSWAP_AGENT_ID`, Tailscale setting, FRPC sidecar, worker host port publishing, or plaintext FRP setting occurs, and optionally call `nvidia-smi -L` on the host. It must never echo env-file/secrets contents.

- [ ] **Step 4: Test the artifacts locally and commit.**

  ```powershell
  bash deploy/worker-frp/verify.sh
  docker compose --env-file deploy/worker-frp/.env.example -f deploy/worker-frp/compose.yaml config
  git add deploy/worker-frp Dockerfile.agent docs/agents/project-map.md README.md examples
  git commit -m "feat: add FRP eight GPU worker deployment"
  ```

### Task 7: Verify, review, and prepare a safe deployment artifact

**Files:** all changed files; update `docs/agents/project-map.md` and `deploy/worker-frp/README.md` only if verification reveals an operational correction.

- [ ] **Step 1: Run the full repository verification in a Go container if the host lacks Go.**

  ```powershell
  docker run --rm -v "${PWD}:/src" -w /src golang:1.23-bookworm go test ./... -count=1
  docker run --rm -v "${PWD}:/src" -w /src golang:1.23-bookworm go vet ./...
  npm --prefix ui/admin ci
  npm --prefix ui/admin run build
  git diff --check
  ```

  If the admin build changes embedded assets, stage those generated `internal/gateway/admin_dist` assets in the same commit that changes the UI/config response contract; otherwise do not regenerate unrelated UI files.

- [ ] **Step 2: Inspect security and compatibility edges.**

  Confirm: direct URLs are absent until transport readiness; encrypted payload fails closed for wrong agent/token/generation; lease store never writes FRP auth token; Admin config GET is redacted; apply keeps an unchanged secret intact; old agents get a protocol/version error rather than silently connecting by tunnel; all direct llama-swap calls retain bearer auth and context cancellation.

- [ ] **Step 3: Request code review and resolve only verified findings.**

  Use `superpowers:requesting-code-review` after tests pass. Triage every finding against current code and tests; do not apply speculative changes. Re-run the affected focused tests and `go test ./... -count=1` after accepted fixes.

- [ ] **Step 4: Create a final implementation commit and retain rollback information.**

  ```powershell
  git status --short
  git log --oneline master..HEAD
  git add -A
  git commit -m "docs: document FRP direct worker operations" # only if documentation changed after prior commits
  ```

  Record the pre-deployment gateway/agent image or commit in the deployment notes. Do not merge or deploy until the user explicitly authorizes the built branch after review.

### Task 8: Perform the authorized real-path simulation on the 8x4090 host

**Precondition:** Task 7 has passed, code has been merged/pushed only after the user authorizes it, and an authorized test gateway YAML plus agent credential are available without printing them.

- [ ] **Step 1: Establish and verify the target host identity.**

  Check `[111.2.199.31]:8230` against existing known-host records. If there is no existing record, accept the host key once using SSH's `StrictHostKeyChecking=accept-new`; if a conflicting key exists, stop and ask the user. Then inspect `nvidia-smi -L`, Docker Engine/Compose versions, NVIDIA Container Toolkit availability, free disk under `/data0/images`, and existing ports in the configured FRP range.

- [ ] **Step 2: Stage source and secrets safely.**

  Under `/data0/images/llm-swap-8x4090`, clone/checkout the authorized commit into `source`, create mode-0700 runtime/secrets directories, and write the supplied agent credential to a mode-0600 secret file. Generate a test gateway configuration from the committed example plus the authorized existing model/OSS settings and FRP values. Do not use shell tracing, `docker compose config` output with secrets, or commands that print the files.

- [ ] **Step 3: Build, launch, and inspect all eight workers.**

  Build on the GPU host with `LLMSWAP_INSTALL_TAILSCALE=0`, run `deploy/worker-frp/verify.sh`, then `docker compose up -d --build`. Confirm all eight containers are running and each reports exactly one visible GPU. Start the test gateway locally on the host using the generated test gateway configuration; preserve its lease-store directory for the duration of the test.

- [ ] **Step 4: Validate the external FRPS hairpin path.**

  Confirm eight current leases use unique slots/ports and the configured external FRPS address. For every worker, request a local llama-swap health endpoint through the gateway-advertised FRP URL and verify it reaches that worker. Then verify the test gateway sees eight non-stale `gpu-4090` workers with direct URLs and no tunnel connection state.

- [ ] **Step 5: Run routed inference validation and record redacted evidence.**

  With an authorized eligible model, issue one non-streaming and one streaming `POST /v1/chat/completions` to the test gateway. Verify request/event records show the intended worker, direct URL operation, valid lease generation, and no sensitive values. If a model artifact cannot be supplied, stop at health/heartbeat/path validation and report inference as an explicit remaining prerequisite rather than claiming it passed.

- [ ] **Step 6: Roll back safely on failure.**

  Stop only this Compose project, release leases through the authenticated API or allow their TTL to expire, retain redacted logs, and restore the recorded prior deployment only if it was modified. Never remove shared model files or unrelated containers.

## Final Acceptance Checklist

- [ ] `go test ./... -count=1`, `go vet ./...`, admin build, Compose config verification, and `git diff --check` pass.
- [ ] No source/dependency reference to Gorilla WebSocket, `TunnelClient`, `AgentTunnel`, or `/internal/agent/tunnel` remains.
- [ ] Gateway config supplies and UI redacts FRP credentials; agent config/Compose does not contain them.
- [ ] Wrong agent identity, wrong token, wrong generation, or tampered payload cannot decrypt a transport bootstrap.
- [ ] Lease allocation is sticky, persistent, unique while live, expiry-safe, generation-bound, and has tested exhaustion behavior.
- [ ] Each of eight worker containers is pinned to one distinct GPU without `NVIDIA_VISIBLE_DEVICES`; each sees only CUDA device 0.
- [ ] The external-FRPS test route proves gateway -> FRPS TCP port -> embedded FRPC -> llama-swap, and routed inference is validated when a real model configuration is available.
