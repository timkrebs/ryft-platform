#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091,SC2015,SC2016
# scripts/check_workflows.sh on throwaway workflow directories: the bypasses of a grep (flow style,
# quoted or spaced keys, escaped names, anchors, external reusable workflows, secrets: inherit,
# dynamic secrets, test hooks) all fail, the repository's own workflows pass.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ check_workflows_test: $*" >&2; exit 1; }
# yq is on ubuntu-latest. Locally a missing yq skips this test with a notice; in CI it is a failure.
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  if [ -n "${CI:-}" ]; then fail "yq (mikefarah v4) is missing in CI: check_workflows.sh cannot run"; fi
  echo "- check_workflows_test: SKIPPED, yq (mikefarah v4) is not installed locally (brew install yq). CI does not skip."
  # Without yq the script itself must refuse, not pass.
  rc=0; "$HERE/scripts/check_workflows.sh" >/dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "without yq the script must exit 2 (got $rc)"
  exit 0
fi
d="$tmp/wf"
fresh() { rm -rf "$d"; mkdir -p "$d"; cp "$HERE/.github/workflows/ci.yml" "$HERE/.github/workflows/pr.yml" "$HERE/.github/workflows/deploy-watch.yml" "$d/"; }
run() { rc=0; "$HERE/scripts/check_workflows.sh" "$d" >"$tmp/out" 2>"$tmp/err" || rc=$?; }
# bad <name> <expected message part> <yaml of x.yml>: the repository's workflows plus x.yml must fail with that message.
bad() { fresh; printf '%s\n' "$3" > "$d/x.yml"; run; [ "$rc" = 1 ] || fail "$1 must fail (rc=$rc): $(cat "$tmp/err")"; grep -q "$2" "$tmp/err" || fail "$1: message lacks '$2': $(cat "$tmp/err")"; }
good() { fresh; printf '%s\n' "$2" > "$d/x.yml"; run; [ "$rc" = 0 ] || fail "$1 must pass (rc=$rc): $(cat "$tmp/err")"; }
JOB='    runs-on: x
    steps:
      - run: true'

fresh; run
[ "$rc" = 0 ] || fail "the repository's own workflows must pass: $(cat "$tmp/err")"

# --- triggers: a key, a list entry, an inline list, a mapping, flow style, YAML escapes, a comment
bad "pull_request_target as a key" 'pull_request_target' "name: x
on:
  pull_request_target:
    types: [opened]
jobs:
  a:
$JOB"
bad "workflow_run as a key" 'workflow_run' "name: x
on:
  workflow_run:
    workflows: [ci]
jobs:
  a:
$JOB"
bad "in an inline list" 'pull_request_target' "name: x
on: [push, pull_request_target]
jobs:
  a:
$JOB"
bad "as a scalar" 'workflow_run' "name: x
on: workflow_run
jobs:
  a:
$JOB"
bad "in a flow mapping" 'pull_request_target' "name: x
on: {pull_request_target: {types: [opened]}}
jobs:
  a:
$JOB"
bad "a quoted trigger name" 'workflow_run' "name: x
on:
  'workflow_run': {}
jobs:
  a:
$JOB"
bad "an escaped trigger name (double-quoted)" 'pull_request_target' 'name: x
on:
  "pull_request\x5ftarget": {}
jobs:
  a:
    runs-on: x
    steps:
      - run: true'
bad "an escaped trigger name (unicode)" 'workflow_run' 'name: x
on: ["workflow_run"]
jobs:
  a:
    runs-on: x
    steps:
      - run: true'
bad "a quoted on key" 'pull_request_target' "name: x
\"on\":
  pull_request_target: {}
jobs:
  a:
$JOB"
good "a comment is not a trigger" "# pull_request_target is why this is not here
name: x
on: push
jobs:
  a:
$JOB"

# --- the environment: another job, another workflow, another spelling
bad "another job naming the environment" 'names an environment' "name: x
on: push
jobs:
  other:
    runs-on: x
    environment: deploy
    steps:
      - run: true"
bad "a flow-style job" 'names an environment' "name: x
on: push
jobs: {steal: {runs-on: x, environment: deploy, steps: [{run: 'true'}]}}"
bad "a quoted environment key" 'names an environment' "name: x
on: push
jobs:
  steal:
    runs-on: x
    \"environment\": deploy
    steps:
      - run: true"
