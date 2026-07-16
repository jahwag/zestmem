# Domain Context

## Ubiquitous language

- **Memory** — explicitly authored durable knowledge, not a transcript, task, or handoff.
- **Principal** — an authenticated human or agent.
- **Knowledge Space** — an authorization and visibility container.
- **Workspace Claim** — a Git remote or local-path hint used to select and rank project context. It never grants access.
- **Workspace Binding** — a registered mapping from claims to a project Space.
- **Topic** — a normalized free-form label.
- **Catalog** — a bounded map of accessible Spaces and Topics.
- **Recall** — explicit hybrid search.
- **Hit** — a compact recall result without a full body.
- **Provenance** — authorship and source evidence attached to a revision.

## Module boundary

The memory domain owns validation, deduplication, revision concurrency,
retraction, and context budgets. PostgreSQL owns durable transactions and
search primitives. MCP is a thin transport adapter and may not bypass domain
services.

