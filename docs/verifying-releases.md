# Verifying releases

Download a release's archives, `checksums.txt`, signature bundles, SPDX JSON
SBOMs, and provenance. From that directory, run `sha256sum --check checksums.txt`.

Verify keyless signatures while binding them to this repository and workflow:

```sh
cosign verify-blob --bundle zestmem_0.1.0_linux_amd64.tar.gz.bundle \
  --certificate-identity-regexp '^https://github.com/jahwag/zestmem/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  zestmem_0.1.0_linux_amd64.tar.gz
```

Use `slsa-verifier verify-artifact --provenance-path multiple.intoto.jsonl
--source-uri github.com/jahwag/zestmem --source-tag v0.1.0 <archive>` for each
native archive.

For images, select the immutable digest shown in the release workflow and run:

```sh
cosign verify --certificate-identity-regexp '^https://github.com/jahwag/zestmem/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/jahwag/zestmem@sha256:...
cosign verify-attestation --type spdxjson ghcr.io/jahwag/zestmem@sha256:...
docker buildx imagetools inspect ghcr.io/jahwag/zestmem@sha256:...
```

The manifest must contain Linux amd64 and arm64. Inspect the configuration to
confirm the server image runs as non-root and has only `/usr/local/bin/memoryd`.
