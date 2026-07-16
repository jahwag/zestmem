CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE principal_type AS ENUM ('human', 'agent');
CREATE TYPE space_type AS ENUM ('organization', 'team', 'project', 'private');
CREATE TYPE space_role AS ENUM ('reader', 'contributor', 'maintainer', 'owner');
CREATE TYPE memory_kind AS ENUM ('directive', 'decision', 'fact', 'procedure', 'lesson', 'reference');
CREATE TYPE memory_status AS ENUM ('active', 'retracted', 'corrected', 'superseded', 'expired', 'purged');
CREATE TYPE relation_type AS ENUM ('corrects', 'supersedes');

CREATE TABLE principals (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type principal_type NOT NULL,
    display_name text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 256),
    external_issuer text,
    external_subject text,
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((external_issuer IS NULL) = (external_subject IS NULL))
);

CREATE UNIQUE INDEX principals_external_identity_idx ON principals(external_issuer,external_subject)
WHERE external_issuer IS NOT NULL;

CREATE TABLE api_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    principal_id uuid NOT NULL REFERENCES principals(id),
    prefix text NOT NULL UNIQUE,
    digest bytea NOT NULL UNIQUE,
    created_by uuid NOT NULL REFERENCES principals(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at timestamptz,
    revoked_at timestamptz
);

CREATE TABLE spaces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    type space_type NOT NULL,
    parent_id uuid REFERENCES spaces(id),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 256),
    slug text NOT NULL CHECK (length(btrim(slug)) BETWEEN 1 AND 128),
    suspended_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE NULLS NOT DISTINCT (parent_id, slug),
    CHECK ((type = 'organization' AND parent_id IS NULL) OR type <> 'organization'),
    CHECK ((type = 'private' AND parent_id IS NULL) OR type <> 'private')
);

CREATE TABLE space_grants (
    principal_id uuid NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
    space_id uuid NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    role space_role NOT NULL,
    granted_by uuid NOT NULL REFERENCES principals(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, space_id)
);

CREATE TABLE workspace_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    project_space_id uuid NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    claim_kind text NOT NULL CHECK (claim_kind IN ('git_remote', 'local_path')),
    normalized_value text NOT NULL,
    created_by uuid NOT NULL REFERENCES principals(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (claim_kind, normalized_value)
);

CREATE TABLE memories (
    id uuid PRIMARY KEY,
    space_id uuid NOT NULL REFERENCES spaces(id),
    status memory_status NOT NULL DEFAULT 'active',
    current_revision integer NOT NULL DEFAULT 1 CHECK (current_revision > 0),
    content_hash text NOT NULL,
    created_by uuid NOT NULL REFERENCES principals(id),
    created_at timestamptz NOT NULL,
    retracted_at timestamptz,
    UNIQUE (space_id, content_hash)
);

CREATE TABLE memory_revisions (
    memory_id uuid NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    revision integer NOT NULL CHECK (revision > 0),
    title text NOT NULL CHECK (length(btrim(title)) > 0),
    body text NOT NULL CHECK (octet_length(body) <= 16384 AND length(btrim(body)) > 0),
    kind memory_kind NOT NULL,
    topics text[] NOT NULL DEFAULT '{}',
    provenance jsonb NOT NULL DEFAULT '[{"class":"unverified"}]'::jsonb,
    valid_until timestamptz,
    content_hash text NOT NULL,
    actor_id uuid NOT NULL REFERENCES principals(id),
    reason text NOT NULL CHECK (length(btrim(reason)) > 0),
    created_at timestamptz NOT NULL,
    search_document tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', coalesce(title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(body, '')), 'B')
    ) STORED,
    PRIMARY KEY (memory_id, revision)
);

CREATE INDEX memory_revisions_search_idx ON memory_revisions USING gin (search_document);
CREATE INDEX memory_revisions_topics_idx ON memory_revisions USING gin (topics);
CREATE INDEX memories_space_status_idx ON memories (space_id, status);

CREATE TABLE memory_embeddings (
    memory_id uuid NOT NULL,
    revision integer NOT NULL,
    embedding vector(384) NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    model_revision text NOT NULL,
    dimensions integer NOT NULL CHECK (dimensions = 384),
    normalization text NOT NULL,
    query_prefix text NOT NULL DEFAULT '',
    document_prefix text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (memory_id, revision),
    FOREIGN KEY (memory_id, revision) REFERENCES memory_revisions(memory_id, revision) ON DELETE CASCADE
);

CREATE TABLE relations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    from_memory_id uuid NOT NULL REFERENCES memories(id),
    to_memory_id uuid NOT NULL REFERENCES memories(id),
    type relation_type NOT NULL,
    actor_id uuid NOT NULL REFERENCES principals(id),
    reason text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    retracted_at timestamptz,
    CHECK (from_memory_id <> to_memory_id),
    UNIQUE (from_memory_id, to_memory_id, type)
);

CREATE TABLE idempotency_records (
    principal_id uuid NOT NULL REFERENCES principals(id),
    operation text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    response jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (principal_id, operation, idempotency_key)
);

CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id uuid REFERENCES principals(id),
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text,
    reason text,
    request_id text,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_actor_time_idx ON audit_events (actor_id, created_at DESC);
