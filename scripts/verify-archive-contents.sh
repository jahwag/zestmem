#!/bin/sh
set -eu

directory="${1:?archive directory required}"

fail() {
	echo "archive contents: $*" >&2
	exit 1
}

members() {
	archive="$1"
	case "$archive" in
		*.tar.gz)
			listing="$(tar -tzf "$archive")" || fail "cannot read $archive"
			;;
		*.zip)
			unzip -tqq "$archive" >/dev/null || fail "cannot test $archive"
			listing="$(unzip -Z1 "$archive")" || fail "cannot list $archive"
			;;
		*) fail "unsupported archive $archive" ;;
	esac
	printf '%s\n' "$listing" | sed '/^$/d' | LC_ALL=C sort
}

verify() {
	pattern="$1"
	expected="$2"
	set -- "$directory"/$pattern
	[ "$#" -eq 1 ] && [ -f "$1" ] || fail "expected exactly one $pattern archive"
	base="${1##*/}"
	case "$base" in
		*.tar.gz) root="${base%.tar.gz}" ;;
		*.zip) root="${base%.zip}" ;;
		*) fail "unsupported archive $1" ;;
	esac
	expected="$(printf '%s\n' "$expected" | awk -v root="$root" '{ print root "/" $0 }')"
	actual="$(members "$1")"
	[ "$actual" = "$expected" ] || {
		echo "unexpected members in $1" >&2
		echo "expected:" >&2
		echo "$expected" >&2
		echo "actual:" >&2
		echo "$actual" >&2
		exit 1
	}
}

unix_members="$(printf '%s\n' LICENSE README.md memory-mcp-bridge memoryctl memoryd usewhen-backfill | LC_ALL=C sort)"
windows_members="$(printf '%s\n' LICENSE README.md memory-mcp-bridge.exe memoryctl.exe memoryd.exe | LC_ALL=C sort)"

verify 'zestmem_*_linux_amd64.tar.gz' "$unix_members"
verify 'zestmem_*_linux_arm64.tar.gz' "$unix_members"
verify 'zestmem_*_darwin_amd64.tar.gz' "$unix_members"
verify 'zestmem_*_darwin_arm64.tar.gz' "$unix_members"
verify 'zestmem_*_windows_amd64.zip' "$windows_members"

archive_count="$(find "$directory" -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.zip' \) | wc -l | tr -d ' ')"
[ "$archive_count" -eq 5 ] || fail "expected 5 native archives, found $archive_count"
