#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" != "1" ]]; then
  printf '%s\n' 'usage: verify_image_rollback.sh ROLLBACK_RECORD' >&2
  exit 1
fi

record="$1"
if [[ ! -f "$record" || -L "$record" || ! -r "$record" ]]; then
  printf '%s\n' 'rollback image record must be a readable regular non-symlink file' >&2
  exit 1
fi

record_mode="$(stat -c '%a' -- "$record" 2>/dev/null || true)"
if [[ ! "$record_mode" =~ ^[0-7]{3,4}$ ]] || (( (8#$record_mode & 077) != 0 )); then
  printf '%s\n' 'rollback image record must grant no permissions to group or other' >&2
  exit 1
fi

OLD_WORKER_IMAGE=""
recorded_image_id=""
image_count=0
id_count=0
while IFS='=' read -r key value; do
  case "$key" in
    WORKER_IMAGE)
      OLD_WORKER_IMAGE="$value"
      image_count=$((image_count + 1))
      ;;
    WORKER_IMAGE_ID)
      recorded_image_id="$value"
      id_count=$((id_count + 1))
      ;;
  esac
done <"$record"

if [[ "$image_count" != "1" || "$id_count" != "1" ||
      ! "$recorded_image_id" =~ ^sha256:[0-9a-fA-F]{64}$ ]]; then
  printf '%s\n' 'rollback image record is invalid' >&2
  exit 1
fi

image_leaf="${OLD_WORKER_IMAGE##*/}"
image_tag="${image_leaf##*:}"
if [[ "$OLD_WORKER_IMAGE" =~ @sha256:[0-9a-fA-F]{64}$ ]]; then
  :
elif [[ "$image_leaf" == "$image_tag" || ! "$image_tag" =~ [0-9a-fA-F]{7,}$ || "$image_tag" == "latest" ]]; then
  printf '%s\n' 'rollback image reference is not immutable' >&2
  exit 1
fi

if ! resolved_image_id="$(docker image inspect --format '{{.Id}}' "$OLD_WORKER_IMAGE" 2>/dev/null)"; then
  printf '%s\n' 'rollback image reference is unavailable' >&2
  exit 1
fi
if [[ "$resolved_image_id" != "$recorded_image_id" ]]; then
  printf '%s\n' 'rollback image identity mismatch; refusing rollback' >&2
  exit 1
fi

printf '%s\n' 'Rollback image identity verified.'