bad "a space before the colon" 'names an environment' "name: x
on: push
jobs:
  steal:
    runs-on: x
    environment : deploy
    steps:
      - run: true"
bad "an environment mapping" 'names an environment' "name: x
on: push
jobs:
  steal:
    runs-on: x
    environment:
      name: deploy
    steps:
      - run: true"
bad "an environment through an anchor" 'names an environment' "name: x
on: push
x-env: &e deploy
jobs:
  steal:
    runs-on: x
    environment: *e
    steps:
      - run: true"
bad "an environment named by an expression" 'names an environment' "name: x
on: push
jobs:
  steal:
    runs-on: x
    environment: \${{ inputs.e }}
    steps:
      - run: true"
fresh; sed 's/^    environment: deploy$/    environment: production/' "$d/ci.yml" > "$d/ci.new"; mv "$d/ci.new" "$d/ci.yml"; run
[ "$rc" = 1 ] || fail "promote in another environment must fail"
fresh; sed 's/^    environment: deploy$/    environment: ${{ vars.E }}/' "$d/ci.yml" > "$d/ci.new"; mv "$d/ci.new" "$d/ci.yml"; run
[ "$rc" = 1 ] || fail "promote in an environment named by an expression must fail"
fresh; grep -v '^    environment: deploy$' "$d/ci.yml" > "$d/ci.new"; mv "$d/ci.new" "$d/ci.yml"; run
[ "$rc" = 1 ] && grep -q "does not run in" "$tmp/err" || fail "promote without its environment must fail"
fresh; sed 's/^    environment: deploy$/    environment: { name: deploy, url: "https:\/\/ryftapp.de" }/' "$d/ci.yml" > "$d/ci.new"; mv "$d/ci.new" "$d/ci.yml"; run
[ "$rc" = 0 ] || fail "promote with an environment mapping and a url is still fine: $(cat "$tmp/err")"
fresh; cp "$d/ci.yml" "$d/ci2.yml"; run
[ "$rc" = 1 ] || fail "the environment in another file named like ci must fail"

# --- the App key
bad "another job reading the App key" 'RYFT_DEPLOY_APP_KEY' "name: x
on: push
jobs:
  steal:
    runs-on: x
    steps:
      - run: echo \${{ secrets.RYFT_DEPLOY_APP_KEY }}"
bad "the App key in lower case" 'RYFT_DEPLOY_APP_KEY' "name: x
on: push
jobs:
  steal:
    runs-on: x
    steps:
      - run: echo \${{ secrets.ryft_deploy_app_key }}"
bad "the App key at workflow level" 'RYFT_DEPLOY_APP_KEY' "name: x
on: push
env:
  K: \${{ secrets.RYFT_DEPLOY_APP_KEY }}
jobs:
  a:
$JOB"
bad "the App key in an env key" 'RYFT_DEPLOY_APP_KEY' "name: x
on: push
jobs:
  steal:
    runs-on: x
    env: {RYFT_DEPLOY_APP_KEY: x}
    steps:
      - run: true"

# --- dynamic secrets and secrets: inherit
bad "secrets[format(...)]" 'other than as secrets' "name: x
on: push
jobs:
  steal:
    runs-on: x
    steps:
      - run: echo \${{ secrets[format('RYFT_{0}_APP_KEY', 'DEPLOY')] }}"
bad "toJSON(secrets)" 'other than as secrets' "name: x
on: push
jobs:
  steal:
    runs-on: x
    steps:
      - run: echo '\${{ toJSON(secrets) }}'"
bad "the bare secrets context" 'other than as secrets' "name: x
on: push
jobs:
  steal:
    runs-on: x
    env:
      ALL: \${{ secrets }}
    steps:
      - run: true"
bad "secrets in an if without braces" 'other than as secrets' "name: x
on: push
jobs:
  steal:
    runs-on: x
    steps:
      - if: secrets['A'] != ''
        run: true"
bad "secrets: inherit to a local workflow" 'secrets: inherit' "name: x
on: push
jobs:
  call:
    uses: ./.github/workflows/pr.yml
    secrets: inherit"
bad "secrets: inherit to an external workflow" 'secrets: inherit' "name: x
on: push
jobs:
  call:
    uses: other/repo/.github/workflows/d.yml@0123456789abcdef0123456789abcdef01234567
    secrets: inherit"
