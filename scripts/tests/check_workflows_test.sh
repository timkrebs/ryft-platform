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
# deploy.yml's rollout step and its record step are also EXECUTED (the `run:` extracted with yq, run under
# `bash -eo pipefail` with a fake gcloud and make): a failed lookup after a good replace, a failed or hung replace,
# a failing smoke, a failing or hung rollback and an unexpected error each roll back the touched services; a job
# with too little time left, or no start time, touches nothing; the restore commands are printed first. The record
# step's facts are proven from the serving api's IMAGE (a throwaway git tree with the commits L, P (D285) and N
# (D286), the registry's tags of the image's digest from a fake gcloud): D286 settings complete, none on a legacy
# image, or config drift; the D285 floor (a tree with and without a writer; a label that outlived its image), fail
# closed, and the selection by mode. The site jobs' `if`s are evaluated for a push and both kinds of dispatch.
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
PREFLIGHT_IF="        if: steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'yes' && !inputs.migrate_only"
BOOTSTRAP_IF="        if: steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'no' && github.event_name == 'workflow_dispatch' && !inputs.migrate_only"
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
deploy_swap "the preflight's tip gate undone by a top-level ||" "no || outside parentheses" \
  "$PREFLIGHT_IF" "        if: steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'yes' || !inputs.migrate_only"
deploy_swap "the bootstrap's tip gate undone by a top-level ||" "no || outside parentheses" \
  "$BOOTSTRAP_IF" "        if: steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'no' || github.event_name == 'workflow_dispatch'"
deploy_swap "smoke preflight removed" "needs one step each, in this order" \
  '          SMOKE_AUTH_FILE="$RUNNER_TEMP/ryft-smoke-auth.json" ./scripts/smoke_prod.sh --auth-preflight' '          true'
deploy_swap "smoke bootstrap removed" "needs one step each, in this order" \
  '          ./scripts/smoke_prod.sh --auth-bootstrap' '          true'
# D286 inputs: `migrate_only` (boolean, default false) is the one dispatch input of the smoke and the rollout; `auth_preflight`
# and `smoke` are gone (the smoke authentication follows the serving api revision; no input skips the smoke of a rollout).
deploy_swap "migrate_only defaults to true" "needs the input \`migrate_only\`" "        default: false" "        default: true"
deploy_swap "migrate_only renamed back to auth_preflight" "has an input \`auth_preflight\`" "      migrate_only:" "      auth_preflight:"
deploy_swap "migrate_only renamed" "needs the input \`migrate_only\`" "      migrate_only:" "      only_migrate:"
fresh
yq -i '."on".workflow_dispatch.inputs.migrate_only.type = "string"' "$d/deploy.yml"
run
expect_fail "migrate_only as a string" "needs the input \`migrate_only\`"
fresh
yq -i '."on".workflow_dispatch.inputs.smoke = {"type": "boolean", "default": true}' "$d/deploy.yml"
run
expect_fail "the input smoke next to migrate_only" "has an input \`smoke\`"
fresh
yq -i '."on".workflow_dispatch.inputs.auth_preflight = {"type": "boolean", "default": true}' "$d/deploy.yml"
run
expect_fail "the input auth_preflight next to migrate_only" "has an input \`auth_preflight\`"
# set_if <step name> <condition>: that step's `if` replaced in a fresh copy (parsed, so the condition is exactly what is given).
set_if() { fresh; step="$1" cond="$2" yq -i '(.jobs.deploy.steps[] | select(.name == strenv(step)).if) = strenv(cond)' "$d/deploy.yml"; run; }
PRE="Verify production smoke authentication"
BOOT="Bootstrap smoke authentication (the running api predates D286)"
MIG="migrate (replace the job, execute it, wait)"
POST="The services, then make smoke-prod, and roll back when either fails"
REC="Record the revisions serving traffic"
set_if "$PRE" "steps.tip.outputs.current == 'true'"
expect_fail "the preflight regardless of the serving api" "smoke auth preflight: its \`if\` must be exactly"
set_if "$PRE" "steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'yes'"
expect_fail "the preflight under migrate_only" "smoke auth preflight: its \`if\` must be exactly"
set_if "$PRE" "steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'no' && !inputs.migrate_only"
expect_fail "the preflight against an api without the D286 sign-in" "smoke auth preflight: its \`if\` must be exactly"
set_if "$PRE" "steps.tip.outputs.current == 'true' && inputs.auth_preflight"
expect_fail "the preflight on the old input" "smoke auth preflight: its \`if\` must be exactly"
set_if "$BOOT" "steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'no' && !inputs.migrate_only"
expect_fail "the bootstrap on a push" "smoke auth bootstrap: its \`if\` must be exactly"
set_if "$BOOT" "steps.tip.outputs.current == 'true' && github.event_name == 'workflow_dispatch' && !inputs.migrate_only"
expect_fail "the bootstrap whatever the serving api" "smoke auth bootstrap: its \`if\` must be exactly"
set_if "$BOOT" "steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'no' && github.event_name == 'workflow_dispatch'"
expect_fail "the bootstrap under migrate_only" "smoke auth bootstrap: its \`if\` must be exactly"
set_if "$BOOT" "steps.tip.outputs.current == 'true' && steps.record.outputs.d286 == 'yes' && github.event_name == 'workflow_dispatch' && !inputs.migrate_only"
expect_fail "the bootstrap against a D286 api" "smoke auth bootstrap: its \`if\` must be exactly"
set_if "$MIG" "steps.tip.outputs.current == 'true' && !inputs.migrate_only"
expect_fail "migrate skipped under migrate_only" "migrate step: its \`if\` must be exactly"
set_if "$REC" "steps.tip.outputs.current == 'true' && !inputs.migrate_only"
expect_fail "the record step skipped under migrate_only" "its \`if\` must be exactly \`steps.tip.outputs.current == 'true'\`"
set_if "$POST" "steps.tip.outputs.current == 'true'"
expect_fail "the rollout runs under migrate_only" "rollout step (the services and the smoke): its \`if\` must be exactly"
set_if "$POST" "steps.tip.outputs.current == 'true' && (github.event_name != 'workflow_dispatch' || inputs.migrate_only)"
expect_fail "another input gates the rollout" "rollout step (the services and the smoke): its \`if\` must be exactly"
set_if "$POST" "steps.tip.outputs.current == 'true' && github.event_name == 'push'"
expect_fail "the rollout only on a push" "rollout step (the services and the smoke): its \`if\` must be exactly"
# The rollout step mints afresh (the preflight's session file is not its), and rolls back inside its own step, in one EXIT trap.
deploy_swap "the rollout reuses the session file" "mints a fresh token, not the preflight's session" \
  '          unset SMOKE_AUTH_FILE' '          SMOKE_AUTH_FILE="$RUNNER_TEMP/ryft-smoke-auth.json"'
deploy_swap "the rollout leaves the session variable alone" "mints a fresh token, not the preflight's session" \
  '          unset SMOKE_AUTH_FILE' '          true'
deploy_swap "no rollback command in the rollout step" "the rollout step rolls back itself through one EXIT trap" \
  '                 timeout -k 10 "$RESTORE_S" gcloud run services update-traffic "$s" --to-revisions="$prev=100" --region="$REGION" --project="$PROJECT" --quiet; then' '                 true; then'
deploy_swap "the rollback in another order" "the rollout step rolls back itself through one EXIT trap" \
  '            for s in api ingest notify scoring; do' '            for s in scoring notify ingest api; do'
deploy_swap "the rollback without the recorded revisions" "the rollout step rolls back itself through one EXIT trap" \
  '              prev=$(sed -n "s/^$s //p" "$RUNNER_TEMP/previous" | head -n 1)' '              prev=x'
deploy_swap "no EXIT trap" "the rollout step rolls back itself through one EXIT trap" '          trap on_exit EXIT' '          true'
deploy_swap "the handler does not keep the exit status" "the rollout step rolls back itself through one EXIT trap" '            exit "$rc"' '            exit 0'
deploy_swap "the handler rolls back only some services" "the rollout step rolls back itself through one EXIT trap" \
  '              rollback "${touched[@]}"' '              rollback api ingest'
# Each service is on the list before its replace is issued, and the trap is set before the loop.
deploy_swap "a service never on the touched list" "comes before \`gcloud run services replace\`" '            touched+=("$s")' '            true'
fresh
deploy_edit '            touched+=("$s")' '            true' && deploy_edit '            reason="the lookup of $s'"'"'s new revision failed"' '            reason="x"\n            touched+=("$s")' ||
  fail "the touched-after-replace fixture lines are not in deploy.yml (the test is stale)"
run
expect_fail "a service on the touched list only after its replace" "comes before \`gcloud run services replace\`"
fresh
deploy_edit '          trap on_exit EXIT' '          true' && deploy_edit '            reason="the replace of $s failed"' '            reason="the replace of $s failed"\n            trap on_exit EXIT' ||
  fail "the trap-after-touched fixture lines are not in deploy.yml (the test is stale)"
