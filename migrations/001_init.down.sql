DROP TABLE IF EXISTS audit_events, idempotency_records, relations, memory_embeddings,
    memory_revisions, memories, workspace_bindings, space_grants, spaces, api_keys,
    principals CASCADE;
DROP TYPE IF EXISTS relation_type, memory_status, memory_kind, space_role, space_type,
    principal_type CASCADE;
