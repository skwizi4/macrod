#!/bin/sh
# Copy the newest daily DB backup from the server to the Mac and keep the
# newest copies (off-server backup, phase 6).
# Usage: fetch-backup.sh <local-dir>
# Env:   DEPLOY_HOST        ssh host of the server (user@host or a ~/.ssh/config alias)
#        DEPLOY_KEY         ssh private key for the server (optional; default: ssh config)
#        KEEP               copies kept locally (default 3)
#        REMOTE_BACKUP_DIR  backups folder on the server (default: macro-collector/data/backups,
#                           relative to the server user's home)
# launchd runs it weekly: deploy/mac/com.macro.backup-fetch.plist.example
set -eu
local_dir="${1:-}"
host="${DEPLOY_HOST:-}"
remote_dir="${REMOTE_BACKUP_DIR:-macro-collector/data/backups}"
keep="${KEEP:-3}"
key="${DEPLOY_KEY:-}"
if [ -z "$local_dir" ] || [ -z "$host" ]; then
  echo "usage: DEPLOY_HOST=<host> fetch-backup.sh <local-dir>" >&2
  exit 2
fi
ssh_opts="-o BatchMode=yes"
if [ -n "$key" ]; then ssh_opts="$ssh_opts -i $key -o IdentitiesOnly=yes"; fi
ts() { date '+%Y-%m-%d %H:%M:%S'; }
newest=$(ssh $ssh_opts "$host" "ls -1 $remote_dir/macro-*.db 2>/dev/null | tail -n 1")
if [ -z "$newest" ]; then
  echo "$(ts) no backups in $host:$remote_dir" >&2
  exit 1
fi
mkdir -p "$local_dir"
name=$(basename "$newest")
scp -q $ssh_opts "$host:$newest" "$local_dir/$name.part"
mv "$local_dir/$name.part" "$local_dir/$name"
echo "$(ts) fetched $name ($(du -h "$local_dir/$name" | cut -f1))"
# keep the newest $keep (names sort by date); BSD head has no negative counts
files=$(ls -1 "$local_dir"/macro-*.db | sort)
extra=$(( $(printf '%s\n' "$files" | wc -l) - keep ))
if [ "$extra" -gt 0 ]; then
  printf '%s\n' "$files" | head -n "$extra" | while read -r old; do
    rm -f "$old"
    echo "$(ts) removed $(basename "$old")"
  done
fi