run
expect_fail "the trap set only after the first service is on the list" "comes before \`gcloud run services replace\`"
# deploy_sub <name> <message part> <from> <to>: the first occurrence of <from> in deploy.yml replaced (fixed strings, any part of a line).
deploy_sub() {
  fresh
  awk -v from="$3" -v to="$4" '{ if (!hit && (i = index($0, from))) { $0 = substr($0, 1, i - 1) to substr($0, i + length(from)); hit = 1 } print } END { exit !hit }' \
    "$d/deploy.yml" > "$d/deploy.new" || { rm -f "$d/deploy.new"; fail "$1: '$3' is not in deploy.yml (the test is stale)"; return; }
  mv "$d/deploy.new" "$d/deploy.yml"
  run; expect_fail "$1" "$2"
}
# deploy_after <name> <message part> <anchor> <line> <replacement>: the first line equal to <line> AFTER the first line that contains <anchor>, replaced.
deploy_after() {
  fresh
  awk -v anchor="$3" -v from="$4" -v to="$5" '
    !seen && index($0, anchor) { seen = 1; print; next }
    seen && !hit && $0 == from { $0 = to; hit = 1 }
    { print } END { exit !hit }' "$d/deploy.yml" > "$d/deploy.new" || { rm -f "$d/deploy.new"; fail "$1: the line '$4' after '$3' is not in deploy.yml (the test is stale)"; return; }
  mv "$d/deploy.new" "$d/deploy.yml"
  run; expect_fail "$1" "$2"
}
deploy_sub "the live traffic compared with the recorded revision again" "no \`cur=\` comparison" '            summary "- $s: $rev"' '            cur=x'
# A failed lookup of the new revision after a replace fails the step into the EXIT handler (no `|| rev=` fallback), with its reason set.
deploy_sub "a best-effort fallback after the lookup of the new revision" "no \`|| rev=\` fallback" "latestReadyRevisionName)')" "latestReadyRevisionName)') || rev=\"unknown\""
deploy_sub "the lookup of the new revision without its reason" "no \`|| rev=\` fallback" "reason=\"the lookup of \$s's new revision failed\"" 'reason="x"'
deploy_sub "an early success in the rollout" "no \`exit 0\`" 'bounded "$SMOKE_S" make smoke-prod' 'exit 0'
deploy_swap "the smoke in an if" "plain command line after the services" '          bounded "$SMOKE_S" make smoke-prod' '          if bounded "$SMOKE_S" make smoke-prod; then true; fi'
deploy_swap "the smoke followed by || true" "plain command line after the services" '          bounded "$SMOKE_S" make smoke-prod' '          bounded "$SMOKE_S" make smoke-prod || true'
deploy_swap "the smoke command removed (only its name in a message is left)" "plain command line after the services" '          bounded "$SMOKE_S" make smoke-prod' '          true'
deploy_swap "the smoke without its bound" "plain command line after the services" '          bounded "$SMOKE_S" make smoke-prod' '          make smoke-prod'
# Time: the constants, the job's start, the budget, the bounded helper, every gcloud under a bound, the targets printed first.
deploy_swap "JOB_S that is not the job's timeout" "must equal the job's timeout-minutes" '          JOB_S=3600' '          JOB_S=3000'
job_swap "a job timeout that JOB_S does not follow" "must equal the job's timeout-minutes" deploy '    timeout-minutes: 60' '    timeout-minutes: 45'
deploy_swap "a time constant that is gone" "assigns JOB_S, RESTORE_S" '          SMOKE_S=900' '          true'
deploy_swap "no run-unique tag pushed" "a tag no later run moves" \
  '            docker --config "$RUNNER_TEMP/docker" push --quiet "$run_ref"' '            true'
deploy_sub "a run tag that a rebuild moves" "a tag no later run moves" 'run_ref=$REGISTRY/$s:$tag-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT' 'run_ref=$REGISTRY/$s:$tag-latest'
deploy_swap "the job's start not written" "writes the job's start" '          date +%s > "$RUNNER_TEMP/deploy-started"' '          true'
deploy_sub "the job's start not checked" "reads \$RUNNER_TEMP/deploy-started" '[[ $started =~ ^[0-9]+$ ]] ||' 'true ||'
deploy_sub "no time kept for the restores" "reads \$RUNNER_TEMP/deploy-started" 'reserve=$(( 4 * RESTORE_S + 180 ))' 'reserve=0'
deploy_sub "a forward end that ignores the reserve" "reads \$RUNNER_TEMP/deploy-started" 'forward_end=$(( started + JOB_S - reserve ))' 'forward_end=$(( started + JOB_S ))'
deploy_sub "bounded without a timeout" "is defined before the loop" 'timeout -k 10 "$(( cap < left ? cap : left ))" "$@"' '"$@"'
deploy_sub "bounded that ignores the time left" "is defined before the loop" 'cap < left ? cap : left' 'cap'
deploy_sub "no budget check before the loop" "under 300 s of forward time left" '-lt 300 ]' '-lt 0 ]'
deploy_sub "a budget error that does not stop the step" "under 300 s of forward time left" "not enough of the job's 60 minutes is left to roll out and still restore: nothing was rolled" 'x'
deploy_swap "an unbounded replace" "every gcloud of the rollout runs under" \
  '            bounded "$REPLACE_S" gcloud run services replace "$RUNNER_TEMP/run/$s.yaml" --region="$REGION" --project="$PROJECT" --quiet' \
  '            gcloud run services replace "$RUNNER_TEMP/run/$s.yaml" --region="$REGION" --project="$PROJECT" --quiet'
deploy_sub "an unbounded lookup of the new revision" "every gcloud of the rollout runs under" 'rev=$(bounded "$DESCRIBE_S" gcloud' 'rev=$(gcloud'
deploy_swap "another unbounded gcloud in the rollout" "every gcloud of the rollout runs under" \
  '          rm -f "$RUNNER_TEMP/ryft-smoke-auth.json"' '          rm -f "$RUNNER_TEMP/ryft-smoke-auth.json"\n          echo x; gcloud run services list'
deploy_sub "a rollback without its own timeout" "the rollback's \`gcloud run services update-traffic\` runs as" 'timeout -k 10 "$RESTORE_S" gcloud run services update-traffic' 'gcloud run services update-traffic'
deploy_sub "a rollback under a fixed number instead of RESTORE_S" "the rollback's \`gcloud run services update-traffic\` runs as" 'timeout -k 10 "$RESTORE_S" gcloud run services update-traffic' 'timeout -k 10 600 gcloud run services update-traffic'
deploy_swap "the targets not printed" "prints the targets" '          cmds=$(hand_restore)' '          cmds=x'
deploy_swap "the targets not in the summary" "prints the targets" '          summary "$cmds"' '          true'
deploy_sub "the targets printed with another command" "prints the targets" 'update-traffic %s --to-revisions=%s=100 --region=%s --project=%s' 'update-traffic %s --to-revisions=%s=50 --region=%s --project=%s'
fresh
deploy_edit '          cmds=$(hand_restore)' '          cmds=x' && deploy_edit '            summary "- $s: $rev"' '            summary "- $s: $rev"\n            cmds=$(hand_restore)' ||
  fail "the targets-after-the-replace fixture lines are not in deploy.yml (the test is stale)"
run
expect_fail "the targets printed only after the first replace" "prints the targets"
deploy_swap "the services in a step of their own" "live in one step" \
  "      - name: The services, then make smoke-prod, and roll back when either fails" \
  "      - name: Early replace\n        if: steps.tip.outputs.current == 'true'\n        run: gcloud run services replace x.yaml\n      - name: The services, then make smoke-prod, and roll back when either fails"
# The record step reads the serving api revision (python3 -I, not grep) and writes d286 and d285 to its outputs, before migrate.
deploy_swap "the record step without the label fact" "after recording the revisions it reads the serving api revision" \
  '          label = (rev.get("metadata", {}).get("labels") or {}).get("ryft-meal-patch") == "d285"' '          label = True'
# D286's settings: the four names, each a literal non-empty value (a valueFrom is not), from the api container.
deploy_sub "a D286 setting dropped from the four" "the D286 settings are the four" ', "SMOKE_USER_ID"]' ']'
deploy_sub "the D286 settings without the valueFrom rule" "the D286 settings are the four" 'elif "valueFrom" in es[0]:' 'elif False:'
deploy_sub "the D286 settings without the non-empty rule" "the D286 settings are the four" 'es[0]["value"].strip()' 'es[0]["value"]'
# The image: the container's, else status.imageDigest, by digest in the job's registry.
deploy_sub "the image not read from the api container" "the serving api's image is its container's image" 'api = containers[0] if containers and isinstance(containers[0], dict) else {}' 'api = {}'
deploy_sub "no status.imageDigest fallback" "the serving api's image is its container's image" 'rev.get("status", {}).get("imageDigest")' 'rev.get("status", {}).get("x")'
deploy_sub "the image pattern not tied to the registry" "the serving api's image is its container's image" "' \"\$REGISTRY\")" "' \"x\")"
# The image's commits: the tags of the digest, the commits of this history, the two directions of ancestry.
deploy_sub "the tags lookup without a timeout" "the commits of the serving api's image" 'timeout -k 10 60 gcloud artifacts docker tags list' 'gcloud artifacts docker tags list'
deploy_sub "tags of any shape" "the commits of the serving api's image" '[0-9a-f]{7,40}' '[0-9a-f]+'
deploy_sub "a tag not resolved to a commit" "the commits of the serving api's image" 'git rev-parse --verify -q "$t^{commit}"' 'git rev-parse "$t"'
deploy_sub "a lookup that fails without a warning" "the commits of the serving api's image" '::warning title=deploy::the tags of' '::notice title=deploy::the tags of'
deploy_sub "an image commit outside this history counts" "an ancestor of HEAD" 'git merge-base --is-ancestor "$c" HEAD || continue' 'true'
deploy_sub "image_d285 from the wrong direction" "image_legacy one that D285_COMMIT contains" \
  'if git merge-base --is-ancestor "$D285_COMMIT" "$c"; then image_d285=yes; fi' 'if git merge-base --is-ancestor "$c" "$D285_COMMIT"; then image_d285=yes; fi'
