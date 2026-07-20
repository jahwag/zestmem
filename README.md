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

Use `memoryctl` to create Principals and Spaces, grant access, bind workspaces, and issue or revoke keys (`memoryctl <command> -h`). Give each stable agent its own Principal. Shared knowledge belongs in a team or project Space. For agent-private knowledge, operators must create a root `private` Space and grant it exclusively to that agent; `private` means non-inheriting, not single-owner. Topics describe subject matter and never visibility. Key secrets are printed once; keep them in the agent's secret store, not repository configuration. See [client setup](clients/README.md) for Codex, Claude Code, and OpenCode examples.

The loopback MCP endpoint is `http://127.0.0.1:8080/mcp`. Caddy serves `https://localhost/mcp` when `MEMORY_DOMAIN=localhost`, using a local CA that clients must trust.

## Browse memories

Open `http://127.0.0.1:8080/console/`. Enter an API key issued to a dedicated Principal with only Reader grants for the Spaces it needs, or a current short-lived OIDC access token.

<p align="center">
  <a href="docs/images/zestmem-memory-browser.png">
    <img src="docs/images/zestmem-memory-browser.png" alt="ZestMem memory browser with searchable memories, structured filters, and privacy-blurred example content" width="1200">
  </a>
</p>

<p align="center"><em>Browse and filter authorized memories. Memory content is blurred in this screenshot.</em></p>

The console shows exact filtered authorized totals while browsing; browses newest memories; filters by Space, topic, kind, active/retracted status, revision, provenance, and date spans; searches a bounded ranked candidate pool by relevance; and distinguishes the authenticated Actor ID/current display name from optional claimed source attribution. Retracted memories require Maintainer access. Credentials stay in page memory by default; an opt-in uses tab-scoped session storage across refresh. **Lock** aborts requests and clears results, metadata, and that browser copy; it does not revoke the key. Memory bodies are rendered as text, never HTML.

Agents can page or search authorized Topic and Provenance vocabularies with usage counts. `remember` and `revise_memory` return unfamiliar Topics plus established examples; retry with the exact `confirm_new_topics` set to introduce them intentionally. `read_memory` keeps its full-Memory response for compatibility. `read_memory_segment` defaults to at most 80 lines and 4 KiB, includes bounded topic/provenance plus freshness metadata, supports resumable line/byte windows, and accepts a regular expression for grep-style inspection.

Source attribution is a free-form, unverified display-name claim—not identity. Agents should reuse a human's chosen stable name found through `list_provenance(field: "attribution")`; they must not infer names or store contact identifiers. The authenticated Actor Principal remains the audit identity. Active administratively recorded Principal associations, when present, record a claimed historical Intended Agent together with who asserted it, when, why, and its stated basis. They do not imply that Principal authored or supplied the Memory, change the authenticated revision Actor, establish Memory ownership, or grant access; retracted assertion history remains in PostgreSQL and audit events.

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
