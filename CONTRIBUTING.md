# Contributing

Contributions to ZestMem are welcome.

Development requires Go 1.25, Docker, and Node.js 22. Before opening a pull
request, run:

```sh
go test ./...
go vet ./...
make integration
make release-snapshot
```

Open an issue before starting a substantial change so the approach can be
discussed. Keep pull requests focused, use conventional commit titles such as
`fix: ...`, `feat: ...`, `docs: ...`, `build: ...`, or `ci: ...`, and do not
commit credentials or generated local state.

Pull requests target `main` and must pass the repository's required checks.

By submitting a contribution, you agree that it may be distributed under the
repository's MIT License.