deploy_sub "image_legacy from the wrong direction" "image_legacy one that D285_COMMIT contains" \
  'if git merge-base --is-ancestor "$c" "$D285_COMMIT"; then image_legacy=yes; fi' 'if git merge-base --is-ancestor "$D285_COMMIT" "$c"; then image_legacy=yes; fi'
# d286 from the settings and the image; drift ends the run in every mode; d285 needs the image, not the label alone.
deploy_sub "d286 yes without complete settings" "d286 is yes with all four settings complete" 'if [ "$smoke" = complete ]; then' 'if [ "$smoke" != none ]; then'
deploy_sub "the bootstrap without a legacy image" "d286 is yes with all four settings complete" \
  'elif [ "$smoke" = none ] && [ "$image_legacy" = yes ]; then' 'elif [ "$smoke" = none ]; then'
deploy_sub "drift that is no error" "config drift, never a bootstrap" 'config drift, never a bootstrap' 'config x'
deploy_after "drift that does not stop the run" "config drift, never a bootstrap" 'config drift, never a bootstrap' '            exit 1' '            true'
deploy_sub "drift without the way out" "config drift, never a bootstrap" 'Restore SMOKE_OIDC_* and SMOKE_USER_ID (deploy/run, config.env)' 'x'
deploy_sub "d285 on the label alone" "the label alone can outlive the image" \
  'if [ "$label" = yes ] && [ "$image_d285" = yes ]; then d285=yes; else d285=no; fi' 'if [ "$label" = yes ]; then d285=yes; else d285=no; fi'
deploy_sub "the floor's error without the proof" "name the proof" 'the proof is the label plus an image built from a commit containing' 'the proof is x'
# Every gcloud of the record step is bounded too.
deploy_sub "an unbounded describe of the services" "every gcloud runs under \`timeout -k 10 60\`" 'json=$(timeout -k 10 60 gcloud run services describe' 'json=$(gcloud run services describe'
deploy_sub "an unbounded describe of the api revision" "every gcloud runs under \`timeout -k 10 60\`" 'rjson=$(timeout -k 10 60 gcloud run revisions describe' 'rjson=$(gcloud run revisions describe'
job_swap "a checkout of the last commit only" "needs \`fetch-depth: 0\`" deploy '          fetch-depth: 0' '          fetch-depth: 1'
deploy_swap "no D285_COMMIT in the record step" "its env must be exactly EVENT" '          D285_COMMIT: 4bca49de1b140dafc3609484a514617a7c6fbe02' '          X: y'
deploy_swap "another D285_COMMIT in the record step" "its env must be exactly EVENT" \
  '          D285_COMMIT: 4bca49de1b140dafc3609484a514617a7c6fbe02' '          D285_COMMIT: 3ceca58000000000000000000000000000000000'
deploy_swap "the record step's guard by grep" "after recording the revisions it reads the serving api revision" \
  "          facts=\$(printf '%s' \"\$rjson\" | python3 -I -c '" "          facts=\$(printf '%s' \"\$rjson\" | grep -c SMOKE_OIDC_SA_ID; echo '"
deploy_swap "the record step without the d286 output" "after recording the revisions it reads the serving api revision" '          echo "d286=$d286" >> "$GITHUB_OUTPUT"' '          true'
deploy_swap "the record step without the d285 output" "after recording the revisions it reads the serving api revision" '          echo "d285=$d285" >> "$GITHUB_OUTPUT"' '          true'
deploy_sub "a failed revision describe that is not an error" "after recording the revisions it reads the serving api revision" 'cannot describe the serving api revision' 'x'
deploy_swap "the record step's revisions not read from the service" "records the serving revisions" \
  '            json=$(timeout -k 10 60 gcloud run services describe "$s" --region="$REGION" --project="$PROJECT" --format=json) ||' '            json=$(true) ||'
deploy_swap "the record step without its id" "needs one step each, in this order" "        id: record" "        id: recorded"
deploy_swap "the record step without MIGRATE_ONLY" "its env must be exactly EVENT" \
  "          MIGRATE_ONLY: \${{ inputs.migrate_only && 'true' || 'false' }}" '          MIGRATE_ONLY: "false"'
deploy_swap "the record step with another env" "its env must be exactly EVENT" \
  '          EVENT: ${{ github.event_name }}' '          EVENT: ${{ github.event_name }}\n          X: y'
# The D285 floor: the exact search, fail closed, an error that names the way out.
deploy_sub "the floor without the tests' exclusion" "the D285 floor" " ':!*_test.go'" ''
deploy_sub "the floor without migration 0053's exclusion" "the D285 floor" " ':!backend/internal/db/migrations/0053_*'" ''
deploy_sub "the floor searching all of the repository" "the D285 floor" ' -- backend ' ' -- . '
deploy_sub "the floor failing open" "the D285 floor" 'elif [ "$rc" != 1 ]; then' 'elif false; then'
deploy_sub "the floor only when the label is there" "the D285 floor" 'if [ "$d285" = no ]; then' 'if [ "$d285" = yes ]; then'
# The selection by mode: migrate_only goes on, a push with d286 no stops before migrate.
deploy_sub "the bootstrap pending is no error" "with migrate_only it goes on" 'echo "::error title=deploy::the D286 bootstrap is pending' 'echo "::error title=deploy::x'
deploy_sub "a push with d286 no goes on" "with migrate_only it goes on" '[ "$EVENT" != workflow_dispatch ]' '[ "$EVENT" = never ]'
deploy_sub "migrate_only unread" "with migrate_only it goes on" '[ "$MIGRATE_ONLY" = true ]' '[ "$MIGRATE_ONLY" = never ]'
# The bootstrap step only runs the helper: the serving api revision is read in the record step.
deploy_swap "the bootstrap reading the revision itself" "the bootstrap step only runs" \
  '          ./scripts/smoke_prod.sh --auth-bootstrap' '          gcloud run revisions describe api-00001-abc\n          ./scripts/smoke_prod.sh --auth-bootstrap'
deploy_swap "a rollback outside the rollout step" "belongs in the rollout step only" \
  "      - name: Sign out" "      - name: Roll back early\n        if: steps.tip.outputs.current == 'true'\n        run: gcloud run services update-traffic api --to-revisions=api-00001-abc=100\n      - name: Sign out"
deploy_swap "a rollout step before the recording and the smoke auth steps" "needs one step each, in this order" \
  "      - name: Record the revisions serving traffic" "      - name: Early migrate\n        if: steps.tip.outputs.current == 'true'\n        run: gcloud run jobs execute migrate\n      - name: Record the revisions serving traffic"
deploy_swap "only some services are recorded" "reads status.traffic" \
  '          for s in scoring notify ingest api; do' '          for s in scoring notify ingest; do'
# D286: the smoke authenticates with a Google ID token of ryft-smoke, minted with the job's sign-in; neither smoke
# step takes a secret, an env of the retired names or the anon key.
SMOKE_NAME="      - name: Verify production smoke authentication"
deploy_swap "a secret in the preflight's env" "\`secrets.SMOKE_USER_ID\`: the smoke authenticates" \
  "$SMOKE_NAME" "$SMOKE_NAME\n        env:\n          SMOKE_USER_ID: \${{ secrets.SMOKE_USER_ID }}"
deploy_swap "any secret in the preflight's env" "\`secrets.OTHER\`: the smoke authenticates" \
  "$SMOKE_NAME" "$SMOKE_NAME\n        env:\n          X: \${{ secrets.OTHER }}"
deploy_swap "the anon key in the preflight" "\`SUPABASE_ANON_KEY\`: the smoke authenticates" \
  "$SMOKE_NAME" "$SMOKE_NAME\n        env:\n          SUPABASE_ANON_KEY: \${{ secrets.SUPABASE_ANON_KEY }}"
deploy_swap "the retired password name in the preflight, without a secret" "\`SMOKE_PASSWORD\`: the smoke authenticates" \
  "$SMOKE_NAME" "$SMOKE_NAME\n        env:\n          SMOKE_PASSWORD: x"
deploy_swap "the retired token name in the preflight's command" "\`SMOKE_TOKEN\`: the smoke authenticates" \
  '          SMOKE_AUTH_FILE="$RUNNER_TEMP/ryft-smoke-auth.json" ./scripts/smoke_prod.sh --auth-preflight' \
  '          SMOKE_TOKEN=x SMOKE_AUTH_FILE="$RUNNER_TEMP/ryft-smoke-auth.json" ./scripts/smoke_prod.sh --auth-preflight'
# Nor behind the steps' backs: the workflow's or a job's env, or a run script that writes a SMOKE_ name to
# $GITHUB_ENV, would hand the helper a SMOKE_TOKEN to use instead of minting its own.
deploy_swap "SMOKE_TOKEN in the workflow's env" "the workflow's \`env:\`: \`SMOKE_TOKEN\`" \
  "permissions: {}" "permissions: {}\n\nenv:\n  SMOKE_TOKEN: x"
