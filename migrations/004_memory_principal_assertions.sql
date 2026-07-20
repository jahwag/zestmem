CREATE TABLE memory_principal_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    memory_id uuid NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    relationship text NOT NULL CHECK (relationship = 'intended_agent'),
    principal_id uuid NOT NULL REFERENCES principals(id),
    basis_revision integer NOT NULL,
    basis_class text NOT NULL CHECK (length(btrim(basis_class)) BETWEEN 1 AND 128),
    basis_locator text CHECK (basis_locator IS NULL OR length(btrim(basis_locator)) BETWEEN 1 AND 2048),
    asserted_by uuid NOT NULL REFERENCES principals(id),
    asserted_at timestamptz NOT NULL DEFAULT now(),
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 2048),
    FOREIGN KEY (memory_id, basis_revision) REFERENCES memory_revisions(memory_id, revision)
);

CREATE TABLE memory_principal_assertion_retractions (
    assertion_id uuid PRIMARY KEY REFERENCES memory_principal_assertions(id) ON DELETE RESTRICT,
    retracted_by uuid NOT NULL REFERENCES principals(id),
    retracted_at timestamptz NOT NULL DEFAULT now(),
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 2048)
);

CREATE INDEX memory_principal_assertions_memory_idx
ON memory_principal_assertions(memory_id, asserted_at, id);

CREATE FUNCTION ensure_single_active_principal_assertion() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM 1 FROM memories WHERE id=NEW.memory_id FOR UPDATE;
    IF EXISTS (
        SELECT 1 FROM memory_principal_assertions a
        LEFT JOIN memory_principal_assertion_retractions x ON x.assertion_id=a.id
        WHERE a.memory_id=NEW.memory_id AND a.relationship=NEW.relationship
          AND x.assertion_id IS NULL
    ) THEN
        RAISE EXCEPTION 'an active % assertion already exists for this Memory', NEW.relationship;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER memory_principal_assertions_single_active
BEFORE INSERT ON memory_principal_assertions
FOR EACH ROW EXECUTE FUNCTION ensure_single_active_principal_assertion();

CREATE FUNCTION reject_principal_assertion_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'historical Principal assertions are append-only';
END;
$$;

CREATE TRIGGER memory_principal_assertions_immutable
BEFORE UPDATE OR DELETE ON memory_principal_assertions
FOR EACH ROW EXECUTE FUNCTION reject_principal_assertion_mutation();

CREATE TRIGGER memory_principal_assertion_retractions_immutable
BEFORE UPDATE OR DELETE ON memory_principal_assertion_retractions
FOR EACH ROW EXECUTE FUNCTION reject_principal_assertion_mutation();

CREATE FUNCTION validate_principal_assertion_retraction() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.retracted_at < (SELECT asserted_at FROM memory_principal_assertions WHERE id=NEW.assertion_id) THEN
        RAISE EXCEPTION 'retraction cannot predate its assertion';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER memory_principal_assertion_retractions_chronology
BEFORE INSERT ON memory_principal_assertion_retractions
FOR EACH ROW EXECUTE FUNCTION validate_principal_assertion_retraction();

CREATE FUNCTION audit_principal_assertion_insert() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO audit_events(actor_id,action,target_type,target_id,reason,metadata)
    VALUES(NEW.asserted_by,'assert_memory_principal','memory_principal_assertion',NEW.id::text,NEW.reason,
      jsonb_build_object('memory_id',NEW.memory_id,'relationship',NEW.relationship,
        'principal_id',NEW.principal_id,'basis_revision',NEW.basis_revision,
        'basis_class',NEW.basis_class,'basis_locator',NEW.basis_locator));
    RETURN NEW;
END;
$$;

CREATE FUNCTION audit_principal_assertion_retraction() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO audit_events(actor_id,action,target_type,target_id,reason,metadata)
    VALUES(NEW.retracted_by,'retract_memory_principal_assertion','memory_principal_assertion',NEW.assertion_id::text,NEW.reason,
      jsonb_build_object('assertion_id',NEW.assertion_id));
    RETURN NEW;
END;
$$;

CREATE TRIGGER memory_principal_assertions_audit
AFTER INSERT ON memory_principal_assertions
FOR EACH ROW EXECUTE FUNCTION audit_principal_assertion_insert();

CREATE TRIGGER memory_principal_assertion_retractions_audit
AFTER INSERT ON memory_principal_assertion_retractions
FOR EACH ROW EXECUTE FUNCTION audit_principal_assertion_retraction();
