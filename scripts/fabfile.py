from __future__ import annotations

import subprocess
import tempfile
import textwrap
import uuid
from datetime import datetime
from pathlib import Path

from fabric import Connection, task


DEFAULT_HOST = "root@8.141.111.101"
APP_ROOT = "/opt/llmswap"
DEPLOY_ROOT = f"{APP_ROOT}/deploy"
CURRENT_DIR = f"{DEPLOY_ROOT}/current"
RELEASES_DIR = f"{DEPLOY_ROOT}/releases"
BACKUPS_DIR = f"{DEPLOY_ROOT}/backups"
BUILD_CACHE_DIR = f"{DEPLOY_ROOT}/cache"
IMAGE = "llmswap-gateway:tailscale"
CONTAINER = "llmswap-gateway"
GO_IMAGE = "golang:1.23-bookworm"
COMPOSE_PROJECT = "llmswap"


def repo_root() -> Path:
    return Path(__file__).resolve().parents[1]


def local_output(*args: str) -> str:
    return subprocess.check_output(args, cwd=repo_root(), text=True).strip()


def local_run(*args: str) -> None:
    subprocess.run(args, cwd=repo_root(), check=True)


def remote_bash(conn: Connection, script: str) -> None:
    rendered = textwrap.dedent(script).lstrip()
    local_path = Path(tempfile.gettempdir()) / f"llmswap-fab-{uuid.uuid4().hex}.sh"
    remote_path = f"/tmp/{local_path.name}"
    local_path.write_text(rendered, encoding="utf-8", newline="\n")
    try:
        conn.put(str(local_path), remote=remote_path)
        conn.run(f"chmod 0700 {remote_path} && bash {remote_path}", pty=False)
    finally:
        try:
            conn.run(f"rm -f {remote_path}", hide=True, warn=True)
        finally:
            local_path.unlink(missing_ok=True)


def make_archive(commit: str) -> Path:
    deploy_dir = repo_root() / ".deploy"
    deploy_dir.mkdir(exist_ok=True)
    archive = deploy_dir / f"llm-swap-{commit}.tar"
    if archive.exists():
        archive.unlink()
    local_run("git", "archive", "--format=tar", "HEAD", "-o", str(archive))
    return archive


def build_time() -> str:
    return datetime.now().astimezone().isoformat(timespec="seconds")


@task
def status(ctx, host: str = DEFAULT_HOST) -> None:
    """Show production gateway container and health status."""
    conn = Connection(host)
    remote_bash(
        conn,
        f"""
        set -euo pipefail
        hostname
        docker ps --filter name={CONTAINER} --format 'table {{{{.Names}}}}\\t{{{{.Image}}}}\\t{{{{.Status}}}}\\t{{{{.Ports}}}}'
        printf 'healthz='
        curl -s -o /dev/null -w '%{{http_code}}\\n' http://127.0.0.1:8080/healthz || true
        if [ -f {DEPLOY_ROOT}/.deployed-commit ]; then
          printf 'deployed_commit='
          cat {DEPLOY_ROOT}/.deployed-commit
          printf '\\n'
        fi
        """,
    )


@task
def logs(ctx, host: str = DEFAULT_HOST, lines: int = 120) -> None:
    """Tail production gateway container logs."""
    conn = Connection(host)
    conn.run(f"docker logs --tail={int(lines)} {CONTAINER}", pty=False)


