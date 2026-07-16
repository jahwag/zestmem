# Contributing

ZestMem is currently private and all contributions require authorization from
Bytelope. Install Go 1.25, Podman (the local default) or Docker, and GNU Make.

Run `make verify`, `make integration`, `make backup`, `make load`, and
`make release-snapshot` before requesting review. Keep migrations append-only
and update operational documentation with behavior changes.

Open a focused pull request. Its title must use Conventional Commits, for
example `feat(mcp): add bounded search filters`. Allowed types are `feat`,
`fix`, `docs`, `refactor`, `test`, `build`, `ci`, `chore`, `perf`, and
`revert`. The repository uses squash merges; the title becomes the commit on
`main` and drives Release Please.

By contributing, you agree that Bytelope may use the contribution under the
repository's proprietary license. Do not submit third-party material without
documented permission.
