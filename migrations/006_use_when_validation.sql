DO $$
BEGIN
    IF current_setting('server_encoding') <> 'UTF8' THEN
        RAISE EXCEPTION 'use_when Unicode validation requires UTF8 server_encoding';
    END IF;
END $$;

-- PostgreSQL 17 has no core Unicode General_Category predicate. Keep this
-- code-point table synchronized with the Unicode 15.0 tables used by Go 1.25.
CREATE FUNCTION public.zestmem_use_when_is_safe(use_when_value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE STRICT PARALLEL SAFE
SET search_path = pg_catalog
AS $$
    SELECT NOT EXISTS (
        SELECT 1
        FROM pg_catalog.generate_series(1, pg_catalog.char_length(use_when_value)) AS g(i)
        CROSS JOIN LATERAL (
            VALUES (pg_catalog.ascii(pg_catalog.substr(use_when_value, g.i, 1)))
        ) AS u(cp)
        WHERE
            -- Cc
            cp BETWEEN 0 AND 31 OR cp BETWEEN 127 AND 159
            -- Cf, Unicode 15.0
            OR cp IN (173, 1564, 1757, 1807, 2274, 6158, 65279,
                      69821, 69837, 917505)
            OR cp BETWEEN 1536 AND 1541
            OR cp BETWEEN 2192 AND 2193
            OR cp BETWEEN 8203 AND 8207
            OR cp BETWEEN 8234 AND 8238
            OR cp BETWEEN 8288 AND 8292
            OR cp BETWEEN 8294 AND 8303
            OR cp BETWEEN 65529 AND 65531
            OR cp BETWEEN 78896 AND 78911
            OR cp BETWEEN 113824 AND 113827
            OR cp BETWEEN 119155 AND 119162
            OR cp BETWEEN 917536 AND 917631
            -- Zl / Zp
            OR cp IN (8232, 8233)
            -- Unicode noncharacters
            OR cp BETWEEN 64976 AND 65007
            OR (cp & 65535) IN (65534, 65535)
    )
$$;

ALTER TABLE memory_revisions
    DROP CONSTRAINT memory_revisions_use_when_check;

ALTER TABLE memory_revisions
    ADD CONSTRAINT memory_revisions_use_when_check CHECK (
        use_when IS NULL OR (
            octet_length(use_when) BETWEEN 1 AND 240
            AND use_when = btrim(use_when)
            AND public.zestmem_use_when_is_safe(use_when)
        )
    );

COMMENT ON FUNCTION public.zestmem_use_when_is_safe(text) IS
    'Structural use_when validator pinned to Unicode 15.0; not semantic or prompt-injection validation.';
