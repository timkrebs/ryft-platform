#!/usr/bin/env bash
# Synthetic Monitoring checks as code (deploy/grafana/synthetics/ryftapp.json).
#   scripts/synthetics.sh check     validate the file offline (no credentials, no network): structure,
#                                   RE2 regexps, the body markers against the site's own sources,
#                                   the free-tier execution budget
#   scripts/synthetics.sh probes    list the probe names of the stack (SM_API_URL, SM_ACCESS_TOKEN)
#   scripts/synthetics.sh apply     upsert the checks by job name. Everything is read and planned
#                                   before the first write; a check that already matches is left
#                                   alone. SYNTHETICS_DRY_RUN=1 prints the plan as a field diff and
#                                   writes nothing.
# Env (apply, probes):
#   SM_API_URL        the "Backend address" of the stack's region (Synthetic Monitoring docs, Set up
#                     -> Private probes; the app's Config page), e.g.
#                     https://synthetic-monitoring-api-eu-west-2.grafana.net . It is the HTTP API,
#                     not the "API server URL" (gRPC) column. Only *.grafana.net over https.
#   SM_ACCESS_TOKEN   a Synthetic Monitoring access token (Synthetics -> Config -> Access tokens);
#                     not a Grafana Cloud access policy token. It goes to curl on stdin, never argv.
#   SYNTHETICS_FILE   another definition file (relative paths from the current directory)
# API: /api/v1/check/{list,add,update}, /api/v1/probe/list, Bearer auth, as in
# grafana/synthetic-monitoring-api-go-client (smapi.go); check fields as in
# grafana/synthetic-monitoring-agent pkg/pb/synthetic_monitoring/checks.proto.
# Checks that are in the stack but not in the file are reported, never deleted.
# Tests only: SYNTHETICS_TEST_LOCAL=1 allows SM_API_URL=http://127.0.0.1:<port> without TLS.
set -euo pipefail
orig=$PWD
cd "$(dirname "$0")/.."

