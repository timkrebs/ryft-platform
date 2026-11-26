#!/usr/bin/env bash
# The production smoke after a deploy (docs/OPERATIONS.md → GCP → Deploy, roll back, rotate):
# the edge answers for api and ingest, the dedicated smoke user is authenticated as the service
# account ryft-smoke (a Google ID token that api and ingest map to that one user, D286), reads
# /v1/me, sends one tiny sync and reads the day back once scoring has scored it
# with the current model. Changes nothing but that user's step count for today.
#
# From a shell: `gcloud auth login` once (your account needs
# roles/iam.serviceAccountOpenIdTokenCreator on ryft-smoke), then `make smoke-prod`. It reads
# SMOKE_USER_ID and SMOKE_SA_ID from deploy/run/config.env (or the environment), mints the token
# through iamcredentials and checks its claims before the first API call. In deploy.yml the
# runner's ryft-deployer does the same: no secret, nothing to rotate.
# Alternatively supply a short-lived SMOKE_TOKEN, a Google token minted by hand with the same helper
# (same checks, only the token written, to a new 0600 file):
#   python3 -I scripts/lib/smoke_auth.py mint "$f"   # $f: a path that does not exist yet
#   SMOKE_TOKEN=$(cat "$f") make smoke-prod; rm -f "$f"
# (or a Supabase access JWT whose sub is SMOKE_USER_ID). Against a loopback stack SMOKE_TOKEN is the
# only way in. A Google smoke token is minted and sent only to https://api.ryftlab.de and
# https://ingest.ryftlab.de (the helper refuses any other origin, even with a SMOKE_TOKEN of Google's);
# another remote target needs a SMOKE_TOKEN that is a Supabase access JWT of SMOKE_USER_ID.
# deploy.yml runs --auth-preflight before the rollout (or --auth-bootstrap, the one deploy against a
# revision before D286, which must see 401) and a plain `make smoke-prod` after it, minting fresh.
# With ADMIN_API_KEY, a smoke user without beta access gets a one-use invite code
# (once; it keeps the access), so the smoke also passes with BETA_GATE on. The key comes
# from the environment only (scripts/lib/admin.sh says how to read production's).
#
# The smoke token writes no consent (D286: consent is a person's act, and the api refuses POST and DELETE
# /v1/me/consent for it). The smoke account's consent is recorded by the operator with
# scripts/sql/consent_smoke_account.sql (docs/OPERATIONS.md). Without any, the smoke and --auth-preflight fail
# with that hint (the preflight before the migration); a consent for another version than the one in force warns.
#
# It then reports what the gates do (on, warn or off): with ADMIN_API_KEY it withdraws the smoke user's beta
# access for one read and restores it (the invite gate). The consent gate is reported as not probed.
# SMOKE_EXPECT_GATES=on fails unless the invite gate is on (EKS, before the first external invite:
# docs/OPERATIONS.md → Beta). On GCP the invite gate is off (D241).
set +x # the session material lives only in memory and private temporary files
set -euo pipefail
# An overall deadline (SMOKE_PROD_TIMEOUT seconds, 600 by default): the script runs again under `timeout`, once (the marker
# SMOKE_PROD_DEADLINE says so). Every curl has its own limits too (--connect-timeout, --max-time). Without `timeout`
# (macOS without coreutils) it runs unbounded, with a notice.
if [ -z "${SMOKE_PROD_DEADLINE:-}" ]; then
  if command -v timeout >/dev/null 2>&1; then
    export SMOKE_PROD_DEADLINE=armed
    # At a terminal, --foreground keeps the script in the terminal's process group, so Ctrl-C stops it too.
    fg=()
    if [ -t 0 ]; then fg=(--foreground); fi
    exec timeout ${fg[@]+"${fg[@]}"} -k 10 "${SMOKE_PROD_TIMEOUT:-600}" "${BASH:-bash}" "$0" "$@"
  fi
  echo "! smoke-prod: no \`timeout\` on this machine: running without an overall deadline (brew install coreutils)" >&2
