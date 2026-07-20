\set ON_ERROR_STOP on

-- Usage:
-- psql "$DATABASE_URL" -f ops/backfill-legacy-intended-agents.sql
--
-- This installation-specific repair records lead/worker as intended-agent
-- associations. It never rewrites Actor, creator, revision, Provenance, audit,
-- or idempotency history. A rerun inserts no duplicate assertions.

BEGIN ISOLATION LEVEL SERIALIZABLE;

CREATE TEMP TABLE intended_agent_backfill_config AS
SELECT '010147f0-c806-4d20-b137-eae86709bcd5'::uuid operator_id,
       'a6f3848a-881e-4b61-8953-a25cdcfaf5f2'::uuid legacy_actor_id,
       '9a226e71-9654-4244-9eb6-ae6bd7e8ddaa'::uuid lead_principal_id,
       '21ceb31d-131a-4fb0-89c4-f4767dc367eb'::uuid worker_principal_id;

WITH inserted AS (
  INSERT INTO principals(id,type,display_name)
  VALUES('9a226e71-9654-4244-9eb6-ae6bd7e8ddaa','agent','Amara'),
        ('21ceb31d-131a-4fb0-89c4-f4767dc367eb','agent','Athena')
  ON CONFLICT (id) DO NOTHING
  RETURNING id
)
INSERT INTO audit_events(actor_id,action,target_type,target_id,reason)
SELECT c.operator_id,'create_principal','principal',i.id::text,'Create stable cdev agent Principal for historical association'
FROM inserted i CROSS JOIN intended_agent_backfill_config c;

LOCK TABLE memories IN SHARE ROW EXCLUSIVE MODE;
LOCK TABLE memory_principal_assertions IN SHARE ROW EXCLUSIVE MODE;

CREATE TEMP TABLE intended_agent_actor_snapshot AS
SELECT m.id memory_id,m.created_by,r.actor_id
FROM memories m
JOIN memory_revisions r ON r.memory_id=m.id AND r.revision=m.current_revision;

CREATE TEMP TABLE intended_agent_targets AS
SELECT DISTINCT r.memory_id,r.revision basis_revision,p->>'locator' basis_locator,
       CASE WHEN p->>'locator' LIKE '%/lead/%' THEN 'lead' ELSE 'worker' END legacy_role,
       CASE WHEN p->>'locator' LIKE '%/lead/%' THEN c.lead_principal_id ELSE c.worker_principal_id END principal_id
FROM memory_revisions r
JOIN memories m ON m.id=r.memory_id
CROSS JOIN LATERAL jsonb_array_elements(r.provenance) p
CROSS JOIN intended_agent_backfill_config c
WHERE r.revision=1 AND m.current_revision=1
  AND r.reason LIKE 'Migrate curated%'
  AND p->>'class'='git_commit'
  AND (p->>'locator' LIKE '%/lead/%' OR p->>'locator' LIKE '%/worker/%');

DO $$
DECLARE
  total_memories integer;
  migrated integer;
  target_count integer;
  lead_count integer;
  worker_count integer;
  legacy_actor_count integer;
  configured_principals integer;
  operator_is_owner boolean;
  principal_name_conflicts integer;
  target_digest text;
  bad_existing integer;
