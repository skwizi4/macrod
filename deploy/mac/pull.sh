#!/bin/sh
# Pull the exchange repository (ТЗ §12.10): the server pushes exports, the Mac
# folder follows. Usage: pull.sh <exchange-dir>   (or EXCHANGE_DIR in env)
# launchd runs it every 15 min: deploy/mac/com.macro.exchange-pull.plist.example
set -eu
dir="${1:-${EXCHANGE_DIR:-}}"
if [ -z "$dir" ]; then
  echo "usage: pull.sh <exchange-dir>" >&2
  exit 2
fi
ts() { date '+%Y-%m-%d %H:%M:%S'; }
before=$(git -C "$dir" rev-parse HEAD 2>/dev/null || echo none)
if ! git -C "$dir" pull --ff-only -q; then
  echo "$(ts) pull failed in $dir" >&2
  exit 1
fi
after=$(git -C "$dir" rev-parse HEAD)
if [ "$before" != "$after" ]; then
  echo "$(ts) updated: $(git -C "$dir" log -1 --format='%h %s')"
fi
