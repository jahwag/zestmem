#!/bin/sh
set -eu

dump="${1:?dump path required}"
test_db="memory_restore_test_$$"
dropdb --if-exists "$test_db"
createdb "$test_db"
trap 'dropdb --if-exists "$test_db"' EXIT
pg_restore --exit-on-error --no-owner --no-acl --dbname "$test_db" "$dump"

psql --dbname "$test_db" --no-psqlrc --tuples-only --command "
SELECT CASE WHEN
  to_regclass('public.memories') IS NOT NULL AND
  to_regclass('public.memory_revisions') IS NOT NULL AND
  to_regclass('public.space_grants') IS NOT NULL AND
  to_regclass('public.memory_embeddings') IS NOT NULL
THEN 'restore_ok' ELSE 'restore_incomplete' END;" | grep -q restore_ok

psql --dbname "$test_db" --no-psqlrc --tuples-only --no-align --command "
SELECT CASE WHEN
  NOT EXISTS(SELECT 1 FROM memories m LEFT JOIN memory_revisions r ON r.memory_id=m.id AND r.revision=m.current_revision WHERE r.memory_id IS NULL)
  AND NOT EXISTS(SELECT 1 FROM memory_embeddings WHERE dimensions<>384)
THEN 'data_ok' ELSE 'data_invalid' END;" | grep -q data_ok

psql --dbname "$test_db" --no-psqlrc --tuples-only --no-align --command "
SELECT 'restored_counts memories='||(SELECT count(*) FROM memories)||
' revisions='||(SELECT count(*) FROM memory_revisions)||
' grants='||(SELECT count(*) FROM space_grants)||
' embeddings='||(SELECT count(*) FROM memory_embeddings);"