BEGIN
  SELECT count(*) INTO total_memories FROM memories;
  SELECT count(*) INTO migrated FROM memory_revisions WHERE revision=1 AND reason LIKE 'Migrate curated%';
  SELECT count(*),count(*) FILTER (WHERE legacy_role='lead'),count(*) FILTER (WHERE legacy_role='worker')
    INTO target_count,lead_count,worker_count FROM intended_agent_targets;
  SELECT count(*) INTO legacy_actor_count
    FROM intended_agent_actor_snapshot s,intended_agent_backfill_config c
    WHERE s.actor_id=c.legacy_actor_id AND s.created_by=c.legacy_actor_id;
  SELECT count(*) INTO configured_principals
    FROM principals p,intended_agent_backfill_config c
    WHERE (p.id=c.lead_principal_id AND p.type='agent' AND p.display_name='Amara')
       OR (p.id=c.worker_principal_id AND p.type='agent' AND p.display_name='Athena')
       OR (p.id=c.legacy_actor_id AND p.type='agent' AND p.display_name='Cdev Agent Team')
       OR (p.id=c.operator_id AND p.type='human' AND p.display_name='Cdev ZestMem Owner');
  SELECT EXISTS(SELECT 1 FROM space_grants g,intended_agent_backfill_config c
    WHERE g.principal_id=c.operator_id AND g.role='owner') INTO operator_is_owner;
  SELECT count(*) INTO principal_name_conflicts
    FROM principals p,intended_agent_backfill_config c
    WHERE (p.display_name='Amara' AND p.id<>c.lead_principal_id)
       OR (p.display_name='Athena' AND p.id<>c.worker_principal_id);
  SELECT encode(digest(string_agg(
      concat_ws(chr(31),memory_id::text,basis_revision::text,basis_locator,principal_id::text),
      chr(30) ORDER BY memory_id,basis_revision,basis_locator,principal_id),'sha256'),'hex')
    INTO target_digest FROM intended_agent_targets;
  SELECT count(*) INTO bad_existing
    FROM intended_agent_targets t
    JOIN memory_principal_assertions a ON a.memory_id=t.memory_id AND a.relationship='intended_agent'
    LEFT JOIN memory_principal_assertion_retractions x ON x.assertion_id=a.id
    WHERE x.assertion_id IS NULL AND
      (a.principal_id<>t.principal_id OR a.basis_revision<>t.basis_revision OR
       a.basis_class<>'legacy_role_path' OR a.basis_locator IS DISTINCT FROM t.basis_locator);

  IF total_memories<>549 OR migrated<>547 OR target_count<>500 OR lead_count<>330 OR worker_count<>170 THEN
    RAISE EXCEPTION 'unexpected inventory: total %, migrated %, targets %, lead %, worker %',
      total_memories,migrated,target_count,lead_count,worker_count;
  END IF;
  IF legacy_actor_count<>549 THEN
    RAISE EXCEPTION 'Actor/creator preflight failed: expected 549 shared-team rows, got %',legacy_actor_count;
  END IF;
  IF configured_principals<>4 OR NOT operator_is_owner OR principal_name_conflicts<>0 OR
     (SELECT lead_principal_id=worker_principal_id FROM intended_agent_backfill_config) THEN
    RAISE EXCEPTION 'configured Principals do not match the reviewed cdev identities';
  END IF;
  IF target_digest<>'d7bdbf29feb3fc323d0ab7a5939805efa32c0a0dc6ffa6db80621f6204b49727' THEN
    RAISE EXCEPTION 'reviewed target manifest digest mismatch: %',target_digest;
  END IF;
  IF bad_existing<>0 THEN
    RAISE EXCEPTION '% active assertions conflict with the reviewed mapping',bad_existing;
  END IF;
END;
$$;

INSERT INTO memory_principal_assertions
 (memory_id,relationship,principal_id,basis_revision,basis_class,basis_locator,asserted_by,reason)
SELECT t.memory_id,'intended_agent',t.principal_id,t.basis_revision,'legacy_role_path',t.basis_locator,c.operator_id,
       'Map the archived legacy role collection to its intended cdev agent; association only, not authenticated authorship'
FROM intended_agent_targets t
CROSS JOIN intended_agent_backfill_config c
WHERE NOT EXISTS (
  SELECT 1 FROM memory_principal_assertions a
  LEFT JOIN memory_principal_assertion_retractions x ON x.assertion_id=a.id
  WHERE a.memory_id=t.memory_id AND a.relationship='intended_agent' AND x.assertion_id IS NULL
);

DO $$
DECLARE
  active_total integer;
  active_lead integer;
  active_worker integer;
  non_target_active integer;
  actor_changes integer;
BEGIN
  SELECT count(*),count(*) FILTER (WHERE t.legacy_role='lead'),count(*) FILTER (WHERE t.legacy_role='worker')
    INTO active_total,active_lead,active_worker
    FROM intended_agent_targets t
    JOIN memory_principal_assertions a ON a.memory_id=t.memory_id AND a.relationship='intended_agent' AND a.principal_id=t.principal_id
    LEFT JOIN memory_principal_assertion_retractions x ON x.assertion_id=a.id
    WHERE x.assertion_id IS NULL;
  SELECT count(*) INTO non_target_active
    FROM memory_principal_assertions a
    LEFT JOIN memory_principal_assertion_retractions x ON x.assertion_id=a.id
    WHERE a.relationship='intended_agent' AND x.assertion_id IS NULL
      AND NOT EXISTS (SELECT 1 FROM intended_agent_targets t WHERE t.memory_id=a.memory_id);
  SELECT count(*) INTO actor_changes FROM (
    (SELECT m.id memory_id,m.created_by,r.actor_id FROM memories m
     JOIN memory_revisions r ON r.memory_id=m.id AND r.revision=m.current_revision
     EXCEPT SELECT memory_id,created_by,actor_id FROM intended_agent_actor_snapshot)
    UNION ALL
    (SELECT memory_id,created_by,actor_id FROM intended_agent_actor_snapshot
     EXCEPT SELECT m.id,m.created_by,r.actor_id FROM memories m
     JOIN memory_revisions r ON r.memory_id=m.id AND r.revision=m.current_revision)
  ) changed;

  IF active_total<>500 OR active_lead<>330 OR active_worker<>170 OR non_target_active<>0 THEN
    RAISE EXCEPTION 'postflight failed: active %, lead %, worker %, non-target %',
      active_total,active_lead,active_worker,non_target_active;
  END IF;
  IF actor_changes<>0 THEN
    RAISE EXCEPTION 'Actor or creator history changed for % rows',actor_changes;
  END IF;
END;
$$;

COMMIT;
