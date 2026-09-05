#!/bin/sh
set -eu

version="${1:?release tag required}"
repo="Bytelope/zestmem"
printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || {
  echo "expected release tag vX.Y.Z" >&2
  exit 2
}
command -v gh >/dev/null
command -v cosign >/dev/null
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
gh release download "$version" --repo "$repo" --dir "$tmp"
(cd "$tmp" && sha256sum --check checksums.txt)
sh "$(dirname "$0")/verify-archive-contents.sh" "$tmp"
for artifact in "$tmp"/*.tar.gz "$tmp"/*.zip; do
  [ -f "$artifact" ] || continue
  cosign verify-blob --bundle "${artifact}.bundle" \
    --certificate-identity "https://github.com/${repo}/.github/workflows/release.yml@refs/tags/${version}" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com "$artifact"
done
