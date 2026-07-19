#!/bin/sh
set -eu

root="$(mktemp -d)"
trap 'rm -rf "$root"' EXIT
bin="$root/bin"
backups="$root/backups"
mkdir -p "$bin" "$backups"
printf 'restore-tested fixture\n' > "$backups/memory-20260719T120000Z.dump"

cat > "$bin/age" <<'EOF'
#!/bin/sh
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output="$2"; shift 2 ;;
    --recipient) shift 2 ;;
    *) input="$1"; shift ;;
  esac
done
printf 'age fixture\n' > "$output"
cat "$input" >> "$output"
EOF

cat > "$bin/curl" <<'EOF'
#!/bin/sh
method=GET
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --request) method="$2"; shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
cat >/dev/null
printf '%s %s\n' "$method" "$url" >> "$CURL_LOG"
case "$url" in
  https://uploads.github.com/*) printf '{"state":"uploaded"}' ;;
  */assets\?per_page=100) printf '[]' ;;
  *releases\?per_page=100) printf '[]' ;;
  */releases) printf '{"id":42}' ;;
  *) printf '{}' ;;
esac
EOF
chmod 0755 "$bin/age" "$bin/curl"

PATH="$bin:$PATH" \
BACKUP_ROOT="$backups" \
CURL_LOG="$root/curl.log" \
GITHUB_BACKUP_REPOSITORY="example/private-backups" \
GITHUB_TOKEN="test-token-not-real" \
AGE_RECIPIENT="age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq" \
GITHUB_BACKUP_RETENTION_DAYS=30 \
  sh ops/github-upload.sh "$backups/memory-20260719T120000Z.dump" >/dev/null

test -s "$backups/memory-20260719T120000Z.dump.age"
grep -q 'POST https://api.github.com/repos/example/private-backups/releases' "$root/curl.log"
grep -q 'POST https://uploads.github.com/repos/example/private-backups/releases/42/assets' "$root/curl.log"
if grep -q 'test-token-not-real' "$root/curl.log"; then
  printf 'token leaked into curl arguments\n' >&2
  exit 1
fi
printf 'github_upload_ok\n'