good "ordinary secrets.<NAME> and the token" "name: x
on: push
jobs:
  a:
    runs-on: x
    env:
      T: \${{ secrets.GITHUB_TOKEN }}
      O: \${{ secrets.SOME_OTHER_SECRET }}
    steps:
      - if: \${{ secrets.SOME_OTHER_SECRET != '' }}
        run: echo 'no secrets here'"

# --- reusable workflows: only this repository's
bad "an external reusable workflow" 'external reusable workflow' "name: x
on: push
jobs:
  deploy:
    uses: other/repo/.github/workflows/d.yml@0123456789abcdef0123456789abcdef01234567"
bad "an external reusable workflow that names deploy, flow style" 'external reusable workflow' "name: x
on: push
jobs: {deploy: {uses: 'other/repo/.github/workflows/d.yml@main'}}"
bad "a local path that leaves the folder" 'external reusable workflow' "name: x
on: push
jobs:
  call:
    uses: ./.github/workflows/../../evil/d.yml"
bad "a local action instead of a workflow" 'external reusable workflow' "name: x
on: push
jobs:
  call:
    uses: ./.github/actions/thing"
bad "a docker reference" 'external reusable workflow' "name: x
on: push
jobs:
  call:
    uses: docker://alpine:3"
good "a reusable workflow of this repository" "name: x
on: push
jobs:
  call:
    uses: ./.github/workflows/pr.yml"

# --- the test hooks of the scripts
for hook in CHECK_REGISTRY CHECK_REGISTRY_OFFLINE CHECK_RENDER RENDER_CMD PIN_QUIET RYFT_HOOKS_NODE_DIRS; do
  bad "$hook in a step env" "$hook" "name: x
on: push
jobs:
  a:
    runs-on: x
    steps:
      - run: ./scripts/promote.sh
        env:
          $hook: /bin/true"
  bad "$hook at workflow level" "$hook" "name: x
on: push
env: {$hook: '1'}
jobs:
  a:
$JOB"
  bad "$hook in a script" "$hook" "name: x
on: push
jobs:
  a:
    runs-on: x
    steps:
      - run: |
          $hook=1 ./scripts/check_registry.sh"
done
bad "TODAY as an environment variable" 'NOW or TODAY' "name: x
on: push
jobs:
  a:
    runs-on: x
    env:
      TODAY: 2026-01-01
    steps:
      - run: true"
bad "NOW assigned in a script" 'NOW or TODAY' "name: x
on: push
jobs:
  a:
    runs-on: x
    steps:
      - run: NOW=1 ./scripts/deploy_watch.sh"
good "a variable that only contains a hook's letters" "name: x
on: push
jobs:
  a:
    runs-on: x
    env:
      MY_CHECK_REGISTRY_URL: x
      TODAYS_DATE: y
    steps:
      - run: true"

# --- shapes the parser must not let through
bad "a duplicate key (readers disagree which one counts)" 'appears twice' "name: x
on: push
jobs:
  a:
    runs-on: x
    environment: deploy
    environment: other
    steps:
      - run: true"
bad "a duplicate trigger key" 'appears twice' "name: x
on: push
on: pull_request_target
jobs:
  a:
$JOB"
bad "two documents" 'YAML documents' "name: x
on: push
jobs:
  a:
$JOB
---
name: y
on: pull_request_target
jobs:
  a:
$JOB"
bad "a YAML merge key" 'merge key' "name: x
on: push
x-base: &base
  runs-on: x
jobs:
  a:
    <<: *base
    steps:
      - run: true"
bad "a YAML merge key in flow style" 'merge key' "name: x
on: push
jobs:
  a: {<<: {runs-on: x}, steps: [{run: true}]}"
fresh; printf 'name: x\non: [push\njobs: {\n' > "$d/x.yml"; run
[ "$rc" = 1 ] || fail "a file that does not parse must fail (rc=$rc)"
fresh; : > "$d/x.yml"; run
[ "$rc" = 1 ] || fail "an empty workflow must fail (rc=$rc)"

echo "✓ check_workflows_test: parsed, not grepped: triggers, the environment and the App key only in promote, no external reusable workflows, no secrets: inherit or dynamic secrets, no test hooks"