fi
HERE=$(cd "$(dirname "$0")" && pwd)
API=${API_URL:-https://api.ryftlab.de}
INGEST=${INGEST_URL:-https://ingest.ryftlab.de}   # its own host on Cloud Run (design §3: HTTP/2 end to end for 32 MiB syncs)
fail() { echo "✗ smoke-prod: $*" >&2; exit 1; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# --auth-preflight: mint, check the claims, require GET /v1/me to return the smoke account (the new api
#   accepts the token). Nothing is kept: the run after the rollout mints its own fresh token. A private
#   SMOKE_AUTH_FILE, if set, still receives the session (compatibility; `run` reads it only when set).
# --auth-bootstrap: only against a revision before D286 (the one deploy that has to precede the preflight):
#   mint (the IAM binding works), then require GET /v1/me to answer 401. Writes no session.
case "${1:-}" in
  --auth-preflight)
    [ "$#" -eq 1 ] || fail 'usage: smoke_prod.sh [--auth-preflight | --auth-bootstrap]'
    python3 -I "$HERE/lib/smoke_auth.py" prepare "$API" "$INGEST" "${SMOKE_AUTH_FILE:-$tmp/preflight.json}"
    echo '✓ smoke-prod auth preflight: the smoke token is accepted as the smoke account'
    exit 0 ;;
  --auth-bootstrap)
    [ "$#" -eq 1 ] || fail 'usage: smoke_prod.sh [--auth-preflight | --auth-bootstrap]'
    python3 -I "$HERE/lib/smoke_auth.py" bootstrap "$API" "$INGEST"
    exit 0 ;;
esac
[ "$#" -eq 0 ] || fail 'usage: smoke_prod.sh [--auth-preflight | --auth-bootstrap]'

json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
python3 -I "$HERE/lib/smoke_auth.py" run "$API" "$INGEST" "$tmp/verified.json"
TOKEN=$(json 'd["token"]' < "$tmp/verified.json")
me=$(json 'json.dumps(d["me"])' < "$tmp/verified.json")
# The checked, normalised origins (https://API.ryftlab.de/ became https://api.ryftlab.de): every request below uses them.
API=$(json 'd["api"]' < "$tmp/verified.json")
INGEST=$(json 'd["ingest"]' < "$tmp/verified.json")
rm -f "$tmp/verified.json"
bearer() {
  local token=$1
  shift
  printf 'Authorization: Bearer %s\n' "$token" | curl -q --connect-timeout 10 --max-time 30 -H @- "$@"
}
# Calls made with the smoke token retry curl's transient statuses (408/429/500/502/503/504; never
# --retry-all-errors): a new instance answers 503 while it loads its keys. Safe for every call below: the GETs,
# POST /v1/sync (D100) and POST /v1/me/invite (a no-op once the account has access); the smoke token makes no
# consent write (D286). The ADMIN_API_KEY calls are not retried.
smoke_curl() { bearer "$TOKEN" --retry 3 --retry-delay 2 --retry-max-time 60 "$@"; }
call() { smoke_curl -fsS -H 'Content-Type: application/json' "$@"; }
# The admin key's other sources (the Vault root's Terraform output, the in-cluster Vault) went with
# D3 (D246): only the environment is left, so there is nothing to look up.
[ -n "${ADMIN_API_KEY:-}" ] || echo "! no ADMIN_API_KEY in the environment (scripts/lib/admin.sh): the beta gate is not probed"

code=$(curl -s --connect-timeout 10 --max-time 30 -o /dev/null -w '%{http_code}' "$API/v1/me")
[ "$code" = 401 ] || fail "GET $API/v1/me without a token: $code, want 401 (api behind the edge)"

code=$(smoke_curl -s -o /dev/null -w '%{http_code}' "$INGEST/v1/sync")
[ "$code" = 404 ] || fail "GET $INGEST/v1/sync: $code, want ingest's own 404 (only POST exists)"

if [ "$(echo "$me" | json 'd["beta_access"]')" != True ]; then
  if [ -n "${ADMIN_API_KEY:-}" ]; then
    code=$(bearer "$ADMIN_API_KEY" -fsS -X POST "$API/v1/admin/invites" -H 'Content-Type: application/json' \
      -d '{"note": "smoke-prod", "max_uses": 1}' | json 'd["code"]') || fail "create an invite code (ADMIN_API_KEY)"
    call -X POST "$API/v1/me/invite" -d "{\"code\": \"$code\"}" || fail "redeem the invite code"
    echo "→ smoke user: beta access with a one-use invite code"
  elif [ "$(smoke_curl -s -o /dev/null -w '%{http_code}' "$API/v1/days")" = 403 ]; then
    fail "BETA_GATE is on and the smoke user has no access: run again with ADMIN_API_KEY (the Secret Manager secret admin-api-key: scripts/lib/admin.sh)"
  else
    echo "! the smoke user has no beta access: fine while BETA_GATE is off; run once with ADMIN_API_KEY before turning it on"
  fi
fi
policy=$(call "$API/v1/config" | json 'd["privacy_policy_version"]') || fail "GET /v1/config"
# The smoke token writes no consent (D286: a person's act, and POST/DELETE /v1/me/consent are off the api's
# smoke allowlist): the operator records it for the bound account with scripts/sql/consent_smoke_account.sql.
# None at all fails (the sync's consent gate needs an unrevoked one); another version only warns: the gate
# accepts any unrevoked consent, and /v1/me shows the newest (one recorded ahead of a version bump, say).
consented=$(echo "$me" | json 'd["consent"]["policy_version"] if isinstance(d.get("consent"), dict) and isinstance(d["consent"].get("policy_version"), str) else ""')
[ -n "$consented" ] || fail "the smoke account has no consent for $policy: record it with scripts/sql/consent_smoke_account.sql (docs/OPERATIONS.md)"
if [ "$consented" != "$policy" ]; then
  echo "! the smoke account's consent is for $consented, the policy in force is $policy: record $policy with scripts/sql/consent_smoke_account.sql (docs/OPERATIONS.md)"
fi
model=$(call "$API/v1/model" | json 'd["current"]') || fail "GET /v1/model"
call "$API/v1/me/connections" | json 'isinstance(d["connections"], list) and isinstance(d["available"], list) and {"garmin", "suunto"} <= {x["provider"] for x in d["providers"]} or sys.exit(1)' >/dev/null \
  || fail "GET /v1/me/connections provider catalog"
echo "→ connection catalog: authenticated response"


tz=$(echo "$me" | json 'd["tz"]')
today=$(TZ="$tz" date +%F)
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
sync=$(call -X POST "$INGEST/v1/sync" -d "{\"device_id\": \"5e0c0a5e-0000-4000-8000-0000000000aa\",
  \"device_name\": \"smoke-prod\", \"app_version\": \"smoke-prod\",
  \"samples\": [{\"type\": \"steps\", \"start\": \"$now\", \"end\": \"$now\", \"value\": 1, \"unit\": \"count\", \"source\": \"smoke-prod\"}],
  \"sleep\": [], \"workouts\": []}") || fail "POST /v1/sync"
echo "$sync" | json '"'"$today"'" in d["days_touched"] or sys.exit(1)' >/dev/null || fail "the sync did not touch $today: $sync"

scored=
for _ in $(seq 1 60); do
  day=$(call "$API/v1/days/$today") || fail "GET /v1/days/$today"
  if echo "$day" | json 'd["score"]["computed_at"] >= "'"${now%Z}"'" or sys.exit(1)' >/dev/null 2>&1; then
    got=$(echo "$day" | json 'd["score"]["model_version"]')
    [ "$got" = "$model" ] || fail "$today scored with model $got, the api serves $model"
    scored=1
    break
  fi
  sleep 1
done
[ -n "$scored" ] || fail "$today was not rescored within 60 s of the sync (scoring, Pub/Sub: docs/OPERATIONS.md → Dead letters and the outbox)"

# ---- the gates: on (403 with the code), warn (served, X-RYFT-Gate), off ------------
mode_of() {   # status file, headers file, body file, code → on|warn|off|unknown
  python3 - "$1" "$2" "$3" "$4" <<'PY'
import json, sys
status, hdrs, body, code = open(sys.argv[1]).read().strip(), open(sys.argv[2]).read().lower(), sys.argv[3], sys.argv[4]
try: got = json.load(open(body)).get("code") if status == "403" else None
except Exception: got = None
if status == "403" and got == code: print("on")
elif status.startswith("2") and ("x-ryft-gate:" in hdrs and code in hdrs.split("x-ryft-gate:", 1)[1].split("\n", 1)[0]): print("warn")
elif status.startswith("2"): print("off")
else: print("unknown")
PY
}
probe() {   # method url code [data] → the gate's mode for that request
  local method=$1 url=$2 code=$3 data=${4:-}
  smoke_curl -s -o "$tmp/body" -D "$tmp/hdrs" -w '%{http_code}' -X "$method" \
    -H 'Content-Type: application/json' ${data:+-d "$data"} "$url" > "$tmp/status"
  mode_of "$tmp/status" "$tmp/hdrs" "$tmp/body" "$code"
}
consent_mode="not probed (D286: the smoke token writes no consent)"
invite_mode="unknown (no ADMIN_API_KEY)"
if [ -n "${ADMIN_API_KEY:-}" ]; then
  me_id=$(echo "$me" | json 'd["id"]')
  admin() { bearer "$ADMIN_API_KEY" -fsS -X "$1" "$API/v1/admin/users/$me_id/access"; }
  admin DELETE >/dev/null || fail "take the smoke user's beta access (gate probe)"
  invite_mode=$(probe GET "$API/v1/days" invite_required)
  admin POST >/dev/null || fail "give the smoke user its beta access back (it is left without)"
fi
echo "✓ smoke-prod: edge, sign-in, /v1/me, consent $policy, a sync, $today scored with model $model; gates: invite $invite_mode, consent $consent_mode"
if [ "${SMOKE_EXPECT_GATES:-}" = on ] && [ "$invite_mode" != on ]; then
  fail "SMOKE_EXPECT_GATES=on, but the invite gate is: $invite_mode (the consent gate is not probed)"
fi
