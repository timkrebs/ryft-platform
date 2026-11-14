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
user=$(printf '9%05d' 0)
printf -v s '%20s' ''; ref=${s// /q}
printf -v s '%10s' ''; akey=${s// /A}; nkey=${s// /N}
host=aws-0-eu-central-1.pooler.supabase.com
otlp=https://otlp-gateway-prod-eu-west-2.grafana.net/otlp
policy=2026-10-26
dg() { local s; printf -v s '%64s' ''; printf 'sha256:%s' "${s// /$1}"; }  # 64 times one hex digit
D=(--digest "api=$(dg a)" --digest "ingest=$(dg b)" --digest "scoring=$(dg c)" --digest "notify=$(dg d)" --digest "migrate=$(dg e)")
cfg=$tmp/config.env
tpl=$tmp/tpl
out=$tmp/out
fresh() {
  printf '%s\n' "# a fake, of the right shape" "PRIVACY_POLICY_VERSION=$policy" LOG_LEVEL=info "GCP_PROJECT_NUMBER=$pn" \
    "PUSH_SA_ID=$said" "SUPABASE_PROJECT_REF=$ref" "SUPABASE_POOLER_HOST=$host" "APPLE_KEY_ID=$akey" \
    "APNS_KEY_ID=$nkey" "OTLP_ENDPOINT=$otlp" "OTLP_USER=$user" > "$cfg"
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
  has "$s.yaml" "name: OTLP_ENDPOINT, value: \"$otlp\" }"
  has "$s.yaml" "name: OTLP_USER, value: \"$user\" }"
done
has migrate.yaml "name: DATABASE_URL, value: \"postgresql://postgres.$ref@$host:5432/postgres?sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt\" }"
if grep -q 'PUSH_AUDIENCE' "$out/migrate.yaml"; then fail "migrate.yaml has a PUSH_AUDIENCE"; fi
for s in api ingest; do
  has "$s.yaml" "name: PRIVACY_POLICY_VERSION, value: \"$policy\" }"
  has "$s.yaml" "name: SUPABASE_PROJECT_REF, value: \"$ref\" }"
done
has api.yaml "name: APPLE_KEY_ID, value: \"$akey\" }"
has notify.yaml "name: APNS_KEY_ID, value: \"$nkey\" }"

# No environment variable reaches the output: the same render with the placeholders' names set.
IMAGE=x DATABASE_URL=y PUSH_AUDIENCE=z LOG_LEVEL=debug PRIVACY_POLICY_VERSION=1999-01-01 OTLP_USER=1 \
  render "${R[@]}" --out "$tmp/again" "${D[@]}" >/dev/null 2>&1 || fail "the second render failed"
diff -r "$out" "$tmp/again" >/dev/null || fail "the environment changed the output: $(diff -r "$out" "$tmp/again")"

# Both OTLP values empty renders (the services push no metrics); one of them alone does not.
fresh
setcfg OTLP_ENDPOINT ''
setcfg OTLP_USER ''
render "${R[@]}" --out "$out" "${D[@]}" >/dev/null 2>&1 || fail "a render without OTLP failed"
has scoring.yaml 'name: OTLP_ENDPOINT, value: "" }'
fresh
setcfg OTLP_USER ''
bad "half the OTLP pair" "OTLP_ENDPOINT and OTLP_USER are set together or both left empty" "${R[@]}" --out "$out" "${D[@]}"
setcfg OTLP_USER FILL_ME_IN
bad "an optional FILL_ME_IN" "OTLP_USER is FILL_ME_IN: leave it empty or set it" "${R[@]}" --out "$out" "${D[@]}"

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
setcfg PRIVACY_POLICY_VERSION 2026-02-30
bad "no calendar date" "PRIVACY_POLICY_VERSION=2026-02-30 is not a calendar date" "${R[@]}" --out "$out" "${D[@]}"
setcfg GCP_PROJECT_NUMBER "0$pn"
bad "a project number with a leading zero" "GCP_PROJECT_NUMBER=" "${R[@]}" --out "$out" "${D[@]}"
setcfg SUPABASE_POOLER_HOST db.example.com
bad "another database host" "SUPABASE_POOLER_HOST='db.example.com' does not have the shape" "${R[@]}" --out "$out" "${D[@]}"
setcfg OTLP_ENDPOINT http://localhost:4318
bad "another OTLP endpoint" "OTLP_ENDPOINT='http://localhost:4318' does not have the shape" "${R[@]}" --out "$out" "${D[@]}"
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

echo "✓ cloudrun_render_test: the derived values render by digest, the environment changes nothing, FILL_ME_IN passes --lint and fails a render, and every wrong shape, key, digest, placeholder and \$ fails closed"
