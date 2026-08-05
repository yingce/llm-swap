# Gateway Compose Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consolidate the running llm-swap gateway, PostgreSQL, VictoriaMetrics, and vmagent services under one stable Docker Compose directory without moving or losing persistent data.

**Architecture:** Keep runtime configuration and persistent bind mounts in their existing `/opt/llmswap/{config,state,logs,tailscale,data}` locations. Create `/opt/llmswap/deploy/production` as the stable Compose control directory, copy the current vmagent scrape configuration into it, preserve the running gateway image and secret values in a root-readable `.env`, then let Compose recreate only containers whose definition changed.

**Tech Stack:** Docker Engine, Docker Compose, PostgreSQL 16, VictoriaMetrics, vmagent, llm-swap gateway

## Global Constraints

- Target only `ssh root@llm-swap-gateway` and `/opt/llmswap`.
- Do not expose tokens, database passwords, DSNs, or Tailscale auth keys in logs or repository files.
- Preserve `/opt/llmswap/data/postgres` and `/opt/llmswap/data/victoriametrics` in place.
- Preserve `/opt/llmswap/config`, `/opt/llmswap/state`, `/opt/llmswap/logs`, and `/opt/llmswap/tailscale` in place.
- Do not modify the unrelated `gost` or `derper` containers.
- Keep Compose project name `llmswap` and existing container names.
- Back up the generated deployment directory and database before container replacement.

---

## File Structure

- Create on server: `/opt/llmswap/deploy/production/compose.yaml` — stable definition for all four llm-swap services.
- Create on server: `/opt/llmswap/deploy/production/.env` — root-readable image and secret settings captured from the running containers.
- Create on server: `/opt/llmswap/deploy/production/vmagent/promscrape.yml` — stable scrape configuration, copied from the current live mount.
- Create on server: `/opt/llmswap/deploy/production/README.md` — operator commands, health checks, upgrade procedure, and rollback procedure.
- Create on server: `/opt/llmswap/backups/<timestamp>/` — pre-cutover Compose/config metadata and PostgreSQL logical backup.

### Task 1: Materialize and validate the stable deployment

**Files:**
- Create: `/opt/llmswap/deploy/production/compose.yaml`
- Create: `/opt/llmswap/deploy/production/.env`
- Create: `/opt/llmswap/deploy/production/vmagent/promscrape.yml`
- Create: `/opt/llmswap/deploy/production/README.md`

**Interfaces:**
- Consumes: current container image names, environment values, bind mounts, and `/opt/llmswap/deploy/releases/bfee99c-20260717091504/deploy/production/vmagent/promscrape.yml`.
- Produces: one valid Compose project rooted at `/opt/llmswap/deploy/production`.

- [x] **Step 1: Create the directory with root-only secret permissions**

Run on the gateway server:

```sh
install -d -m 0755 /opt/llmswap/deploy/production/vmagent
install -m 0600 /dev/null /opt/llmswap/deploy/production/.env
```

- [x] **Step 2: Capture current secret values without printing them**

Read `PG_DSN` from `llmswap-gateway` and `POSTGRES_PASSWORD` from `llmswap-records-postgres` using `docker inspect`, then write shell-quoted values plus `LLMSWAP_GATEWAY_IMAGE=llmswap-gateway:tailscale` into `.env`. Verify only variable names and file mode, never values.

- [x] **Step 3: Write the unified Compose definition**

Define `gateway`, `records-postgres`, `victoriametrics`, and `vmagent` with the current container names, restart policies, ports, health checks, and existing absolute bind mounts. Mount vmagent from `./vmagent/promscrape.yml`; keep Gateway dependent on healthy PostgreSQL and started VictoriaMetrics.

- [x] **Step 4: Copy the active scrape configuration and write operator documentation**

Copy the currently mounted vmagent scrape file into the stable directory. Document `docker compose ps`, `up -d`, `logs`, `/healthz`, PostgreSQL `pg_isready`, VictoriaMetrics `/health`, upgrades, and rollback to the backup definition.

- [x] **Step 5: Validate without changing containers**

Run:

```sh
cd /opt/llmswap/deploy/production
docker compose config --quiet
docker compose config --services
```

Expected services: `gateway`, `records-postgres`, `victoriametrics`, `vmagent`.

### Task 2: Back up, cut over, and verify

**Files:**
- Create: `/opt/llmswap/backups/<timestamp>/compose.yaml`
- Create: `/opt/llmswap/backups/<timestamp>/postgres.sql.gz`

**Interfaces:**
- Consumes: validated stable Compose project from Task 1.
- Produces: all four containers managed from one stable Compose config path, with verified persistent data and health.

- [x] **Step 1: Record the current rollback state**

Save `docker compose ls --all`, container image IDs, mounts, and old Compose config paths in the timestamped backup directory. Copy the new Compose files there before cutover.

- [x] **Step 2: Create a PostgreSQL logical backup**

Run `pg_dump` inside `llmswap-records-postgres`, stream it directly through gzip into the timestamped backup directory, and verify the gzip archive with `gzip -t`.

- [x] **Step 3: Apply the stable Compose project**

Run:

```sh
cd /opt/llmswap/deploy/production
docker compose up -d --remove-orphans
```

Do not run `docker compose down`; Compose should reuse the existing project network and preserve bind-mounted data.

- [x] **Step 4: Verify service and data health**

Require all of the following:

```sh
docker compose ps
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8428/health
docker exec llmswap-records-postgres pg_isready -U llmswap -d llmswap
docker compose logs --since=5m gateway records-postgres victoriametrics vmagent
```

Also verify that `docker compose ls --all` shows exactly one llmswap config file rooted at `/opt/llmswap/deploy/production/compose.yaml`, and that every llmswap container's Compose working directory is `/opt/llmswap/deploy/production`.

- [ ] **Step 5: Roll back if verification fails**

Use the recorded image IDs and old definitions to restore the prior containers while leaving all bind-mounted data untouched. Do not restore the PostgreSQL dump unless the database itself is proven corrupt; container rollback should normally be sufficient.

## Self-Review

- Scope covers the four llmswap services and excludes unrelated containers.
- Persistent data remains at its existing absolute paths.
- Secrets are written only to a root-readable server file and are not copied into the repository.
- Validation occurs before cutover, and both logical backup and container-level rollback are defined.
- Success criteria explicitly detect the current split-Compose condition.
