#!/bin/sh
set -eu

runtime="${CONTAINER_RUNTIME:-podman}"
suffix="$$"
name="zestmem-load-${suffix}"
port="$((50000 + suffix % 5000))"
image="docker.io/pgvector/pgvector:pg17@sha256:d2ef61f42ef767baa5a1475393303cc235bcd92febd9d7014eddb48b41f3bad0"
cleanup() { "$runtime" rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
"$runtime" run --name "$name" --detach --publish "127.0.0.1:${port}:5432" \
  --env POSTGRES_PASSWORD=test --env POSTGRES_DB=memory "$image" >/dev/null
until "$runtime" exec "$name" pg_isready -U postgres -d memory >/dev/null 2>&1; do sleep 1; done
"$runtime" exec --interactive "$name" psql -v ON_ERROR_STOP=1 -U postgres -d memory < migrations/001_init.sql >/dev/null
RUN_LOAD_TEST=1 TEST_DATABASE_URL="postgres://postgres:test@localhost:${port}/memory?sslmode=disable" \
  go test ./internal/postgres -run TestTenAgentsTenThousandMemories -count=1 -timeout=2m
