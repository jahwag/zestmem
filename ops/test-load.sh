#!/bin/sh
set -eu

name="agent-memory-load"
port="55433"
cleanup() { podman rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
podman run --name "$name" --detach --publish "${port}:5432" \
  --env POSTGRES_PASSWORD=test --env POSTGRES_DB=memory docker.io/pgvector/pgvector:pg17 >/dev/null
until podman exec "$name" pg_isready -U postgres -d memory >/dev/null 2>&1; do sleep 1; done
podman exec --interactive "$name" psql -v ON_ERROR_STOP=1 -U postgres -d memory < migrations/001_init.sql >/dev/null
RUN_LOAD_TEST=1 TEST_DATABASE_URL="postgres://postgres:test@localhost:${port}/memory?sslmode=disable" \
  go test ./internal/postgres -run TestTenAgentsTenThousandMemories -count=1 -timeout=2m
