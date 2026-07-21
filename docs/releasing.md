# Releasing

## Normal release

Release Please reads Conventional Commits on `main` and maintains one release
PR plus `CHANGELOG.md`. Merge that PR with squash. It creates the GitHub release
and `vX.Y.Z` tag; `release.yml` then reproducibly builds, verifies, attests,
signs, scans, and publishes assets and images. Never create release tags by hand.

The first release is bootstrapped as `v0.1.0`. Before merging it, run the
`ci.yml` workflow-dispatch rehearsal, confirm required checks exist, and only
then enable the `main` ruleset.

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

The SLSA reusable workflow is pinned to its required full version tag rather
than a commit SHA because GitHub's reusable-workflow verifier binds provenance
to that public tag. This is the sole action pinning exception.