@task
def deploy(ctx, host: str = DEFAULT_HOST, skip_tests: bool = False) -> None:
    """Deploy the current git HEAD to the production gateway host."""
    commit = local_output("git", "rev-parse", "--short", "HEAD")
    full_commit = local_output("git", "rev-parse", "HEAD")
    dirty = local_output("git", "status", "--porcelain")
    if dirty:
        print("Local tree has uncommitted files; git archive will deploy committed HEAD only:")
        print(dirty)

    archive = make_archive(commit)
    stamp = datetime.now().strftime("%Y%m%d%H%M%S")
    remote_archive = f"/tmp/llm-swap-{commit}-{stamp}.tar"
    release_dir = f"{RELEASES_DIR}/{commit}-{stamp}"
    remote_build_time = build_time()
    test_flag = "0" if skip_tests else "1"

    conn = Connection(host)
    conn.run(f"mkdir -p {RELEASES_DIR} {BACKUPS_DIR} {BUILD_CACHE_DIR}")
    conn.put(str(archive), remote=remote_archive)

    remote_bash(
        conn,
        f"""
        set -euo pipefail

        APP_ROOT={APP_ROOT}
        DEPLOY_ROOT={DEPLOY_ROOT}
        CURRENT_DIR={CURRENT_DIR}
        BACKUPS_DIR={BACKUPS_DIR}
        RELEASE_DIR={release_dir}
        BUILD_CACHE_DIR={BUILD_CACHE_DIR}
        GATEWAY_CONTEXT="$APP_ROOT/docker-gateway-tailscale"
        COMPOSE_FILE="$RELEASE_DIR/deploy/production/compose.yaml"
        IMAGE={IMAGE}
        CONTAINER={CONTAINER}
        COMPOSE_PROJECT={COMPOSE_PROJECT}
        GO_IMAGE={GO_IMAGE}
        COMMIT={commit}
        FULL_COMMIT={full_commit}
        BUILD_TIME={remote_build_time}
        RUN_TESTS={test_flag}

        install -d "$APP_ROOT/logs" "$APP_ROOT/tailscale" "$APP_ROOT/data" "$RELEASE_DIR" "$BUILD_CACHE_DIR/go-build" "$BUILD_CACHE_DIR/go-mod" "$GATEWAY_CONTEXT"
        if [ ! -f "$APP_ROOT/gateway.yaml" ]; then
          echo "missing $APP_ROOT/gateway.yaml; refusing to deploy over an unconfigured gateway" >&2
          exit 1
        fi

        tar -xf {remote_archive} -C "$RELEASE_DIR"
        rm -f {remote_archive}

        if [ "$RUN_TESTS" = "1" ]; then
          docker run --rm \\
            -v "$RELEASE_DIR:/src" \\
            -v "$BUILD_CACHE_DIR/go-build:/root/.cache/go-build" \\
            -v "$BUILD_CACHE_DIR/go-mod:/go/pkg/mod" \\
            -w /src "$GO_IMAGE" \\
            go test ./internal/gateway ./cmd/gateway -count=1
        fi

        docker run --rm \\
          -v "$RELEASE_DIR:/src" \\
          -v "$BUILD_CACHE_DIR/go-build:/root/.cache/go-build" \\
          -v "$BUILD_CACHE_DIR/go-mod:/go/pkg/mod" \\
          -w /src \\
          -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=amd64 \\
          -e LLMSWAP_BUILD_VERSION="$COMMIT" \\
          -e LLMSWAP_BUILD_COMMIT="$COMMIT" \\
          -e LLMSWAP_BUILD_TIME="$BUILD_TIME" \\
          "$GO_IMAGE" bash -lc 'mkdir -p dist && go build -ldflags "-X llm-swap/internal/buildinfo.Version=$LLMSWAP_BUILD_VERSION -X llm-swap/internal/buildinfo.Commit=$LLMSWAP_BUILD_COMMIT -X llm-swap/internal/buildinfo.BuildTime=$LLMSWAP_BUILD_TIME" -o dist/llm-swap-gateway ./cmd/gateway'

        install -m 0755 "$RELEASE_DIR/dist/llm-swap-gateway" "$GATEWAY_CONTEXT/llm-swap-gateway"

        if [ ! -x "$GATEWAY_CONTEXT/tailscale" ] || [ ! -x "$GATEWAY_CONTEXT/tailscaled" ]; then
          if docker ps -a --format '{{{{.Names}}}}' | grep -Fxq "$CONTAINER"; then
            docker cp "$CONTAINER:/usr/bin/tailscale" "$GATEWAY_CONTEXT/tailscale" || true
            docker cp "$CONTAINER:/usr/sbin/tailscaled" "$GATEWAY_CONTEXT/tailscaled" || true
          fi
        fi
        if [ ! -x "$GATEWAY_CONTEXT/tailscale" ] || [ ! -x "$GATEWAY_CONTEXT/tailscaled" ]; then
          echo "missing tailscale/tailscaled in $GATEWAY_CONTEXT" >&2
          exit 1
        fi
        chmod 0755 "$GATEWAY_CONTEXT/tailscale" "$GATEWAY_CONTEXT/tailscaled"

        cat > "$GATEWAY_CONTEXT/entrypoint.sh" <<'ENTRYPOINT'
#!/usr/bin/env sh
set -eu
ROOT="${{LLMSWAP_ROOT:-/opt/llmswap}}"
LOG_DIR="${{LLMSWAP_LOG_DIR:-$ROOT/logs}}"
STATE_DIR="${{LLMSWAP_TAILSCALE_STATE_DIR:-$ROOT/tailscale}}"
SOCKET="${{LLMSWAP_TAILSCALE_SOCKET:-/run/tailscale/tailscaled.sock}}"
TUN="${{LLMSWAP_TAILSCALE_TUN:-tun}}"
PORT="${{LLMSWAP_TAILSCALE_PORT:-41641}}"
CONFIG="${{LLMSWAP_GATEWAY_CONFIG:-$ROOT/gateway.yaml}}"
mkdir -p "$LOG_DIR" "$STATE_DIR" "$(dirname "$SOCKET")"
if [ "${{LLMSWAP_ENABLE_TAILSCALE:-0}}" = "1" ] || [ -n "${{LLMSWAP_TAILSCALE_AUTHKEY_FILE:-}}" ] || [ -n "${{LLMSWAP_TAILSCALE_HOSTNAME:-}}" ]; then
  tailscaled --state="$STATE_DIR/tailscaled.state" --socket="$SOCKET" --port="$PORT" --tun="$TUN" >>"$LOG_DIR/tailscaled.out.log" 2>>"$LOG_DIR/tailscaled.err.log" &
  i=0
  while [ ! -S "$SOCKET" ]; do
    i=$((i+1))
    if [ "$i" -ge 30 ]; then
      echo "tailscaled did not create $SOCKET in time" >&2
      exit 1
    fi
    sleep 1
  done
  if [ -n "${{LLMSWAP_TAILSCALE_AUTHKEY_FILE:-}}" ] && [ -s "$LLMSWAP_TAILSCALE_AUTHKEY_FILE" ]; then
    key="$(cat "$LLMSWAP_TAILSCALE_AUTHKEY_FILE")"
    tailscale --socket="$SOCKET" login --auth-key "$key" >>"$LOG_DIR/tailscale-init.out.log" 2>>"$LOG_DIR/tailscale-init.err.log" || true
    rm -f "$LLMSWAP_TAILSCALE_AUTHKEY_FILE" || true
  fi
  if [ -n "${{LLMSWAP_TAILSCALE_HOSTNAME:-}}" ]; then
    tailscale --socket="$SOCKET" set --hostname "$LLMSWAP_TAILSCALE_HOSTNAME" >>"$LOG_DIR/tailscale-init.out.log" 2>>"$LOG_DIR/tailscale-init.err.log" || true
  fi
fi
exec /usr/local/bin/llm-swap-gateway --config "$CONFIG"
ENTRYPOINT
        chmod 0755 "$GATEWAY_CONTEXT/entrypoint.sh"

        cat > "$GATEWAY_CONTEXT/Dockerfile" <<'DOCKERFILE'
FROM golang:1.23-bookworm
WORKDIR /opt/llmswap
COPY llm-swap-gateway /usr/local/bin/llm-swap-gateway
COPY tailscale /usr/bin/tailscale
COPY tailscaled /usr/sbin/tailscaled
COPY entrypoint.sh /usr/local/bin/gateway-entrypoint.sh
RUN chmod 0755 /usr/local/bin/llm-swap-gateway /usr/bin/tailscale /usr/sbin/tailscaled /usr/local/bin/gateway-entrypoint.sh \
 && mkdir -p /opt/llmswap/logs /opt/llmswap/tailscale /run/tailscale
ENV LLMSWAP_GATEWAY_CONFIG=/opt/llmswap/gateway.yaml
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/gateway-entrypoint.sh"]
DOCKERFILE

        docker build -t "$IMAGE" "$GATEWAY_CONTEXT"

        had_previous=0
        if docker ps -a --format '{{{{.Names}}}}' | grep -Fxq "$CONTAINER"; then
          docker rm -f "$CONTAINER.previous" >/dev/null 2>&1 || true
          had_previous=1
          docker rename "$CONTAINER" "$CONTAINER.previous"
          docker update --restart=no "$CONTAINER.previous" >/dev/null
          docker stop "$CONTAINER.previous" >/dev/null
        fi

        set +e
        LLMSWAP_GATEWAY_IMAGE="$IMAGE" docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" up -d --no-deps --force-recreate gateway
        run_status=$?
        set -e
        if [ "$run_status" -ne 0 ]; then
          if [ "$had_previous" = "1" ]; then docker start "$CONTAINER.previous" >/dev/null || true; fi
          exit "$run_status"
        fi

        healthy=0
        for i in $(seq 1 40); do
          code="$(curl -s -o /dev/null -w '%{{http_code}}' http://127.0.0.1:8080/healthz || true)"
          if [ "$code" = "204" ]; then
            healthy=1
            break
          fi
          sleep 2
        done

        if [ "$healthy" != "1" ]; then
          echo "new gateway failed health check; rolling back" >&2
          docker logs --tail=120 "$CONTAINER" >&2 || true
          LLMSWAP_GATEWAY_IMAGE="$IMAGE" docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" rm -sf gateway >/dev/null 2>&1 || docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
          if [ "$had_previous" = "1" ]; then
            docker start "$CONTAINER.previous" >/dev/null
            docker rename "$CONTAINER.previous" "$CONTAINER"
          fi
          exit 1
        fi

        if [ -d "$CURRENT_DIR" ] && [ ! -L "$CURRENT_DIR" ]; then
          mv "$CURRENT_DIR" "$BACKUPS_DIR/current-before-$COMMIT-$BUILD_TIME"
        else
          rm -rf "$CURRENT_DIR"
        fi
        cp -a "$RELEASE_DIR" "$CURRENT_DIR"

        printf '%s\\n' "$FULL_COMMIT" > "$DEPLOY_ROOT/.deployed-commit"
        printf '%s\\n' "$BUILD_TIME" > "$DEPLOY_ROOT/.deployed-at"

        docker ps --filter name="$CONTAINER" --format 'table {{{{.Names}}}}\\t{{{{.Image}}}}\\t{{{{.Status}}}}\\t{{{{.Ports}}}}'
        curl -s -o /dev/null -w 'healthz=%{{http_code}}\\n' http://127.0.0.1:8080/healthz
        """,
    )


@task
def rollback(ctx, host: str = DEFAULT_HOST) -> None:
    """Start the saved previous gateway container if one exists."""
    conn = Connection(host)
    remote_bash(
        conn,
        f"""
        set -euo pipefail
        if ! docker ps -a --format '{{{{.Names}}}}' | grep -Fxq {CONTAINER}.previous; then
          echo "no {CONTAINER}.previous container found" >&2
          exit 1
        fi
        docker rm -f {CONTAINER} >/dev/null 2>&1 || true
        docker start {CONTAINER}.previous >/dev/null
        docker rename {CONTAINER}.previous {CONTAINER}
        curl -s -o /dev/null -w 'healthz=%{{http_code}}\\n' http://127.0.0.1:8080/healthz
        """,
    )
