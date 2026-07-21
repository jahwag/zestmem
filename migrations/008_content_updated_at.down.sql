BEGIN;
LOCK TABLE schema_migrations, memories IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    applied_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM schema_migrations WHERE version > 8) THEN
        RAISE EXCEPTION 'refusing to roll back migration 008 while newer migrations are applied';
    END IF;
    SELECT name INTO applied_name FROM schema_migrations WHERE version = 8;
    IF applied_name IS DISTINCT FROM 'content_updated_at' THEN
        RAISE EXCEPTION 'migration 008_content_updated_at is not applied';
    END IF;
END $$;

DROP INDEX memories_content_recency_idx;
ALTER TABLE memories DROP COLUMN content_updated_at;
DELETE FROM schema_migrations WHERE version = 8 AND name = 'content_updated_at';
COMMIT;