deploy_swap "a SMOKE secret in the workflow's env under another name" "the workflow's \`env:\`: \`T\`" \
  "permissions: {}" "permissions: {}\n\nenv:\n  T: \${{ secrets.SMOKE_TOKEN }}"
deploy_swap "SMOKE_EMAIL in the deploy job's env" "job 'deploy' \`env:\`: \`SMOKE_EMAIL\`" \
  "      PROJECT: ryftlab-prod" "      PROJECT: ryftlab-prod\n      SMOKE_EMAIL: x"
deploy_swap "SMOKE_PASSWORD in a lower-case spelling in the deploy job's env" "job 'deploy' \`env:\`: \`smoke_password\`" \
  "      PROJECT: ryftlab-prod" "      PROJECT: ryftlab-prod\n      smoke_password: x"
deploy_swap "SUPABASE_ANON_KEY in the deploy job's env" "job 'deploy' \`env:\`: \`SUPABASE_ANON_KEY\`" \
  "      PROJECT: ryftlab-prod" "      PROJECT: ryftlab-prod\n      SUPABASE_ANON_KEY: x"
deploy_swap "a SMOKE secret in the deploy job's env under another name" "job 'deploy' \`env:\`: \`X\`" \
  "      PROJECT: ryftlab-prod" "      PROJECT: ryftlab-prod\n      X: \${{ secrets['SMOKE_PASSWORD'] }}"
deploy_swap "SMOKE_TOKEN written to GITHUB_ENV" "names SMOKE_\\* and \\\$GITHUB_ENV" \
  "      - name: Sign out" "      - name: Smuggle\n        run: echo \"SMOKE_TOKEN=x\" >> \"\$GITHUB_ENV\"\n      - name: Sign out"
deploy_swap "SMOKE_TOKEN written to GITHUB_ENV through a variable" "names SMOKE_\\* and \\\$GITHUB_ENV" \
  "      - name: Sign out" "      - name: Smuggle\n        run: |\n          f=\${GITHUB_ENV}\n          printf 'SMOKE_TOKEN=%s\\\\n' x >> \"\$f\"\n      - name: Sign out"
deploy_swap "a secret in the post-rollout smoke" "it takes no secret" \
  "      - name: The services, then make smoke-prod, and roll back when either fails" "      - name: The services, then make smoke-prod, and roll back when either fails\n        env:\n          X: \${{ secrets.SMOKE_EMAIL }}"
deploy_swap "a secret in the bootstrap" "it takes no secret" \
  "      - name: Bootstrap smoke authentication (the running api predates D286)" "      - name: Bootstrap smoke authentication (the running api predates D286)\n        env:\n          X: \${{ secrets.SMOKE_EMAIL }}"
deploy_swap "a secret in the record step" "it takes no secret" \
  '          EVENT: ${{ github.event_name }}' '          EVENT: ${{ github.event_name }}\n          X: ${{ secrets.SMOKE_EMAIL }}'
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

# --- deploy.yml's steps EXECUTED (the greps above pin the shape; these run the scripts). The `run:` of the rollout step and of
# the record step is extracted with yq and run under `bash -eo pipefail` as Actions runs it, with a fake gcloud and make on the
# PATH that log their calls (FAIL_REPLACE=<svc>, FAIL_TRAFFIC=<svc>, FAIL_DESCRIBE=<svc> (the lookup of that service's new revision),
# FAIL_REVDESC=1, FAIL_TAGS=1, FAIL_SMOKE=1 pick what fails; SLOW_REPLACE=<svc> and SLOW_TRAFFIC=<svc> hang for 30 s). The time constants of the
# rollout step are rewritten in the extracted copy (SETC=(REPLACE_S=2 ...)): a hung call is cut by its own bound.
x="$tmp/exec"; rm -rf "$x"; mkdir -p "$x/bin" "$x/nogit"
DEPLOY="$HERE/.github/workflows/deploy.yml"
step_run() { yq -r ".jobs.deploy.steps[] | select($1) | .run" "$DEPLOY"; }
step_run ".name == \"$POST\"" > "$x/rollout.sh"
step_run '.id == "record"' > "$x/record.sh"
[ -s "$x/rollout.sh" ] || fail "the rollout step's script cannot be extracted (the test is stale)"
[ -s "$x/record.sh" ] || fail "the record step's script cannot be extracted (the test is stale)"
cat > "$x/bin/gcloud" <<'EOF'
#!/usr/bin/env bash
echo "gcloud $*" >> "$CALLS"
case "$1 $2 $3" in
  "run services replace")
    svc=${4##*/}; svc=${svc%.yaml}
    [ "$svc" != "${SLOW_REPLACE:-}" ] || exec sleep 30
    echo "replaced $svc"
    [ "$svc" != "${FAIL_REPLACE:-}" ] || exit 1 ;;
  "run services update-traffic")
    [ "$4" != "${SLOW_TRAFFIC:-}" ] || exec sleep 30
    [ "$4" != "${FAIL_TRAFFIC:-}" ] || exit 1 ;;
  "run services describe")
    case "$*" in
      *--format=json*) printf '{"status":{"traffic":[{"percent":100,"revisionName":"%s-00044-5lh"}]}}\n' "$4" ;;
      *) [ "$4" != "${FAIL_DESCRIBE:-}" ] || exit 1; echo "$4-00099-new" ;;
    esac ;;
  "run revisions describe") [ -z "${FAIL_REVDESC:-}" ] || exit 1; cat "$REV_JSON" ;;
  "artifacts docker tags") [ -z "${FAIL_TAGS:-}" ] || exit 1; cat "$TAGS_JSON" ;;
esac
EOF
printf '%s\n' '#!/usr/bin/env bash' 'echo "make $*" >> "$CALLS"' '[ -z "${FAIL_SMOKE:-}" ] || exit 1' > "$x/bin/make"
chmod +x "$x/bin/gcloud" "$x/bin/make"
ex_ok() { [ "$rc" = 0 ] || fail "$1 must exit 0 (rc=$rc): $(cat "$x/out")"; }
ex_fail() { [ "$rc" != 0 ] || fail "$1 must exit non-zero: $(cat "$x/out")"; }
ex_out() { grep -qF -- "$2" "$x/out" || fail "$1: the output lacks '$2': $(cat "$x/out")"; }
ex_no_out() { ! grep -qF -- "$2" "$x/out" || fail "$1: the output has '$2': $(cat "$x/out")"; }
eq() { [ "$2" = "$3" ] || fail "$1: got '$2', want '$3'"; }
replaces() { sed -n 's#^gcloud run services replace .*/\([a-z]*\)\.yaml .*#\1#p' "$x/calls" | tr '\n' ' '; }
rolled() { sed -n 's/^gcloud run services update-traffic \([a-z]*\) .*/\1/p' "$x/calls" | tr '\n' ' '; }
smoked() { grep -c '^make smoke-prod$' "$x/calls" || true; }
rolled_to() { grep -qF -- "gcloud run services update-traffic $1 --to-revisions=$2=100 " "$x/calls" || fail "$3: $1 was not sent back to $2: $(cat "$x/calls")"; }

