#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
set -a
. "$root/.memory-client.env"
set +a

case "${1:-}" in
  codex) MEMORY_TOKEN="$MEMORY_TOKEN_CODEX" ;;
  claude) MEMORY_TOKEN="$MEMORY_TOKEN_CLAUDE" ;;
  *) echo "usage: memory-mcp-local.sh <codex|claude>" >&2; exit 2 ;;
esac
export MEMORY_TOKEN

exec "$root/bin/memory-mcp-bridge"
