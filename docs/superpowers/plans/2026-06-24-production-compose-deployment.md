# Production Compose Deployment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy the production gateway with Docker Compose and include optional VictoriaMetrics history storage.

**Architecture:** Add a production multi-stage gateway Dockerfile plus a compose stack for gateway, VictoriaMetrics, and vmagent. Production mounts `/opt/llmswap/gateway.yaml`, `/opt/llmswap/logs`, and `/opt/llmswap/data/victoriametrics` so config and data survive container replacement.

**Tech Stack:** Go 1.23, Docker Engine, Docker Compose plugin, VictoriaMetrics, vmagent.

---

### Task 1: Add Production Docker Assets

**Files:**
- Create: `Dockerfile.gateway`
- Create: `deploy/production/compose.yaml`
- Create: `deploy/production/vmagent/promscrape.yml`
- Modify: `docs/agents/project-map.md`

- [ ] **Step 1: Create a multi-stage gateway Dockerfile**

Create `Dockerfile.gateway` that builds `./cmd/gateway` with `CGO_ENABLED=0`
and copies it into a Debian bookworm slim runtime with certificates.

- [ ] **Step 2: Create production compose stack**

Create `deploy/production/compose.yaml` with `gateway`,
`victoriametrics`, and `vmagent`. Mount `/opt/llmswap/gateway.yaml` read-only,
`/opt/llmswap/logs`, and `/opt/llmswap/data/victoriametrics`.

- [ ] **Step 3: Create vmagent scrape config**

Create `deploy/production/vmagent/promscrape.yml` scraping
`gateway:8080/metrics`.

- [ ] **Step 4: Document deployment**

Update `docs/agents/project-map.md` with the production compose paths and
basic deployment command.

- [ ] **Step 5: Verify and commit**

Run:

```bash
go test ./...
docker compose -f deploy/production/compose.yaml config
```

Commit:

```bash
git add Dockerfile.gateway deploy/production docs/agents/project-map.md
git commit -m "feat: add production compose deployment"
```

### Task 2: Build and Deploy to Production

**Files on production:**
- `/opt/llmswap/deploy/current`
- `/opt/llmswap/gateway.yaml`
- `/opt/llmswap/logs`
- `/opt/llmswap/data/victoriametrics`

- [ ] **Step 1: Verify Docker availability**

Run on `47.84.130.96`:

```bash
docker --version
docker compose version
```

Install Docker Engine and compose plugin if either command is missing.

- [ ] **Step 2: Upload source tree**

Archive local `HEAD` plus working deployment files and upload to
`/opt/llmswap/deploy/current`.

- [ ] **Step 3: Enable metrics store in production config**

Back up `/opt/llmswap/gateway.yaml`, then set:

```yaml
metrics_store:
  enabled: true
  type: victoriametrics
  query_url: http://victoriametrics:8428
  default_range: 1h
  max_range: 7d
  timeout_ms: 3000
```

- [ ] **Step 4: Switch process manager**

Stop supervisor `llmswap-gateway`, start compose:

```bash
supervisorctl stop llmswap-gateway
cd /opt/llmswap/deploy/current
docker compose -f deploy/production/compose.yaml up -d --build
```

- [ ] **Step 5: Verify production**

Run:

```bash
docker compose -f deploy/production/compose.yaml ps
curl -fsS http://127.0.0.1:8080/metrics
curl -fsS -H "Authorization: Bearer <agent-token>" http://127.0.0.1:8080/ui/status
```

Check gateway logs:

```bash
docker compose -f deploy/production/compose.yaml logs --tail=80 gateway
```
