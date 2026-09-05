# Verifying releases

Authenticate `gh` with access to the private `Bytelope/zestmem` repository.
The examples below target the canonical release, `v0.10.1`; substitute
the exact version you are verifying.

Download a release's archives, `checksums.txt`, signature bundles, SPDX JSON
SBOMs, and provenance. From that directory, run `sha256sum --check checksums.txt`.

Verify keyless signatures while binding them to this repository and workflow:

```sh
cosign verify-blob --bundle zestmem_0.10.1_linux_amd64.tar.gz.bundle \
  --certificate-identity 'https://github.com/Bytelope/zestmem/.github/workflows/release.yml@refs/tags/v0.10.1' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  zestmem_0.10.1_linux_amd64.tar.gz
```

Use `slsa-verifier verify-artifact --provenance-path multiple.intoto.jsonl
--source-uri github.com/Bytelope/zestmem --source-tag v0.10.1 <archive>` for each
native archive.

For images, select the immutable digest shown in the release workflow and run:

```sh
cosign verify --certificate-identity 'https://github.com/Bytelope/zestmem/.github/workflows/release.yml@refs/tags/v0.10.1' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/bytelope/zestmem@sha256:...
cosign verify-attestation --type spdxjson ghcr.io/bytelope/zestmem@sha256:...
docker buildx imagetools inspect ghcr.io/bytelope/zestmem@sha256:...
```

The manifest must contain Linux amd64 and arm64. Inspect the configuration to
confirm the server image runs as non-root and has only `/usr/local/bin/memoryd`.

## Retired public repository

The retained `ghcr.io/jahwag/zestmem` and `ghcr.io/jahwag/zestmem-backup`
images belong to the retired public distribution. Existing deployments and
backup jobs may continue using their pinned digests. Their signatures retain
the original `jahwag/zestmem` workflow identity; do not verify historical
images against the new repository identity. New canonical releases publish
to `ghcr.io/bytelope/` and require access to those private packages.
