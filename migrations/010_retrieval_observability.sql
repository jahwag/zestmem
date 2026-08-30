DROP INDEX memory_revisions_search_idx;

ALTER TABLE memory_revisions DROP COLUMN search_document;
ALTER TABLE memory_revisions ADD COLUMN search_document tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(use_when, '')), 'A') ||
    setweight(to_tsvector('english', coalesce(body, '')), 'B')
) STORED;
CREATE INDEX memory_revisions_search_idx ON memory_revisions USING gin (search_document);

CREATE TABLE retrieval_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id uuid NOT NULL REFERENCES principals(id),
    operation text NOT NULL CHECK (
        operation IN ('recall', 'reverie', 'read_memory', 'read_memory_segment')
    ),
    cue text NOT NULL DEFAULT '' CHECK (octet_length(cue) <= 240),
    cue_hash text NOT NULL CHECK (length(cue_hash) = 64),
    result_count integer NOT NULL CHECK (result_count >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX retrieval_events_actor_time_idx ON retrieval_events (actor_id, created_at DESC);

CREATE TABLE retrieval_event_memories (
    event_id bigint NOT NULL REFERENCES retrieval_events(id) ON DELETE CASCADE,
    memory_id uuid NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    revision integer NOT NULL CHECK (revision > 0),
    rank integer NOT NULL CHECK (rank > 0),
    PRIMARY KEY (event_id, memory_id)
);
CREATE INDEX retrieval_event_memories_memory_idx
    ON retrieval_event_memories (memory_id, event_id DESC);
