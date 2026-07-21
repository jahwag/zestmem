# M1 acceptance record

## Automated and run successfully

- Unit and component suite: domain validation, explicit writes, exact/near dedup behavior, idempotency reuse, optimistic revisions, retraction, bounded catalog/recall output, pagination, hidden unauthorized reads, visible-but-forbidden writes, OIDC issuer/audience/expiry validation, fallback-key hashing, rate limiting, and bounded normalized `use_when` metadata that preserves legacy idempotency/content identity.
- Disposable pgvector integration: create/retry/dedup/revise/retract, exact vector plus full-text recall, nullable `use_when` persistence/revision/hit output without ranking changes, direct grants, descendant read inheritance, cross-Space isolation, retracted exclusion, revoked/expired keys, disabled Principals, and all six tools through the official Go MCP Streamable HTTP client.
- Mutation-context test: neither successful writes nor exact-duplicate errors return a full body.
- Context-budget acceptance: serialized `catalog` content is capped at 800 bytes and serialized `recall` content at 2,000 bytes. Counting every UTF-8 byte as a token is a conservative hard bound, and full bodies remain available only by explicit `read_memory` ID.
- Load acceptance: 10 isolated Agent Principals, 10 Spaces, 10,000 fingerprinted Memories/embeddings, and 100 hybrid recalls from 10 concurrent agents. The post-ACL-refactor verification run completed in 1.60 seconds with 72.14 ms p95, 80.76 ms maximum latency, and no cross-Space hit.
- Backup acceptance: a custom-format dump containing a representative Memory, revision with `use_when`, grant, and embedding was restored into a fresh uniquely named database and passed structural/data checks before promotion.
- Both application and backup OCI images build with Podman; Compose configuration renders successfully.
- `govulncheck` reports no reachable vulnerabilities after upgrading pgx, go-jose, and x/net; the race-enabled Go suite also passes.

Commands are listed in `README.md` and are repeatable under `ops/`.

## Client acceptance boundary

- The official Go MCP client completes initialize, tools/list, catalog, remember, duplicate-error, `use_when`-only revise, and recall calls against the endpoint.
- Claude Code completed an operator-approved live create, idempotent retry, near-duplicate advisory, revise, stale-revision conflict, retract, authorized read-after-retract, invalid-ID read, and cleanup cycle. It also confirmed distinct Claude/Codex actor IDs and immediate recall-index updates after revision.
- Codex is registered and enabled with its separate Principal; its bridge completes the same MCP protocol calls, but the current branded session must be restarted to load a newly approved tool. OpenCode configuration parsing remains verified but its branded live call is still operator-owned.

## Local client smoke test

- A live local stack is configured at `http://127.0.0.1:8080/mcp` with separate revocable Codex and Claude agent Principals, both scoped to the `graph-rag` project Space.
- Both profiles completed MCP initialization, discovered all six tools, and called `catalog` through the stdio token-refresh bridge.
- The Codex Principal explicitly wrote Memory `019f6b35-9439-785f-ae54-266c0472d359`; the separate Claude Principal recalled it and read its full body, proving cross-agent persistence and authorization end to end.
- Codex registers `agent_memory` as an enabled user MCP server, and Claude's project server has operator approval.
- Live feedback fixes are deployed and protocol-tested: `target_space_id` is honestly required by the `remember` schema, while an idempotent replay reports `written:false` and `replayed:true` with the original Memory ID.
- Live startup exposed and drove a regression fix in the active-embedding fingerprint join; the database integration suite now exercises that startup query.

## Intentionally deferred

The Nuxt console, Groups, PostgreSQL RLS, promotion/dispute workflows, relation expansion, learned decay/reinforcement, ANN indexes, and portable NDJSON export remain M2/M3 work. They are not hidden M1 dependencies.