# The rollout step: the previous revisions as the record step writes them.
rollout_setup() {
  rm -rf "$x/rt"; mkdir -p "$x/rt"; : > "$x/calls"; : > "$x/summary"
  printf '%s\n' 'scoring scoring-00001-aaa' 'notify notify-00002-bbb' 'ingest ingest-00003-ccc' 'api api-00044-5lh' > "$x/rt/previous"
  date +%s > "$x/rt/deploy-started"
}
SETC=() # NAME=seconds: the time constants rewritten in the extracted copy of the step (a case sets and clears it)
rollout_go() { # [VAR=value]...
  rc=0
  cp "$x/rollout.sh" "$x/rollout_use.sh"
  local kv
  for kv in ${SETC[@]+"${SETC[@]}"}; do
    sed "s/^${kv%%=*}=.*/$kv/" "$x/rollout_use.sh" > "$x/rollout_use.new" && mv "$x/rollout_use.new" "$x/rollout_use.sh"
    grep -qx -- "$kv" "$x/rollout_use.sh" || fail "the constant ${kv%%=*} is not assigned in the rollout step (the test is stale)"
  done
  env PATH="$x/bin:$PATH" CALLS="$x/calls" RUNNER_TEMP="$x/rt" GITHUB_STEP_SUMMARY="$x/summary" REGION=europe-west1 PROJECT=ryftlab-prod "$@" \
    bash --noprofile --norc -eo pipefail "$x/rollout_use.sh" > "$x/out" 2>&1 || rc=$?
}
run_rollout() { rollout_setup; rollout_go "$@"; }
ALL4="api ingest notify scoring "
# (a) everything succeeds: the four replaced consumers first, the smoke once, no rollback.
run_rollout
ex_ok "(a) a good rollout"
eq "(a) the order of the replaces" "$(replaces)" "scoring notify ingest api "
eq "(a) no rollback" "$(rolled)" ""
eq "(a) the smoke ran once" "$(smoked)" 1
grep -qF -- '- api: api-00099-new' "$x/summary" || fail "(a) the summary lacks the new api revision: $(cat "$x/summary")"
# (b) a good replace followed by a failed lookup of that service's new revision: the step fails into the EXIT handler. scoring and notify
# (the failed one included: its traffic has moved) go back to their recorded revisions, ingest and api are never replaced, no smoke.
run_rollout FAIL_DESCRIBE=notify
ex_fail "(b) a failed revision lookup after a replace"
eq "(b) the replaces (later ones never start)" "$(replaces)" "scoring notify "
eq "(b) rolled back, in order" "$(rolled)" "notify scoring "
eq "(b) no smoke" "$(smoked)" 0
ex_out "(b) the reason" "the lookup of notify's new revision failed"
rolled_to notify notify-00002-bbb "(b)"; rolled_to scoring scoring-00001-aaa "(b)"
! grep -qE 'replace .*/(ingest|api)\.yaml' "$x/calls" || fail "(b) ingest or api was replaced: $(cat "$x/calls")"
# A lookup that fails for another service than the one asked (the fake only fails notify's) changes nothing.
run_rollout FAIL_DESCRIBE=nobody
ex_ok "(b) a lookup failing for nobody"
eq "(b) the replaces without a failing lookup" "$(replaces)" "scoring notify ingest api "
# A failing smoke without a failing lookup rolls back all four too.
run_rollout FAIL_SMOKE=1
ex_fail "a failing smoke"
eq "a failing smoke rolls back all four" "$(rolled)" "$ALL4"
# (c) the replace of ingest fails: scoring, notify and ingest are rolled back (the failed one too: its traffic may have moved), api untouched.
run_rollout FAIL_REPLACE=ingest
ex_fail "(c) the replace of ingest fails"
eq "(c) the replaces issued" "$(replaces)" "scoring notify ingest "
eq "(c) rolled back, in order" "$(rolled)" "ingest notify scoring "
eq "(c) no smoke" "$(smoked)" 0
ex_out "(c) the reason" "the replace of ingest failed"
! grep -q 'api' "$x/calls" || fail "(c) api was touched: $(cat "$x/calls")"
rolled_to ingest ingest-00003-ccc "(c)"; rolled_to notify notify-00002-bbb "(c)"; rolled_to scoring scoring-00001-aaa "(c)"
# The first replace failing: only scoring (it is on the list before its replace is issued).
run_rollout FAIL_REPLACE=scoring
ex_fail "the replace of scoring fails"
eq "scoring alone is rolled back" "$(rolled)" "scoring "
eq "nothing else was replaced" "$(replaces)" "scoring "
# (d) one rollback fails: the others are still attempted, the step fails and names the one.
run_rollout FAIL_SMOKE=1 FAIL_TRAFFIC=notify
ex_fail "(d) a rollback fails"
eq "(d) every rollback attempted" "$(rolled)" "$ALL4"
ex_out "(d) the failed rollback is named" "the rollback of notify to 'notify-00002-bbb' failed"
ex_out "(d) the summary of the failures" "not rolled back: notify"
# Anything unexpected after the first replace rolls back too (here: the step cannot clear the preflight's session file).
rollout_setup; mkdir -p "$x/rt/ryft-smoke-auth.json/keep"; rollout_go
ex_fail "an unexpected error after the replaces"
eq "an unexpected error rolls back all four" "$(rolled)" "$ALL4"
eq "an unexpected error stops before the smoke" "$(smoked)" 0
ex_out "an unexpected error is named" "unexpected error"
# A summary that cannot be written does not keep the rollback from running.
run_rollout FAIL_SMOKE=1 GITHUB_STEP_SUMMARY="$x"
ex_fail "an unwritable summary and a failing smoke"
eq "an unwritable summary does not stop the rollback" "$(rolled)" "$ALL4"
# (e) ingest's replace hangs: it is cut at REPLACE_S, and scoring, notify and ingest (its traffic may have moved) are restored; api never starts.
SETC=(REPLACE_S=2)
run_rollout SLOW_REPLACE=ingest
SETC=()
ex_fail "(e) a replace that hangs"
eq "(e) the replaces issued (api never starts)" "$(replaces)" "scoring notify ingest "
eq "(e) rolled back, in order" "$(rolled)" "ingest notify scoring "
eq "(e) no smoke" "$(smoked)" 0
ex_out "(e) the reason" "the replace of ingest failed"
rolled_to ingest ingest-00003-ccc "(e)"; rolled_to notify notify-00002-bbb "(e)"; rolled_to scoring scoring-00001-aaa "(e)"
# (f) a restore that hangs is cut at RESTORE_S; the others are still attempted, the step fails and names it.
SETC=(RESTORE_S=2)
run_rollout FAIL_SMOKE=1 SLOW_TRAFFIC=notify
SETC=()
ex_fail "(f) a restore that hangs"
eq "(f) every restore attempted" "$(rolled)" "$ALL4"
ex_out "(f) the hanging restore is named" "the rollback of notify to 'notify-00002-bbb' failed"
ex_out "(f) the summary of the failures" "not rolled back: notify"
# (g) the job's time is nearly spent before the rollout starts: nothing is replaced, nothing restored (nothing was touched), the step fails.
rollout_setup; echo $(( $(date +%s) - 3500 )) > "$x/rt/deploy-started"; rollout_go
ex_fail "(g) too little of the job's time left"
eq "(g) no replace" "$(replaces)" ""
eq "(g) no rollback" "$(rolled)" ""
eq "(g) no smoke" "$(smoked)" 0
ex_out "(g) the error" "not enough of the job's 60 minutes is left to roll out and still restore: nothing was rolled"
rollout_setup; echo $(( $(date +%s) - 3000 )) > "$x/rt/deploy-started"; rollout_go
ex_fail "(g) 540 s of reserve and under 300 s of forward time left"
eq "(g) no replace at the edge" "$(replaces)" ""
rollout_setup; echo $(( $(date +%s) - 1000 )) > "$x/rt/deploy-started"; rollout_go
ex_ok "(g) plenty of the job's time left"
eq "(g) the replaces" "$(replaces)" "scoring notify ingest api "
# (h) the restore commands, printed before the first replace, in the log and in the summary.
run_rollout
ex_ok "(h) a good rollout"
first_hand=$({ grep -n 'restore by hand' "$x/out" || true; } | head -n 1 | cut -d: -f1)
first_replace=$({ grep -n '^replaced ' "$x/out" || true; } | head -n 1 | cut -d: -f1)
{ [ -n "$first_hand" ] && [ -n "$first_replace" ] && [ "$first_hand" -lt "$first_replace" ]; } \
  || fail "(h) the restore commands are not printed before the first replace (lines: hand '$first_hand', replace '$first_replace'): $(cat "$x/out")"
for s in api:api-00044-5lh ingest:ingest-00003-ccc notify:notify-00002-bbb scoring:scoring-00001-aaa; do
  cmd="gcloud run services update-traffic ${s%%:*} --to-revisions=${s#*:}=100 --region=europe-west1 --project=ryftlab-prod"
  ex_out "(h) the log has the restore command of ${s%%:*}" "$cmd"
  grep -qxF -- "$cmd" "$x/summary" || fail "(h) the summary lacks '$cmd': $(cat "$x/summary")"
done
grep -qF 'restore by hand (OPERATIONS.md' "$x/summary" || fail "(h) the summary lacks the sentence: $(cat "$x/summary")"
# They are also printed when nothing is replaced (a budget that is spent), so a killed job always leaves them.
rollout_setup; echo $(( $(date +%s) - 3500 )) > "$x/rt/deploy-started"; rollout_go
ex_out "(h) the restore commands before the budget error" "gcloud run services update-traffic scoring --to-revisions=scoring-00001-aaa=100"
# (i) deploy-started missing, or not a time: nothing is touched, the step fails.
rollout_setup; rm -f "$x/rt/deploy-started"; rollout_go
ex_fail "(i) deploy-started missing"
eq "(i) no replace" "$(replaces)" ""
eq "(i) no rollback" "$(rolled)" ""
ex_out "(i) the error" "deploy-started is missing or not a time"
rollout_setup; echo 'soon' > "$x/rt/deploy-started"; rollout_go
ex_fail "(i) deploy-started that is no time"
eq "(i) no replace after garbage" "$(replaces)" ""

