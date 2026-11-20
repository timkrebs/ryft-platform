#!/usr/bin/env bash
# scripts/check_cloudrun.py against copies of deploy/run (and of infra/gcp/gates.auto.tfvars, whose gate
# edge api's and ingest's ingress follows, D242): the committed templates pass, and each
# mutation of a copy fails with its own message (design §3's limits, ingress, accounts, the h2c port,
# probes, ticket-b.md's env and secret tables, the pins, P5, the key allowlist and the YAML shape).
# The fake values the check renders with are its own, built at run time.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
unset GITHUB_ACTIONS  # plain findings, no workflow commands, also in CI
fail() { echo "✗ check_cloudrun_test: $*" >&2; exit 1; }
# The interpreter itself, not a shim: the cases without yq run with a PATH of one empty directory.
py=$(python3 -I -c 'import sys; print(sys.executable)') || fail "python3 is missing"
check() { "$py" -I "$HERE/scripts/check_cloudrun.py" "$@"; }

# Without a mikefarah yq the check refuses (exit 2), it never passes: no yq at all, and another yq.
mkdir "$tmp/bin"
rc=0; PATH="$tmp/bin" check >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "without yq the check must exit 2 (got $rc)"
printf '#!/bin/sh\necho "yq 3.4.1"\n' > "$tmp/bin/yq"
chmod +x "$tmp/bin/yq"
rc=0; PATH="$tmp/bin" check >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "with a yq that is not mikefarah's the check must exit 2 (got $rc)"

# yq is on ubuntu-latest and CI installs the pinned one. Locally a missing yq skips the rest with a
# notice; in CI it is a failure.
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  if [ -n "${CI:-}" ]; then fail "yq (mikefarah v4) is missing in CI: check_cloudrun.py cannot run"; fi
  echo "- check_cloudrun_test: SKIPPED the checks against copies, yq (mikefarah v4) is not installed locally (brew install yq). CI does not skip."
  exit 0
fi

