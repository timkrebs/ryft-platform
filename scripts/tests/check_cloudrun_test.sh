#!/usr/bin/env bash
# scripts/check_cloudrun.py against copies of deploy/run: the committed templates pass, and each
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
fresh() { rm -rf "$d"; cp -R "$HERE/deploy/run" "$d"; }
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
# bad <what> <text>: the check fails on the copy (exit 1) and names <text>; then a fresh copy.
bad() {
  local rc=0 out
  out=$(check --dir "$d" 2>&1) || rc=$?
  [ "$rc" = 1 ] || fail "$1: want exit 1, got $rc: $out"
  case $out in *"$2"*) ;; *) fail "$1: the findings do not say '$2': $out" ;; esac
  fresh
}
ok() {
  local rc=0 out
  out=$(check --dir "$d" 2>&1) || rc=$?
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
  printf '%s\n' "PRIVACY_POLICY_VERSION=$(sed -n 's/^PRIVACY_POLICY_VERSION=//p' "$HERE/deploy/run/config.env")" LOG_LEVEL=info
  printf 'GCP_PROJECT_NUMBER=7%011d\nPUSH_SA_ID=1%020d\n' 0 0
  printf '%s\n' "SUPABASE_PROJECT_REF=$ref" SUPABASE_POOLER_HOST=aws-0-eu-central-1.pooler.supabase.com \
    "APPLE_KEY_ID=$akey" "APNS_KEY_ID=$nkey" OTLP_ENDPOINT=https://otlp-gateway-prod-eu-west-2.grafana.net/otlp
  printf 'OTLP_USER=9%05d\n' 0
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
edit api.yaml '/health/ready' '/health/ready' '/health/live'
bad "api's startup probe on /health/live" "api.yaml startupProbe: httpGet must be"
edit notify.yaml '/health/live' '/health/live' '/health/ready'
bad "notify's liveness probe on /health/ready" "notify.yaml livenessProbe: httpGet must be"
edit scoring.yaml 'failureThreshold: 120' '120' '241'
bad "a startup probe past 240 s" "must stay <= 240"
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
add ingest.yaml 'name: LOG_LEVEL,' '- { name: DATA_ENCRYPTION_KEY, valueFrom: { secretKeyRef: { name: data-key, key: "1" } } }'
bad "ingest with the data key" "ingest reads data-key, which only api can access"
add api.yaml 'name: LOG_LEVEL,' '- { name: OWNER_PASSWORD, valueFrom: { secretKeyRef: { name: db-password-owner, key: "1" } } }'
bad "api with the owner's password" "api reads db-password-owner, which only migrate can access"
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

# The files and the YAML itself.
printf 'apiVersion: v1\n' > "$d/web.yaml"
bad "an extra file" "web.yaml: not a file deploy/run may hold"
rm "$d/notify.yaml"
bad "a missing template" "notify.yaml: missing"
edit api.yaml 'limits: {' 'limits: {' 'limits: &l {'
bad "an anchor" "anchors and aliases are not allowed"
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

echo "✓ check_cloudrun_test: the committed deploy/run passes; limits, scaling, ingress, accounts, port, probes, traffic, image, env, P5, secret pins and accessors, migrate's pins, files and YAML shape each fail"
