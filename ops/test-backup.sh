#!/bin/sh
set -eu

suffix="$$"
db="agent-memory-backup-db-${suffix}"
network="agent-memory-backup-net-${suffix}"
volume="agent-memory-backup-vol-${suffix}"
cleanup() {
  podman rm -f "$db" >/dev/null 2>&1 || true
  podman network rm "$network" >/dev/null 2>&1 || true
  podman volume rm "$volume" >/dev/null 2>&1 || true
}
trap cleanup EXIT
podman network create "$network" >/dev/null
podman volume create "$volume" >/dev/null
podman run --name "$db" --detach --network "$network" --network-alias postgres \
  --env POSTGRES_PASSWORD=test --env POSTGRES_DB=memory docker.io/pgvector/pgvector:pg17 >/dev/null
until podman exec "$db" pg_isready -U postgres -d memory >/dev/null 2>&1; do sleep 1; done
podman exec --interactive "$db" psql -v ON_ERROR_STOP=1 -U postgres -d memory < migrations/001_init.sql >/dev/null
podman exec --interactive "$db" psql -v ON_ERROR_STOP=1 -U postgres -d memory < ops/backup-fixture.sql >/dev/null
podman run --rm --network "$network" \
  --env PGHOST=postgres --env PGDATABASE=memory --env PGUSER=postgres --env PGPASSWORD=test \
  --volume "${volume}:/backups" localhost/agent-memory-backup:m1 /usr/local/bin/backup.sh
