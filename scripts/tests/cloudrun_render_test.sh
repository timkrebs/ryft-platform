#!/usr/bin/env bash
# scripts/cloudrun_render.py, the renderer deploy.yml applies Cloud Run with: a config of the right
# shape renders the derived values (DATABASE_URL, PUSH_AUDIENCE, IMAGE by digest) into every template
# and nothing else changes; the environment changes nothing; FILL_ME_IN fails a render and passes
# --lint; every wrong shape, key, digest, placeholder and stray `$` fails closed (exit 1, nothing
# written); a wrong invocation is exit 2. The fake values are built here at run time (none is a
# credential), the templates are copies of deploy/run.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
unset GITHUB_ACTIONS
fail() { echo "✗ cloudrun_render_test: $*" >&2; exit 1; }
py=$(python3 -I -c 'import sys; print(sys.executable)') || fail "python3 is missing"
render() { "$py" -I "$HERE/scripts/cloudrun_render.py" "$@"; }

pn=$(printf '7%011d' 0)
said=$(printf '1%020d' 0)
printf -v s '%20s' ''; ref=${s// /q}
printf -v s '%10s' ''; akey=${s// /A}; nkey=${s// /N}
host=aws-0-eu-central-1.pooler.supabase.com
policy=2026-10-26
gate=warn
hops=1
attest=warn
# uid <n>: a lower-case UUID built from <n> (test accounts, no secrets).
uid() { printf '%08x-0000-4000-8000-%012x' "$1" "$1"; }
exempt="$(uid 1),$(uid 2)"
dg() { local s; printf -v s '%64s' ''; printf 'sha256:%s' "${s// /$1}"; }  # 64 times one hex digit
D=(--digest "api=$(dg a)" --digest "ingest=$(dg b)" --digest "scoring=$(dg c)" --digest "notify=$(dg d)" --digest "migrate=$(dg e)")
cfg=$tmp/config.env
tpl=$tmp/tpl
out=$tmp/out
fresh() {
  printf '%s\n' "# a fake, of the right shape" "PRIVACY_POLICY_VERSION=$policy" LOG_LEVEL=info "BETA_GATE=$gate" "XFF_TRUSTED_HOPS=$hops" \
    "APP_ATTEST=$attest" "APP_ATTEST_EXEMPT_USERS=$exempt" "GCP_PROJECT_NUMBER=$pn" "PUSH_SA_ID=$said" "SUPABASE_PROJECT_REF=$ref" "SUPABASE_POOLER_HOST=$host" "APPLE_KEY_ID=$akey" \
    "APNS_KEY_ID=$nkey" > "$cfg"
  rm -rf "$tpl" "$out"
  mkdir "$tpl"
  cp "$HERE"/deploy/run/{api,ingest,scoring,notify,migrate}.yaml "$tpl/"
}
# setcfg <key> <value|->: the key's line set to <value>, or removed with -.
setcfg() {
  awk -v k="$1" -v v="$2" 'index($0, k "=") == 1 { if (v != "-") print k "=" v; next } { print }' "$cfg" > "$cfg.new"
  mv "$cfg.new" "$cfg"
}
R=(--config "$cfg" --templates "$tpl")
has() { grep -qF -- "$2" "$out/$1" || fail "rendered $1 has no '$2'"; }
# bad <what> <text> <args>...: exit 1, the message names <text>, and nothing is written; then fresh fixtures.
bad() {
  local what=$1 want=$2 rc=0 o
  shift 2
  o=$(render "$@" 2>&1) || rc=$?
  [ "$rc" = 1 ] || fail "$what: want exit 1, got $rc: $o"
  case $o in *"$want"*) ;; *) fail "$what: the message does not say '$want': $o" ;; esac
  [ ! -e "$out" ] || fail "$what: something was written to $out"
  fresh
}
usage() {
  local rc=0
  render "$@" >/dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "render $*: a wrong invocation must exit 2, got $rc"
}

# The committed config.env and templates lint clean (FILL_ME_IN may be left before the bootstrap).
o=$(render --lint 2>&1) || fail "the committed deploy/run does not lint: $o"
o=$(render --lint --config "$HERE/deploy/run/config.env" --templates "$HERE/deploy/run" 2>&1) || fail "lint with explicit paths: $o"

