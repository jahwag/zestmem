BEGIN;
LOCK TABLE schema_migrations, principals, space_grants, spaces IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE
    applied_name text;
BEGIN
    IF EXISTS (SELECT 1 FROM schema_migrations WHERE version > 9) THEN
        RAISE EXCEPTION 'refusing to roll back migration 009 while newer migrations are applied';
    END IF;
    SELECT name INTO applied_name FROM schema_migrations WHERE version = 9;
    IF applied_name IS DISTINCT FROM 'active_direct_roles' THEN
        RAISE EXCEPTION 'migration 009_active_direct_roles is not applied';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION has_direct_space_role(actor_id uuid, target_space_id uuid, minimum_role space_role)
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

DELETE FROM schema_migrations WHERE version = 9 AND name = 'active_direct_roles';
COMMIT;
