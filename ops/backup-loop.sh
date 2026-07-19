#!/bin/sh
set -eu

while true; do
  now="$(date +%s)"
  next="$(date -d 'today 02:17 UTC' +%s)"
  if [ "$next" -le "$now" ]; then next="$(date -d 'tomorrow 02:17 UTC' +%s)"; fi
  sleep "$((next-now))"
  until /usr/local/bin/backup.sh; do
    printf 'backup_retry delay_seconds=3600\n' >&2
    sleep 3600
  done
done
