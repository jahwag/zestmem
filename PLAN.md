# Distributed Agent Memory

## Outcome

Build a self-hosted shared memory service for small autonomous agent teams such as Clem. It must:

- Work over MCP from Codex, Claude Code, and OpenCode.
- Store only explicit, opt-in Memory writes—never transcripts or automatic conversation summaries.
- Keep startup context near 800 tokens, recall responses near 2,000 tokens, and full bodies behind an explicit one-at-a-time read.
- Prevent duplicate writes, retry duplication, stale pollution, and cross-project authorization leaks.
- Run on one modest Linux host using Go, PostgreSQL/pgvector, and TEI; M1 administration is CLI-first and the small Nuxt UI is M2.

PostgreSQL is the sole system of record. Neo4j, generic GraphRAG frameworks, ANN indexes, policy engines, and automatic extraction remain evidence-gated future options.

## Product Model

### Terms

- **Memory:** explicitly authored durable knowledge, not a transcript, task, or handoff.
- **Principal:** authenticated human or agent.
- **Knowledge Space:** authorization and visibility container at organization, team, project, or principal-private scope.
- **Workspace Claim:** a Git remote or local path supplied by a client to identify task context. Claims affect selection and ranking but never grant access.
- **Topic:** normalized free-form label.
- **Catalog:** bounded map of accessible Spaces and Topics injected at session start.
- **Hit:** compact recall result without the full Memory body.
- **Provenance:** authorship and source locator/evidence attached to a Memory revision.

Record this vocabulary in CONTEXT.md and capture material decisions as ADRs.

### Target schema established in M1

Land the complete storage shape before agents create production data, even where later milestones do not expose every workflow:

- principals: human/agent type, external (issuer, subject), display metadata, disabled state.
- api_keys: optional fallback keys, prefix, secure digest, Principal, creator, created/last-used/revoked/expiry timestamps.
- spaces: organization/team/project/private type, parent, suspended state.
- space_grants: Principal, Space, role (reader, contributor, maintainer, owner).
- workspace_bindings: registered Git remotes and local-path hints mapped to stable project Spaces.
- memories: UUIDv7, Space, current revision, lifecycle status, content hash, creation metadata.
- memory_revisions: immutable revision number, title, Markdown body capped at 16 KiB UTF-8, kind, Topics, provenance, optional valid_until, actor, timestamp, and mutation reason.
- memory_embeddings: revision, vector, and full embedding fingerprint.
- relations: schema reserved for later CORRECTS/SUPERSEDES workflows.
- idempotency_records: unique (principal_id, operation, idempotency_key) plus stored response.
- audit_events: authentication, mutations, grants, key administration, and operator recovery actions. Do not audit every read.

Kinds remain directive, decision, fact, procedure, lesson, and reference. M1 defaults provenance to explicit unverified; M2 makes meaningful provenance mandatory in authoring workflows without requiring a manual verification queue.

Lifecycle storage supports active, retracted, corrected, superseded, expired, and purged, while M1 exposes only active/retracted behavior.

### Authorization contract

- Humans and workload agents use the same internal Principal and Space-grant model.
- Roles are additive:
  - Reader: catalog, recall, read.
  - Contributor: create and revise.
  - Maintainer: later lifecycle and relation operations.
  - Owner: Space and grant administration.
- Ancestor grants inherit catalog/search/read only. Writes require a direct grant on the target Space.
- Principal-private Spaces do not inherit organization or team access.
- No explicit deny, Groups, permission materialization, or external policy engine in v1.
- Go handlers are the M1 enforcement point. PostgreSQL RLS is an M3 defense-in-depth item.
- Apply ACL and lifecycle filtering before lexical/vector ranking.
- Return 404 for inaccessible IDs and 403 for forbidden actions on visible objects.

## M1 — First Useful Release

### M1.1 Identity, administration, and persistence

- Initialize a Go service, PostgreSQL/pgvector migrations, TEI integration, and Docker Compose/Podman-compatible deployment.
- Accept issuer- and audience-bound JWT access tokens from one configurable OIDC/OAuth issuer:
  - Entra is the first certified provider.
  - Human identities use OIDC authorization-code login once the UI arrives.
  - Unattended agents should migrate from Entra user accounts to one service principal/workload identity per stable agent and use client credentials, certificates, managed identity, or federation as their environment permits.
  - Client-credentials access-token expiry is handled by reacquiring a short-lived token; there is no user refresh session.
