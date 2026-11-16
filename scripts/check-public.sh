#!/usr/bin/env bash
# check-public.sh — fail when tracked files carry personal data or secrets
# (phase 15, ТЗ §21.7 п.6). Run from the repository root; CI runs it too.
#
# Generic rules live here. Personal patterns (your e-mail, server address,
# bot and channel names) never go into the repo: put one extended regex per
# line into the file named by PUBLIC_DENY_FILE (default secrets/public-deny.txt,
# which is git-ignored). Lines starting with # are comments.
#
# Usage: scripts/check-public.sh [dir]   (dir: a plain tree, e.g. a snapshot;
#                                         default: tracked files of this repo)
set -euo pipefail

dir=${1:-.}
deny=${PUBLIC_DENY_FILE:-secrets/public-deny.txt}
found=0

# files the public snapshot leaves out (scripts/public-exclude.txt) are not checked
skip=()
here=$(cd "$(dirname "$0")" && pwd)
if [ -f "$here/public-exclude.txt" ]; then
	while IFS= read -r p; do
		case "$p" in '' | '#'*) continue ;; esac
		skip+=(":!$p")
	done <"$here/public-exclude.txt"
fi

# search PATTERN [EXCLUDES...] — print matches, remember the failure
search() {
	local pattern=$1
	shift
	local out
	if [ -d "$dir/.git" ] && [ "$dir" = "." ]; then
		out=$(git grep -I -n -i -E -e "$pattern" -- . "$@" ${skip[@]+"${skip[@]}"} || true)
	else
		local ex=()
		for e in "$@"; do ex+=(--exclude-dir="${e#:!}"); done
		out=$(grep -r -I -n -i -E ${ex[@]+"${ex[@]}"} --exclude-dir=.git -e "$pattern" "$dir" || true)
	fi
	if [ -n "$out" ]; then
		echo "$out" | cut -c1-200
		found=1
	fi
}

# 1. absolute home paths (ТЗ §21.1)
search '/(Users|home)/[a-z0-9._-]+/' ':!testdata'
# 2. private keys and well-known token shapes
search '-----BEGIN [A-Z ]*PRIVATE KEY-----'
search '\b(ghp|gho|ghs|ghu)_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|\bAKIA[0-9A-Z]{16}\b|\bxox[abp]-[A-Za-z0-9-]{10,}|\bAIza[0-9A-Za-z_-]{35}\b|\b[0-9]{8,10}:AA[0-9A-Za-z_-]{33}\b'
# 3. e-mail addresses outside recorded fixtures, except placeholders
out=$( { if [ "$dir" = "." ]; then git grep -I -n -o -E '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' -- . ':!testdata' ${skip[@]+"${skip[@]}"}; \
	else grep -r -I -n -o -E --exclude-dir=.git --exclude-dir=testdata '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' "$dir"; fi; } 2>/dev/null \
	| grep -v -i -E '@(example\.(com|org|net|test)|users\.noreply\.github\.com)$|:git@github\.com$|:git@example\.test$' || true)
if [ -n "$out" ]; then
	echo "$out"
	found=1
fi
# 4. personal patterns from the local deny file
if [ -f "$deny" ]; then
	while IFS= read -r p; do
		case "$p" in '' | '#'*) continue ;; esac
		search "$p"
	done <"$deny"
else
	echo "note: $deny not found — personal patterns not checked" >&2
fi

if [ "$found" -ne 0 ]; then
	echo "check-public: personal data or secrets found (see above)" >&2
	exit 1
fi
echo "✅ check-public passed"
