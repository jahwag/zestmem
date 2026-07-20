# ADR 0001: Keep historical Principal associations separate from Actors

## Decision

ZestMem records an authenticated Actor on every immutable revision. Historical
agent mappings are stored separately as append-only, administratively recorded
Memory-to-Principal assertions with an immutable revision and source locator as
their stated basis. The initial relationship is `intended_agent`.

An assertion does not modify `memory_revisions.actor_id`, `memories.created_by`,
existing audit events, or idempotency records. It does not establish authorship,
ownership, or access; Knowledge Space grants remain the authorization model.
Retractions are separate append-only records, and both assertions and
retractions emit audit events.

Assertions are administrative database operations. `asserted_by` records the
Principal named by the trusted database operator; it is not workload-authenticated
by the MCP service. Operators must use controlled scripts, bind their actual
operator Principal, and retain the generated audit events.

## Rationale

Legacy role directories can support an intended-agent mapping, but they cannot
prove which authenticated Principal wrote or originated the knowledge. Rewriting
Actor IDs would therefore falsify audit history. Keeping the association at the
Memory level also lets its revision-specific evidence remain visible after later
content revisions.

## Consequences

Clients must present Actor, Provenance, and Historical Principal Association as
distinct concepts. Deployment-specific backfills must be idempotent, preserve
Actor/creator fields, cite exact source evidence, and leave ambiguous records
unassociated.

Because older binaries reject a newer schema, rollback after applying this
migration means restoring the verified pre-deployment database backup together
with the previous binary. The `.down.sql` file is development support, not an
automated production rollback path.