- Provide simple opaque bearer API keys as a migration/development fallback:
  - One high-entropy key authenticates one Agent Principal.
  - Store only a secure digest and prefix; support immediate revocation.
  - Do not build expiry alerting, overlapping-key ceremonies, or a general secrets-management subsystem in M1.
  - Clem may keep the key in agent-vault and inject it only for the memory-service HTTPS host.
- Provide an operator CLI/seed workflow—there is no hidden dependency on the M2 console—to:
  - Bootstrap the first Owner.
  - Create Spaces and Workspace Bindings.
  - Create Principals and direct grants.
  - Issue/revoke fallback keys.
- Add per-Principal token-bucket rate limits and bounded TEI concurrency. Return retry guidance rather than allowing agent retry storms.

### M1.2 Memory write and recall slice

Expose these MCP tools:

- catalog(workspace_claims?, cursor?)
- recall(query, workspace_claims?, space_ids?, kinds?, topics?, limit?, cursor?)
- read_memory(memory_id)
- remember(target_space_id | workspace_claims, title, body, kind, topics, provenance?, valid_until?, reason, idempotency_key, duplicate_resolution?)
- revise_memory(memory_id, expected_revision, changed_fields, provenance?, reason, idempotency_key)
- retract_memory(memory_id, expected_revision, reason, idempotency_key)

Behavior:

- Every mutation requires an idempotency key and human-readable reason.
- Optimistic revision checks apply to revise/retract from day one.
- Retraction is a soft lifecycle change; retracted Memories are excluded from ordinary recall but remain directly inspectable by authorized operators.
- Before remember writes, run hybrid similarity search inside the target Space:
  - Exact normalized content hashes are always rejected as duplicates.
  - Meaningful near-duplicate candidates return compact IDs, titles, excerpts, and scores without writing.
  - The agent must either call revise_memory or retry with an explicit create_distinct resolution and reason.
  - Never auto-merge, auto-revise, or infer durable knowledge from a transcript.
- Add duplicate scenarios to the golden recall/write evaluation.

Recall pipeline:

1. Resolve ACL, lifecycle, explicit filters, and optional Workspace Claims.
2. Union PostgreSQL full-text and exact pgvector candidates.
3. Fuse lexical/vector ranks with reciprocal-rank fusion.
4. Return compact Hits; do not generate an answer server-side.

Context invariants:

- Catalog contains Space/Topic labels and omitted counts, never bodies, with a hard limit near 800 model tokens.
- Recall defaults to 6 Hits and never exceeds 20 Hits or roughly 2,000 response tokens.
- Full bodies appear only through singular read_memory.
- Startup and compaction integrations never capture or store transcript content.

Workspace support in M1 is limited to:

- Normalized Git remote URLs as the primary claim.
- Local filesystem paths as weak, registered hints.

Package coordinates, monorepo detectors, and ecosystem-specific metadata are deferred until real failures justify them. Workspace Claims influence catalog ordering and search ranking but never authorization.

Use BAAI/bge-small-en-v1.5 as the initial English embedding model through one shared TEI process. Persist provider, immutable model revision, dimensions, normalization, and query/document prefixes. Mixed active fingerprints are prohibited; controlled re-embedding enables later model replacement. Use exact vector search until traces demonstrate an ANN requirement.

Promote HNSW only after exact search is isolated as the bottleneck: either two consecutive cdev-class benchmark runs with 10,000 active embeddings in the queried Space show database-backed recall p95 above 250 ms, or production recall p95 remains above 500 ms for 15 minutes and tracing attributes at least half of that latency to the PostgreSQL vector scan. Record corpus size, `EXPLAIN (ANALYZE, BUFFERS)`, recall-quality comparison, and rollback evidence before enabling the index.

### M1.3 Client delivery and operations

- Expose one standards-compliant remote Streamable HTTP MCP endpoint usable by Codex, Claude Code, and OpenCode.
- Treat portable project instructions as the baseline bootstrap:
  - AGENTS.md/CLAUDE.md instruct the agent to call catalog before task work and after context loss.
  - Keep this instruction short and client-neutral.
- Add native startup hooks only as progressive enhancement:
  - Claude Code and Codex adapters fetch the same bounded catalog on supported start/resume/clear/compact events.
  - OpenCode uses its supported plugin/event surface where stable; experimental context transforms are optional and version-pinned.
  - Hook absence or rejection must degrade to explicit catalog, never disable MCP access.