d=$tmp/run
g=$tmp/gates.auto.tfvars  # a copy of infra/gcp's gates: api's and ingest's ingress follows its edge (D242)
# fresh: new copies in one starting shape, whatever phase of runbook GCP 16 is committed: edge off, ingress
# all on api and ingest, one hop. So a PR that moves the gate or makes the deploy PR keeps this test green;
# the committed tree itself is checked as it is (below, before the first fresh).
# App Attest likewise starts at off, no exempt account and no ring pinned, whether the warn flip (D247) is
# committed or not; base is the PR base's config.env the next check reads (none: the order rule is skipped).
fresh() {
  rm -rf "$d"; cp -R "$HERE/deploy/run" "$d"; cp "$HERE/infra/gcp/gates.auto.tfvars" "$g"
  edge_is off
  baseline config.env '^XFF_TRUSTED_HOPS=' 'XFF_TRUSTED_HOPS=1'
  baseline api.yaml '^ *run[.]googleapis[.]com/ingress:' '    run.googleapis.com/ingress: all'
  baseline ingest.yaml '^ *run[.]googleapis[.]com/ingress:' '    run.googleapis.com/ingress: all'
  baseline config.env '^APP_ATTEST=' 'APP_ATTEST=off'
  baseline config.env '^APP_ATTEST_EXEMPT_USERS=' 'APP_ATTEST_EXEMPT_USERS='
  for f in api.yaml ingest.yaml; do
    if grep -qF -- "$RINGROW" "$d/$f"; then drop "$f" "$RINGROW"; fi
  done
  base=
}
# The ring's env row (D245): the marker starts with the list item, so the templates' comment that shows
# the row is not it.
RINGROW='- { name: APP_ATTEST_TOKEN_KEYS,'
base=
# baseline <file> <regex> <line>: the copy's one line matching <regex> becomes <line>.
baseline() {
  awk -v re="$2" -v to="$3" '$0 ~ re { $0 = to; n++ } { print } END { exit n == 1 ? 0 : 3 }' "$d/$1" > "$d/$1.new" ||
    fail "fixture: deploy/run/$1 has no single line matching $2"
  mv "$d/$1.new" "$d/$1"
}
# edge_is <phase>: the gates copy's one edge line set to <phase> (any text, quoted).
edge_is() {
  awk -v to="$1" '/^edge *=/ { $0 = "edge = \"" to "\""; n++ } { print } END { exit n == 1 ? 0 : 3 }' "$g" > "$g.new" ||
    fail "fixture: infra/gcp/gates.auto.tfvars has no single edge line"
  mv "$g.new" "$g"
}
one_line() { # <file> <marker>: the marker is on exactly one line of the copy
  [ "$(grep -cF -- "$2" "$d/$1")" = 1 ] || fail "fixture: '$2' is not on exactly one line of deploy/run/$1"
}
# edit <file> <marker> <from> <to>: on the one line with <marker>, the first <from> becomes <to> (fixed strings).
edit() {
  one_line "$1" "$2"
  awk -v id="$2" -v from="$3" -v to="$4" '
    index($0, id) && (i = index($0, from)) { $0 = substr($0, 1, i - 1) to substr($0, i + length(from)); n++ }
    { print }
    END { exit n == 1 ? 0 : 3 }' "$d/$1" > "$d/$1.new" || fail "fixture: '$3' is not on the line of deploy/run/$1 with '$2'"
  mv "$d/$1.new" "$d/$1"
}
# add <file> <marker> <line>: <line> right after the one line with <marker>, with that line's indentation.
add() {
  one_line "$1" "$2"
  awk -v id="$2" -v add="$3" '{ print } index($0, id) { ind = $0; sub(/[^ ].*$/, "", ind); print ind add }' "$d/$1" > "$d/$1.new"
  mv "$d/$1.new" "$d/$1"
}
# edit_next <file> <marker> <from> <to>: like edit, on the line right after the one with <marker> (both
# probes read /health/live, so the probe's own key is the marker).
edit_next() {
  one_line "$1" "$2"
  awk -v id="$2" -v from="$3" -v to="$4" '
    after && (i = index($0, from)) { $0 = substr($0, 1, i - 1) to substr($0, i + length(from)); n++ }
    { was = after; after = 0 }
    !was && index($0, id) { after = 1 }
    { print }
    END { exit n == 1 ? 0 : 3 }' "$d/$1" > "$d/$1.new" || fail "fixture: '$3' is not on the line after '$2' in deploy/run/$1"
  mv "$d/$1.new" "$d/$1"
}
# drop <file> <marker>: the one line with <marker> removed.
drop() { one_line "$1" "$2"; grep -vF -- "$2" "$d/$1" > "$d/$1.new"; mv "$d/$1.new" "$d/$1"; }
# pinkey <file> <secret> <text>: that secret's `key: "<n>"` becomes <text>.
pinkey() {
  one_line "$1" "name: $2,"
  awk -v id="name: $2," -v to="$3" 'index($0, id) { n += sub(/key: "[^"]*"/, to) } { print } END { exit n == 1 ? 0 : 3 }' \
    "$d/$1" > "$d/$1.new" || fail "fixture: no key: \"<n>\" on the line of deploy/run/$1 with $2"
  mv "$d/$1.new" "$d/$1"
}
pin_of() { sed -nE "s/.*name: $2, key: \"([0-9]+)\".*/\1/p" "$d/$1"; }
# bad <what> <text> [<not>]: the check fails on the copy (exit 1), names <text> (and not <not>); then a
# fresh copy.
copy() { if [ -n "$base" ]; then check --dir "$d" --gates "$g" --base-config "$base"; else check --dir "$d" --gates "$g"; fi; }
bad() {
  local rc=0 out
  out=$(copy 2>&1) || rc=$?
  [ "$rc" = 1 ] || fail "$1: want exit 1, got $rc: $out"
  case $out in *"$2"*) ;; *) fail "$1: the findings do not say '$2': $out" ;; esac
  if [ -n "${3:-}" ]; then
    case $out in *"$3"*) fail "$1: the findings say '$3': $out" ;; esac
  fi
  fresh
}
ok() {
  local rc=0 out
  out=$(copy 2>&1) || rc=$?
  [ "$rc" = 0 ] || fail "$1: want exit 0, got $rc: $out"
  fresh
}

