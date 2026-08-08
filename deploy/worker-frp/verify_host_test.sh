#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
verify="$script_dir/verify.sh"
verify_rollback="$script_dir/verify_image_rollback.sh"
test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT
real_docker="$(command -v docker)"
original_path="$PATH"
fake_bin="$test_root/bin"
mkdir -p -- "$fake_bin"
cat >"$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "image" && "${2:-}" == "inspect" && "${3:-}" == *@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef ]]; then
  exit 0
fi
if [[ "${1:-}" == "image" && "${2:-}" == "inspect" && "${*: -1}" == "registry.local:5000/llmswap/agent:frp-deadbeef" ]]; then
  if [[ " $* " == *" --format "* ]]; then
    printf '%s\n' 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
  fi
  exit 0
fi
exec "$DOCKER_REAL" "$@"
EOF
chmod 0755 "$fake_bin/docker"

new_fixture() {
  local name="$1"
  local image="${2:-registry.local:5000/llmswap/agent:frp-abcdef1}"
  fixture="$test_root/$name"
  mkdir -p -- "$fixture/state" "$fixture/models" "$fixture/secrets"
  for gpu in 0 1 2 3 4 5 6 7; do
    mkdir -p -- "$fixture/state/worker-gpu${gpu}/logs"
  done
  printf '%s' 'fixture-agent-token' >"$fixture/secrets/agent-token"
  chmod 0600 "$fixture/secrets/agent-token"
  cat >"$fixture/.env" <<EOF
WORKER_IMAGE=$image
LLMSWAP_BUILD_VERSION=2026.08.08.1
LLMSWAP_BUILD_COMMIT=abcdef1234567890
LLMSWAP_GATEWAY_URL=http://gateway-host:8080
WORKER_STATE_ROOT=$fixture/state
MODEL_ROOT=$fixture/models
AGENT_TOKEN_FILE=$fixture/secrets/agent-token
EOF
}

expect_pass() {
  local name="$1"
  shift
  if ! "$@" >"$test_root/$name.output" 2>&1; then
    printf 'expected pass: %s\n' "$name" >&2
    sed -n '1,20p' "$test_root/$name.output" >&2
    exit 1
  fi
}

expect_fail() {
  local name="$1"
  shift
  if "$@" >"$test_root/$name.output" 2>&1; then
    printf 'expected failure: %s\n' "$name" >&2
    exit 1
  fi
}

expect_output_contains() {
  local name="$1"
  local expected="$2"
  if ! grep -Fq -- "$expected" "$test_root/$name.output"; then
    printf 'expected output marker missing: %s\n' "$name" >&2
    exit 1
  fi
}

new_fixture valid
expect_pass valid "$verify" "$fixture/.env"
expect_output_contains valid 'Image mode: commit-tag build path.'

new_fixture duplicated-build-identity
sed -i 's/^LLMSWAP_BUILD_VERSION=.*/LLMSWAP_BUILD_VERSION=abcdef1234567890/' "$fixture/.env"
expect_fail duplicated-build-identity "$verify" "$fixture/.env"

new_fixture digest registry.local:5000/llmswap/agent@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
expect_pass digest env PATH="$fake_bin:$original_path" DOCKER_REAL="$real_docker" "$verify" "$fixture/.env"
expect_output_contains digest 'Image mode: preloaded digest deployment path.'

new_fixture digest-not-loaded registry.local:5000/llmswap/agent@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
expect_fail digest-not-loaded "$verify" "$fixture/.env"

new_fixture missing-log
rm -rf -- "$fixture/state/worker-gpu7/logs"
expect_fail missing-log "$verify" "$fixture/.env"

new_fixture missing-token
rm -f -- "$fixture/secrets/agent-token"
expect_fail missing-token "$verify" "$fixture/.env"

new_fixture symlink-token
mv -- "$fixture/secrets/agent-token" "$fixture/secrets/agent-token.real"
ln -s -- "$fixture/secrets/agent-token.real" "$fixture/secrets/agent-token"
expect_fail symlink-token "$verify" "$fixture/.env"

new_fixture symlink-model-root
mv -- "$fixture/models" "$fixture/models.real"
ln -s -- "$fixture/models.real" "$fixture/models"
expect_fail symlink-model-root "$verify" "$fixture/.env"

new_fixture relative-state-root
cat >"$fixture/.env" <<EOF
WORKER_IMAGE=registry.local:5000/llmswap/agent:frp-abcdef1
LLMSWAP_BUILD_VERSION=2026.08.08.1
LLMSWAP_BUILD_COMMIT=abcdef1234567890
LLMSWAP_GATEWAY_URL=http://gateway-host:8080
WORKER_STATE_ROOT=relative/state
MODEL_ROOT=$fixture/models
AGENT_TOKEN_FILE=$fixture/secrets/agent-token
EOF
expect_fail relative-state-root "$verify" "$fixture/.env"

new_fixture public-token
chmod 0644 "$fixture/secrets/agent-token"
expect_fail public-token "$verify" "$fixture/.env"

if [[ "$(id -u)" == "0" ]]; then
  new_fixture wrong-owner-token
  chown 12345 "$fixture/secrets/agent-token"
  expect_fail wrong-owner-token "$verify" "$fixture/.env"
fi

new_fixture empty-token
: >"$fixture/secrets/agent-token"
chmod 0600 "$fixture/secrets/agent-token"
expect_fail empty-token "$verify" "$fixture/.env"

new_fixture whitespace-token
printf '  \n' >"$fixture/secrets/agent-token"
chmod 0600 "$fixture/secrets/agent-token"
expect_fail whitespace-token "$verify" "$fixture/.env"

new_fixture oversized-token
head -c 16385 /dev/zero >"$fixture/secrets/agent-token"
chmod 0600 "$fixture/secrets/agent-token"
expect_fail oversized-token "$verify" "$fixture/.env"

new_fixture mutable-tag llmswap-agent:frp-cu128
expect_fail mutable-tag "$verify" "$fixture/.env"

new_fixture latest-tag registry.local:5000/llmswap/agent:latest
expect_fail latest-tag "$verify" "$fixture/.env"

new_fixture short-commit-tag registry.local:5000/llmswap/agent:frp-abc123
expect_fail short-commit-tag "$verify" "$fixture/.env"

expect_fail missing-default-env "$verify"
expect_fail placeholder-example "$verify" "$script_dir/.env.example"

rollback_record="$test_root/rollback-image.env"
cat >"$rollback_record" <<'EOF'
WORKER_IMAGE=registry.local:5000/llmswap/agent:frp-deadbeef
WORKER_IMAGE_ID=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
EOF
chmod 0600 "$rollback_record"
expect_pass rollback-match env PATH="$fake_bin:$original_path" DOCKER_REAL="$real_docker" "$verify_rollback" "$rollback_record"

cat >"$rollback_record" <<'EOF'
WORKER_IMAGE=registry.local:5000/llmswap/agent:frp-deadbeef
WORKER_IMAGE_ID=sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
EOF
chmod 0600 "$rollback_record"
expect_fail rollback-moved-tag env PATH="$fake_bin:$original_path" DOCKER_REAL="$real_docker" "$verify_rollback" "$rollback_record"

printf '%s\n' 'verify host safety tests passed'
