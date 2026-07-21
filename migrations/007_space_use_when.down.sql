BEGIN;
LOCK TABLE schema_migrations, spaces IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    applied_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM schema_migrations WHERE version > 7) THEN
        RAISE EXCEPTION 'refusing to roll back migration 007 while newer migrations are applied';
    END IF;
    SELECT name INTO applied_name FROM schema_migrations WHERE version = 7;
    IF applied_name IS DISTINCT FROM 'space_use_when' THEN
        RAISE EXCEPTION 'migration 007_space_use_when is not applied';
    END IF;
    IF EXISTS (SELECT 1 FROM spaces WHERE use_when IS NOT NULL) THEN
        RAISE EXCEPTION 'refusing to discard non-NULL Space use_when data; export and clear it explicitly first';
    END IF;
END $$;

ALTER TABLE spaces DROP CONSTRAINT spaces_use_when_check;
ALTER TABLE spaces DROP COLUMN use_when;
DELETE FROM schema_migrations WHERE version = 7 AND name = 'space_use_when';
COMMIT;
