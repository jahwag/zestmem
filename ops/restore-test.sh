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
  to_regclass('public.memory_embeddings') IS NOT NULL AND
  to_regclass('public.memory_principal_assertions') IS NOT NULL AND
  to_regclass('public.memory_principal_assertion_retractions') IS NOT NULL
THEN 'restore_ok' ELSE 'restore_incomplete' END;" | grep -q restore_ok

psql --dbname "$test_db" --no-psqlrc --tuples-only --no-align --command "
SELECT CASE WHEN
  NOT EXISTS(SELECT 1 FROM memories m LEFT JOIN memory_revisions r ON r.memory_id=m.id AND r.revision=m.current_revision WHERE r.memory_id IS NULL)
  AND NOT EXISTS(SELECT 1 FROM memory_embeddings WHERE dimensions<>384)
  AND NOT EXISTS(SELECT 1 FROM memory_principal_assertions a LEFT JOIN memories m ON m.id=a.memory_id WHERE m.id IS NULL)
  AND NOT EXISTS(SELECT 1 FROM memory_principal_assertion_retractions x LEFT JOIN memory_principal_assertions a ON a.id=x.assertion_id WHERE a.id IS NULL)
  AND NOT EXISTS(
    SELECT 1 FROM memory_principal_assertions a
    LEFT JOIN memory_principal_assertion_retractions x ON x.assertion_id=a.id
    WHERE x.assertion_id IS NULL GROUP BY a.memory_id,a.relationship HAVING count(*)>1)
  AND to_regprocedure('ensure_single_active_principal_assertion()') IS NOT NULL
  AND to_regprocedure('reject_principal_assertion_mutation()') IS NOT NULL
  AND EXISTS(SELECT 1 FROM memory_revisions WHERE use_when='Testing durable backup and restore behavior.')
THEN 'data_ok' ELSE 'data_invalid' END;" | grep -q data_ok

if [ "${BACKUP_FIXTURE_ASSERTIONS:-}" = "1" ]; then
  psql --dbname "$test_db" --no-psqlrc --tuples-only --no-align --command "
  SELECT CASE WHEN
    (SELECT count(*) FROM memory_principal_assertions)=2
    AND (SELECT count(*) FROM memory_principal_assertion_retractions)=1
    AND (SELECT count(*) FROM memory_principal_assertions a
         LEFT JOIN memory_principal_assertion_retractions x ON x.assertion_id=a.id
         WHERE x.assertion_id IS NULL AND a.id='00000000-0000-0000-0000-000000000006')=1
    AND (SELECT count(*) FROM audit_events WHERE action='assert_memory_principal')=2
    AND (SELECT count(*) FROM audit_events WHERE action='retract_memory_principal_assertion')=1
  THEN 'fixture_ok' ELSE 'fixture_invalid' END;" | grep -q fixture_ok
fi

psql --dbname "$test_db" --no-psqlrc --tuples-only --no-align --command "
SELECT 'restored_counts memories='||(SELECT count(*) FROM memories)||
' revisions='||(SELECT count(*) FROM memory_revisions)||
' grants='||(SELECT count(*) FROM space_grants)||
' embeddings='||(SELECT count(*) FROM memory_embeddings)||
' principal_assertions='||(SELECT count(*) FROM memory_principal_assertions)||
' assertion_retractions='||(SELECT count(*) FROM memory_principal_assertion_retractions);"
