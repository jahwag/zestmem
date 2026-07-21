#!/bin/sh
set -eu

if [ -z "${MEMORY_URL:-}" ] || [ -z "${MEMORY_TOKEN:-}" ]; then
  exit 0
fi

base="${MEMORY_URL%/}"
context_url="${base}/bootstrap/context"
if [ -n "${MEMORY_STARTUP_SPACE_ID:-}" ]; then
	context_url="${context_url}?space_id=${MEMORY_STARTUP_SPACE_ID}"
fi
if ! catalog="$(curl --fail --silent --show-error --max-time 10 \
	--config - "$context_url" <<EOF
header = "Authorization: Bearer ${MEMORY_TOKEN}"
EOF
)"; then
	catalog=""
fi

if [ -n "$catalog" ]; then
	printf '%s\n' "$catalog"
fi