# The record step: the serving api revision's facts, the D285 floor and the selection by mode, in a throwaway git tree.
# The api revision is proven from its IMAGE: the revision JSON names an image by digest, the registry's tags of that digest name the
# commits that built it (the fake gcloud answers `artifacts docker tags list` from $TAGS_JSON), and git says where those commits sit.
printf '%s\n' '[]' > "$x/rev_bad.json"
hex64() { printf '%64s' "$1" | tr ' ' 0; }
DL="sha256:$(hex64 a1)"; DP="sha256:$(hex64 a2)"; DN="sha256:$(hex64 a3)"   # the images of L, P and N
env_item() { printf '{"name":"%s","value":"%s"}' "$1" "$2"; }
S_EMAIL=$(env_item SMOKE_OIDC_SA_EMAIL smoke@example.invalid); S_ID=$(env_item SMOKE_OIDC_SA_ID 1)
S_AUD=$(env_item SMOKE_OIDC_AUDIENCE aud); S_USER=$(env_item SMOKE_USER_ID u)
ENV_FULL="[$S_EMAIL,$S_ID,$S_AUD,$S_USER]"
ENV_NONE='[{"name":"AUTH_MODE","value":"supabase"}]'
ENV_EMPTY_ID="[$S_EMAIL,$(env_item SMOKE_OIDC_SA_ID ''),$S_AUD,$S_USER]"
ENV_SECRET="[$S_EMAIL,$S_ID,$S_AUD,{\"name\":\"SMOKE_USER_ID\",\"valueFrom\":{\"secretKeyRef\":{\"name\":\"smoke-user\",\"key\":\"1\"}}}]"
ENV_THREE="[$S_EMAIL,$S_ID,$S_AUD]"
mkrev() { # <file> <label: yes|no|other> <env json> <image: a digest, "tag" or "none"> [status.imageDigest]
  python3 -I -c '
import json, sys
f, label, env, image = sys.argv[1:5]
status = sys.argv[5] if len(sys.argv) > 5 else ""
repo = "europe-west1-docker.pkg.dev/ryftlab-prod/ryft/api"
labels = {"serving.knative.dev/service": "api"}
if label == "yes":
    labels["ryft-meal-patch"] = "d285"
elif label == "other":
    labels["ryft-meal-patch"] = "d284"
c = {"name": "api", "env": json.loads(env)}
if image == "tag":
    c["image"] = repo + ":abc1234"
elif image != "none":
    c["image"] = repo + "@" + image
json.dump({"metadata": {"labels": labels}, "spec": {"containers": [c]}, "status": {"imageDigest": repo + "@" + status} if status else {}}, open(f, "w"))
' "$@"
}
mktags() { # <file> <digest> <tag>...: those tags on that digest, and N's tag as a decoy on another image
  python3 -I -c '
import json, sys
f, digest, decoy, tags = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4:]
base = "projects/ryftlab-prod/locations/europe-west1/repositories/ryft/packages/api"
out = [{"name": base + "/tags/" + t, "tag": base + "/tags/" + t, "version": base + "/versions/" + digest} for t in tags]
out.append({"name": base + "/tags/" + decoy, "tag": base + "/tags/" + decoy, "version": base + "/versions/sha256:" + "f" * 64})
json.dump(out, open(f, "w"))
' "$1" "$2" "${N_SHA:0:7}" "${@:3}"
}
api_is() { # <label> <env json> <image digest|tag|none> [tag]...: the serving api revision ($x/rev.json) and its tags ($x/tags.json)
  mkrev "$x/rev.json" "$1" "$2" "$3"
  mktags "$x/tags.json" "$3" "${@:4}"
}
treedir="$x/tree"
gitc() { env GIT_AUTHOR_DATE=2026-01-01T00:00:00Z GIT_COMMITTER_DATE=2026-01-01T00:00:00Z git -C "$x/tree" -c core.hooksPath=/dev/null -c commit.gpgsign=false -c user.name=t -c user.email=t@example.invalid "$@"; }
L_SHA=; P_SHA=; N_SHA=; S_SHA=
tree() { # [path]...: a fresh git repository of three commits, L (the legacy one), P (L's child: D285's) and N (P's child: a D286 commit, HEAD),
  # and S, a root commit of another history; then an innocent file and each path naming the meal portion label column (tracked, not committed)
  rm -rf "$x/tree"; mkdir -p "$x/tree/backend"
  git -C "$x/tree" init -q
  echo 'package main' > "$x/tree/backend/main.go"
  local l
  gitc add -A && gitc commit -q -m L; l=$(gitc rev-parse HEAD)
  echo d285 > "$x/tree/backend/d285.txt"; gitc add -A && gitc commit -q -m P; P_SHA=$(gitc rev-parse HEAD)
  echo d286 > "$x/tree/backend/d286.txt"; gitc add -A && gitc commit -q -m N; N_SHA=$(gitc rev-parse HEAD)
  S_SHA=$(gitc commit-tree "$(gitc mktree < /dev/null)" -m side)
  [ -z "$L_SHA" ] || [ "$L_SHA" = "$l" ] || fail "the fixture commits are not reproducible: L was $L_SHA, is $l"
  L_SHA=$l
  local p
  for p in "$@"; do mkdir -p "$x/tree/$(dirname "$p")"; echo 'INSERT INTO meal_items (portion_label_snapshot) VALUES ($1)' > "$x/tree/$p"; done
  gitc add -A
}
run_record() { # <revision json> [VAR=value]...: the record step in $treedir, a push by default
  local rev=$1; shift
  rm -rf "$x/rt"; mkdir -p "$x/rt"; : > "$x/calls"; : > "$x/summary"; : > "$x/gout"
  rc=0
  ( cd "$treedir" && env PATH="$x/bin:$PATH" CALLS="$x/calls" RUNNER_TEMP="$x/rt" GITHUB_STEP_SUMMARY="$x/summary" GITHUB_OUTPUT="$x/gout" \
      REGION=europe-west1 PROJECT=ryftlab-prod REGISTRY=europe-west1-docker.pkg.dev/ryftlab-prod/ryft D285_COMMIT="$P_SHA" TAGS_JSON="$x/tags.json" \
      EVENT=push MIGRATE_ONLY=false REV_JSON="$rev" GIT_CEILING_DIRECTORIES="$tmp" "$@" \
      bash --noprofile --norc -eo pipefail "$x/record.sh" ) > "$x/out" 2>&1 || rc=$?
}
outputs() { tr '\n' ' ' < "$x/gout"; }
NOWRITER=(backend/internal/store/meals_test.go backend/internal/db/migrations/0053_portion_labels.sql backend/internal/db/migrations/0054_validate_portion_labels.sql)
BOOTSTRAP_ERR="the D286 bootstrap is pending: dispatch with migrate_only, bind the smoke account (scripts/sql/bind_smoke_account.sql), then dispatch"
D285_ERR="D285 requires a D285 api serving first"
DRIFT_ERR="config drift, never a bootstrap"
PROOF_ERR="the proof is the label plus an image built from a commit containing"
record_modes() { # <name> <expectation: ok|fail> <outputs after, or "" when none>: the same revision on a push, a dispatch and a migrate_only dispatch
  local mode ev mo
  for mode in "push false" "workflow_dispatch false" "workflow_dispatch true"; do
    read -r ev mo <<<"$mode"
    run_record "$x/rev.json" EVENT="$ev" MIGRATE_ONLY="$mo"
    if [ "$2" = ok ]; then ex_ok "$1 ($ev, migrate_only $mo)"; else ex_fail "$1 ($ev, migrate_only $mo)"; fi
    eq "$1: the outputs ($ev, migrate_only $mo)" "$(outputs)" "$3"
  done
}
# --- D286: complete settings, or none at all on an image that predates it, or config drift (an error in every mode, nothing changed).
tree "${NOWRITER[@]}" backend/internal/store/meals.go
P_SHA7=${P_SHA:0:7}; N_SHA7=${N_SHA:0:7}; L_SHA7=${L_SHA:0:7}
# A labelled api whose image is N's, with the four settings complete: it goes on in every mode, whatever the tree writes.
api_is yes "$ENV_FULL" "$DN" "$N_SHA7"
run_record "$x/rev.json"
ex_ok "a labelled D286 api (image N) on a push"
eq "the facts of a labelled D286 api" "$(outputs)" "d286=yes d285=yes "
grep -qF "image $DN (commit(s) $N_SHA7)" "$x/summary" || fail "the summary lacks the image and its commit: $(cat "$x/summary")"
# A rebuild of the serving commit moved <sha7> to another digest: the run-unique <sha7>-<run>-<attempt> still proves the image.
api_is yes "$ENV_FULL" "$DN" "$N_SHA7-4581418616-1"
run_record "$x/rev.json"
ex_ok "a labelled D286 api known by its run-unique tag only"
eq "the run-unique tag proves the image" "$(outputs)" "d286=yes d285=yes "
api_is yes "$ENV_FULL" "$DN" "$N_SHA7-x-1"
run_record "$x/rev.json"
eq "a malformed run tag proves nothing" "$(outputs)" "d286=yes d285=no "
api_is yes "$ENV_FULL" "$DN" "$N_SHA7"
run_record "$x/rev.json" EVENT=workflow_dispatch MIGRATE_ONLY=true
ex_ok "a labelled D286 api under migrate_only, whatever the tree writes"
ex_out "migrate_only is announced" "migrate_only: only the migrations are applied"
eq "no write: the record step is read only" "$(grep -cE 'replace|update-traffic|run jobs|run deploy' "$x/calls" || true)" 0
# Config drift: an image that is not legacy and settings that are missing, empty, partial or secret-backed. Never a bootstrap.
for drift in "none|$ENV_NONE|SMOKE_OIDC_SA_EMAIL missing, SMOKE_OIDC_SA_ID missing, SMOKE_OIDC_AUDIENCE missing, SMOKE_USER_ID missing" \
             "empty|$ENV_EMPTY_ID|SMOKE_OIDC_SA_ID empty" "secret|$ENV_SECRET|SMOKE_USER_ID secret-backed" "three|$ENV_THREE|SMOKE_USER_ID missing"; do
  IFS='|' read -r name env_json detail <<<"$drift"
  api_is no "$env_json" "$DN" "$N_SHA7"
  record_modes "a D286 image with settings ($name)" fail ""
  ex_out "the drift error ($name)" "$DRIFT_ERR"
  ex_out "the drift error names the image and its commit ($name)" "(image $DN, commit(s) $N_SHA7)"
  ex_out "the drift error names what is wrong ($name)" "($detail)"
  ex_out "the drift error says nothing was changed ($name)" "Nothing was changed."
  ex_no_out "no bootstrap pending for drift ($name)" "bootstrap is pending"
