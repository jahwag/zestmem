#!/bin/sh
set -eu

runtime="${CONTAINER_RUNTIME:-podman}"
suffix="$$"
name="zestmem-integration-${suffix}"
port="$((55000 + suffix % 5000))"
image="docker.io/pgvector/pgvector:pg17@sha256:d2ef61f42ef767baa5a1475393303cc235bcd92febd9d7014eddb48b41f3bad0"
cleanup() { "$runtime" rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
"$runtime" run --name "$name" --detach --publish "127.0.0.1:${port}:5432" \
  --env POSTGRES_PASSWORD=test --env POSTGRES_DB=memory "$image" >/dev/null
# Probe over TCP: the entrypoint's temporary init-phase server listens only on
# the unix socket, so a socket probe can pass before the final server is up.
tries=0
until "$runtime" exec --env PGPASSWORD=test "$name" psql -h 127.0.0.1 -U postgres -d memory -c 'select 1' >/dev/null 2>&1; do
  tries=$((tries + 1))
  [ "$tries" -le 60 ] || { echo "postgres not ready after 60s" >&2; exit 1; }
  sleep 1
done
"$runtime" exec --interactive "$name" psql -v ON_ERROR_STOP=1 -U postgres -d memory < migrations/001_init.sql >/dev/null
TEST_DATABASE_URL="postgres://postgres:test@localhost:${port}/memory?sslmode=disable" go test ./migrations -count=1
TEST_DATABASE_URL="postgres://postgres:test@localhost:${port}/memory?sslmode=disable" go test ./internal/postgres -count=1
TEST_DATABASE_URL="postgres://postgres:test@localhost:${port}/memory?sslmode=disable" go test ./internal/mcpserver -count=1
