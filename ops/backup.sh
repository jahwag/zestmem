#!/bin/sh
set -eu

umask 077
lockdir="/backups/.agent-memory-backup.lock"
if ! mkdir "$lockdir" 2>/dev/null; then
  printf 'backup_skipped reason=already_running\n' >&2
  exit 1
fi
trap 'rmdir "$lockdir"' EXIT
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
partial="/backups/memory-${stamp}.dump.partial"
final="/backups/memory-${stamp}.dump"

pg_dump -Fc --no-owner --no-acl --file "$partial"
/usr/local/bin/restore-test.sh "$partial"
mv "$partial" "$final"
date -u +%Y-%m-%dT%H:%M:%SZ > /backups/last-success
if [ -n "${GITHUB_BACKUP_REPOSITORY:-}" ] || [ -n "${AGE_RECIPIENT:-}" ] || [ -n "${GITHUB_TOKEN:-}" ]; then
  /usr/local/bin/github-upload.sh "$final"
  date -u +%Y-%m-%dT%H:%M:%SZ > /backups/last-offsite-success
fi
find /backups -type f -name 'memory-*.dump' -mtime "+${BACKUP_RETENTION_DAYS:-14}" -delete
printf 'backup_verified file=%s\n' "$final"
