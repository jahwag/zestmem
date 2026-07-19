<p align="center">
  <img src="assets/brand/readme/zestmem-header.png" alt="ZestMem" width="720">
</p>

ZestMem is self-hosted, durable memory for teams of autonomous agents. Codex, Claude Code, and OpenCode share useful decisions, facts, lessons, and procedures through one authenticated Streamable HTTP MCP endpoint—without capturing transcripts.

## What it provides

- Hybrid full-text and vector recall, plus a read-only browser for exploring memories by Space, topic, and kind.
- Explicit, reason-carrying writes; immutable revisions; optimistic concurrency; duplicate advice; and retry idempotency.
- Hierarchical Spaces with Reader, Contributor, Maintainer, and Owner grants.
- OIDC workload identity or individually revocable API keys.
- PostgreSQL 17 with pgvector as the sole system of record, pinned BGE-small embeddings, metrics, verified backups, and signed release artifacts.

## Quick start

The included Compose file runs published ZestMem images. It requires Podman Compose or Docker Compose.

```sh
cp .env.example .env
chmod 600 .env
# Set a long random POSTGRES_PASSWORD and MEMORY_DOMAIN=localhost.
# Clear both OIDC values for API-key-only development.
podman compose pull
podman compose up -d
```

Run `podman compose run --rm memory --version`, then download the matching `memoryctl` from [ZestMem releases](https://github.com/Bytelope/zestmem/releases) and follow [release verification](docs/verifying-releases.md).

Compose binds the memory API to `127.0.0.1:8080` and PostgreSQL administration to `127.0.0.1:15432`. Bootstrap the first operator and organization using the password from `.env`:

```sh
PGPASSWORD='the POSTGRES_PASSWORD from .env' \
  DATABASE_URL='postgres://memory@127.0.0.1:15432/memory?sslmode=disable' \
  memoryctl bootstrap --name "Operator" --org "Example Organization" --slug example
```

Use `memoryctl` to create Principals and Spaces, grant access, bind workspaces, and issue or revoke keys (`memoryctl <command> -h`). Key secrets are printed once; keep them in the agent's secret store, not repository configuration. See [client setup](clients/README.md) for Codex, Claude Code, and OpenCode examples.

The loopback MCP endpoint is `http://127.0.0.1:8080/mcp`. Caddy serves `https://localhost/mcp` when `MEMORY_DOMAIN=localhost`, using a local CA that clients must trust.

## Browse memories

Open `http://127.0.0.1:8080/console/`. Enter an API key issued to a dedicated Principal with only Reader grants for the Spaces it needs, or a current short-lived OIDC access token.

The console browses newest memories, filters by Space/topic/kind, searches by relevance, and shows the current revision's provenance and reason. It is read-only and applies the same authorization checks as MCP. The credential lives only in page memory and memory bodies are rendered as text, never HTML.

For a remote loopback-only deployment:

```sh
ssh -N -L 18080:127.0.0.1:8080 memory-host
# Then open http://127.0.0.1:18080/console/
```

## Authentication

For OIDC workload identity, set both values:

```text
OIDC_ISSUER=https://login.microsoftonline.com/TENANT_ID/v2.0
OIDC_AUDIENCE=api://MEMORY_API_APP_ID
```

Create a local Principal with the token's exact `iss` and `sub`, then grant that Principal to the required Spaces. Give each stable agent its own service principal or fallback key; do not share credentials. ZestMem stores only SHA-256 API-key digests and supports immediate revocation.

## Operate and verify

`memoryd` applies checksummed, numbered migrations at startup. Never edit an applied migration; add the next numbered file. Each backup is restored into a temporary database and checked for required tables, current-revision integrity, and 384-dimensional embeddings before it is reported successful. Optional age-encrypted offsite backups are documented in [docs/github-backups.md](docs/github-backups.md).

Operational endpoints (`/healthz`, `/readyz`, and `/metrics`) should be restricted at the proxy or firewall when they are not intended to be public.

Source verification requires Go 1.25 and Node.js 22:

```sh
make verify
npm ci && npx playwright install --with-deps chromium firefox webkit && npm run test:e2e
make integration
make backup
make load
```

Architecture and domain decisions live in [PLAN.md](PLAN.md) and [CONTEXT.md](CONTEXT.md). See [CONTRIBUTING.md](CONTRIBUTING.md) to contribute, [SECURITY.md](SECURITY.md) to report a vulnerability, and [docs/releasing.md](docs/releasing.md) for the release process.

ZestMem is proprietary software. See [LICENSE.md](LICENSE.md).
