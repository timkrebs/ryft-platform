#!/usr/bin/env bash
# CI's pr-title job (.github/workflows/pr.yml), runnable locally:
#   scripts/check_pr.sh "<PR title>" [<base-sha> <head-sha>]
# 1. The title reads "area: what changed". The area is any lower-case word, optionally
#    with path segments and a scope: web, deploy, infra/aws/kubernetes, events, ci(deps).
#    (A closed list would reject most of this repository's history.)
# 2. The denylisted brand (web/config/brand-denylist.json) is in neither the title nor
#    the message of any commit in base..head: a squash merge may land under a commit's
#    message (settings COMMIT_OR_PR_TITLE / COMMIT_MESSAGES). Node built-ins only.
set -euo pipefail
cd "$(dirname "$0")/.."
title=${1:?usage: check_pr.sh "<PR title>" [<base-sha> <head-sha>]}
base=${2:-}; head=${3:-}
rc=0
if printf '%s' "$title" | grep -Eq '^[a-z][a-z0-9-]*(/[a-z0-9-]+)*(\([a-z0-9-]+\))?: [^[:space:]].{2,}$'; then
  echo "ok: $title"
else
  echo "::error title=PR title::The PR title must read 'area: what changed' (lower-case area, e.g. web, deploy, infra/vault, ci), not '$title'."
  rc=1
fi
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
printf '%s\n' "$title" > "$tmp/msg"
node web/scripts/check-brand.mjs --message "$tmp/msg" || { echo "::error title=PR title::the PR title names the denylisted brand"; rc=1; }
if [ -n "$base" ] && [ -n "$head" ]; then
  for c in $(git rev-list --no-merges "$base..$head"); do
    git log -1 --format=%B "$c" > "$tmp/msg"
    node web/scripts/check-brand.mjs --message "$tmp/msg" || { echo "::error title=commit message::commit $(git rev-parse --short "$c") names the denylisted brand"; rc=1; }
  done
fi
exit "$rc"