file=deploy/grafana/synthetics/ryftapp.json
if [ -n "${SYNTHETICS_FILE:-}" ]; then
  case $SYNTHETICS_FILE in /*) file=$SYNTHETICS_FILE ;; *) file=$orig/$SYNTHETICS_FILE ;; esac
fi
cmd=${1:-}
free_executions=${SYNTHETICS_FREE_EXECUTIONS:-100000}   # API test executions a month on the free tier (grafana.com/pricing)

die() { echo "synthetics: $*" >&2; exit 1; }
command -v jq >/dev/null || die "jq is required"
command -v curl >/dev/null || die "curl is required"

# ---- offline validation ------------------------------------------------------------
validate_structure() {
  local errs
  errs=$(jq -r '
    def err(m): "\(.job // "?"): " + m;
    def http: .settings.http;
    ( if (.probes | type) != "array" or (.probes | length) == 0 or (.probes | map(type == "string" and length > 0) | all | not)
      then "probes: a non-empty list of probe names" else empty end ),
    ( if (.probes | type) == "array" and ((.probes | unique | length) != (.probes | length)) then "probes: duplicates" else empty end ),
    ( if (.checks | type) != "array" or (.checks | length) == 0 then "checks: a non-empty list" else empty end ),
    ( if (.checks | type) == "array" and ((.checks | map(.job) | unique | length) != (.checks | length)) then "checks: duplicate job names" else empty end ),
    ( (.checks // [])[] |
        ( . as $c | ((keys - ["job","target","frequency","timeout","enabled","basicMetricsOnly","alertSensitivity","labels","settings"]) | .[])
          | . as $k | $c | err("unknown key " + $k) ),
        ( . as $c | (((http // {}) | keys) - ["method","ipVersion","noFollowRedirects","failIfNotSSL","validStatusCodes","failIfBodyNotMatchesRegexp","failIfHeaderNotMatchesRegexp"])[]
          | . as $k | $c | err("unknown http setting " + $k) ),
        ( if ((.job // "") | test("^ryft-site-[a-z0-9-]+$") | not) then err("job must match ^ryft-site-[a-z0-9-]+$ (the alert rules select job=~\"ryft-site-.*\")") else empty end ),
        ( if ((.target // "") | test("^https://(www\\.)?ryftapp\\.de/[A-Za-z0-9._~/-]*$") | not) then err("target must be https://ryftapp.de/... or https://www.ryftapp.de/...") else empty end ),
        ( if ((.frequency | type) != "number" or .frequency < 60000 or .frequency > 3600000)
            then err("frequency in milliseconds, 60000 to 3600000 (below a minute breaks the execution budget)") else empty end ),
        ( if ((.timeout | type) != "number" or .timeout < 1000 or .timeout > 60000 or ((.frequency | type) == "number" and .timeout > .frequency))
            then err("timeout in milliseconds, 1000 to 60000 and not above frequency") else empty end ),
        ( if (.enabled | type) != "boolean" then err("enabled must be true or false") else empty end ),
        ( if ((.settings // {}) | keys) != ["http"] then err("settings must hold exactly http") else empty end ),
        ( if ((http.validStatusCodes // []) | length) == 0 or ((http.validStatusCodes // []) | map(type == "number" and . >= 100 and . < 600) | all | not)
            then err("validStatusCodes must list HTTP status codes") else empty end ),
        ( if (http.failIfNotSSL // false) | not then err("failIfNotSSL must be true (the certificate is part of the check)") else empty end ),
        ( if (http.noFollowRedirects // false) | not then err("noFollowRedirects must be true (a redirect is a result, not something to follow)") else empty end ),
        ( . as $c | (http.failIfHeaderNotMatchesRegexp // [])[]
            | if ((.header // "") == "" or (.regexp // "") == "") then $c | err("failIfHeaderNotMatchesRegexp needs header and regexp") else empty end ),
        ( . as $c | [(http.failIfBodyNotMatchesRegexp // [])[], ((http.failIfHeaderNotMatchesRegexp // [])[] | .regexp)][]
            | if (type != "string") or (length == 0) or contains("\n") then $c | err("a regexp must be a non-empty one-line string") else empty end )
    )
  ' "$file" 2>&1) || die "jq failed on $file: $errs"
  [ -z "$errs" ] || { printf '%s\n' "$errs" | sed 's/^/synthetics: /' >&2; exit 1; }
}

# Synthetic Monitoring compiles with Go's regexp (RE2), not jq's Oniguruma: ask Go.
validate_regexps() {
  command -v go >/dev/null || die "go is required to check the regexps as RE2 (scripts/synthetics_re2.go)"
  local out
  out=$(jq -r '.checks[] | (.settings.http.failIfBodyNotMatchesRegexp // [])[], ((.settings.http.failIfHeaderNotMatchesRegexp // [])[] | .regexp)' "$file" |
    go run scripts/synthetics_re2.go compile 2>&1) || { printf '%s\n' "$out" | sed 's/^/synthetics: /' >&2; exit 1; }
}

# A body marker that the site's own sources do not contain would page after the deploy:
# fail in the PR instead. The privacy page's source is the newest English policy text (and
# its rendered HTML when web/out exists); the AASA is the file nginx serves.
validate_markers() {
  local job res src files newest
  while IFS= read -r job; do
    [ -n "$job" ] || continue
    case $job in
      ryft-site-aasa) src=(web/public/.well-known/apple-app-site-association) ;;
      ryft-site-privacy)
        files=(web/content/legal/en/privacy/*.mdx)
        newest=${files[${#files[@]}-1]}
        src=("$newest")
        [ ! -f web/out/privacy.html ] || src+=(web/out/privacy.html) ;;
      *) die "$job: has a body regexp but no known source of truth (add it to validate_markers)" ;;
    esac
    res=$(jq -r --arg j "$job" '.checks[] | select(.job == $j) | .settings.http.failIfBodyNotMatchesRegexp[]' "$file")
    local s out
    for s in "${src[@]}"; do
      [ -f "$s" ] || die "$job: $s not found (the marker is checked against it)"
      out=$(printf '%s\n' "$res" | go run scripts/synthetics_re2.go match "$s" 2>&1) ||
        die "$job: $out (changed text or app ID? fix the regexp in the same PR)"
    done
  done < <(jq -r '.checks[] | select((.settings.http.failIfBodyNotMatchesRegexp // []) | length > 0) | .job' "$file")
}

validate() {
  [ -f "$file" ] || die "$file not found"
  jq -e . "$file" >/dev/null 2>&1 || die "$file is not valid JSON"
  validate_structure
  validate_regexps
  validate_markers
  # the execution budget: one execution per probe and run (a run under a minute), 30 days
  local runs enabled
  enabled=$(jq '[.checks[] | select(.enabled)] | length' "$file")
  [ "$enabled" -gt 0 ] || die "every check is disabled: nothing would watch the site"
  runs=$(jq '(.probes | length) as $p | [.checks[] | select(.enabled) | ((2592000000 / .frequency | floor) * $p)] | add' "$file")
  echo "ok: $(jq '.checks | length' "$file") checks ($enabled enabled), $(jq '.probes | length' "$file") probes, about $runs executions in 30 days (free tier: $free_executions)"
  [ "$runs" -le "$free_executions" ] || die "over the free tier ($free_executions executions a month): fewer probes, checks or a longer frequency"
}

# ---- the API -----------------------------------------------------------------------
base=
tmpout=
curl_args=()
cleanup() { [ -z "$tmpout" ] || rm -f "$tmpout"; }
trap cleanup EXIT

api_env() {
  [ -n "${SM_API_URL:-}" ] || die "SM_API_URL: the Backend address of the stack region, e.g. https://synthetic-monitoring-api-eu-west-2.grafana.net"
  [ -n "${SM_ACCESS_TOKEN:-}" ] || die "SM_ACCESS_TOKEN: a Synthetic Monitoring access token (Synthetics -> Config -> Access tokens)"
  local re_prod='^https://synthetic-monitoring-api(-[a-z0-9-]+)?\.grafana\.net/?$' re_test='^http://127\.0\.0\.1:[0-9]+$'
  # -q first: ~/.curlrc (a `verbose`, a proxy) is ignored. The token never reaches argv.
  curl_args=(-q -sS --connect-timeout 10 --max-time 30)
  if [ -n "${SYNTHETICS_TEST_LOCAL:-}" ]; then
    [[ $SM_API_URL =~ $re_test ]] || die "SM_API_URL (test mode) must be http://127.0.0.1:<port>"
  else
    [[ $SM_API_URL =~ $re_prod ]] || die "SM_API_URL must be https://synthetic-monitoring-api-<region>.grafana.net (the Backend address)"
    curl_args+=(--proto '=https')
  fi
  printf '%s' "$SM_ACCESS_TOKEN" | jq -Rse 'test("\\A[^\\s\\p{Cc}\"\\\\]+\\z")' >/dev/null 2>&1 ||
    die "SM_ACCESS_TOKEN holds a quote, a backslash, whitespace or a control character: not a token"
  base=${SM_API_URL%/}/api/v1
  tmpout=$(mktemp)
}

# api METHOD PATH [JSON-BODY]: prints the response body; returns 1 with a message on a non-2xx status
api() {
  local method=$1 path=$2 body=${3:-} status resp
  # the header goes through a curl config on stdin, not the command line (ps)
  status=$(printf 'header = "Authorization: Bearer %s"\n' "$SM_ACCESS_TOKEN" |
    curl "${curl_args[@]}" -K - -o "$tmpout" -w '%{http_code}' -X "$method" -H 'Content-Type: application/json' \
      ${body:+--data-binary "$body"} "$base$path") || { echo "synthetics: $method $path: request failed" >&2; return 1; }
  resp=$(cat "$tmpout")
  case "$status" in
    2??) printf '%s\n' "$resp" ;;
    401|403) echo "synthetics: $method $path: $status, the access token is wrong or for another stack" >&2; return 1 ;;
    *) echo "synthetics: $method $path: HTTP $status: ${resp:0:300}" >&2; return 1 ;;
  esac
}

# fields of the wanted check that differ from the stack's (the stack adds its own fields; those are ignored)
diff_fields() {  # wanted-json existing-json
  jq -n --argjson d "$1" --argjson e "$2" '
    ($d | .probes |= ((. // []) | sort)) as $d | ($e | .probes |= ((. // []) | sort)) as $e
    | [ $d | paths(type != "array" and type != "object") as $p
        | { path: ($p | map(tostring) | join(".")), old: (try ($e | getpath($p)) catch null), new: ($d | getpath($p)) }
        | select(.old != .new and ((.old == null and .new == false) | not)) ]'
}

apply() {
  api_env
  local probes_json checks_json ids job existing payload n changes
  probes_json=$(api GET /probe/list) || exit 1
  checks_json=$(api GET /check/list) || exit 1
  probes_json=$(jq '. // []' <<<"$probes_json")
  checks_json=$(jq '. // []' <<<"$checks_json")

  # ---- plan: every lookup and every payload before the first write ----
  ids=$(jq -c -n --argjson all "$probes_json" --slurpfile f "$file" '
    [ $f[0].probes[] as $n
      | [$all[] | select(.name == $n)] as $m
      | if ($m | length) == 0 then error("no probe named " + $n)
        elif ($m | length) > 1 then error("more than one probe named " + $n)
        elif ($m[0].online | not) then error("probe " + $n + " is offline")
        else $m[0].id end ]' 2>&1) || {
    echo "synthetics: ${ids#*): }" >&2
    echo "synthetics: the stack offers: $(jq -r 'map(.name + (if .online then "" else " (offline)" end)) | join(", ")' <<<"$probes_json")" >&2
    exit 1
  }
  local -a plan_job=() plan_kind=() plan_payload=()
  while IFS= read -r job; do
    n=$(jq --arg j "$job" '[.[] | select(.job == $j)] | length' <<<"$checks_json")
    [ "$n" -le 1 ] || die "$job: $n checks with this job in the stack; remove the extras in the app"
    payload=$(jq --arg j "$job" --argjson ids "$ids" '.checks[] | select(.job == $j) | .probes = $ids' "$file")
    if [ "$n" = 1 ]; then
      existing=$(jq --arg j "$job" '.[] | select(.job == $j)' <<<"$checks_json")
      changes=$(diff_fields "$payload" "$existing")
      if [ "$(jq 'length' <<<"$changes")" = 0 ]; then
        plan_job+=("$job"); plan_kind+=(unchanged); plan_payload+=("")
        continue
      fi
      payload=$(jq --argjson e "$existing" '. + {id: $e.id, tenantId: $e.tenantId}' <<<"$payload")
      plan_job+=("$job"); plan_kind+=(update); plan_payload+=("$payload")
      if [ "${SYNTHETICS_DRY_RUN:-}" = 1 ]; then
        echo "dry run: $job: would update"
        jq -r '.[] | "    \(.path): \(.old | tojson) -> \(.new | tojson)"' <<<"$changes"
      fi
    else
      plan_job+=("$job"); plan_kind+=(add); plan_payload+=("$payload")
      [ "${SYNTHETICS_DRY_RUN:-}" != 1 ] || echo "dry run: $job: would add ($(jq -r '.target' <<<"$payload"), every $(jq '.frequency / 1000' <<<"$payload") s, probes $ids)"
    fi
  done < <(jq -r '.checks[].job' "$file")

  # ---- write ----
  local i done_list=()
  for i in "${!plan_job[@]}"; do
    job=${plan_job[$i]}
    case ${plan_kind[$i]} in
      unchanged) echo "unchanged: $job" ;;
      *)
        [ "${SYNTHETICS_DRY_RUN:-}" != 1 ] || continue
        local ep=/check/add; [ "${plan_kind[$i]}" != update ] || ep=/check/update
        api POST "$ep" "${plan_payload[$i]}" >/dev/null ||
          { echo "synthetics: stopped at $job; applied before it: ${done_list[*]:-nothing}; running apply again continues (it is idempotent)" >&2; exit 1; }
        done_list+=("$job")
        echo "applied: $job (${plan_kind[$i]})" ;;
    esac
  done
  # not managed here: reported, not deleted
  jq -r --argjson want "$(jq '[.checks[].job]' "$file")" '.[] | select(.job as $j | $want | index($j) | not) | "not in the file: \(.job) (\(.target)), left alone"' <<<"$checks_json"
}

case "$cmd" in
  check) validate ;;
  probes)
    api_env
    api GET /probe/list | jq -r '(. // [])[] | "\(.name)\t\(.region)\t\(if .public then "public" else "private" end)\t\(if .online then "online" else "offline" end)"' | sort ;;
  apply) validate; apply ;;
  *) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2; exit 2 ;;
esac
