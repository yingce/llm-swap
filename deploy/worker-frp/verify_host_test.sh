#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
verify="$script_dir/verify.sh"
test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT

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

new_fixture valid
expect_pass valid "$verify" "$fixture/.env"

new_fixture digest registry.local:5000/llmswap/agent@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
expect_pass digest "$verify" "$fixture/.env"

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

printf '%s\n' 'verify host safety tests passed'
