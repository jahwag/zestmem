CREATE OR REPLACE FUNCTION has_direct_space_role(actor_id uuid, target_space_id uuid, minimum_role space_role)
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
    SELECT EXISTS(
        SELECT 1
        FROM space_grants grant_row
        JOIN spaces granted_space ON granted_space.id = grant_row.space_id
        JOIN principals actor ON actor.id = grant_row.principal_id
        WHERE grant_row.principal_id = actor_id
          AND grant_row.space_id = target_space_id
          AND actor.disabled_at IS NULL
          AND granted_space.suspended_at IS NULL
          AND grant_row.role >= minimum_role
    )
$$;
