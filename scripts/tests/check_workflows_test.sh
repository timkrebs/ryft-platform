#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091,SC2015,SC2016
# scripts/check_workflows.sh on throwaway workflow directories: the bypasses of a grep (flow style,
# quoted or spaced keys, escaped names, anchors, external reusable workflows, secrets: inherit,
# dynamic secrets, test hooks), the environment outside deploy.yml's deploy and site, the old App
# key anywhere, deploy.yml's gate, status functions and continue-on-error, triggers, tokens,
# concurrency, its superseded and tip gates, its scan-before-sign-in order, a third-party action or
# docker.sock in it, the site jobs' shape (C1: no token in site-build, the switch and the superseded
# conjuncts, the actions by SHA, no cache, nothing signs in up to `tip`, the deploy and the live
# check behind the tip gate) and a second ci-ok all fail; the repository's own workflows pass.
# Every case runs, also after one has failed; the test fails at the end and names each failed case.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
failures=0
fail() { echo "✗ check_workflows_test: $*" >&2; failures=$((failures + 1)); }
# yq is on ubuntu-latest. Locally a missing yq skips this test with a notice; in CI it is a failure.
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  if [ -n "${CI:-}" ]; then fail "yq (mikefarah v4) is missing in CI: check_workflows.sh cannot run"; exit 1; fi
  echo "- check_workflows_test: SKIPPED, yq (mikefarah v4) is not installed locally (brew install yq). CI does not skip."
  # Without yq the script itself must refuse, not pass.
  rc=0; "$HERE/scripts/check_workflows.sh" >/dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || { fail "without yq the script must exit 2 (got $rc)"; exit 1; }
  exit 0
