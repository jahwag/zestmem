# Releasing

## Normal release

Release Please reads Conventional Commits on `main` and maintains one release
PR plus `CHANGELOG.md`. Merge that PR with squash. It creates the GitHub release
and `vX.Y.Z` tag. Release Please explicitly dispatches `release.yml` at that
tag, because releases made with `GITHUB_TOKEN` do not trigger another workflow
through the `release` event. The workflow reproducibly builds, verifies, attests,
signs, scans, and publishes assets and images. Never create release tags by hand.

Release Please also dispatches CI and the required title check for each updated release PR, because its
`GITHUB_TOKEN`-created PRs do not trigger `pull_request` workflows. The weekly
merge workflow waits for these checks before merging the release PR.

For a manual artifact rebuild, dispatch `release.yml` with both `--ref vX.Y.Z`
and `-f tag=vX.Y.Z`. The workflow rejects a branch ref or a mismatched tag, so
keyless signatures always identify the exact release tag.

## Canonical repository and version continuity

`Bytelope/zestmem` is the source of truth for source, GitHub releases, and new
container images (`ghcr.io/bytelope/zestmem` and `ghcr.io/bytelope/zestmem-backup`).
Both the repository and these packages are private; consumers need the relevant
GitHub and registry access. The Go module path remains
`github.com/jahwag/zestmem` as an internal build/import identifier, not a source
or release-download location.

The deleted public repository published different code as `v0.9.0`. That
version is reserved: the next canonical release is `v0.10.0`, requested with a
`Release-As: 0.10.0` Conventional Commit footer on the reconciliation change.
The manifest remains at the last actual canonical release, `0.8.0`, until
Release Please prepares and merges the `0.10.0` release PR. Do not create or
move a `v0.9.0` tag in this repository, or reuse its image tags for different
code. Keep the existing release notes and tags as historical evidence.

The public `ghcr.io/jahwag/` images are retained for existing installations,
including cdev backup jobs. New release workflows never overwrite those
images. `compose.yaml` retains the old image references until a verified
canonical release exists and registry access is configured; changing those
references is a deployment migration, not part of release bookkeeping.

## Failure and retry

Do not move a published tag. Fix the workflow or source on `main` and publish a
new patch release. A failed release may be rerun only when inputs and tag are
unchanged. Mutable image aliases are created after scan and signature steps, so
an earlier failure cannot promote an unverified image.

## Emergency and rollback

For a critical fix, use a `fix:` PR and the owner bypass only when normal checks
cannot run; record why. Roll back deployments to a previously verified immutable
image digest or native version. Releases and tags remain as audit evidence and
must not be deleted or rewritten.

Before a schema-bearing deployment, stop or drain all writers and retain a
verified database backup paired with the previous binary. Older ZestMem
binaries intentionally reject databases containing newer migration versions,
so a binary-only rollback is not supported after a migration is applied.
Restore the paired pre-deployment database for a production rollback. Numbered
`.down.sql` files are development aids and must never be used to discard live
data without an explicit export and operator review.

For a reviewed `use_when` backfill, retain a second restore-tested backup after
the new schema is running but before the first cue revision. Pair it with the
new binaries. Freeze mutation-capable clients across both backup and backfill,
apply a one-entry canary from the exact hashed manifest, and revoke the
temporary migration Principal after full current-state verification. A rollback
after backfill restores the post-schema/pre-backfill pair; do not delete
immutable revision history in place.

Native Linux and macOS archives contain `memoryd`, `memoryctl`,
`memory-mcp-bridge`, and the POSIX-permission-aware `usewhen-backfill` tool.
The Windows archive intentionally contains only the three long-lived binaries.
Snapshot CI, both reproducible release builds, and release verification enforce
this exact five-archive matrix before publication or installation succeeds.

The SLSA reusable workflow is pinned to its required full version tag rather
than a commit SHA because GitHub's reusable-workflow verifier binds provenance
to that public tag. This is the sole action pinning exception.