# A clean render: the derived values, by digest, every placeholder filled, nothing else changed.
fresh
o=$(render "${R[@]}" --out "$out" "${D[@]}" 2>&1) || fail "a clean render failed: $o"
letter() { case $1 in api) echo a ;; ingest) echo b ;; scoring) echo c ;; notify) echo d ;; migrate) echo e ;; esac; }
for f in api ingest scoring notify migrate; do
  [ -f "$out/$f.yaml" ] || fail "no $f.yaml rendered"
  if grep -n '\$' "$out/$f.yaml"; then fail "$f.yaml: a \$ is left after rendering"; fi
  [ "$(wc -l < "$tpl/$f.yaml")" = "$(wc -l < "$out/$f.yaml")" ] || fail "$f.yaml: the line count changed"
  # Every line without a placeholder is the template's, in order: the diff only adds (the filled lines).
  gone=$( { diff <(grep -v '\${' "$tpl/$f.yaml") "$out/$f.yaml" || true; } | grep -c '^<' || true)
  [ "$gone" = 0 ] || fail "$f.yaml: $gone line(s) without a placeholder changed"
  has "$f.yaml" "image: \"europe-west1-docker.pkg.dev/ryftlab-prod/ryft/$f@$(dg "$(letter "$f")")\""
  has "$f.yaml" "name: LOG_LEVEL, value: \"info\" }"
done
for s in api ingest scoring notify; do
  has "$s.yaml" "name: DATABASE_URL, value: \"postgresql://ryft_$s.$ref@$host:6543/postgres?sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt&default_query_exec_mode=exec\" }"
  has "$s.yaml" "name: PUSH_AUDIENCE, value: \"https://$s-$pn.europe-west1.run.app\" }"
  has "$s.yaml" "name: PUSH_SA_ID, value: \"$said\" }"
  if grep -q 'OTLP_' "$out/$s.yaml"; then fail "$s.yaml still has an OTLP_ variable (gone with Grafana Cloud, D236)"; fi
done
has migrate.yaml "name: DATABASE_URL, value: \"postgresql://postgres.$ref@$host:5432/postgres?sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt\" }"
if grep -q 'PUSH_AUDIENCE' "$out/migrate.yaml"; then fail "migrate.yaml has a PUSH_AUDIENCE"; fi
for s in api ingest; do
  has "$s.yaml" "name: PRIVACY_POLICY_VERSION, value: \"$policy\" }"
  has "$s.yaml" "name: SUPABASE_PROJECT_REF, value: \"$ref\" }"
  has "$s.yaml" "name: BETA_GATE, value: \"$gate\" }"
  has "$s.yaml" "name: XFF_TRUSTED_HOPS, value: \"$hops\" }"
  has "$s.yaml" "name: APP_ATTEST, value: \"$attest\" }"
  has "$s.yaml" "name: APP_ATTEST_EXEMPT_USERS, value: \"$exempt\" }"
  # The ring's pin comes with the warn flip PR (D247), never from the renderer: no placeholder makes it.
  if grep -q '^ *- { name: APP_ATTEST_TOKEN_KEYS,.*\${' "$tpl/$s.yaml"; then fail "$s.yaml: the ring's pin from a placeholder"; fi
done
# The beta gate only where it is read (D238): scoring reads the consent mode alone, notify and migrate neither.
for f in scoring notify migrate; do
  if grep -q 'BETA_GATE' "$out/$f.yaml"; then fail "$f.yaml has BETA_GATE: only api and ingest run the beta gate"; fi
  # The hop count likewise (D240): only the two public services run the edge limit.
  if grep -q 'XFF_TRUSTED_HOPS' "$out/$f.yaml"; then fail "$f.yaml has XFF_TRUSTED_HOPS: only api and ingest run the edge limit"; fi
  # App Attest likewise (D245): only the app's requests to api and ingest carry a token.
  if grep -q 'APP_ATTEST' "$out/$f.yaml"; then fail "$f.yaml has APP_ATTEST: only api and ingest check App Attest"; fi
done
has api.yaml "name: APPLE_KEY_ID, value: \"$akey\" }"
has notify.yaml "name: APNS_KEY_ID, value: \"$nkey\" }"

# No environment variable reaches the output: the same render with the placeholders' names set.
IMAGE=x DATABASE_URL=y PUSH_AUDIENCE=z LOG_LEVEL=debug PRIVACY_POLICY_VERSION=1999-01-01 BETA_GATE=off XFF_TRUSTED_HOPS=7 \
  APP_ATTEST=on APP_ATTEST_EXEMPT_USERS="$(uid 9)" render "${R[@]}" --out "$tmp/again" "${D[@]}" >/dev/null 2>&1 || fail "the second render failed"
