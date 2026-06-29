# Agent Image llama-swap Release Download Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the agent image's upstream source build for `llama-swap` with download-and-extract of the official release artifact while preserving explicit override behavior.

**Architecture:** Keep `llm-swap-agent` compiled from the local repository, but simplify `Dockerfile.agent` so `llama-swap` comes from a deterministic release tarball URL derived from `LLAMA_SWAP_REF`. Preserve `LLAMA_SWAP_DOWNLOAD_URL` as an explicit build-time override, and keep runtime override behavior in `scripts/agent-container-entrypoint.sh` unchanged.

**Tech Stack:** Docker multi-stage build, Bash, curl, tar, existing agent container entrypoint, markdown docs.

---

### Task 1: Replace source-build stages with release download in `Dockerfile.agent`

**Files:**
- Modify: `Dockerfile.agent`

- [ ] **Step 1: Write the failing verification loop**

Run:

```bash
docker build -f Dockerfile.agent --build-arg LLMSWAP_RUNTIME=llamacpp --build-arg LLMSWAP_INSTALL_TAILSCALE=0 -t llmswap-agent-release-smoke .
```

Expected before the change: the build still spends time in `llama-swap-ui-build` / `llama-swap-build` or otherwise proves the image is compiling upstream source instead of downloading a release asset.

- [ ] **Step 2: Remove the upstream source/UI stages and add release download variables**

Update `Dockerfile.agent` so it no longer defines `NODE_BUILD_IMAGE`, `llama-swap-ui-build`, or `llama-swap-build`, and instead defines a release URL template:

```dockerfile
ARG LLAMA_SWAP_REF=v232
ARG LLAMA_SWAP_RELEASE_URL=
```

The effective download URL must resolve like:

```dockerfile
https://github.com/mostlygeek/llama-swap/releases/download/${LLAMA_SWAP_REF}/llama-swap_${LLAMA_SWAP_REF#v}_linux_amd64.tar.gz
```

- [ ] **Step 3: Download and extract the release artifact into the final image**

Replace the previous source-build copy step with a direct download in the final stage:

```dockerfile
RUN release_url="${LLAMA_SWAP_RELEASE_URL:-https://github.com/mostlygeek/llama-swap/releases/download/${LLAMA_SWAP_REF}/llama-swap_${LLAMA_SWAP_REF#v}_linux_amd64.tar.gz}" \
 && curl -fL --retry 5 --retry-delay 5 -o /tmp/llama-swap-release.tar.gz "$release_url" \
 && tar -C /tmp -xzf /tmp/llama-swap-release.tar.gz \
 && install -m 0755 /tmp/llama-swap "${LLMSWAP_ROOT}/bin/llama-swap.bundled" \
 && install -m 0755 /tmp/llama-swap "${LLMSWAP_ROOT}/bin/llama-swap" \
 && rm -f /tmp/llama-swap /tmp/llama-swap-release.tar.gz
```

Preserve the later `LLAMA_SWAP_DOWNLOAD_URL` override block so explicit build-time override still wins.

- [ ] **Step 4: Run the docker build again**

Run:

```bash
docker build -f Dockerfile.agent --build-arg LLMSWAP_RUNTIME=llamacpp --build-arg LLMSWAP_INSTALL_TAILSCALE=0 -t llmswap-agent-release-smoke .
```

Expected: PASS, and the logs show a direct `curl`/`tar` release download instead of upstream UI/source compilation.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile.agent
git commit -m "fix: use llama-swap release in agent image"
```

### Task 2: Update container-image docs to match the new release contract

**Files:**
- Modify: `docs/agents/project-map.md`

- [ ] **Step 1: Write the failing documentation check**

Run:

```bash
rg -n "builds `llama-swap` from an upstream source archive|LLAMA_SWAP_SOURCE_ARCHIVE_URL|llama-swap-ui-build|llama-swap-build" docs/agents/project-map.md
```

Expected before the change: matches still describe source-archive build behavior.

- [ ] **Step 2: Update the image contract text**

Replace the source-build description with release-download wording:

```md
- By default the image downloads the official `llama-swap` release artifact during Docker build.
- The default URL is `https://github.com/mostlygeek/llama-swap/releases/download/${LLAMA_SWAP_REF}/llama-swap_${LLAMA_SWAP_REF#v}_linux_amd64.tar.gz`.
- `LLAMA_SWAP_RELEASE_URL` can override the exact release artifact URL.
```

- [ ] **Step 3: Update the sample build command**

Adjust the sample to show `LLAMA_SWAP_REF` or `LLAMA_SWAP_RELEASE_URL`, and remove references to source-archive build inputs.

- [ ] **Step 4: Run the documentation check again**

Run:

```bash
rg -n "builds `llama-swap` from an upstream source archive|LLAMA_SWAP_SOURCE_ARCHIVE_URL|llama-swap-ui-build|llama-swap-build" docs/agents/project-map.md
```

Expected: no matches.

- [ ] **Step 5: Commit**

```bash
git add docs/agents/project-map.md
git commit -m "docs: update agent image llama-swap release contract"
```

### Task 3: Verify the final image contains bundled `llama-swap`

**Files:**
- Verify: `Dockerfile.agent`
- Verify: `scripts/agent-container-entrypoint.sh`

- [ ] **Step 1: Run the final container smoke check**

Run:

```bash
docker run --rm \
  -e LLMSWAP_GATEWAY_URL=http://gateway.example.local:8080 \
  -e LLMSWAP_AGENT_TOKEN=test-token \
  --entrypoint bash llmswap-agent-release-smoke \
  -lc 'test -x /opt/llmswap/bin/llama-swap.bundled && test -x /opt/llmswap/bin/llama-swap && echo bundled-ok'
```

Expected: prints `bundled-ok`.

- [ ] **Step 2: Confirm the working tree and push**

Run:

```bash
git status -sb
$env:GIT_SSH_COMMAND='ssh -i C:/Users/admin/.ssh/id_rsa.git -o IdentitiesOnly=yes'; git push origin master
```

Expected: local branch clean or only intentionally staged changes, then push succeeds.