# The committed state passes (the real directory and a copy), as does a config.env filled in after the
# bootstrap (values of the right shape, built here).
out=$(check 2>&1) || fail "the committed deploy/run fails: $out"
case $out in *"check_cloudrun: api, ingest, scoring, notify and migrate render"*) ;; *) fail "no summary line: $out" ;; esac
fresh
ok "a copy of deploy/run"
printf -v s '%20s' ''; ref=${s// /q}
printf -v s '%10s' ''; akey=${s// /A}; nkey=${s// /N}
{
  printf '%s\n' "PRIVACY_POLICY_VERSION=$(sed -n 's/^PRIVACY_POLICY_VERSION=//p' "$HERE/deploy/run/config.env")" LOG_LEVEL=info \
    BETA_GATE=on XFF_TRUSTED_HOPS=1 APP_ATTEST=off "APP_ATTEST_EXEMPT_USERS=$(printf '%08x-0000-4000-8000-%012x' 7 7)"
  printf 'GCP_PROJECT_NUMBER=7%011d\nPUSH_SA_ID=1%020d\n' 0 0
  printf '%s\n' "SUPABASE_PROJECT_REF=$ref" SUPABASE_POOLER_HOST=aws-0-eu-central-1.pooler.supabase.com \
    "APPLE_KEY_ID=$akey" "APNS_KEY_ID=$nkey"
} > "$d/config.env"
ok "config.env filled in"

# design §3: limits, scaling, billing.
edit api.yaml 'limits: {' 'memory: 512Mi' 'memory: 1Gi'
bad "api with 1 GiB" "api.yaml: resources"
edit ingest.yaml 'limits: {' 'cpu: "1"' 'cpu: "2"'
bad "ingest with 2 vCPU" "ingest.yaml: resources"
edit api.yaml 'minScale' '"0"' '"1"'
bad "api with an instance always on" "minScale: '1', want '0'"
edit api.yaml 'maxScale' '"3"' '"10"'
bad "api with 10 instances" "maxScale: '10', want '3'"
edit scoring.yaml 'cpu-throttling' '"true"' '"false"'
bad "scoring billed per instance" "cpu-throttling: 'false', want 'true'"
drop notify.yaml 'startup-cpu-boost'
bad "notify without the startup boost" "missing: run.googleapis.com/startup-cpu-boost"
edit ingest.yaml 'containerConcurrency' ' 4' ' 80'
bad "ingest with 80 requests" "concurrency 80, want 4"
edit scoring.yaml 'timeoutSeconds: 300' '300' '900'
bad "scoring with a 900 s timeout" "request timeout 900, want 300"

# Ingress, accounts, port, probes, traffic, image.
edit scoring.yaml 'run.googleapis.com/ingress' 'internal' 'all'
bad "scoring public" "ingress 'all', want 'internal'"
edit api.yaml 'run.googleapis.com/ingress' 'all' 'internal'
bad "api internal" "ingress 'internal', want 'all'"
add scoring.yaml 'run.googleapis.com/ingress' 'run.googleapis.com/default-url-disabled: "true"'
bad "scoring without its run.app URL" "keys not allowed here: run.googleapis.com/default-url-disabled"
add api.yaml 'startup-cpu-boost' 'run.googleapis.com/invoker-iam-disabled: "true"'
bad "api without the invoker check" "keys not allowed here: run.googleapis.com/invoker-iam-disabled"
edit api.yaml 'serviceAccountName' 'ryft-api@' 'ryft-ingest@'
bad "api as ingest" "api.yaml: runs as 'ryft-ingest@ryftlab-prod.iam.gserviceaccount.com'"
edit notify.yaml 'containerPort' 'name: h2c' 'name: http1'
bad "notify on HTTP/1" "ports must be exactly"
add ingest.yaml 'containerPort' '- { name: metrics, containerPort: 9090 }'
bad "ingest with two ports" "ports must be exactly"
# The startup probe waits for the port (/health/live), not the database: an instance starts during a
# database outage and its tick logs result error. /health/ready is the uptime checks' target.
edit_next api.yaml 'startupProbe:' '/health/live' '/health/ready'
bad "api's startup probe on /health/ready" "api.yaml startupProbe: httpGet must be {path: /health/live, port: 8080}"
edit_next ingest.yaml 'startupProbe:' '/health/live' '/health/ready'
bad "ingest's startup probe on /health/ready" "ingest.yaml startupProbe: httpGet must be {path: /health/live, port: 8080}"
edit_next notify.yaml 'livenessProbe:' '/health/live' '/health/ready'
bad "notify's liveness probe on /health/ready" "notify.yaml livenessProbe: httpGet must be"
edit scoring.yaml 'failureThreshold: 20' '20' '120'
bad "a startup budget of 120 s (each failed start billed that long)" "must stay <= 30"
edit api.yaml 'failureThreshold: 20' '20' '241'
bad "a startup probe past Cloud Run's 240 s" "must stay <= 30"
edit notify.yaml 'failureThreshold: 20' '20' '30'
ok "a startup budget of exactly 30 s"
edit ingest.yaml 'timeoutSeconds: 2' '2' '60'
bad "a liveness timeout over its period" "1 <= timeoutSeconds <= periodSeconds"
edit api.yaml 'latestRevision' 'true' 'false'
bad "traffic pinned to a revision" "traffic must be"
edit api.yaml 'image:' '${IMAGE}' 'europe-west1-docker.pkg.dev/ryftlab-prod/ryft/api:latest'
bad "api by tag" "api.yaml: image"
add api.yaml 'image:' 'command: ["/bin/sh"]'
bad "a command override" "keys not allowed here: command"

# ticket-b.md's env: exact names and values.
edit api.yaml 'PRIVACY_POLICY_URL' 'https://ryftlab.de/privacy' 'https://ryftlab.de/privacy-old'
bad "a wrong plain value" "PRIVACY_POLICY_URL='https://ryftlab.de/privacy-old', want"
add api.yaml 'name: LOG_LEVEL,' '- { name: EXTRA_FLAG, value: "on" }'
bad "an env outside the table" "EXTRA_FLAG is not in ticket-b.md's env table for api"
drop notify.yaml 'name: PUSH_SA_ID,'
bad "notify without PUSH_SA_ID" "PUSH_SA_ID is missing"
add notify.yaml 'name: LOG_LEVEL,' '- { name: LOG_LEVEL, value: debug }'
bad "an env twice" "LOG_LEVEL is set twice"
# The beta gate (D238): config.env's mode in api and ingest, nowhere else, never written into a template.
gate=$(sed -n 's/^BETA_GATE=//p' "$HERE/deploy/run/config.env")
[ -n "$gate" ] || fail "fixture: deploy/run/config.env has no BETA_GATE line"
other=on; [ "$gate" != on ] || other=off
drop api.yaml 'name: BETA_GATE,'
bad "api without the beta gate" "api.yaml env: BETA_GATE is missing"
drop ingest.yaml 'name: BETA_GATE,'
bad "ingest without the beta gate" "ingest.yaml env: BETA_GATE is missing"
edit ingest.yaml 'name: BETA_GATE,' '${BETA_GATE}' "$other"
bad "a gate mode written into the template" "BETA_GATE='$other', want '$gate'"
add scoring.yaml 'name: LOG_LEVEL,' '- { name: BETA_GATE, value: "${BETA_GATE}" }'
bad "scoring with the beta gate" "BETA_GATE is not in ticket-b.md's env table for scoring"
add notify.yaml 'name: LOG_LEVEL,' '- { name: BETA_GATE, value: "${BETA_GATE}" }'
bad "notify with the beta gate" "BETA_GATE is not in ticket-b.md's env table for notify"
edit config.env 'BETA_GATE=' "$gate" 'enforce'
bad "a gate mode outside off|warn|on" "BETA_GATE='enforce' does not have the shape off, warn or on"
drop config.env 'BETA_GATE='
bad "config.env without the gate mode" "BETA_GATE is missing"
# The edge limit's hop count (D240): config.env's in api and ingest, nowhere else, never written into a
# template, and above 1 only behind the load balancer (ingress internal-and-cloud-load-balancing).
hops=1  # every copy's (fresh): the committed count may be 2 once the edge's deploy PR is in (runbook GCP 16.6)
drop api.yaml 'name: XFF_TRUSTED_HOPS,'
bad "api without the hop count" "api.yaml env: XFF_TRUSTED_HOPS is missing"
drop ingest.yaml 'name: XFF_TRUSTED_HOPS,'
bad "ingest without the hop count" "ingest.yaml env: XFF_TRUSTED_HOPS is missing"
edit api.yaml 'name: XFF_TRUSTED_HOPS,' '${XFF_TRUSTED_HOPS}' 3
bad "a hop count written into the template" "XFF_TRUSTED_HOPS='3', want '1'"
add scoring.yaml 'name: LOG_LEVEL,' '- { name: XFF_TRUSTED_HOPS, value: "${XFF_TRUSTED_HOPS}" }'
bad "scoring with the hop count" "XFF_TRUSTED_HOPS is not in ticket-b.md's env table for scoring"
add notify.yaml 'name: LOG_LEVEL,' '- { name: XFF_TRUSTED_HOPS, value: "${XFF_TRUSTED_HOPS}" }'
bad "notify with the hop count" "XFF_TRUSTED_HOPS is not in ticket-b.md's env table for notify"
edit config.env 'XFF_TRUSTED_HOPS=' "$hops" 9
bad "a hop count outside 1-8" "XFF_TRUSTED_HOPS='9' does not have the shape 1 to 8"
drop config.env 'XFF_TRUSTED_HOPS='
bad "config.env without the hop count" "XFF_TRUSTED_HOPS is missing"
edit config.env 'XFF_TRUSTED_HOPS=' "$hops" 2
bad "two hops with ingress all on api" "api.yaml: XFF_TRUSTED_HOPS=2 with ingress 'all'"
edit config.env 'XFF_TRUSTED_HOPS=' "$hops" 2
bad "two hops with ingress all on ingest" "ingest.yaml: XFF_TRUSTED_HOPS=2 with ingress 'all'"
edit config.env 'XFF_TRUSTED_HOPS=' "$hops" 2
edge_is live
bad "two hops with ingress all, edge live" "api.yaml: XFF_TRUSTED_HOPS=2 with ingress 'all'"

# The edge (D242): api's and ingest's ingress against the gate in infra/gcp/gates.auto.tfvars.
# lb <hops>: the deploy PR of runbook GCP 16.6 on the copy (both services behind the load balancer).
lb() {
  edit config.env 'XFF_TRUSTED_HOPS=' "$hops" "$1"
  edit api.yaml 'run.googleapis.com/ingress' 'all' 'internal-and-cloud-load-balancing'
  edit ingest.yaml 'run.googleapis.com/ingress' 'all' 'internal-and-cloud-load-balancing'
}
for phase in off built live locked; do
  # Every phase allows today's shape but locked, where the mappings are gone.
  edge_is "$phase"
  if [ "$phase" = locked ]; then
    bad "ingress all with edge locked" "api.yaml: ingress 'all' with edge = 'locked'"
  else
    ok "ingress all, one hop, edge $phase"
  fi
done
for phase in off built; do
  edge_is "$phase"; lb 2
  bad "the load balancer's ingress with edge $phase" \
    "api.yaml: ingress 'internal-and-cloud-load-balancing' with edge = '$phase'" "XFF_TRUSTED_HOPS=2 with"
done
for phase in live locked; do
  edge_is "$phase"; lb 2
  ok "the load balancer's ingress and two hops, edge $phase"
done
edge_is live; lb 3
ok "the load balancer's ingress and three hops, edge live (built's XFF check decides the count)"
edge_is live; lb 1
bad "the load balancer's ingress with one hop" "api.yaml: ingress 'internal-and-cloud-load-balancing' with XFF_TRUSTED_HOPS=1"
edge_is locked; lb 2
edit ingest.yaml 'run.googleapis.com/ingress' 'internal-and-cloud-load-balancing' 'all'
bad "edge locked with ingest still on ingress all" "ingest.yaml: ingress 'all' with edge = 'locked'" "api.yaml: ingress"
edge_is live
edit api.yaml 'run.googleapis.com/ingress' 'all' 'internal'
bad "api internal, edge live" "api.yaml: ingress 'internal', want 'all' or"
edge_is live
edit scoring.yaml 'run.googleapis.com/ingress' 'internal' 'internal-and-cloud-load-balancing'
bad "scoring behind the load balancer" "scoring.yaml: ingress 'internal-and-cloud-load-balancing', want 'internal'"
edge_is on
bad "a gate value outside the four" "edge must be one of off, built, live, locked"
printf 'edge = "live"\n' >> "$g"
bad "the gate set twice" "edge is set on lines"
grep -v '^edge' "$g" > "$g.new"; mv "$g.new" "$g"
ok "no edge line (variables.tf's default, off)"
grep -v '^edge' "$g" > "$g.new"; mv "$g.new" "$g"
lb 2
bad "the load balancer's ingress with no edge line" "api.yaml: ingress 'internal-and-cloud-load-balancing' with edge = 'off'"
rm "$g"
bad "the gates missing" "cannot read it"

# P5 (design §5.1): never on Cloud Run, in a template or in config.env.
add ingest.yaml 'name: LOG_LEVEL,' '- { name: PORT, value: "8080" }'
bad "PORT set" "PORT is never set on Cloud Run"
add scoring.yaml 'name: LOG_LEVEL,' '- { name: ALLOW_INSECURE_PUSH, value: "true" }'
bad "ALLOW_INSECURE_PUSH set" "ALLOW_INSECURE_PUSH is never set on Cloud Run"
add api.yaml 'name: LOG_LEVEL,' '- { name: API_KEY, valueFrom: { secretKeyRef: { name: admin-api-key, key: "1" } } }'
bad "API_KEY set" "API_KEY is never set on Cloud Run"
edit api.yaml 'name: AUTH_MODE,' 'supabase' 'apikey'
bad "AUTH_MODE apikey" "AUTH_MODE=apikey: on Cloud Run only supabase"
printf 'AUTH_MODE=apikey\n' >> "$d/config.env"
bad "AUTH_MODE in config.env" "AUTH_MODE is never set on Cloud Run"
printf 'USER_ID=1\n' >> "$d/config.env"
bad "USER_ID in config.env" "USER_ID is never set on Cloud Run"

# Secrets: by number, from the table, only where the accessor table allows.
pinkey api.yaml supabase-secret-key 'key: "latest"'
bad "a secret at latest" "pins supabase-secret-key at 'latest'"
pinkey api.yaml data-key 'key: "0"'
bad "a secret at version 0" "pins data-key at '0'"
pinkey notify.yaml apns-key-p8 'key: 1'
bad "a version as a number, not a string" "pins apns-key-p8 at 1"
edit scoring.yaml 'name: db-password-scoring,' 'db-password-scoring' 'db-password-api'
bad "scoring with api's password" "PGPASSWORD reads secret db-password-api, want db-password-scoring"
add migrate.yaml 'name: LOG_LEVEL,' '- { name: OTLP_TOKEN, valueFrom: { secretKeyRef: { name: otlp-token, key: "1" } } }'
bad "migrate with the OTLP token" "migrate reads otlp-token, which only api, ingest, notify, scoring can access"
# The OTLP pins went with Grafana Cloud (D236): the token is in no service's table any more, and the
# plain pair in no env table (otlp-token itself stays until its own PR, runbook GCP step 11.6).
add scoring.yaml 'name: LOG_LEVEL,' '- { name: OTLP_TOKEN, valueFrom: { secretKeyRef: { name: otlp-token, key: "1" } } }'
bad "scoring with the OTLP token again" "OTLP_TOKEN from otlp-token is not in ticket-b.md's secret table for scoring"
add api.yaml 'name: LOG_LEVEL,' '- { name: OTLP_ENDPOINT, value: "" }'
bad "api with OTLP_ENDPOINT again" "OTLP_ENDPOINT is not in ticket-b.md's env table for api"
add ingest.yaml 'name: LOG_LEVEL,' '- { name: DATA_ENCRYPTION_KEY, valueFrom: { secretKeyRef: { name: data-key, key: "1" } } }'
bad "ingest with the data key" "ingest reads data-key, which only api can access"
add api.yaml 'name: LOG_LEVEL,' '- { name: OWNER_PASSWORD, valueFrom: { secretKeyRef: { name: db-password-owner, key: "1" } } }'
bad "api with the owner's password" "api reads db-password-owner, which only migrate can access"
# D231: api's PostHog key is optional (its pin follows the secret's first version), checked like the rest.
# Each case starts from a copy without the pin, so they hold before and after the pin PR.
nopin() { if grep -qF 'name: POSTHOG_PROJECT_KEY,' "$d/api.yaml"; then drop api.yaml 'name: POSTHOG_PROJECT_KEY,'; fi; }
nopin
ok "api without the PostHog key's pin"
nopin
add api.yaml 'name: PUSH_SA_ID,' '- { name: POSTHOG_PROJECT_KEY, valueFrom: { secretKeyRef: { name: posthog-project-key, key: "1" } } }'
ok "api with the PostHog key pinned at 1"
nopin
add api.yaml 'name: PUSH_SA_ID,' '- { name: POSTHOG_PROJECT_KEY, valueFrom: { secretKeyRef: { name: posthog-project-key, key: "latest" } } }'
bad "the PostHog key at latest" "pins posthog-project-key at 'latest'"
nopin
add api.yaml 'name: PUSH_SA_ID,' '- { name: POSTHOG_PROJECT_KEY, valueFrom: { secretKeyRef: { name: admin-api-key, key: "1" } } }'
bad "the PostHog key from another secret" "POSTHOG_PROJECT_KEY reads secret admin-api-key, want posthog-project-key"
nopin
add api.yaml 'name: PUSH_SA_ID,' '- { name: POSTHOG_PROJECT_KEY, value: "phc_inline" }'
bad "the PostHog key as a plain value" "POSTHOG_PROJECT_KEY is not in ticket-b.md's env table for api"
add ingest.yaml 'name: PUSH_SA_ID,' '- { name: POSTHOG_PROJECT_KEY, valueFrom: { secretKeyRef: { name: posthog-project-key, key: "1" } } }'
bad "ingest with the PostHog key" "ingest reads posthog-project-key, which only api can access"

# App Attest (D245, D247): the mode and the exempt accounts from config.env in api and ingest only, never
# written into a template; the ring (APP_ATTEST_TOKEN_KEYS from app-attest-token-key) may be pinned under
# off, must be pinned in both under warn and on, only api and ingest read it; and against the PR's base
# never off -> on. Every case starts from fresh's off without a ring, so they hold after the flips too.
uid() { printf '%08x-0000-4000-8000-%012x' "$1" "$1"; }  # a test account's id, built here
mode() { baseline config.env '^APP_ATTEST=' "APP_ATTEST=$1"; }
# ring <file> [<secret> [<version>]]: the ring's row in <file> (after PUSH_SA_ID).
ring() {
  add "$1" 'name: PUSH_SA_ID,' "- { name: APP_ATTEST_TOKEN_KEYS, valueFrom: { secretKeyRef: { name: ${2:-app-attest-token-key}, key: \"${3:-1}\" } } }"
}
# base_is <mode|->: the base's config.env (as CI writes it from git show) with APP_ATTEST=<mode>, or without
# the line (every base before this PR).
base_is() {
  base=$tmp/base.env
  grep -v '^APP_ATTEST=' "$HERE/deploy/run/config.env" > "$base" || true
  if [ "$1" != - ]; then printf 'APP_ATTEST=%s\n' "$1" >> "$base"; fi
}
drop api.yaml 'name: APP_ATTEST,'
bad "api without App Attest's mode" "api.yaml env: APP_ATTEST is missing"
drop ingest.yaml 'name: APP_ATTEST_EXEMPT_USERS,'
bad "ingest without the exempt accounts" "ingest.yaml env: APP_ATTEST_EXEMPT_USERS is missing"
edit api.yaml 'name: APP_ATTEST,' '${APP_ATTEST}' warn
bad "a mode written into the template" "APP_ATTEST='warn', want 'off'"
edit ingest.yaml 'name: APP_ATTEST_EXEMPT_USERS,' '${APP_ATTEST_EXEMPT_USERS}' "$(uid 1)"
bad "an exempt account written into the template" "APP_ATTEST_EXEMPT_USERS='$(uid 1)', want ''"
add scoring.yaml 'name: LOG_LEVEL,' '- { name: APP_ATTEST, value: "${APP_ATTEST}" }'
bad "scoring with App Attest's mode" "APP_ATTEST is not in ticket-b.md's env table for scoring"
add notify.yaml 'name: LOG_LEVEL,' '- { name: APP_ATTEST_EXEMPT_USERS, value: "${APP_ATTEST_EXEMPT_USERS}" }'
bad "notify with the exempt accounts" "APP_ATTEST_EXEMPT_USERS is not in ticket-b.md's env table for notify"
mode Warn
bad "a mode in another case" "APP_ATTEST='Warn' does not have the shape off, warn or on"
drop config.env 'APP_ATTEST='
bad "config.env without App Attest's mode" "APP_ATTEST is missing"
drop config.env 'APP_ATTEST_EXEMPT_USERS='
bad "config.env without the exempt line" "APP_ATTEST_EXEMPT_USERS is missing"
baseline config.env '^APP_ATTEST_EXEMPT_USERS=' "APP_ATTEST_EXEMPT_USERS=$(uid 1),$(uid 2),$(uid 3),$(uid 4),$(uid 5),$(uid 6)"
bad "six exempt accounts" "does not have the shape empty, or 1 to 5 lower-case UUIDs"
baseline config.env '^APP_ATTEST_EXEMPT_USERS=' "APP_ATTEST_EXEMPT_USERS=$(uid 10 | tr a-f A-F)"
bad "an upper-case exempt id" "does not have the shape empty, or 1 to 5 lower-case UUIDs"
baseline config.env '^APP_ATTEST_EXEMPT_USERS=' "APP_ATTEST_EXEMPT_USERS=$(uid 2),$(uid 2)"
bad "an exempt account twice" "APP_ATTEST_EXEMPT_USERS names an account twice"
# The ring under off: allowed, absent or pinned in both (an operator may pin it before warn, plan
# decision 1), but never in one service only, and at one version in both (api signs, ingest verifies).
ok "off without the ring"
ring api.yaml; ring ingest.yaml
ok "off with the ring pinned in api and ingest"
ring api.yaml
bad "off with the ring pinned in api only" "APP_ATTEST_TOKEN_KEYS is pinned in api.yaml only"
mode warn; ring api.yaml app-attest-token-key 2; ring ingest.yaml app-attest-token-key 1
bad "the ring at two versions" "app-attest-token-key is pinned at 2 in api.yaml and at 1 in ingest.yaml"
# warn and on without the ring in both: the services would refuse to start (D245).
mode warn
bad "warn without the ring" "api.yaml: APP_ATTEST=warn without APP_ATTEST_TOKEN_KEYS pinned"
mode warn; ring api.yaml
bad "warn with the ring in api only" "ingest.yaml: APP_ATTEST=warn without APP_ATTEST_TOKEN_KEYS pinned" "api.yaml: APP_ATTEST=warn"
mode on; ring ingest.yaml
bad "on with the ring in ingest only" "api.yaml: APP_ATTEST=on without APP_ATTEST_TOKEN_KEYS pinned" "ingest.yaml: APP_ATTEST=on"
mode warn; ring api.yaml; ring ingest.yaml
ok "warn with the ring in api and ingest"
mode warn; ring api.yaml; ring ingest.yaml
baseline config.env '^APP_ATTEST_EXEMPT_USERS=' "APP_ATTEST_EXEMPT_USERS=$(uid 1),$(uid 2)"
ok "warn with the ring and two exempt accounts"
mode on; ring api.yaml; ring ingest.yaml
out=$(copy 2>&1) || fail "on with the ring in both and no base: $out"
case $out in *"the APP_ATTEST order against a base (off -> warn -> on, D245) is skipped"*) ;; *) fail "no base, no note: $out" ;; esac
fresh
# The ring: by number, from its secret, only in api and ingest, never a plain value.
mode warn; ring api.yaml app-attest-token-key latest; ring ingest.yaml
bad "the ring at latest" "pins app-attest-token-key at 'latest'"
ring ingest.yaml data-key
bad "the ring from another secret" "APP_ATTEST_TOKEN_KEYS reads secret data-key, want app-attest-token-key"
ring scoring.yaml
bad "scoring with the ring" "scoring reads app-attest-token-key, which only api, ingest can access"
ring notify.yaml
bad "notify with the ring" "notify reads app-attest-token-key, which only api, ingest can access"
add api.yaml 'name: PUSH_SA_ID,' '- { name: APP_ATTEST_TOKEN_KEYS, value: "inline" }'
bad "the ring as a plain value" "APP_ATTEST_TOKEN_KEYS is not in ticket-b.md's env table for api"
# The order against the base (D238's, a rule since D245): off -> warn -> on, the way back is free.
mode on; ring api.yaml; ring ingest.yaml; base_is off
bad "off -> on" "config.env: APP_ATTEST=on with the base at 'off': off -> warn -> on"
mode on; ring api.yaml; ring ingest.yaml; base_is -
bad "a base without the line (off) -> on" "APP_ATTEST=on with the base at 'off'"
mode on; base_is off
bad "off -> on without the ring" "off -> warn -> on"
mode warn; ring api.yaml; ring ingest.yaml; base_is off
out=$(copy 2>&1) || fail "off -> warn: $out"
case $out in *"APP_ATTEST's order against the base hold"*) ;; *) fail "off -> warn: the summary does not name the order: $out" ;; esac
case $out in *"is skipped"*) fail "off -> warn with a base: the note says skipped: $out" ;; esac
fresh
mode warn; ring api.yaml; ring ingest.yaml; base_is -
ok "a base without the line -> warn"
mode on; ring api.yaml; ring ingest.yaml; base_is warn
ok "warn -> on"
mode on; ring api.yaml; ring ingest.yaml; base_is on
ok "on stays on"
mode warn; ring api.yaml; ring ingest.yaml; base_is on
ok "on -> warn (the rollback)"
base_is on
ok "on -> off"
base_is off
ok "off stays off"
base=$tmp/nope.env
bad "a base that cannot be read" "cannot read it"
base_is warn; printf 'APP_ATTEST=on\n' >> "$base"
bad "a base with the mode twice" "APP_ATTEST is set more than once there"
# design §4: migrate's pin of a role equals the service's own.
pinkey migrate.yaml db-password-api "key: \"$(( $(pin_of api.yaml db-password-api) + 1 ))\""
bad "migrate's pin ahead of api's" "db-password-api is pinned at"
pinkey ingest.yaml db-password-ingest "key: \"$(( $(pin_of migrate.yaml db-password-ingest) + 1 ))\""
bad "ingest's pin ahead of migrate's" "move both in one commit (scripts/rotate_db_password.sh ingest <version>)"