diff -r "$out" "$tmp/again" >/dev/null || fail "the environment changed the output: $(diff -r "$out" "$tmp/again")"

# The OTLP pair went with Grafana Cloud (D236): its keys are unknown in config.env, its placeholders
# outside the list.
fresh
printf 'OTLP_ENDPOINT=\n' >> "$cfg"
bad "a retired OTLP key" "OTLP_ENDPOINT is not a key of deploy/run/config.env" "${R[@]}" --out "$out" "${D[@]}"
printf '# ${OTLP_USER}\n' >> "$tpl/ingest.yaml"
bad "a retired OTLP placeholder" "placeholder(s) outside the fixed list: OTLP_USER" "${R[@]}" --out "$out" "${D[@]}"

# FILL_ME_IN: --lint passes and names it, a render fails.
setcfg PUSH_SA_ID FILL_ME_IN
setcfg APNS_KEY_ID FILL_ME_IN
o=$(render "${R[@]}" --lint 2>&1) || fail "--lint fails on FILL_ME_IN: $o"
case $o in *"FILL_ME_IN left (fill before the first deploy): PUSH_SA_ID, APNS_KEY_ID"*) ;; *) fail "--lint does not name what is left: $o" ;; esac
bad "a render with FILL_ME_IN" "PUSH_SA_ID is still FILL_ME_IN" "${R[@]}" --out "$out" "${D[@]}"

# config.env: shapes, keys.
setcfg LOG_LEVEL verbose
bad "a wrong shape" "LOG_LEVEL='verbose' does not have the shape" "${R[@]}" --out "$out" "${D[@]}"
setcfg LOG_LEVEL verbose
bad "a wrong shape, linted" "LOG_LEVEL='verbose' does not have the shape" "${R[@]}" --lint
setcfg LOG_LEVEL '"info"'
bad "a quoted value" "does not have the shape" "${R[@]}" --out "$out" "${D[@]}"
setcfg LOG_LEVEL 'info # the default'
bad "an inline comment" "does not have the shape" "${R[@]}" --out "$out" "${D[@]}"
printf 'LOG_LEVEL = info\n' >> "$cfg"
bad "spaces around =" "not KEY=value" "${R[@]}" --out "$out" "${D[@]}"
# BETA_GATE (D238): off, warn or on as committed, never left to the services' default.
for m in off on; do
  setcfg BETA_GATE "$m"
  o=$(render "${R[@]}" --out "$out" "${D[@]}" 2>&1) || fail "BETA_GATE=$m does not render: $o"
  has api.yaml "name: BETA_GATE, value: \"$m\" }"
  has ingest.yaml "name: BETA_GATE, value: \"$m\" }"
  rm -rf "$out"
  fresh
done
setcfg BETA_GATE enforce
bad "a gate mode outside off|warn|on" "BETA_GATE='enforce' does not have the shape off, warn or on" "${R[@]}" --out "$out" "${D[@]}"
setcfg BETA_GATE Warn
bad "a gate mode in another case" "BETA_GATE='Warn' does not have the shape" "${R[@]}" --out "$out" "${D[@]}"
setcfg BETA_GATE ''
bad "an empty gate mode" "BETA_GATE is empty" "${R[@]}" --out "$out" "${D[@]}"
setcfg BETA_GATE -
bad "no gate mode" "BETA_GATE is missing" "${R[@]}" --out "$out" "${D[@]}"
setcfg BETA_GATE -
bad "no gate mode, linted" "BETA_GATE is missing" "${R[@]}" --lint
# XFF_TRUSTED_HOPS (D240): 1 to 8 as committed, one digit, never left to the services' default.
for n in 2 8; do
  setcfg XFF_TRUSTED_HOPS "$n"
  o=$(render "${R[@]}" --out "$out" "${D[@]}" 2>&1) || fail "XFF_TRUSTED_HOPS=$n does not render: $o"
  has api.yaml "name: XFF_TRUSTED_HOPS, value: \"$n\" }"
  has ingest.yaml "name: XFF_TRUSTED_HOPS, value: \"$n\" }"
  rm -rf "$out"
  fresh
done
for n in 0 9 02 10 -1 ' 1' one; do
  setcfg XFF_TRUSTED_HOPS "$n"
  bad "a hop count of '$n'" "XFF_TRUSTED_HOPS='$n' does not have the shape 1 to 8" "${R[@]}" --out "$out" "${D[@]}"
