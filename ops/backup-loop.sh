#!/bin/sh
set -eu

while true; do
  now="$(date +%s)"
  next="$(date -d 'today 02:17 UTC' +%s)"
  if [ "$next" -le "$now" ]; then next="$(date -d 'tomorrow 02:17 UTC' +%s)"; fi
  sleep "$((next-now))"
  /usr/local/bin/backup.sh
done
