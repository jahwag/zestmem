# Startup routing and reviewed cue backfills

ZestMem separates two routing layers:

- A Space may have one operator-curated `use_when` descriptor. Startup emits it only when the host configuration approves the SHA-256 of the exact text.
- A Memory may have one revisioned, body-supported `use_when` cue. It appears in compact recall hits, never in startup context. Recall preserves cue text exactly; when a bounded page cannot fit, it returns fewer lower-ranked hits instead of semantically truncating a cue.

The startup endpoint returns a complete plain-text envelope of at most 800 bytes. The envelope labels all values as untrusted routing metadata. Contributor-controlled Topics are never emitted at startup; they remain available through the on-demand `catalog` tool. Long owner-controlled display fields are compressed before a Space is omitted, while cues are never semantically truncated. Every exact approved cue is attempted before catalog-only entries, and an approved cue that cannot fit exactly is omitted. `omitted_count` reports additional authorized Spaces. An empty approval map keeps owner-controlled Space names discoverable but publishes no Space cue. Agents with access to many Spaces can set `MEMORY_STARTUP_SPACE_ID` to prioritize one authorized, approved Space without transmitting a Git remote or local path.

This makes startup cost independent of Memory count. With the current single Space, the normal envelope is roughly 400–600 bytes, or about 100–180 tokens for common code-model tokenizers. The hard worst-case is 800 input tokens because the complete envelope is capped at 800 UTF-8 bytes. At 549, 10,000, or 100,000 Memories, startup remains at that cap: Memory cues add zero startup tokens. Recall remains bounded separately to 2,000 conservatively counted bytes; a typical six-hit page adds roughly 120–220 tokens of cue text, depending on language and cue length.

## Approve a Space cue

Set or clear the cue through an active direct Owner of the target Space:

```sh
DATABASE_URL='postgres://…' memoryctl set-space-use-when \
  --space SPACE_UUID \
  --by OWNER_PRINCIPAL_UUID \
  --use-when 'Work involving this project architecture or operations.' \
  --reason 'Approve the bounded startup routing descriptor.'
```

The command prints the cue SHA-256, not the cue body. Put a JSON mapping of the Space UUID to that exact hash in the root-owned host environment as `ZESTMEM_STARTUP_CUE_APPROVALS`, then recreate `memoryd`. Do not expose this approval map or an API key through repository hook configuration. Verify `/bootstrap/context` with a Reader credential and confirm that one actual agent startup invokes only one hook.

## Apply a reviewed legacy manifest

`usewhen-backfill` accepts only a mode-`0600`, regular JSON manifest whose exact SHA-256 and entry count are supplied on the command line. Each entry binds the Space, Memory UUID, expected revision, title, content hash, reviewed cue, evidence rule, and reviewer. The tool pre-reads and post-reads every Memory, uses deterministic idempotency keys, paces MCP HTTP requests to 90 per minute by default, and stops on any source drift. Cue-only revisions copy the existing embedding and fingerprint transactionally without an embedding request and preserve substantive browse/search recency.

Production sequence:

1. Stop or drain all mutation-capable agent credentials.
2. Create and restore-test a pre-deployment database backup paired with the old binaries.
3. Deploy the new schema and binaries; create and restore-test a post-schema/pre-backfill backup paired with them.
4. Create a dedicated temporary agent Principal, grant it direct Contributor access only to the target Space, and issue a temporary key without logging it.
5. Run an offline dry run with the exact manifest hash and count.
6. Apply one new revision with `--limit 1`; verify revision history, copied vector and fingerprint, audit actor, idempotent replay, and unchanged substantive recency.
7. Re-run the same manifest without a limit. The canary replays and the remaining entries apply.
8. Verify current and historical cue counts, revision and embedding counts, current-state identities, and representative positive and negative recalls.
9. Revoke the temporary key and disable the migration Principal. Re-enable normal writers only after verification.

Never generate or truncate production cues during application. If any entry drifts, stop and review a new immutable manifest. Rollback restores one of the paired backups and its matching binaries; it does not edit or delete immutable revisions in place.
