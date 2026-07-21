BEGIN;

SELECT pg_advisory_xact_lock(hashtext('zestmem-schema-migrations'));
LOCK TABLE schema_migrations, memory_revisions IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    applied_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM schema_migrations WHERE version > 6) THEN
        RAISE EXCEPTION 'roll back migrations newer than 006 first';
    END IF;
    SELECT name INTO applied_name FROM schema_migrations WHERE version = 6;
    IF applied_name IS DISTINCT FROM 'use_when_validation' THEN
        RAISE EXCEPTION 'migration 006_use_when_validation is not applied';
    END IF;
END $$;

ALTER TABLE memory_revisions DROP CONSTRAINT memory_revisions_use_when_check;

ALTER TABLE memory_revisions
    ADD CONSTRAINT memory_revisions_use_when_check CHECK (
        use_when IS NULL OR (
            octet_length(use_when) BETWEEN 1 AND 240
            AND use_when = btrim(use_when)
            AND use_when !~ '[[:cntrl:]]'
        )
    );

DROP FUNCTION IF EXISTS public.zestmem_use_when_is_safe(text);
DELETE FROM schema_migrations WHERE version = 6 AND name = 'use_when_validation';

COMMIT;