done
# A legacy image (tag to L, or to P: the D285 commit predates D286 too) with none of the settings is the bootstrap: reachable on a dispatch.
tree "${NOWRITER[@]}"
for image in "$DL|$L_SHA7|no|d286=no d285=no " "$DP|$P_SHA7|yes|d286=no d285=yes "; do
  IFS='|' read -r img tag label want <<<"$image"
  api_is "$label" "$ENV_NONE" "$img" "$tag"
  run_record "$x/rev.json"
  ex_fail "a push with an api that predates D286 (image $tag)"
  ex_out "the push-run refusal ($tag)" "$BOOTSTRAP_ERR"
  eq "the facts of an old api ($tag)" "$(outputs)" "$want"
  run_record "$x/rev.json" EVENT=workflow_dispatch
  ex_ok "the bootstrap dispatch against an api that predates D286 (image $tag, no writer in the tree)"
  eq "the facts for the bootstrap ($tag)" "$(outputs)" "$want"
  ex_no_out "the bootstrap dispatch is no migrate_only" "migrate_only"
  ex_no_out "no drift for a legacy image ($tag)" "$DRIFT_ERR"
  run_record "$x/rev.json" EVENT=workflow_dispatch MIGRATE_ONLY=true
  ex_ok "migrate_only against an api that predates D286 (image $tag, no writer in the tree)"
  ex_out "migrate_only is announced" "migrate_only: only the migrations are applied"
  ex_no_out "migrate_only is no refusal" "bootstrap is pending"
done
# Even a legacy image with settings that are half there is drift (an emptied SMOKE_OIDC_SA_ID is no bootstrap, no D286 either).
api_is no "$ENV_EMPTY_ID" "$DL" "$L_SHA7"
record_modes "a legacy image with an emptied SMOKE_OIDC_SA_ID" fail ""
ex_out "the emptied SMOKE_OIDC_SA_ID is drift" "$DRIFT_ERR"
# D286 settings complete on a push, no label: d285 is no (no writer in the tree: it goes on).
api_is no "$ENV_FULL" "$DN" "$N_SHA7"
run_record "$x/rev.json"
ex_ok "a push with the D286 sign-in and no label (no writer in the tree)"
eq "the facts of a D286 api without the label" "$(outputs)" "d286=yes d285=no "
# A label of another value counts as none.
api_is other "$ENV_NONE" "$DL" "$L_SHA7"
run_record "$x/rev.json" EVENT=workflow_dispatch
ex_ok "a label of another value counts as none, no writer in the tree"
eq "the facts of a wrong label" "$(outputs)" "d286=no d285=no "
# --- no proof from the image: nothing the registry or git says counts (no commit, not this history, no digest, no tags).
# A tag that is no commit of this history (a root commit of another one, a hex string that is no object, the decoy of another image).
S_SHA7=${S_SHA:0:7}
for tags in "$S_SHA7" "abcdef1" "$S_SHA7 abcdef1" ""; do
  # shellcheck disable=SC2086  # the tags are words
  api_is yes "$ENV_NONE" "$DL" $tags
  record_modes "an image whose tags are no commit of this history (${tags:-no tag})" fail ""
  ex_out "no proof from tags outside this history (${tags:-no tag})" "commit(s) none proven"
done
api_is yes "$ENV_FULL" "$DL" "$S_SHA7"
run_record "$x/rev.json" EVENT=workflow_dispatch
ex_ok "complete settings need no image proof"
eq "the label alone is no D285 proof (tag outside this history)" "$(outputs)" "d286=yes d285=no "
# An image by tag, not by digest: no digest, no proof, and the label alone is no D285.
mkrev "$x/rev.json" yes "$ENV_NONE" tag; mktags "$x/tags.json" tag "$L_SHA7"
record_modes "an image by tag with no settings" fail ""
ex_out "an image by tag is named" "image none, commit(s) none proven"
ex_out "the missing digest is a warning" "::warning title=deploy::"
mkrev "$x/rev.json" yes "$ENV_FULL" tag; mktags "$x/tags.json" tag "$L_SHA7"
run_record "$x/rev.json"
ex_ok "an image by tag with complete settings"
eq "the label alone is no D285 proof (image by tag)" "$(outputs)" "d286=yes d285=no "
# status.imageDigest stands in for a container image that is no digest; an image of another registry is none.
mkrev "$x/rev.json" yes "$ENV_FULL" tag "$DN"; mktags "$x/tags.json" "$DN" "$N_SHA7"
run_record "$x/rev.json"
ex_ok "the digest from status.imageDigest"
eq "status.imageDigest proves the image" "$(outputs)" "d286=yes d285=yes "
api_is yes "$ENV_FULL" "$DN" "$N_SHA7"
sed 's#europe-west1-docker.pkg.dev/ryftlab-prod/ryft/api@#europe-west1-docker.pkg.dev/other/ryft/api@#' "$x/rev.json" > "$x/rev.other"
run_record "$x/rev.other"
ex_ok "an image of another registry"
eq "an image of another registry proves nothing" "$(outputs)" "d286=yes d285=no "
# D285: the label AND an image of a commit that contains D285_COMMIT. The label alone can outlive the image.
api_is yes "$ENV_NONE" "$DL" "$L_SHA7"
run_record "$x/rev.json" EVENT=workflow_dispatch
ex_ok "the label with an image that predates D285 (no writer in the tree)"
eq "the label outlived the image: d285 is no" "$(outputs)" "d286=no d285=no "
api_is yes "$ENV_FULL" "$DN" "$N_SHA7"
run_record "$x/rev.json" FAIL_TAGS=1
ex_ok "a tag lookup that fails (no writer in the tree)"
eq "the label with no tag lookup is no D285 proof" "$(outputs)" "d286=yes d285=no "
ex_out "a failed tag lookup is a warning" "::warning title=deploy::the tags of europe-west1-docker.pkg.dev/ryftlab-prod/ryft/api cannot be listed"
api_is yes "$ENV_FULL" "$DN" "$N_SHA7"; printf '%s\n' '{"not":"a list"}' > "$x/tags.json"
run_record "$x/rev.json"
ex_ok "a tag list that is no list (no writer in the tree)"
eq "an unreadable tag list is no proof" "$(outputs)" "d286=yes d285=no "
ex_out "an unreadable tag list is a warning" "::warning title=deploy::the tag list of"
api_is no "$ENV_NONE" "$DL" "$L_SHA7"
run_record "$x/rev.json" EVENT=workflow_dispatch FAIL_TAGS=1
ex_fail "a legacy api whose tags cannot be listed is not proven legacy"
ex_out "that is drift" "$DRIFT_ERR"
# The D285 floor: no proof of D285 and a writer in backend/ stops every mode before migrate, naming the way out and the proof.
for w in backend/internal/store/meals.go backend/internal/db/migrations/0055_backfill_portion_labels.sql; do
  tree "${NOWRITER[@]}" "$w"
  api_is no "$ENV_NONE" "$DL" "$L_SHA7"
  for mode in "push false" "workflow_dispatch false" "workflow_dispatch true"; do
    read -r ev mo <<<"$mode"
    run_record "$x/rev.json" EVENT="$ev" MIGRATE_ONLY="$mo"
    ex_fail "the floor: $w, $ev, migrate_only $mo"
    ex_out "the floor names the way out ($w, $ev, migrate_only $mo)" "$D285_ERR"
    ex_out "the floor names the proof ($w, $ev, migrate_only $mo)" "$PROOF_ERR $P_SHA"
    ex_out "the floor names the image's commits ($w, $ev, migrate_only $mo)" "commit(s) $L_SHA7;"
    ex_out "the floor names the writer ($w)" "$w"
  done
  # The label outlived the image: the writer deploy is refused.
  api_is yes "$ENV_NONE" "$DL" "$L_SHA7"
  run_record "$x/rev.json" EVENT=workflow_dispatch
  ex_fail "the floor: $w against a label that outlived its image"
  ex_out "the floor ($w, label and a legacy image)" "$D285_ERR"
  # A label and a tag lookup that fails: no proof either (the image is N's, settings complete).
  api_is yes "$ENV_FULL" "$DN" "$N_SHA7"
  run_record "$x/rev.json" FAIL_TAGS=1
  ex_fail "the floor: $w against a label and a tag lookup that fails"
  ex_out "the floor ($w, no tag lookup)" "commit(s) none proven"
  api_is no "$ENV_FULL" "$DN" "$N_SHA7"
  run_record "$x/rev.json"
  ex_fail "the floor: $w against an api with the D286 sign-in but no label"
  ex_out "the floor ($w, d286 yes)" "$D285_ERR"
  api_is other "$ENV_NONE" "$DL" "$L_SHA7"
  run_record "$x/rev.json" EVENT=workflow_dispatch
  ex_fail "the floor: $w against a label of another value"
  # The label and an image of D285 (P) or newer (N): the proof, the writer is fine.
  api_is yes "$ENV_NONE" "$DP" "$P_SHA7"
  run_record "$x/rev.json" EVENT=workflow_dispatch
  ex_ok "the labelled D285 api (image P) passes the floor: $w"
  eq "the facts of a labelled api (image P)" "$(outputs)" "d286=no d285=yes "
  api_is yes "$ENV_FULL" "$DN" "$N_SHA7"
  run_record "$x/rev.json"
  ex_ok "the labelled D286 api (image N) passes the floor: $w"
  eq "the facts of a labelled api (image N)" "$(outputs)" "d286=yes d285=yes "
