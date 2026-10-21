#!/usr/bin/env bash
# CI's pr-title job (.github/workflows/pr.yml), runnable locally:
#   scripts/check_pr.sh "<PR title>" [<base-sha> <head-sha>]
# 1. The title is one line reading "area: what changed". The area is any lower-case word,
#    optionally with path segments and a scope: web, deploy, infra/aws/kubernetes, events,
#    ci(deps). (A closed list would reject most of this repository's history.) GitHub's own
#    revert titles, Revert "<a valid title>", pass.
# 2. The denylisted brand (web/config/brand-denylist.json) is in neither the title nor the
#    message of any commit in base..head, merge commits included: a squash merge may land
#    under a commit's message (settings COMMIT_OR_PR_TITLE / COMMIT_MESSAGES). Node
#    built-ins only. In CI (GITHUB_BASE_REF set) the check and its denylist come from the
#    BASE branch, so a PR cannot weaken the rule it is judged by.
# The title is never echoed raw (it is untrusted text in a workflow log).
set -euo pipefail
cd "$(dirname "$0")/.."
title=${1:?usage: check_pr.sh "<PR title>" [<base-sha> <head-sha>]}
base=${2:-}; head=${3:-}
rc=0
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

shape=$title
case "$shape" in 'Revert "'*'"') shape=${shape#Revert \"}; shape=${shape%\"} ;; esac
case "$title" in
  *$'\n'*|*$'\r'*) echo "::error title=PR title::The PR title must be a single line."; rc=1 ;;
  *)
    if printf '%s' "$shape" | grep -Eq '^[a-z][a-z0-9-]*(/[a-z0-9-]+)*(\([a-z0-9-]+\))?: [^[:space:]].{2,}$'; then
      echo "ok: the title reads 'area: what changed'"
    else
      echo "::error title=PR title::The PR title must read 'area: what changed' (lower-case area, e.g. web, deploy, infra/vault, ci)."
      rc=1
    fi ;;
esac

brand=web/scripts/check-brand.mjs
if [ -n "${GITHUB_BASE_REF:-}" ]; then
  git archive "origin/$GITHUB_BASE_REF" web/scripts web/config | tar -x -C "$tmp"
  brand=$tmp/web/scripts/check-brand.mjs
fi
printf '%s\n' "$title" > "$tmp/msg"
node "$brand" --message "$tmp/msg" || { echo "::error title=PR title::the PR title names the denylisted brand"; rc=1; }
if [ -n "$base" ] && [ -n "$head" ]; then
  revs=$(git rev-list "$base..$head")
  for c in $revs; do
    git log -1 --format=%B "$c" > "$tmp/msg"
    node "$brand" --message "$tmp/msg" || { echo "::error title=commit message::commit $(git rev-parse --short "$c") names the denylisted brand"; rc=1; }
  done
fi
exit "$rc"
