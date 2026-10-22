#!/usr/bin/env bash
# Synthetic Monitoring checks as code (deploy/grafana/synthetics/ryftapp.json).
#   scripts/synthetics.sh check     validate the file offline (no network, no credentials)
#   scripts/synthetics.sh probes    list the probe names of the stack (SM_API_URL, SM_ACCESS_TOKEN)
#   scripts/synthetics.sh apply     upsert the checks by job name (same env); SYNTHETICS_DRY_RUN=1
#                                   reads the stack and prints what apply would do, writes nothing
# Env (apply, probes):
#   SM_API_URL        the Probe API server URL of the stack's region, e.g.
#                     https://synthetic-monitoring-api-eu-west-3.grafana.net (docs/OPERATIONS.md -> Monitoring)
#   SM_ACCESS_TOKEN   a Synthetic Monitoring access token (Synthetics -> Config -> Access tokens);
#                     it is not a Grafana Cloud access policy token
#   SYNTHETICS_FILE   another definition file (default: deploy/grafana/synthetics/ryftapp.json)
# API: /api/v1/check/{list,add,update}, /api/v1/probe/list, Bearer auth, as in
# grafana/synthetic-monitoring-api-go-client (smapi.go); check fields as in
# grafana/synthetic-monitoring-agent pkg/pb/synthetic_monitoring/checks.proto.
# Checks that are in the stack but not in the file are reported, never deleted.
set -euo pipefail
cd "$(dirname "$0")/.."

file=${SYNTHETICS_FILE:-deploy/grafana/synthetics/ryftapp.json}
cmd=${1:-}
free_executions=${SYNTHETICS_FREE_EXECUTIONS:-100000}   # API test executions a month on the free tier (grafana.com/pricing)

die() { echo "synthetics: $*" >&2; exit 1; }
command -v jq >/dev/null || die "jq is required"
command -v curl >/dev/null || die "curl is required"