done
# Fail closed: a tree git cannot search (a .git that points nowhere: git grep exits 128) counts as a writer, while D285 is not proven. Git cannot
# resolve a commit there either, so no image is proven: complete settings are what lets the run reach the floor.
echo 'gitdir: /nonexistent/ryft-test' > "$x/nogit/.git"
treedir="$x/nogit"
api_is no "$ENV_FULL" "$DN" "$N_SHA7"
run_record "$x/rev.json" EVENT=workflow_dispatch
ex_fail "the floor in a tree git cannot search"
ex_out "the floor fails closed" "counts as a writer"
api_is yes "$ENV_FULL" "$DN" "$N_SHA7"
run_record "$x/rev.json" EVENT=workflow_dispatch
ex_fail "a label alone in a tree git cannot search (no image is proven)"
ex_out "the label alone fails closed" "counts as a writer"
treedir="$x/tree"
# A revision that cannot be described or read stops the run with nothing migrated and no output.
tree "${NOWRITER[@]}"
api_is no "$ENV_NONE" "$DL" "$L_SHA7"
run_record "$x/rev.json" EVENT=workflow_dispatch FAIL_REVDESC=1
ex_fail "a revision that cannot be described"
ex_out "the describe failure" "cannot describe the serving api revision api-00044-5lh"
eq "no output after a failed describe" "$(outputs)" ""
run_record "$x/rev_bad.json" EVENT=workflow_dispatch
ex_fail "a revision that is not an object"
ex_out "the unreadable revision" "cannot be read"
eq "no output after an unreadable revision" "$(outputs)" ""
# The selection of the smoke authentication and the rest by mode: each step's `if`, evaluated for each row (a push with an api that
# predates D286 never gets here: the record step has stopped it).
if_of() { yq -r ".jobs.deploy.steps[] | select(.name == \"$1\") | .if" "$DEPLOY"; }
runs_if() { # <expression> <event> <d286> <migrate_only as Python: True, False or None>
  python3 -I -c '
import re, sys
expr, event, d286, mo = sys.argv[1:5]
e = expr.replace("steps.tip.outputs.current", repr("true")).replace("steps.record.outputs.d286", repr(d286))
e = e.replace("github.event_name", repr(event)).replace("inputs.migrate_only", mo)
# a job `if`: the switches on, the wait found main at this commit
e = e.replace("vars.GCP_DEPLOY", repr("on")).replace("vars.SITE_DEPLOY", repr("on")).replace("needs.ci.outputs.current", repr("true"))
e = re.sub(r"!(?!=)", " not ", e.replace("&&", " and ").replace("||", " or "))
print("run" if eval(e, {"__builtins__": {}}) else "skip")
' "$@"
}
mode_row() { # <event> <d286> <migrate_only> <what runs: preflight bootstrap migrate rollout>
  local got="" n
  for n in "$PRE" "$BOOT" "$MIG" "$POST"; do got+="$(runs_if "$(if_of "$n")" "$1" "$2" "$3") "; done
  eq "the steps that run on $1, d286 $2, migrate_only $3 (preflight bootstrap migrate rollout)" "$got" "$4 "
}
mode_row push yes None "run skip run run"
mode_row workflow_dispatch yes False "run skip run run"
mode_row workflow_dispatch yes True "skip skip run skip"
mode_row workflow_dispatch no False "skip run run run"
mode_row workflow_dispatch no True "skip skip run skip"
# The site jobs, evaluated the same way (GCP_DEPLOY and SITE_DEPLOY on, the wait found main current): a migrate_only dispatch
# publishes nothing; a push (the input is null there) and a plain dispatch build and deploy the site.
job_if_of() { yq -r ".jobs.\"$1\".if" "$DEPLOY"; }
site_row() { # <event> <migrate_only as Python> <site-build site>
  eq "the site jobs on $1, migrate_only $2 (site-build site)" \
    "$(runs_if "$(job_if_of site-build)" "$1" yes "$2") $(runs_if "$(job_if_of site)" "$1" yes "$2")" "$3"
}
site_row push None "run run"
site_row workflow_dispatch False "run run"
site_row workflow_dispatch True "skip skip"

# --- the site (C1): site-off, site-build, site.
SITE_IF="    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on' && needs.ci.outputs.current == 'true' && !inputs.migrate_only"
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
job_swap "the site's conjuncts in another order" "" site "$SITE_IF" "    if: vars.GCP_DEPLOY == 'on' && !inputs.migrate_only && needs.ci.outputs.current == 'true' && vars.SITE_DEPLOY == 'on'"
# A migrate_only dispatch applies the migrations only: neither site job runs (a conjunct of each, evaluated below).
job_swap "the site without !inputs.migrate_only" "top-level conjunct !inputs.migrate_only" site "$SITE_IF" \
  "    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on' && needs.ci.outputs.current == 'true'"
job_swap "site-build without !inputs.migrate_only" "top-level conjunct !inputs.migrate_only" site-build "$SITE_IF" \
  "    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on' && needs.ci.outputs.current == 'true'"
job_swap "the site with migrate_only inside an ||" "top-level conjunct !inputs.migrate_only" site "$SITE_IF" \
  "    if: vars.GCP_DEPLOY == 'on' && vars.SITE_DEPLOY == 'on' && needs.ci.outputs.current == 'true' && (inputs.migrate_only || !inputs.migrate_only)"
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
pinned_uses() { # <job>: that job's step lines `uses: <action>@<40 hex>` (first key or not) in the repository's deploy.yml
  awk -v job="  $1:" '/^  [A-Za-z0-9_-]+:$/ { in_job = ($0 == job) } in_job { print }' "$HERE/.github/workflows/deploy.yml" |
    grep -E '^      (- |  )uses: [^ @]+@[0-9a-f]{40}( |$)' || true
}
for job in deploy site-build site; do
  found=0
  # fd 3, not stdin: nothing in the loop body can read the remaining lines away.
  while IFS= read -r -u 3 line; do
    found=$((found + 1))
    prefix=${line%%uses: *}
    action=${line#*uses: }; action=${action%%@*}
    tag=${line##*# }; [ "$tag" != "$line" ] || tag=v1  # the version in the pin's comment, else any tag
    # Both of check_workflows.sh's messages quote the step's `uses: …`, so the failure is this step's.
    job_swap "$action by tag in $job" "uses: $action@$tag\`" "$job" "$line" "${prefix}uses: $action@$tag"
  done 3< <(pinned_uses "$job")
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

# --- the test hooks of the scripts (one left since D3, D246; the loop takes the next one)
# shellcheck disable=SC2043
for hook in RYFT_HOOKS_NODE_DIRS; do
  bad "$hook in a step env" "$hook" "name: x
on: push
jobs:
  a:
    runs-on: x
    steps:
      - run: ./scripts/check_bytes.sh
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
          $hook=1 ./scripts/check_bytes.sh"
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
      - run: NOW=1 ./scripts/golden_guard.sh"
good "a variable that only contains a hook's letters" "name: x
on: push
jobs:
  a:
    runs-on: x
    env:
      MY_RYFT_HOOKS_NODE_DIRS_URL: x
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

# The linter release of ci.yml's lint job must be the Makefile's (make lint warns on another). The version is
# read from the Makefile, never written here: a linter bump moves both (D235).
lint_v=$(sed -n 's/^GOLANGCI_LINT_VERSION := //p' "$HERE/Makefile")
fresh
if [ -n "$lint_v" ] && awk -v from="          version: $lint_v" '
    $0 == from { $0 = "          version: v9.9.9"; hit = 1 } { print } END { exit !hit }' "$d/ci.yml" > "$d/ci.new"; then
  mv "$d/ci.new" "$d/ci.yml"; run
  expect_fail "a golangci-lint version that differs from the Makefile's" 'GOLANGCI_LINT_VERSION'
else
  fail "ci.yml has no line 'version: $lint_v', the Makefile's GOLANGCI_LINT_VERSION (the test is stale)"
fi

if [ "$failures" -gt 0 ]; then
  echo "✗ check_workflows_test: $failures case(s) failed" >&2
  exit 1
fi
echo "✓ check_workflows_test: parsed, not grepped: triggers, the environment only in deploy.yml's deploy and site, the old App key nowhere, deploy.yml's gate, status functions, continue-on-error, triggers, tokens, concurrency, the superseded and tip gates, scan before sign-in, no secret (secrets.*, SUPABASE_ANON_KEY, SMOKE_EMAIL, SMOKE_PASSWORD, SMOKE_TOKEN) in the smoke steps (D286), the migrate_only input (no auth_preflight, no smoke) and the exact gates of the record step, the preflight, the bootstrap, migrate and the rollout (the smoke of a rollout is in its step and no input skips it; it mints afresh, touches each service before its replace and rolls back in one EXIT trap), the serving revisions and the serving api's facts recorded before migrate (the D285 floor, fail closed; the selection by mode), the rollout and the record step executed (rollbacks, hung calls cut by their bounds, the job's time budget, the restore commands printed first, the serving api proven from its image: D286 settings, drift, the D285 floor, outputs), the site jobs off under migrate_only, no third-party action or docker.sock, the site built without a token and deployed and checked only from main's tip (switch, actions by SHA, no cache, nothing signs in up to tip), ci-ok only in ci.yml, no external reusable workflows, no secrets: inherit or dynamic secrets, no test hooks"
