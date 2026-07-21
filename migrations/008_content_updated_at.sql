ALTER TABLE memories
    ADD COLUMN content_updated_at timestamptz;

WITH compared AS (
    SELECT r.memory_id,r.revision,r.created_at,r.title,r.body,r.kind,r.topics,r.provenance,r.valid_until,
           lag(r.title) OVER revision_window AS previous_title,
           lag(r.body) OVER revision_window AS previous_body,
           lag(r.kind) OVER revision_window AS previous_kind,
           lag(r.topics) OVER revision_window AS previous_topics,
           lag(r.provenance) OVER revision_window AS previous_provenance,
           lag(r.valid_until) OVER revision_window AS previous_valid_until
    FROM memory_revisions r
    JOIN memories m ON m.id=r.memory_id AND r.revision<=m.current_revision
    WINDOW revision_window AS (PARTITION BY r.memory_id ORDER BY r.revision)
), latest_substantive AS (
    SELECT DISTINCT ON (memory_id) memory_id,created_at
    FROM compared
    WHERE revision=1
       OR title IS DISTINCT FROM previous_title
       OR body IS DISTINCT FROM previous_body
       OR kind IS DISTINCT FROM previous_kind
       OR topics IS DISTINCT FROM previous_topics
       OR provenance IS DISTINCT FROM previous_provenance
       OR valid_until IS DISTINCT FROM previous_valid_until
    ORDER BY memory_id,revision DESC
)
UPDATE memories m
SET content_updated_at = latest.created_at
FROM latest_substantive latest
WHERE latest.memory_id = m.id;

ALTER TABLE memories
    ALTER COLUMN content_updated_at SET NOT NULL,
    ALTER COLUMN content_updated_at SET DEFAULT now();

CREATE INDEX memories_content_recency_idx
    ON memories (content_updated_at DESC, id DESC);

COMMENT ON COLUMN memories.content_updated_at IS
    'Timestamp of the latest substantive revision; retrieval-cue-only revisions preserve this value.';
