# ZestMem

Self-hosted, context-bounded durable memory for autonomous agent teams. It exposes one authenticated Streamable HTTP MCP endpoint for Codex, Claude Code, and OpenCode. It never captures transcripts: agents can only write through explicit mutation tools carrying a reason and idempotency key.

## What M1 includes

- PostgreSQL 17 with pgvector as the sole system of record.
- Hybrid full-text/exact-vector recall with reciprocal-rank fusion.
- Six tools: `catalog`, `recall`, `read_memory`, `remember`, `revise_memory`, and `retract_memory`.
- Direct Space grants, read inheritance to descendants, optimistic revisions, soft retraction, duplicate advice, and retry idempotency.
- Entra-compatible issuer/audience JWT validation plus revocable, SHA-256-digested opaque keys as a migration fallback.
- Pinned BGE-small embeddings through a bounded shared TEI service.
- Caddy TLS, per-Principal rate limits, health/readiness/metrics, nightly custom-format backups, and a real restore/count test.

The phased scope and decisions are in [PLAN.md](PLAN.md). Domain terminology is in [CONTEXT.md](CONTEXT.md), and executed evidence is recorded in [M1_ACCEPTANCE.md](M1_ACCEPTANCE.md).

## Start locally

Requirements are Go 1.25 and Podman Compose or Docker Compose.

```sh
cp .env.example .env
# Replace every placeholder in .env. Leave both OIDC values blank for API-key-only development.
podman compose up --build -d
```

The application also binds `http://127.0.0.1:8080/mcp` for loopback-only development. The external MCP URL is `https://localhost/mcp` by default when Caddy is running. Caddy uses a local CA for localhost; production should set `MEMORY_DOMAIN` to a DNS name and allow Caddy to obtain a public certificate.

Initialize the first operator and organization from the app image:

```sh
podman compose run --rm --entrypoint /usr/local/bin/memoryctl memory \
  bootstrap --name "Operator" --org "Consultant Dev" --slug consultant-dev
```

`memoryctl` also provides `create-principal`, `create-space`, `grant`, `bind-workspace`, `issue-key`, and `revoke-key`. Run a command with `-h` for its flags. Key issuance prints the secret once; put it in the agent's secret store, never in repository configuration.

Migrations under `migrations/` initialize a new volume. For an existing deployment, apply new numbered migrations in order before starting the corresponding application release.

## Entra workload identity

Register the memory API with an Application ID URI/audience and configure:

```text
OIDC_ISSUER=https://login.microsoftonline.com/TENANT_ID/v2.0
OIDC_AUDIENCE=api://MEMORY_API_APP_ID
```

Create one service principal/workload identity per stable agent and grant it access to the memory API. Seed its Entra token `sub` as the Principal's `external_subject` and the exact issuer as `external_issuer`. Agents use client credentials, a certificate, managed identity, or workload federation to acquire a short-lived access token for the MCP request. Token expiry is routine: acquire another token; there is no interactive OAuth session or refresh-session ceremony.

Existing Entra user-account agents can migrate individually. Until then, issue each one a distinct fallback key. Do not use ROPC to make user accounts headless; it conflicts with MFA and Conditional Access.

## Client bootstrap

See [clients/README.md](clients/README.md). Portable project guidance is primary. Codex and Claude hooks call the same bounded catalog endpoint as a progressive enhancement; MCP itself works when a hook is unsupported, untrusted, or disabled.

## Verification

```sh
go test ./...
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
sh ops/test-integration.sh
sh ops/test-backup.sh
sh ops/test-load.sh
podman compose --env-file .env.example config
```

The integration script starts a disposable pgvector database and verifies durable create/retry/dedup/revise/retract behavior, ACL isolation/inheritance, hybrid recall, and an actual MCP Streamable HTTP handshake listing and calling the tools.

Backups run at 02:17 UTC. Each dump is restored into a fresh database and table counts for Memories, revisions, grants, and embeddings must match before the dump is reported healthy. Run one immediately with `podman compose exec backup /usr/local/bin/backup.sh`.