- Validate actual MCP tool interoperability in all three clients; maintain separate acceptance expectations for automatic bootstrap.
- Emit structured logs, request IDs, basic Prometheus metrics, health/readiness endpoints, search/embedding latency, rate-limit counts, and mutation failures.
- Schedule nightly pg_dump -Fc into a mounted backup target with retention and failure visibility.
- Include an automated restore test that creates a fresh database, restores the newest dump, and verifies representative Memories, revisions, grants, and embeddings. A dump is not considered healthy until this test passes.

### M1 acceptance

- Ten concurrent agents and 10,000 Memories operate on the cdev-class 8-vCPU/15-GiB host without authorization leakage or unbounded TEI queues.
- Catalog/recall/body token invariants are executable tests.
- Duplicate exact writes, near-duplicate advisories, deliberate distinct writes, idempotent retries, revision conflicts, and retraction exclusion are tested.
- Cross-Space reads/writes, read-only inheritance, private-Space isolation, disabled Principals, wrong audiences, expired JWTs, revoked fallback keys, and rate limiting are tested.
- Golden queries cover exact identifiers, semantic paraphrases, multi-repository tasks, rare directives, duplicates, retracted knowledge, and irrelevant neighboring Spaces.
- PostgreSQL backup restoration is exercised automatically.
- Codex, Claude Code, and OpenCode can all call the six M1 tools; bootstrap guarantees are reported separately.

## M2 — Governance and Usability

- Add Nuxt 4 console with configured OIDC login, setup-token first-Owner binding, browse/search/read, revision history, basic Space/grant administration, and fallback-key issuance/revocation.
- Improve mutation/read DX from live M1 traces: give recall rank a name distinct from duplicate similarity, include the current revision in conflict errors, and expose `retracted_at` plus `retract_reason` on authorized direct reads.
- Enforce meaningful provenance in UI/MCP authoring flows.
- Add CORRECTS and SUPERSEDES only:
  - Creating either relation and retiring the target is one optimistic transaction.
  - Ordinary recall excludes retired targets and resolves to the active replacement.
  - Historical direct reads remain available.
- Add relation and lifecycle audit views.
- Add a portable operator export only if migration or interoperability needs exceed normal PostgreSQL backup/restore.

## M3 / Evidence-Gated Backlog

Do not implement these until production traces or operating pain justify them:

- Internal Groups and external identity-group synchronization.
- PostgreSQL RLS as defense-in-depth and materialized effective permissions.
- Automatic decay, read reinforcement, authority boosts, and tunable ranking formulas.
- Additional relations (RELATED_TO, DEPENDS_ON, DERIVED_FROM, DISPUTES) and graph expansion.
- Promotion/review workflows between Spaces.
- Maven, npm, Go, Cargo, Python, and monorepo Workspace detectors.
- ANN indexes, rerankers, embedding bake-offs, or Neo4j.
- Custom portable NDJSON archives, HA, PITR, or federation.
- Full curator console, audit explorer, and advanced binding cleanup.

Promotion criteria must be written before moving an item forward—for example, measured recall failures, authorization-management burden, latency traces, or concrete cross-service policy requirements.

## Build Order

1. **Tracer slice:** migrations, Principal/Space grants, JWT validation, CLI seed, remember, recall, read_memory, BGE-small, and one Codex connection.
2. **Safe mutation:** idempotency, duplicate advisory, revise_memory, retract_memory, revision conflicts, and mutation audit.
3. **Context contract:** catalog budget, Workspace Claims, full recall budgets, golden evaluation, and rate limiting.
4. **Portable clients:** Claude Code and OpenCode MCP validation, portable instruction fallback, then optional native hooks.
5. **Operations:** Compose/Podman deployment, metrics, nightly dumps, automated restore test, and 10-agent/10k-Memory benchmark.
6. **M2 only after M1 is used:** console, mandatory provenance workflow, and correction/supersession semantics.

## Mem0 Position

Mem0 remains comparative prior art, not the implementation base. Its MCP transport and semantic memory demonstrate demand, but its project/API-key authorization, caller-selected entity partitions, automatic capture patterns, and lack of this system's explicit revision, Space, provenance, and context-budget contracts do not satisfy the consultant.dev/Clem requirements.

Run identical golden queries against Mem0 and this service after M1. Compare duplicate pollution, irrelevant context, authorization isolation, and operator effort using evidence rather than feature counts.
