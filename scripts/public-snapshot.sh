#!/usr/bin/env bash
# public-snapshot.sh — copy HEAD of this (private) repository into a checkout
# of the public repository as one commit (D30). Never pushes.
#
# Usage: PUBLIC_AUTHOR="Name <id+login@users.noreply.github.com>" \
#          scripts/public-snapshot.sh <public-checkout-dir> [commit message]
#
# The tree is `git archive HEAD` minus scripts/public-exclude.txt, checked by
# scripts/check-public.sh before anything is written. The target directory
# is a git repository (created with `git init -b master` when missing); its
# tracked files are replaced by the snapshot.
set -euo pipefail

dest=${1:?usage: public-snapshot.sh <public-checkout-dir> [message]}
msg=${2:-"macro-collector snapshot $(git rev-parse --short HEAD)"}
author=${PUBLIC_AUTHOR:?set PUBLIC_AUTHOR="Name <email>" (a GitHub noreply address)}
root=$(git rev-parse --show-toplevel)
cd "$root"

if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
	echo "public-snapshot: uncommitted changes in tracked files; the snapshot is HEAD" >&2
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git archive HEAD | tar -x -C "$tmp"
while IFS= read -r p; do
	case "$p" in '' | '#'*) continue ;; esac
	rm -rf "${tmp:?}/$p"
done <scripts/public-exclude.txt

scripts/check-public.sh "$tmp"

mkdir -p "$dest"
if [ ! -d "$dest/.git" ]; then
	git -C "$dest" init -q -b master
fi
# replace the tracked tree: remove everything but .git, copy the snapshot in
find "$dest" -mindepth 1 -maxdepth 1 ! -name .git -exec rm -rf {} +
cp -R "$tmp"/. "$dest"/
git -C "$dest" add -A
if git -C "$dest" diff --cached --quiet; then
	echo "public-snapshot: nothing changed"
	exit 0
fi
name=${author% <*}
email=${author##*<}
email=${email%>}
GIT_AUTHOR_NAME=$name GIT_AUTHOR_EMAIL=$email GIT_COMMITTER_NAME=$name GIT_COMMITTER_EMAIL=$email \
	git -C "$dest" commit -q -m "$msg"
git -C "$dest" log --oneline -1
echo "public-snapshot: committed in $dest (not pushed)"
