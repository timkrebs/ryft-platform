#!/usr/bin/env bash
# The workflow rules that actionlint cannot know (CI's `lint` job):
#   1. no workflow triggers on pull_request_target or workflow_run: they run with the base
#      repository's secrets and token on code a pull request influences;
#   2. the environment `deploy` (where the deploy App's key lives, only main may use it) is
#      named by exactly one job, `promote` in ci.yml, and RYFT_DEPLOY_APP_KEY is read only there.
# Comments are ignored. Usage: scripts/check_workflows.sh [workflow-dir]   (default .github/workflows)
set -euo pipefail
cd "$(dirname "$0")/.."
dir=${1:-.github/workflows}
rc=0
fail() { echo "::error title=workflow rule::$*" >&2; rc=1; }
shopt -s nullglob
files=("$dir"/*.yml "$dir"/*.yaml)
[ "${#files[@]}" -gt 0 ] || { echo "check_workflows: no workflows in $dir" >&2; exit 2; }

for f in "${files[@]}"; do
  if grep -v '^[[:space:]]*#' "$f" | grep -Eq '(^|[^[:alnum:]_])(pull_request_target|workflow_run)([^[:alnum:]_]|$)'; then
    fail "$f uses pull_request_target or workflow_run: not allowed (secrets and write token with a pull request's influence)"
  fi
  # "<job> <line>" for every environment: / RYFT_DEPLOY_APP_KEY outside comments, job = the key
  # at two spaces of indent under jobs:.
  hits=$(awk '
    /^jobs:/ { injobs = 1; next }
    /^[^ #]/ { injobs = 0 }
    injobs && /^  [A-Za-z0-9_-]+:/ { job = $1; sub(/:$/, "", job) }
    /^[[:space:]]*#/ { next }
    /^[[:space:]]*environment:/ { v = $0; sub(/^[[:space:]]*environment:[[:space:]]*/, "", v); print "env " job " " v }
    /RYFT_DEPLOY_APP_KEY/ { print "key " job }
  ' "$f")
  while read -r kind job value; do
    [ -n "${kind:-}" ] || continue
    case "$kind" in
      env)
        if [ "$job" != promote ] || [ "$(basename "$f")" != ci.yml ]; then fail "$f: job '$job' names an environment ($value): only ci.yml's promote may (the deploy App's key is scoped to it)"; fi
        if [ "$value" != deploy ]; then fail "$f: promote must run in the environment 'deploy', not '$value'"; fi ;;
      key)
        if [ "$job" != promote ] || [ "$(basename "$f")" != ci.yml ]; then fail "$f: job '$job' reads RYFT_DEPLOY_APP_KEY: only ci.yml's promote may"; fi ;;
    esac
  done <<EOT
$hits
EOT
done
# promote has its environment.
if [ -f "$dir/ci.yml" ]; then
  awk '/^  promote:/ { p = 1; next } /^  [A-Za-z0-9_-]+:/ { p = 0 } p && /^    environment: deploy[[:space:]]*$/ { ok = 1 } END { exit ok ? 0 : 1 }' "$dir/ci.yml" \
    || fail "$dir/ci.yml: the promote job does not run in 'environment: deploy'"
fi
[ "$rc" = 0 ] && echo "workflows: no pull_request_target/workflow_run; only promote names the environment deploy and reads the App key"
exit "$rc"
