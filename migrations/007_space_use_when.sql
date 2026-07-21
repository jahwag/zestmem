ALTER TABLE spaces
    ADD COLUMN use_when text;

ALTER TABLE spaces
    ADD CONSTRAINT spaces_use_when_check CHECK (
        use_when IS NULL OR (
            octet_length(use_when) BETWEEN 1 AND 240
            AND use_when = btrim(use_when)
            AND public.zestmem_use_when_is_safe(use_when)
        )
    );

COMMENT ON COLUMN spaces.use_when IS
    'Untrusted routing metadata describing when an authorized client may search this Space; startup publication requires a separate host approval.';