done
setcfg XFF_TRUSTED_HOPS 9
bad "a hop count of 9, linted" "XFF_TRUSTED_HOPS='9' does not have the shape" "${R[@]}" --lint
setcfg XFF_TRUSTED_HOPS ''
bad "an empty hop count" "XFF_TRUSTED_HOPS is empty" "${R[@]}" --out "$out" "${D[@]}"
setcfg XFF_TRUSTED_HOPS -
bad "no hop count" "XFF_TRUSTED_HOPS is missing" "${R[@]}" --out "$out" "${D[@]}"
setcfg XFF_TRUSTED_HOPS -
bad "no hop count, linted" "XFF_TRUSTED_HOPS is missing" "${R[@]}" --lint
# APP_ATTEST (D245, D247): off, warn or on as committed, like the beta gate.
for m in off on; do
  setcfg APP_ATTEST "$m"
  o=$(render "${R[@]}" --out "$out" "${D[@]}" 2>&1) || fail "APP_ATTEST=$m does not render: $o"
  has api.yaml "name: APP_ATTEST, value: \"$m\" }"
  has ingest.yaml "name: APP_ATTEST, value: \"$m\" }"
  rm -rf "$out"
  fresh
done
for m in enforce On WARN 1 ' off'; do
  setcfg APP_ATTEST "$m"
  bad "an App Attest mode of '$m'" "APP_ATTEST='$m' does not have the shape off, warn or on" "${R[@]}" --out "$out" "${D[@]}"
done
setcfg APP_ATTEST ''
bad "an empty App Attest mode" "APP_ATTEST is empty" "${R[@]}" --out "$out" "${D[@]}"
setcfg APP_ATTEST FILL_ME_IN
bad "App Attest's mode left FILL_ME_IN" "APP_ATTEST is still FILL_ME_IN" "${R[@]}" --out "$out" "${D[@]}"
setcfg APP_ATTEST -
bad "no App Attest mode" "APP_ATTEST is missing" "${R[@]}" --out "$out" "${D[@]}"
setcfg APP_ATTEST -
bad "no App Attest mode, linted" "APP_ATTEST is missing" "${R[@]}" --lint
# APP_ATTEST_EXEMPT_USERS: the line is required, its value empty or 1 to 5 lower-case UUIDs, each once.
setcfg APP_ATTEST_EXEMPT_USERS ''
o=$(render "${R[@]}" --out "$out" "${D[@]}" 2>&1) || fail "an empty exempt list does not render: $o"
has api.yaml 'name: APP_ATTEST_EXEMPT_USERS, value: "" }'
has ingest.yaml 'name: APP_ATTEST_EXEMPT_USERS, value: "" }'
rm -rf "$out"; fresh
five="$(uid 1),$(uid 2),$(uid 3),$(uid 4),$(uid 5)"
setcfg APP_ATTEST_EXEMPT_USERS "$five"
o=$(render "${R[@]}" --out "$out" "${D[@]}" 2>&1) || fail "five exempt accounts do not render: $o"
has ingest.yaml "name: APP_ATTEST_EXEMPT_USERS, value: \"$five\" }"
rm -rf "$out"; fresh
upper=$(uid 10 | tr a-f A-F)
for v in "$five,$(uid 6)" "$upper" "$(uid 1)," ",$(uid 1)" "$(uid 1), $(uid 2)" "$(uid 1);$(uid 2)" "{$(uid 1)}" \
    "$(uid 1 | tr -d -)" "${five:0:35}" all; do
  setcfg APP_ATTEST_EXEMPT_USERS "$v"
  bad "an exempt list '$v'" "APP_ATTEST_EXEMPT_USERS='$v' does not have the shape empty, or 1 to 5 lower-case UUIDs" \
    "${R[@]}" --out "$out" "${D[@]}"
