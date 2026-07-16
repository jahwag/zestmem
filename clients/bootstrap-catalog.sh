#!/bin/sh
set -eu

root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [ -r "$root/.memory-client.env" ]; then
  set -a
  . "$root/.memory-client.env"
  set +a
  case "${MEMORY_PROFILE:-}" in
    codex) MEMORY_TOKEN="${MEMORY_TOKEN_CODEX:-${MEMORY_TOKEN:-}}" ;;
    claude) MEMORY_TOKEN="${MEMORY_TOKEN_CLAUDE:-${MEMORY_TOKEN:-}}" ;;
  esac
  export MEMORY_TOKEN
fi

if [ -z "${MEMORY_URL:-}" ] || [ -z "${MEMORY_TOKEN:-}" ]; then
  exit 0
fi

base="${MEMORY_URL%/}"
remote=""
if command -v git >/dev/null 2>&1; then
  remote="$(git config --get remote.origin.url 2>/dev/null || true)"
fi

if [ -n "$remote" ]; then
  curl --fail --silent --show-error --max-time 10 --get \
    --header "Authorization: Bearer ${MEMORY_TOKEN}" --data-urlencode "git_remote=${remote}" \
    "${base}/bootstrap/catalog" || true
else
  curl --fail --silent --show-error --max-time 10 \
    --header "Authorization: Bearer ${MEMORY_TOKEN}" "${base}/bootstrap/catalog" || true
fi