# migrate: the job's own limits and account.
edit migrate.yaml 'serviceAccountName' 'ryft-migrate@' 'ryft-api@'
bad "migrate as api" "runs as 'ryft-api@ryftlab-prod.iam.gserviceaccount.com', want ryft-migrate@"
edit migrate.yaml 'taskCount' '1' '2'
bad "migrate in two tasks" "one task, parallelism 1"
edit migrate.yaml 'timeoutSeconds' '600' '3600'
bad "migrate for an hour" "task timeout 3600, want 600"
edit migrate.yaml 'limits: {' 'memory: 512Mi' 'memory: 2Gi'
bad "migrate with 2 GiB" "migrate.yaml: resources"
# design §4: no retry, a second failed owner login feeds the pooler's circuit breaker.
edit migrate.yaml 'maxRetries:' '0' '1'
bad "migrate retried once" "maxRetries 1, want 0"
edit migrate.yaml 'maxRetries:' '0' 'false'
bad "maxRetries as a boolean" "maxRetries False, want 0"

# The files and the YAML itself.
printf 'apiVersion: v1\n' > "$d/web.yaml"
bad "an extra file" "web.yaml: not a file deploy/run may hold"
rm "$d/notify.yaml"
bad "a missing template" "notify.yaml: missing"
edit api.yaml 'limits: {' 'limits: {' 'limits: &l {'
bad "an anchor" "anchors and aliases are not allowed"
# Anchors and aliases as the parser sees them: the two probes sharing one httpGet through &probe and
# *probe (the same values once resolved), and an alias without its anchor (which does not parse).
edit_next api.yaml 'startupProbe:' 'httpGet: {' 'httpGet: &probe {'
edit_next api.yaml 'livenessProbe:' 'httpGet: { path: /health/live, port: 8080 }' 'httpGet: *probe'
bad "an anchor and its alias" "anchors and aliases are not allowed"
edit_next api.yaml 'livenessProbe:' 'httpGet: { path: /health/live, port: 8080 }' 'httpGet: *probe'
bad "an alias without its anchor" "api.yaml: yq cannot parse it"
# `&` and `*` inside a comment or a quoted value are text (so is the rendered DATABASE_URL's query
# string, `...&sslrootcert=...&default_query_exec_mode=exec`, with which every copy above passes).
add api.yaml 'name: LOG_LEVEL,' '# &a and *a here are a comment, as is https://*.run.app/?a=1&b=2'
edit api.yaml 'name: AUTH_MODE,' 'value: supabase }' 'value: supabase }  # *.run.app, &a, *a'
ok "& and * in comments"
edit api.yaml 'PRIVACY_POLICY_URL' '"https://ryftlab.de/privacy"' '"&a *a https://*.run.app/?a=1&b=2"'
bad "& and * in a quoted value (a wrong value, no anchor)" "PRIVACY_POLICY_URL='&a *a https://*.run.app/?a=1&b=2', want" \
  "anchors and aliases"