fi
d="$tmp/wf"
fresh() { rm -rf "$d"; mkdir -p "$d"; cp "$HERE"/.github/workflows/*.yml "$d/"; }
run() { rc=0; "$HERE/scripts/check_workflows.sh" "$d" >"$tmp/out" 2>"$tmp/err" || rc=$?; }
# expect_fail <name> <expected message part>, expect_pass <name>: the verdict of the last run.
expect_fail() {
  if [ "$rc" != 1 ]; then fail "$1 must fail (rc=$rc): $(cat "$tmp/err")"; return; fi
  grep -q -- "$2" "$tmp/err" || fail "$1: message lacks '$2': $(cat "$tmp/err")"
}
expect_pass() { [ "$rc" = 0 ] || fail "$1 must pass (rc=$rc): $(cat "$tmp/err")"; }
# bad <name> <expected message part> <yaml of x.yml>: the repository's workflows plus x.yml must fail with that message.
bad() { fresh; printf '%s\n' "$3" > "$d/x.yml"; run; expect_fail "$1" "$2"; }
good() { fresh; printf '%s\n' "$2" > "$d/x.yml"; run; expect_pass "$1"; }
JOB='    runs-on: x
    steps:
      - run: true'

fresh; run
expect_pass "the repository's own workflows"

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

# --- deploy.yml edits.
# deploy_edit <line> <replacement> [job]: deploy.yml in $d with that exact line replaced (awk -v: "\n" in
# the replacement is a line break), every occurrence, or only inside the job <job> (from "  <job>:" to
# the next key at that indentation). Returns 1 when the line is not there (the test is stale).
deploy_edit() {
  awk -v from="$1" -v to="$2" -v job="${3:+  $3:}" '
    /^  [A-Za-z0-9_-]+:$/ { in_job = (job == "" || $0 == job) }
    job == "" { in_job = 1 }
    in_job && $0 == from { $0 = to; hit = 1 }
    { print } END { exit !hit }' "$d/deploy.yml" > "$d/deploy.new" || { rm -f "$d/deploy.new"; return 1; }
  mv "$d/deploy.new" "$d/deploy.yml"
}
# deploy_swap <name> <expected message part> <line> <replacement>: every such line replaced must fail.
deploy_swap() {
  fresh
  deploy_edit "$3" "$4" || { fail "$1: the line '$3' is not in deploy.yml (the test is stale)"; return; }
  run; expect_fail "$1" "$2"
}
# job_swap <name> <expected message part, or "" to pass> <job> <line> <replacement>: the line replaced in that job only.
job_swap() {
  fresh
  deploy_edit "$4" "$5" "$3" || { fail "$1: the line '$4' is not in deploy.yml's job $3 (the test is stale)"; return; }
  run
  if [ -z "$2" ]; then expect_pass "$1"; else expect_fail "$1" "$2"; fi
}

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
# ci.yml runs no job in an environment any more (promote is gone, C1).
fresh; awk '{ print } $0 == "  ci-ok:" { print "    environment: deploy" }' "$d/ci.yml" > "$d/ci.new"; mv "$d/ci.new" "$d/ci.yml"; run
expect_fail "a ci.yml job in the environment deploy" "job 'ci-ok' names an environment"
# The site job: only in the environment site, by name (D233: its own WIF principal set), never deploy's.
job_swap "the site in another environment" "must run in the environment 'site'" site "    environment: site" "    environment: production"
job_swap "the site back in the environment deploy" "must run in the environment 'site'" site "    environment: site" "    environment: deploy"
job_swap "the site in an environment named by an expression" "must run in the environment" site "    environment: site" "    environment: \${{ vars.E }}"
job_swap "the site without its environment" "site job does not run in \`environment: site\`" site "    environment: site" "    # no environment"
job_swap "the site with an environment mapping and a url is still fine" "" site "    environment: site" '    environment: { name: site, url: "https://ryftlab.de" }'
job_swap "the site in a mapping naming deploy" "must run in the environment 'site'" site "    environment: site" '    environment: { name: deploy, url: "https://ryftlab.de" }'
job_swap "site-build in the environment" "site-build job runs in no environment" site-build "    timeout-minutes: 20" "    timeout-minutes: 20\n    environment: deploy"
job_swap "site-build in the environment site" "site-build job runs in no environment" site-build "    timeout-minutes: 20" "    timeout-minutes: 20\n    environment: site"
fresh; cp "$d/deploy.yml" "$d/deploy2.yml"; run
expect_fail "the environment in a copy of deploy.yml" "deploy2.yml: job 'deploy' names an environment"
grep -q "deploy2.yml: job 'site' names an environment" "$tmp/err" || fail "the site job in a copy of deploy.yml must fail too: $(cat "$tmp/err")"

# --- deploy.yml (Cloud Run): the gate, the triggers, the token, the serialization, the wait.
DEPLOY_IF="    if: vars.GCP_DEPLOY == 'on' && vars.DEPLOY_HOLD_SERVICES == '' && needs.ci.outputs.current == 'true'"
TIP_IF="        if: steps.tip.outputs.current == 'true'"
SMOKE_IF="        if: steps.tip.outputs.current == 'true' && (github.event_name != 'workflow_dispatch' || inputs.smoke)"
deploy_swap "a deploy.yml job without the gate" "must start with vars.GCP_DEPLOY" "    if: vars.GCP_DEPLOY == 'on'" "    if: always()"
deploy_swap "the gate behind an ||" "must start with vars.GCP_DEPLOY" \
  "$DEPLOY_IF" "    if: vars.GCP_DEPLOY == 'on' || vars.DEPLOY_HOLD_SERVICES == '' && needs.ci.outputs.current == 'true'"
# No status function in any if (job or step): each one lets a job or step run past a failure or the gate.
deploy_swap "always() behind the gate (the wait)" "status function always()" \
  "    if: vars.GCP_DEPLOY == 'on'" "    if: vars.GCP_DEPLOY == 'on' && always()"
deploy_swap "!cancelled() behind the gate (the hold)" "status function cancelled()" \
  "    if: vars.GCP_DEPLOY == 'on' && vars.DEPLOY_HOLD_SERVICES != ''" "    if: vars.GCP_DEPLOY == 'on' && vars.DEPLOY_HOLD_SERVICES != '' && !cancelled()"
deploy_swap "Always() in another case (the deploy)" "status function Always()" "$DEPLOY_IF" "$DEPLOY_IF && (Always())"
deploy_swap "always() on a step" "status function always()" "      - name: Sign out" "      - name: Sign out\n        if: always()"
deploy_swap "success() || true on a step" "status function success()" \
  "      - name: Render deploy/run with the digests" "      - name: Render deploy/run with the digests\n        if: success() || true"
deploy_swap "failure() in braces on a step" "status function failure()" "$TIP_IF" "        if: \${{ steps.tip.outputs.current == 'true' || failure() }}"
deploy_swap "continue-on-error on migrate" "continue-on-error" \
  "      - name: migrate (replace the job, execute it, wait)" "      - name: migrate (replace the job, execute it, wait)\n        continue-on-error: true"
# The tokens: the wait reads the Actions API only, the hold has none.
deploy_swap "the wait with a write token" "exactly actions: read" "      actions: read" "      actions: read\n      checks: write"
deploy_swap "the wait back on check runs" "exactly actions: read" "      actions: read" "      checks: read"
job_swap "the hold with a token" "has no token" held "    permissions: {}" "    permissions:\n      contents: write"
# Only main's tip deploys: the job, and every step that changes Cloud Run or smokes.
deploy_swap "a deploy that ignores the superseded wait" "needs.ci.outputs.current" "$DEPLOY_IF" "    if: vars.GCP_DEPLOY == 'on' && vars.DEPLOY_HOLD_SERVICES == ''"
job_swap "migrate and the services without the tip gate" "steps.tip.outputs.current == 'true'" deploy "$TIP_IF" "        if: github.sha != ''"
deploy_swap "the smoke's tip gate undone by a top-level ||" "no || outside parentheses" \
  "$SMOKE_IF" "        if: steps.tip.outputs.current == 'true' && github.event_name != 'workflow_dispatch' || inputs.smoke"
job_swap "no tip step" "deploy job has no step with id" deploy "        id: tip" "        id: tip2"
# Build and scan before any sign-in or push, with no third-party action and no docker.sock.
deploy_swap "setup-buildx-action back" "its only action" \
  "      - name: Build the five images (linux/amd64) and scan each saved image (Trivy, fail on critical)" \
  "      - uses: docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069\n      - name: Build the five images (linux/amd64) and scan each saved image (Trivy, fail on critical)"
deploy_swap "docker.sock in the job" "docker.sock" '          mkdir -p "$RUNNER_TEMP/images"' '          mkdir -p "$RUNNER_TEMP/images"\n          test -S /var/run/docker.sock'
deploy_swap "the OIDC request token before the scan" "before the scan" \
  '          rm -rf "$RUNNER_TEMP/dry-run"' '          rm -rf "$RUNNER_TEMP/dry-run"\n          echo "${#ACTIONS_ID_TOKEN_REQUEST_TOKEN}"'
deploy_swap "gcloud before the scan" "before the scan" '          rm -rf "$RUNNER_TEMP/dry-run"' '          rm -rf "$RUNNER_TEMP/dry-run"\n          gcloud auth list'
deploy_swap "a push in the scan step" "before the scan" '          rm -rf "$RUNNER_TEMP/images"' '          docker push "ryft-api:$GITHUB_SHA"\n          rm -rf "$RUNNER_TEMP/images"'
deploy_swap "Trivy on the daemon instead of the saved image" "no scan step" \
  '            docker run --rm -v "$RUNNER_TEMP/images:/in:ro" -v ryft-trivy-cache:/cache "$TRIVY" image --cache-dir /cache --input "/in/$s.tar" --exit-code 1 --severity CRITICAL --ignore-unfixed --no-progress' \
  '            docker run --rm "$TRIVY" image --exit-code 1 --severity CRITICAL --ignore-unfixed --no-progress "ryft-$s:$GITHUB_SHA"'
deploy_swap "the gate after another term" "must start with vars.GCP_DEPLOY" \
  "    if: vars.GCP_DEPLOY == 'on' && vars.DEPLOY_HOLD_SERVICES != ''" "    if: vars.DEPLOY_HOLD_SERVICES != '' && vars.GCP_DEPLOY == 'on'"
job_swap "a deploy that can be cancelled" "cancel-in-progress: false" deploy "      cancel-in-progress: false" "      cancel-in-progress: true"
deploy_swap "a concurrency group per commit" "concurrency group" "      group: deploy" "      group: deploy-\${{ github.sha }}"
deploy_swap "a pull_request trigger" "runs on exactly" "  workflow_dispatch:" "  pull_request:\n  workflow_dispatch:"
deploy_swap "a push on every branch" "runs on exactly" "    branches: [main]" "    branches: ['**']"
job_swap "a wider token" "id-token: write" deploy "      id-token: write" "      id-token: write\n      packages: write"
job_swap "the deploy in another environment" "must run in the environment" deploy "    environment: deploy" "    environment: production"
job_swap "the deploy in the site's environment" "must run in the environment 'deploy'" deploy "    environment: deploy" "    environment: site"
job_swap "a deploy that does not wait for ci-ok" "deploy job must need" deploy "    needs: [ci]" "    needs: []"
deploy_swap "workflow-level permissions" "permissions: {}" "permissions: {}" "permissions:\n  contents: write"
bad "another job with the id ci-ok" 'only ci.yml' "name: x
on: push
jobs:
  ci-ok:
$JOB"
bad "a job named ci-ok" 'only ci.yml' "name: x
on: push
jobs:
  fake:
    name: ci-ok
$JOB"

# --- the site (C1): site-off, site-build, site.
SITE_IF="    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on' && needs.ci.outputs.current == 'true'"
SITE_STEP_IF="        if: steps.tip.outputs.current == 'true' && steps.live.outputs.deploy == 'true'"
# The tokens: site-off none, site-build contents: read only, site contents: read and id-token: write.
job_swap "site-off with a token" "has no token" site-off "    permissions: {}" "    permissions:\n      contents: read"
job_swap "site-off with no permissions key (the workflow's none)" "" site-off "    permissions: {}" "    # no permissions key"
job_swap "site-build with id-token" "without id-token" site-build "      contents: read" "      contents: read\n      id-token: write"
job_swap "site-build with id-token only" "may not have id-token" site-build "      contents: read" "      id-token: write"
job_swap "the site with a wider token" "site job has exactly contents: read and id-token: write" site "      id-token: write" "      id-token: write\n      deployments: write"
# Both need ci, the superseded conjunct and the switch; the gate stays first and without ||.
job_swap "the site without ci" "site job must need" site "    needs: [ci, site-build]" "    needs: [site-build]"
job_swap "the site's needs in another order" "" site "    needs: [ci, site-build]" "    needs: [site-build, ci]"
job_swap "site-build without ci" "site-build job must need" site-build "    needs: [ci]" "    needs: []"
job_swap "the site ignoring the superseded wait" "top-level conjunct needs.ci.outputs.current" site "$SITE_IF" "    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on'"
job_swap "the site with the superseded wait negated" "top-level conjunct needs.ci.outputs.current" site "$SITE_IF" "    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on' && !(needs.ci.outputs.current == 'true')"
job_swap "site-build ignoring the superseded wait" "top-level conjunct needs.ci.outputs.current" site-build "$SITE_IF" "    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on'"
job_swap "the site's conjuncts in another order" "" site "$SITE_IF" "    if: vars.GCP_DEPLOY == 'on' && needs.ci.outputs.current == 'true' && vars.SITE_DEPLOY == 'on'"
job_swap "the site without its switch" "vars.SITE_DEPLOY == 'on'" site "$SITE_IF" "    if: vars.GCP_DEPLOY == 'on' && needs.ci.outputs.current == 'true'"
job_swap "the site's gate behind an ||" "must start with vars.GCP_DEPLOY" site "$SITE_IF" "    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on' && needs.ci.outputs.current == 'true' || github.event_name == 'workflow_dispatch'"
job_swap "site-off without the gate" "must start with vars.GCP_DEPLOY" site-off "    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY != 'on'" "    if: vars.SITE_DEPLOY != 'on'"
# One site deploy at a time, never cancelled.
job_swap "a site deploy that can be cancelled" "concurrency group \`site\`" site "      cancel-in-progress: false" "      cancel-in-progress: true"
job_swap "a site concurrency group per commit" "concurrency group \`site\`" site "      group: site" "      group: site-\${{ github.sha }}"
# The actions: by SHA, and only these.
job_swap "a third-party sign-in action in the site" "its only actions are actions/checkout, setup-node and download-artifact" site \
  "      - name: main is still at this commit" "      - uses: google-github-actions/auth@7c6bc770dae815cd3e89ee6cdf493a5fab2cc093\n      - name: main is still at this commit"
job_swap "actions/cache in site-build" "its only actions are actions/checkout, setup-node and upload-artifact" site-build \
  "      - run: npm ci --no-audit --no-fund" "      - uses: actions/cache@55cc8345863c7cc4c66a329aec7e433d2d1c52a9\n      - run: npm ci --no-audit --no-fund"
# Every action of deploy, site-build and site back on its tag must fail. The lines are read from
# deploy.yml as it is pinned today, never written here: Dependabot moves the SHAs (#198 broke main).
pinned_uses() { # <job>: that job's `- uses: <action>@<40 hex>` lines in the repository's deploy.yml
  awk -v job="  $1:" '/^  [A-Za-z0-9_-]+:$/ { in_job = ($0 == job) } in_job && /^      - uses: / { print }' \
    "$HERE/.github/workflows/deploy.yml" | grep -E '^      - uses: [^ @]+@[0-9a-f]{40}( |$)' || true
}
for job in deploy site-build site; do
  found=0
  while IFS= read -r line; do
    found=$((found + 1))
    action=${line#      - uses: }; action=${action%%@*}
    tag=${line##*# }; [ "$tag" != "$line" ] || tag=v1  # the version in the pin's comment, else any tag
    job_swap "$action by tag in $job" "by SHA" "$job" "$line" "      - uses: $action@$tag"
  done < <(pinned_uses "$job")
  [ "$found" -gt 0 ] || fail "deploy.yml's job $job pins no action by SHA (the test is stale)"
done
# setup-node without a cache in the site (site-build may have one: it has no token).
job_swap "setup-node with an npm cache in the site" "setup-node without a cache" site "          node-version-file: web/.nvmrc" "          node-version-file: web/.nvmrc\n          cache: npm"
job_swap "setup-node with a cache path in the site" "setup-node without a cache" site "          node-version-file: web/.nvmrc" "          node-version-file: web/.nvmrc\n          cache-dependency-path: web/package-lock.json"
job_swap "setup-node in the site without package-manager-cache: false" "package-manager-cache: false" site "          package-manager-cache: false" "          # the automatic cache left on"
job_swap "setup-node in the site with the automatic cache on" "setup-node without a cache" site "          package-manager-cache: false" "          package-manager-cache: true"
job_swap "setup-node with an npm cache in site-build" "" site-build "          node-version-file: web/.nvmrc" "          node-version-file: web/.nvmrc\n          cache: npm"
# Nothing signs in up to and including tip.
job_swap "gcloud in the site's tip step" "at or before the step \`tip\`" site "          tip=" "          gcloud auth list\n          tip="
job_swap "the OIDC request token before the site's tip" "at or before the step \`tip\`" site "          tip=" '          echo "${#ACTIONS_ID_TOKEN_REQUEST_URL}"\n          tip='
job_swap "firebase-tools before the site's tip" "at or before the step \`tip\`" site "          tip=" '          npx --yes firebase-tools@15.35.0 login:list\n          tip='
job_swap "no tip step in the site" "site job has no step with id" site "        id: tip" "        id: tip2"
# firebase deploy and check-hosting only behind the tip gate, and only in the site job.
job_swap "the site's deploy and live check without the tip gate" "it deploys the site or checks it live" site "$SITE_STEP_IF" "        if: steps.live.outputs.deploy == 'true'"
job_swap "the site's tip gate undone by a top-level ||" "no || outside parentheses" site \
  "$SITE_STEP_IF" "        if: steps.tip.outputs.current == 'true' && steps.live.outputs.deploy == 'true' || github.event_name == 'workflow_dispatch'"
job_swap "the site's tip gate with an || in parentheses" "" site \
  "$SITE_STEP_IF" "        if: steps.tip.outputs.current == 'true' && (steps.live.outputs.deploy == 'true' || github.event_name == 'workflow_dispatch')"
job_swap "check-hosting before the site's tip" "it deploys the site or checks it live" site "          tip=" "          node web/scripts/check-hosting.mjs --base https://ryftlab.de\n          tip="
job_swap "firebase deploy in site-build" "outside the site job" site-build \
  "      - run: npm run build" "      - run: npm run build\n      - run: npx --yes firebase-tools@15.35.0 deploy --only hosting --project ryftlab-site"
job_swap "firebase.json named in site-build is no deploy" "" site-build \
  "      - run: npm run build" "      - run: npm run build\n      - run: test -s firebase.json && echo deploying later"

# --- the App key: named nowhere (promote is gone, C1)
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
bad "the App key in a job named promote" 'RYFT_DEPLOY_APP_KEY' "name: x
on: push
jobs:
  promote:
    runs-on: x
    steps:
      - run: echo \${{ secrets.RYFT_DEPLOY_APP_KEY }}"
job_swap "the App key in deploy.yml's site job" "RYFT_DEPLOY_APP_KEY" site "      SITE_PROJECT: ryftlab-site" "      SITE_PROJECT: ryftlab-site\n      K: \${{ secrets.RYFT_DEPLOY_APP_KEY }}"

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
      - run: ./scripts/check_registry.sh
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
bad "an explicit !!merge key (the tag, not the text)" 'merge key' "name: x
on: push
jobs:
  a:
    !!merge '<<': {runs-on: x}
    steps:
      - run: true"
bad "a !!binary name" 'merge key' "name: !!binary eA==
on: push
jobs:
  a:
    runs-on: x
    steps:
      - run: true"
fresh; printf 'name: x\non: [push\njobs: {\n' > "$d/x.yml"; run
[ "$rc" = 1 ] || fail "a file that does not parse must fail (rc=$rc)"
fresh; : > "$d/x.yml"; run
[ "$rc" = 1 ] || fail "an empty workflow must fail (rc=$rc)"

# The linter release of ci.yml's lint job must be the Makefile's (make lint warns on another).
fresh; sed 's/version: v2\.15\.0/version: v9.9.9/' "$d/ci.yml" > "$d/ci.new" && mv "$d/ci.new" "$d/ci.yml"; run
expect_fail "a golangci-lint version that differs from the Makefile's" 'GOLANGCI_LINT_VERSION'

if [ "$failures" -gt 0 ]; then
  echo "✗ check_workflows_test: $failures case(s) failed" >&2
  exit 1
fi
echo "✓ check_workflows_test: parsed, not grepped: triggers, the environment only in deploy.yml's deploy and site, the old App key nowhere, deploy.yml's gate, status functions, continue-on-error, triggers, tokens, concurrency, the superseded and tip gates, scan before sign-in, no third-party action or docker.sock, the site built without a token and deployed and checked only from main's tip (switch, actions by SHA, no cache, nothing signs in up to tip), ci-ok only in ci.yml, no external reusable workflows, no secrets: inherit or dynamic secrets, no test hooks"
