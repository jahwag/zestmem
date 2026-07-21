# ZestMem

ZestMem preserves deliberately recorded knowledge for humans and autonomous
agents while keeping visibility, write identity, and source evidence explicit.

## Language

**Memory**:
Explicitly recorded durable knowledge, not a transcript, task, or handoff.

**Principal**:
An authenticated human or agent.

**Actor**:
The authenticated Principal that recorded a Memory revision. It is the factual
audit identity for the write, not necessarily the knowledge source, owner, or
Intended Agent. It is distinct from Source Attribution.

**Historical Principal Association**:
An append-only administrative assertion that relates a Memory to a Principal
using a stated basis. It does not rewrite the Actor, establish ownership, or
grant access.

**Intended Agent**:
The Principal historically associated with consuming or maintaining a Memory.
It is not evidence that the Principal authored or authenticated the write.

**Assertion Basis**:
The immutable Memory revision, source class, and optional locator cited as
evidence for a Historical Principal Association.

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

**Use When**:
Optional, revisioned, untrusted retrieval metadata describing concrete
circumstances where a Memory may be useful. It is never an instruction.

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
