# Verifying releases

The examples below target release `v0.10.1`; substitute the exact version you
are verifying.

Download a release's archives, `checksums.txt`, signature bundles, SPDX JSON
SBOMs, and provenance. From that directory, run `sha256sum --check checksums.txt`.

Verify keyless signatures while binding them to this repository and workflow:

```sh
cosign verify-blob --bundle zestmem_0.10.1_linux_amd64.tar.gz.bundle \
  --certificate-identity 'https://github.com/jahwag/zestmem/.github/workflows/release.yml@refs/tags/v0.10.1' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  zestmem_0.10.1_linux_amd64.tar.gz
```

Use `slsa-verifier verify-artifact --provenance-path multiple.intoto.jsonl
--source-uri github.com/jahwag/zestmem --source-tag v0.10.1 <archive>` for each
native archive.

For images, select the immutable digest shown in the release workflow and run:

```sh
cosign verify --certificate-identity 'https://github.com/jahwag/zestmem/.github/workflows/release.yml@refs/tags/v0.10.1' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/jahwag/zestmem@sha256:...
docker buildx imagetools inspect ghcr.io/jahwag/zestmem@sha256:...
```

Repeat the image signature check for `ghcr.io/jahwag/zestmem-backup`.
The workflow signs image digests; it does not publish a separate signed SPDX
image attestation. Native archive SPDX SBOMs are attached to the release.

The manifest must contain Linux amd64 and arm64. Inspect the configuration to
confirm the server image runs as non-root and has only `/usr/local/bin/memoryd`.

## Historical identities

Releases `v0.10.0` and `v0.10.1` were signed while the repository was named
`Bytelope/zestmem`. Verify their signatures and provenance against
`https://github.com/Bytelope/zestmem/.github/workflows/release.yml@refs/tags/<tag>`
and `--source-uri github.com/Bytelope/zestmem`. Images tagged `0.9.0` belong to
an earlier distribution; do not verify them against the current repository
identity.
