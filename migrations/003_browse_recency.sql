CREATE INDEX memory_revisions_recency_idx
ON memory_revisions (created_at DESC, memory_id DESC);