# ---- offline validation ------------------------------------------------------------
validate() {
  [ -f "$file" ] || die "$file not found"
  jq -e . "$file" >/dev/null 2>&1 || die "$file is not valid JSON"
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
        ( if ((.job // "") | test("^ryft-site-[a-z0-9-]+$") | not) then err("job must match ^ryft-site-[a-z0-9-]+$ (the alert rules select job=~\"ryft-site-.*\")") else empty end ),
        ( if ((.target // "") | test("^https://[a-z0-9.-]+(/[^ ]*)?$") | not) then err("target must be an https URL") else empty end ),
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
            | if (type != "string") or ((try test("") catch "bad") == "bad") or (. as $re | (try ("" | test($re)) catch "bad") == "bad")
              then $c | err("a regexp is not a string or does not compile") else empty end )
    )
  ' "$file" 2>&1) || die "jq failed on $file: $errs"
  [ -z "$errs" ] || { printf '%s\n' "$errs" | sed 's/^/synthetics: /' >&2; exit 1; }

  # the execution budget: one execution per probe and run (a run under a minute), 30 days
  local runs
  runs=$(jq '(.probes | length) as $p | [.checks[] | select(.enabled) | ((2592000000 / .frequency | floor) * $p)] | add' "$file")
  echo "ok: $(jq '.checks | length' "$file") checks, $(jq '.probes | length' "$file") probes, about $runs executions in 30 days (free tier: $free_executions)"
  [ "$runs" -le "$free_executions" ] || die "over the free tier ($free_executions executions a month): fewer probes, checks or a longer frequency"
}

# ---- the API -----------------------------------------------------------------------
api_env() {
  : "${SM_API_URL:?the Probe API server URL of the stack region, e.g. https://synthetic-monitoring-api-eu-west-3.grafana.net}"
  : "${SM_ACCESS_TOKEN:?a Synthetic Monitoring access token (Synthetics -> Config -> Access tokens)}"
  case "$SM_API_URL" in https://*|http://127.0.0.1*|http://localhost*) ;; *) die "SM_API_URL must start with https://" ;; esac
  base=${SM_API_URL%/}/api/v1
}

# api METHOD PATH [JSON-BODY]: prints the response body; fails on a non-2xx status without echoing the token
api() {
  local method=$1 path=$2 body=${3:-} out status resp
  out=$(mktemp)
  # the header goes through a config on stdin, not the command line (ps)
  status=$(printf 'header = "Authorization: Bearer %s"\n' "$SM_ACCESS_TOKEN" |
    curl -sS -K - -o "$out" -w '%{http_code}' -X "$method" -H 'Content-Type: application/json' \
      ${body:+--data-binary "$body"} "$base$path") || { rm -f "$out"; die "$method $path: request failed"; }
  resp=$(cat "$out"); rm -f "$out"
  case "$status" in
    2??) printf '%s\n' "$resp" ;;
    401|403) die "$method $path: $status, the access token is wrong or for another stack" ;;
    *) die "$method $path: HTTP $status: ${resp:0:300}" ;;
  esac
}

probes_json=
load_probes() { probes_json=$(api GET /probe/list); }

apply() {
  api_env
  load_probes
  local checks job id tenant payload names action
  checks=$(api GET /check/list)
  # probe names -> ids; a missing name stops the run and lists what the stack offers
  local ids
  ids=$(jq -n --argjson all "$probes_json" --slurpfile f "$file" '
    [$f[0].probes[] as $n | ($all | map(select(.name == $n)) | first // null) | if . == null then error("no probe named " + ($n)) else .id end]' 2>&1) || {
    echo "synthetics: $ids" >&2
    echo "synthetics: the stack offers: $(jq -r 'map(.name) | join(", ")' <<<"$probes_json")" >&2
    exit 1
  }
  names=$(jq -r '.checks[].job' "$file")
  while IFS= read -r job; do
    [ -n "$job" ] || continue
    local n
    n=$(jq --arg j "$job" '[.[] | select(.job == $j)] | length' <<<"$checks")
    [ "$n" -le 1 ] || die "$job: $n checks with this job in the stack; remove the extras in the app"
    payload=$(jq --arg j "$job" --argjson ids "$ids" '.checks[] | select(.job == $j) | .probes = $ids' "$file")
    if [ "$n" = 1 ]; then
      id=$(jq --arg j "$job" '.[] | select(.job == $j) | .id' <<<"$checks")
      tenant=$(jq --arg j "$job" '.[] | select(.job == $j) | .tenantId' <<<"$checks")
      payload=$(jq --argjson id "$id" --argjson t "$tenant" '. + {id: $id, tenantId: $t}' <<<"$payload")
      action="update /check/update"
    else
      action="add /check/add"
    fi
    if [ "${SYNTHETICS_DRY_RUN:-}" = 1 ]; then
      echo "dry run: $job: would ${action%% *} ($(jq -r '.target' <<<"$payload"), every $(jq '.frequency / 1000' <<<"$payload") s, probes $(jq -c . <<<"$ids"))"
    else
      api POST "${action##* }" "$payload" >/dev/null
      echo "applied: $job (${action%% *})"
    fi
  done <<<"$names"
  # not managed here: reported, not deleted
  jq -r --argjson want "$(jq '[.checks[].job]' "$file")" '.[] | select(.job as $j | $want | index($j) | not) | "not in \(env.SYNTHETICS_FILE // "the file"): \(.job) (\(.target)), left alone"' <<<"$checks" || true
}

case "$cmd" in
  check) validate ;;
  probes) api_env; load_probes; jq -r '.[] | "\(.name)\t\(.region)\t\(if .public then "public" else "private" end)\t\(if .online then "online" else "offline" end)"' <<<"$probes_json" | sort ;;
  apply) validate; apply ;;
  *) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2; exit 2 ;;
esac