edit api.yaml 'timeoutSeconds: 60' '60' '!!float 60'
bad "a float tag" "YAML tags not allowed: !!float"
edit api.yaml 'limits: {' '{ cpu: "1",' '{ <<: { cpu: "1" },'
bad "a merge key" "api.yaml"
printf -- '---\n{}\n' >> "$d/scoring.yaml"
bad "two documents" "2 YAML documents: exactly one"
add notify.yaml 'run.googleapis.com/ingress' 'run.googleapis.com/ingress: all'
bad "a key twice" "notify.yaml"

# What the renderer refuses fails the check too.
edit config.env 'LOG_LEVEL=' 'info' 'verbose'
bad "a config value of the wrong shape" "LOG_LEVEL='verbose' does not have the shape"
edit api.yaml 'name: LOG_LEVEL,' '${LOG_LEVEL}' '${HOME}'
bad "a placeholder outside the list" "placeholder(s) outside the fixed list: HOME"
add migrate.yaml 'name: LOG_LEVEL,' '- { name: PUSH_AUDIENCE, value: "${PUSH_AUDIENCE}" }'
bad "migrate with an audience" "migrate is no push target"

echo "✓ check_cloudrun_test: the committed deploy/run passes; limits, scaling, ingress, accounts, port, probes (startup on /health/live within 30 s), traffic, image, env (BETA_GATE, XFF_TRUSTED_HOPS, APP_ATTEST and APP_ATTEST_EXEMPT_USERS from config.env in api and ingest only, two hops only behind the load balancer), App Attest's ring pinned in api and ingest under warn and on and readable by them only, off -> on against a base refused (off -> warn, warn -> on and back pass), the load balancer's ingress only at edge live or locked and with more than one hop, locked only behind it, P5, secret pins and accessors, migrate's pins and no-retry, files and YAML shape (anchors and aliases as parsed; & and * in comments and values pass) each fail"
