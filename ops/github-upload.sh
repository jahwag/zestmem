#!/bin/sh
set -eu

umask 077
dump="${1:?verified dump path required}"
backup_root="${BACKUP_ROOT:-/backups}"
: "${GITHUB_BACKUP_REPOSITORY:?set GITHUB_BACKUP_REPOSITORY to owner/private-repo}"
: "${GITHUB_TOKEN:?set a fine-grained token with Contents write access to the backup repository}"
: "${AGE_RECIPIENT:?set the age public recipient}"

case "$GITHUB_BACKUP_REPOSITORY" in
  */*) ;;
  *) printf 'invalid GITHUB_BACKUP_REPOSITORY\n' >&2; exit 2 ;;
esac
case "$AGE_RECIPIENT" in
  age1*) ;;
  *) printf 'invalid AGE_RECIPIENT\n' >&2; exit 2 ;;
esac
case "$dump" in
  "$backup_root"/memory-*.dump) ;;
  *) printf 'refusing unverified or unexpected dump path\n' >&2; exit 2 ;;
esac

name="$(basename "$dump").age"
tag="zestmem-backup-${name#memory-}"
tag="${tag%.dump.age}"
encrypted="$backup_root/$name"
partial="$encrypted.partial"
trap 'rm -f "$partial"' EXIT

age --recipient "$AGE_RECIPIENT" --output "$partial" "$dump"
mv "$partial" "$encrypted"
checksum="$(sha256sum "$encrypted" | awk '{print $1}')"
size="$(wc -c < "$encrypted" | tr -d ' ')"

github() {
  method="$1"
  url="$2"
  shift 2
  curl --silent --show-error --request "$method" --config - "$@" "$url" <<EOF
header = "Authorization: Bearer $GITHUB_TOKEN"
header = "X-GitHub-Api-Version: 2022-11-28"
header = "Accept: application/vnd.github+json"
EOF
}

payload="$(jq -cn --arg tag "$tag" --arg name "$name" --arg checksum "$checksum" --argjson size "$size" '{tag_name:$tag,name:$name,body:("age-encrypted, restore-tested ZestMem backup\nsha256: "+$checksum+"\nbytes: "+($size|tostring)),prerelease:true}')"
release="$(github POST "https://api.github.com/repos/$GITHUB_BACKUP_REPOSITORY/releases" --header 'Content-Type: application/json' --data "$payload")"
release_id="$(printf '%s' "$release" | jq -r '.id // empty')"
if [ -z "$release_id" ]; then
  release="$(github GET "https://api.github.com/repos/$GITHUB_BACKUP_REPOSITORY/releases/tags/$tag")"
  release_id="$(printf '%s' "$release" | jq -r '.id // empty')"
fi
if [ -z "$release_id" ]; then
  printf 'GitHub release creation failed\n' >&2
  exit 1
fi

assets="$(github GET "https://api.github.com/repos/$GITHUB_BACKUP_REPOSITORY/releases/$release_id/assets?per_page=100")"
old_asset_id="$(printf '%s' "$assets" | jq -r --arg name "$name" '.[] | select(.name==$name) | .id' | head -n 1)"
if [ -n "$old_asset_id" ]; then
  github DELETE "https://api.github.com/repos/$GITHUB_BACKUP_REPOSITORY/releases/assets/$old_asset_id" >/dev/null
fi
upload="$(github POST "https://uploads.github.com/repos/$GITHUB_BACKUP_REPOSITORY/releases/$release_id/assets?name=$name" --header 'Content-Type: application/octet-stream' --data-binary "@$encrypted")"
if [ "$(printf '%s' "$upload" | jq -r '.state // empty')" != "uploaded" ]; then
  printf 'GitHub backup asset upload failed\n' >&2
  exit 1
fi

retention_days="${GITHUB_BACKUP_RETENTION_DAYS:-30}"
case "$retention_days" in *[!0-9]*|'') printf 'invalid GITHUB_BACKUP_RETENTION_DAYS\n' >&2; exit 2;; esac
cutoff="$(( $(date -u +%s) - retention_days * 86400 ))"
github GET "https://api.github.com/repos/$GITHUB_BACKUP_REPOSITORY/releases?per_page=100" |
  jq -r --argjson cutoff "$cutoff" '.[] | select(.tag_name|startswith("zestmem-backup-")) | select((.created_at|fromdateiso8601) < $cutoff) | [.id,.tag_name] | @tsv' |
  while IFS="$(printf '\t')" read -r old_id old_tag; do
    [ -n "$old_id" ] || continue
    github DELETE "https://api.github.com/repos/$GITHUB_BACKUP_REPOSITORY/releases/$old_id" >/dev/null
    github DELETE "https://api.github.com/repos/$GITHUB_BACKUP_REPOSITORY/git/refs/tags/$old_tag" >/dev/null
  done

find "$backup_root" -type f -name 'memory-*.dump.age' -mtime "+$retention_days" -delete
printf 'offsite_backup_uploaded repository=%s tag=%s asset=%s sha256=%s\n' "$GITHUB_BACKUP_REPOSITORY" "$tag" "$name" "$checksum"
