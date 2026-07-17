-- Keep authorization semantics behind these functions until PostgreSQL RLS
-- becomes evidence-justified defense in depth.

CREATE FUNCTION readable_space_ids(actor_id uuid)
RETURNS TABLE(space_id uuid)
LANGUAGE sql
STABLE
AS $$
    WITH RECURSIVE readable(id) AS (
        SELECT grant_row.space_id
        FROM space_grants grant_row
        JOIN spaces granted_space ON granted_space.id = grant_row.space_id
        WHERE grant_row.principal_id = actor_id
          AND granted_space.suspended_at IS NULL
        UNION
        SELECT child.id
        FROM spaces child
        JOIN readable parent ON child.parent_id = parent.id
        WHERE child.suspended_at IS NULL
    )
    SELECT DISTINCT id FROM readable
$$;

CREATE FUNCTION can_read_space(actor_id uuid, target_space_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS(
        SELECT 1 FROM readable_space_ids(actor_id) readable
        WHERE readable.space_id = target_space_id
    )
$$;

CREATE FUNCTION has_direct_space_role(actor_id uuid, target_space_id uuid, minimum_role space_role)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS(
        SELECT 1
        FROM space_grants grant_row
        JOIN spaces granted_space ON granted_space.id = grant_row.space_id
        WHERE grant_row.principal_id = actor_id
          AND grant_row.space_id = target_space_id
          AND granted_space.suspended_at IS NULL
          AND grant_row.role >= minimum_role
    )
$$;
