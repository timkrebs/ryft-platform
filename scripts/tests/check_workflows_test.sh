#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091,SC2015,SC2016
# scripts/check_workflows.sh on throwaway workflow directories.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ check_workflows_test: $*" >&2; exit 1; }
d="$tmp/wf"
fresh() { rm -rf "$d"; mkdir -p "$d"; cp "$HERE/.github/workflows/ci.yml" "$HERE/.github/workflows/pr.yml" "$d/"; }
run() { rc=0; "$HERE/scripts/check_workflows.sh" "$d" >"$tmp/out" 2>"$tmp/err" || rc=$?; }

fresh; run
[ "$rc" = 0 ] || fail "the repository's own workflows must pass: $(cat "$tmp/err")"
cp "$HERE/.github/workflows/deploy-watch.yml" "$d/"; run
[ "$rc" = 0 ] || fail "with deploy-watch.yml: $(cat "$tmp/err")"

# pull_request_target / workflow_run, as a key, a list entry or an inline list; a comment is fine.
for t in "pull_request_target:" "workflow_run:" "pull_request_target, push"; do
  fresh; printf 'name: x\non: [%s]\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n' "$t" > "$d/x.yml"
  if [ "$t" = "pull_request_target:" ] || [ "$t" = "workflow_run:" ]; then printf 'name: x\non:\n  %s\n    types: [completed]\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: true\n' "$t" > "$d/x.yml"; fi
  run; [ "$rc" = 1 ] || fail "'$t' must fail (rc=$rc)"
done
fresh; printf '# pull_request_target is why this is not here\nname: x\non: push\njobs:\n  a:\n    runs-on: x\n    steps:\n      - run: true\n' > "$d/x.yml"; run
[ "$rc" = 0 ] || fail "a comment is not a trigger"

# The environment: another job, another workflow, another name.
fresh; printf 'name: x\non: push\njobs:\n  other:\n    runs-on: x\n    environment: deploy\n    steps:\n      - run: true\n' > "$d/x.yml"; run
[ "$rc" = 1 ] || fail "another workflow naming the environment must fail"
fresh; sed 's/^    environment: deploy$/    environment: production/' "$d/ci.yml" > "$d/ci.new"; mv "$d/ci.new" "$d/ci.yml"; run
[ "$rc" = 1 ] || fail "promote in another environment must fail"
fresh; grep -v '^    environment: deploy$' "$d/ci.yml" > "$d/ci.new"; mv "$d/ci.new" "$d/ci.yml"; run
[ "$rc" = 1 ] || fail "promote without its environment must fail"
fresh; printf 'name: x\non: push\njobs:\n  steal:\n    runs-on: x\n    steps:\n      - run: echo ${{ secrets.RYFT_DEPLOY_APP_KEY }}\n' > "$d/x.yml"; run
[ "$rc" = 1 ] || fail "another job reading the App key must fail"
echo "✓ check_workflows_test: no pull_request_target/workflow_run, the environment and the App key only in promote"
