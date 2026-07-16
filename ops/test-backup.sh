#!/bin/sh
set -eu

suffix="$$"
runtime="${CONTAINER_RUNTIME:-podman}"
image="docker.io/pgvector/pgvector:pg17@sha256:d2ef61f42ef767baa5a1475393303cc235bcd92febd9d7014eddb48b41f3bad0"
db="agent-memory-backup-db-${suffix}"
network="agent-memory-backup-net-${suffix}"
volume="agent-memory-backup-vol-${suffix}"
cleanup() {
  "$runtime" rm -f "$db" >/dev/null 2>&1 || true
  "$runtime" network rm "$network" >/dev/null 2>&1 || true
  "$runtime" volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
"$runtime" network create "$network" >/dev/null
"$runtime" volume create "$volume" >/dev/null
"$runtime" run --name "$db" --detach --network "$network" --network-alias postgres \
  --env POSTGRES_PASSWORD=test --env POSTGRES_DB=memory "$image" >/dev/null
until "$runtime" exec "$db" pg_isready -U postgres -d memory >/dev/null 2>&1; do sleep 1; done
"$runtime" exec --interactive "$db" psql -v ON_ERROR_STOP=1 -U postgres -d memory < migrations/001_init.sql >/dev/null
"$runtime" exec --interactive "$db" psql -v ON_ERROR_STOP=1 -U postgres -d memory < ops/backup-fixture.sql >/dev/null
"$runtime" run --rm --network "$network" \
  --env PGHOST=postgres --env PGDATABASE=memory --env PGUSER=postgres --env PGPASSWORD=test \
  --volume "${volume}:/backups" localhost/agent-memory-backup:m1 /usr/local/bin/backup.sh