done
setcfg APP_ATTEST_EXEMPT_USERS "$(uid 3),$(uid 4),$(uid 3)"
bad "an exempt account twice" "APP_ATTEST_EXEMPT_USERS names an account twice" "${R[@]}" --out "$out" "${D[@]}"
setcfg APP_ATTEST_EXEMPT_USERS "$(uid 3),$(uid 3)"
bad "an exempt account twice, linted" "APP_ATTEST_EXEMPT_USERS names an account twice" "${R[@]}" --lint
setcfg APP_ATTEST_EXEMPT_USERS FILL_ME_IN
bad "the exempt list left FILL_ME_IN" "APP_ATTEST_EXEMPT_USERS is FILL_ME_IN: leave it empty or set it" "${R[@]}" --lint
setcfg APP_ATTEST_EXEMPT_USERS -
bad "no exempt line" "APP_ATTEST_EXEMPT_USERS is missing" "${R[@]}" --out "$out" "${D[@]}"
setcfg PRIVACY_POLICY_VERSION 2026-02-30
bad "no calendar date" "PRIVACY_POLICY_VERSION=2026-02-30 is not a calendar date" "${R[@]}" --out "$out" "${D[@]}"
setcfg GCP_PROJECT_NUMBER "0$pn"
bad "a project number with a leading zero" "GCP_PROJECT_NUMBER=" "${R[@]}" --out "$out" "${D[@]}"
setcfg SUPABASE_POOLER_HOST db.example.com
bad "another database host" "SUPABASE_POOLER_HOST='db.example.com' does not have the shape" "${R[@]}" --out "$out" "${D[@]}"
printf 'PORT=8080\n' >> "$cfg"
bad "an unknown key" "PORT is not a key of deploy/run/config.env" "${R[@]}" --out "$out" "${D[@]}"
printf 'LOG_LEVEL=info\n' >> "$cfg"
bad "a key twice" "LOG_LEVEL is set twice" "${R[@]}" --out "$out" "${D[@]}"
setcfg APNS_KEY_ID -
bad "a missing key" "APNS_KEY_ID is missing" "${R[@]}" --out "$out" "${D[@]}"
setcfg APPLE_KEY_ID ''
bad "an empty required key" "APPLE_KEY_ID is empty" "${R[@]}" --out "$out" "${D[@]}"
bad "no config.env" "cannot read $tmp/nope" --config "$tmp/nope" --templates "$tpl" --out "$out" "${D[@]}"

# Digests: all five, each once, sha256 and 64 lower-case hex.
bad "a missing digest" "no --digest for migrate" "${R[@]}" --out "$out" "${D[@]:0:8}"
bad "a digest twice" "--digest api given twice" "${R[@]}" --out "$out" "${D[@]}" --digest "api=$(dg f)"
bad "an image that is not deployed" "want <api|ingest|scoring|notify|migrate>=sha256:<64 hex>" "${R[@]}" --out "$out" "${D[@]}" --digest "web=$(dg a)"
bad "upper-case hex" "is not sha256:<64 lower-case hex>" "${R[@]}" --out "$out" "${D[@]:2}" --digest "api=$(dg A)"
bad "a short digest" "is not sha256:<64 lower-case hex>" "${R[@]}" --out "$out" "${D[@]:2}" --digest "api=sha256:abc"
bad "a tag for a digest" "is not sha256:<64 lower-case hex>" "${R[@]}" --out "$out" "${D[@]:2}" --digest "api=latest"

# Templates: only the fixed placeholders, no other `$`, migrate without an audience, all five present.
printf '# costs $5 a month\n' >> "$tpl/api.yaml"
bad "a stray \$" "a '\$' that is not a \${NAME} placeholder" "${R[@]}" --out "$out" "${D[@]}"
printf '# $HOME\n' >> "$tpl/notify.yaml"
bad "a stray \$, linted" "a '\$' that is not a \${NAME} placeholder" "${R[@]}" --lint
printf '# ${PORT}\n' >> "$tpl/scoring.yaml"
bad "a placeholder outside the list" "placeholder(s) outside the fixed list: PORT" "${R[@]}" --out "$out" "${D[@]}"
printf '# ${PUSH_AUDIENCE}\n' >> "$tpl/migrate.yaml"
bad "migrate with an audience" "migrate is no push target" "${R[@]}" --out "$out" "${D[@]}"
rm "$tpl/ingest.yaml"
bad "a missing template" "cannot read $tpl/ingest.yaml" "${R[@]}" --out "$out" "${D[@]}"

# Usage: exactly one of --lint and --out, no digest with --lint, no unknown option.
usage
usage "${R[@]}" "${D[@]}"
usage "${R[@]}" --lint --out "$out"
usage "${R[@]}" --lint "${D[@]}"
usage "${R[@]}" --out "$out" "${D[@]}" --force
[ ! -e "$out" ] || fail "a usage error wrote $out"

echo "✓ cloudrun_render_test: the derived values render by digest, BETA_GATE, XFF_TRUSTED_HOPS, APP_ATTEST and APP_ATTEST_EXEMPT_USERS only into api and ingest, the environment changes nothing, FILL_ME_IN passes --lint and fails a render, and every wrong shape, key, digest, placeholder and \$ fails closed"
