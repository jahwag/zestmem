ALTER TABLE memory_revisions
    ADD COLUMN use_when text;

ALTER TABLE memory_revisions
    ADD CONSTRAINT memory_revisions_use_when_check CHECK (
        use_when IS NULL OR (
            octet_length(use_when) BETWEEN 1 AND 240
            AND use_when = btrim(use_when)
            AND use_when !~ '[[:cntrl:]]'
        )
    );

COMMENT ON COLUMN memory_revisions.use_when IS
    'Optional revisioned, untrusted retrieval metadata; never instructions.';
