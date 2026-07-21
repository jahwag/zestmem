BEGIN;

SELECT pg_advisory_xact_lock(hashtext('zestmem-schema-migrations'));
LOCK TABLE schema_migrations, memory_revisions IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    applied_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM schema_migrations WHERE version > 5) THEN
        RAISE EXCEPTION 'roll back migrations newer than 005 first';
    END IF;
    SELECT name INTO applied_name FROM schema_migrations WHERE version = 5;
    IF applied_name IS DISTINCT FROM 'use_when' THEN
        RAISE EXCEPTION 'migration 005_use_when is not applied';
    END IF;
    IF EXISTS (SELECT 1 FROM memory_revisions WHERE use_when IS NOT NULL) THEN
        RAISE EXCEPTION 'refusing to discard non-NULL use_when data; export and clear it explicitly first';
    END IF;
END $$;

ALTER TABLE memory_revisions DROP COLUMN use_when;
DELETE FROM schema_migrations WHERE version = 5 AND name = 'use_when';

COMMIT;
