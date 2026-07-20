# ZestMem

ZestMem preserves deliberately authored knowledge for humans and autonomous
agents while keeping visibility, authorship, and source evidence explicit.

## Language

**Memory**:
Explicitly authored durable knowledge, not a transcript, task, or handoff.

**Principal**:
An authenticated human or agent.

**Actor**:
The authenticated Principal that created a Memory revision. An Actor is factual
authorship and is distinct from a claimed source.

**Knowledge Space**:
An authorization and visibility container. A `private` Space is a non-inheriting
root, not an ownership guarantee; agent-personal use requires operator-managed
exclusive direct grants. Topics never control audience or ownership.
_Avoid_: Bucket, namespace

**Workspace Claim**:
A Git remote or local-path hint used to select and rank project context. It
never grants access.

**Workspace Binding**:
A registered mapping from Workspace Claims to a project Knowledge Space.

**Topic**:
A normalized subject label drawn from a Space's discoverable vocabulary. A new
Topic is intentional classification, not an ownership or audience marker.
_Avoid_: Owner label, agent label

**Catalog**:
A bounded map of accessible Knowledge Spaces and their most common Topics.

**Metadata Discovery**:
Searchable, paginated vocabulary discovery for Topics and Provenance values.

**Recall**:
Explicit hybrid search over readable Memories.

**Hit**:
A compact Recall result without a full body.

**Provenance**:
Source evidence attached to a revision. It may carry Source Attribution but is
distinct from the authenticated Actor.

**Source Attribution**:
An optional claim naming the human or agent that supplied a Provenance source.
It aids interpretation but does not prove identity or grant access.

## Memory module interface

The memory domain owns validation, deduplication, revision concurrency,
retraction, and context budgets. PostgreSQL owns durable transactions and
search primitives. MCP is a thin transport adapter and may not bypass domain
services.
