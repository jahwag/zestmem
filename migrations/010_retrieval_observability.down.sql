BEGIN;

LOCK TABLE schema_migrations, retrieval_event_memories, retrieval_events,
    memory_revisions IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    applied_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM schema_migrations WHERE version > 10) THEN
        RAISE EXCEPTION 'refusing to roll back migration 010 while newer migrations are applied';
    END IF;
    SELECT name INTO applied_name FROM schema_migrations WHERE version = 10;
    IF applied_name IS DISTINCT FROM 'retrieval_observability' THEN
        RAISE EXCEPTION 'migration 010_retrieval_observability is not applied';
    END IF;
END $$;

DROP TABLE retrieval_event_memories;
DROP TABLE retrieval_events;

DROP INDEX memory_revisions_search_idx;
ALTER TABLE memory_revisions DROP COLUMN search_document;
ALTER TABLE memory_revisions ADD COLUMN search_document tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(body, '')), 'B')
) STORED;
CREATE INDEX memory_revisions_search_idx ON memory_revisions USING gin (search_document);

DELETE FROM schema_migrations
WHERE version = 10 AND name = 'retrieval_observability';

COMMIT;
