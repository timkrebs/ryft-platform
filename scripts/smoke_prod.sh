#!/usr/bin/env bash
# The production smoke after a deploy (docs/OPERATIONS.md → GCP → Deploy, roll back, rotate):
# the edge answers for api and ingest, the dedicated smoke user signs in, reads
# /v1/me, sends one tiny sync and reads the day back once scoring has scored it
# with the current model. Changes nothing but that user's step count for today.
#
#   SUPABASE_URL=https://<ref>.supabase.co SUPABASE_ANON_KEY=... \
#   SMOKE_EMAIL=smoke@ryftlab.de SMOKE_PASSWORD=... make smoke-prod
# Alternatively supply a short-lived SMOKE_TOKEN and the dedicated SMOKE_USER_ID.
# deploy.yml uses --auth-preflight and a private SMOKE_AUTH_FILE across rollout.
# With ADMIN_API_KEY, a smoke user without beta access gets a one-use invite code
# (once; it keeps the access), so the smoke also passes with BETA_GATE on. The key comes
# from the environment only (scripts/lib/admin.sh says how to read production's).
#
# It then reports what the gates do (on, warn or off): it withdraws the smoke user's
# consent for one sync and, with ADMIN_API_KEY, its beta access for one read, and
# restores both. SMOKE_EXPECT_GATES=on fails unless both are on (EKS, before the first
# external invite: docs/OPERATIONS.md → Beta). On GCP the invite gate is off (D241).
set +x # the session material lives only in memory and private temporary files
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
API=${API_URL:-https://api.ryftlab.de}
INGEST=${INGEST_URL:-https://ingest.ryftlab.de}   # its own host on Cloud Run (design §3: HTTP/2 end to end for 32 MiB syncs)
fail() { echo "✗ smoke-prod: $*" >&2; exit 1; }
if [ "${1:-}" = --auth-preflight ] && [ "$#" -eq 1 ]; then
  [ -n "${SMOKE_AUTH_FILE:-}" ] || fail 'set SMOKE_AUTH_FILE for the preflight session'
  python3 -I "$HERE/lib/smoke_auth.py" prepare "$API" "$INGEST" "$SMOKE_AUTH_FILE"
  echo '✓ smoke-prod auth preflight: verified dedicated session'
  exit 0
fi
[ "$#" -eq 0 ] || fail 'usage: smoke_prod.sh [--auth-preflight]'

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
python3 -I "$HERE/lib/smoke_auth.py" run "$API" "$INGEST" "$tmp/verified.json"
TOKEN=$(json 'd["token"]' < "$tmp/verified.json")
me=$(json 'json.dumps(d["me"])' < "$tmp/verified.json")
rm -f "$tmp/verified.json"
bearer() {
  local token=$1
  shift
  printf 'Authorization: Bearer %s\n' "$token" | curl -q -H @- "$@"
}
call() { bearer "$TOKEN" -fsS -H 'Content-Type: application/json' "$@"; }
# The admin key's other sources (the Vault root's Terraform output, the in-cluster Vault) went with
# D3 (D246): only the environment is left, so there is nothing to look up.
[ -n "${ADMIN_API_KEY:-}" ] || echo "! no ADMIN_API_KEY in the environment (scripts/lib/admin.sh): the beta gate is not probed"

code=$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/me")
[ "$code" = 401 ] || fail "GET $API/v1/me without a token: $code, want 401 (api behind the edge)"

code=$(bearer "$TOKEN" -s -o /dev/null -w '%{http_code}' "$INGEST/v1/sync")
[ "$code" = 404 ] || fail "GET $INGEST/v1/sync: $code, want ingest's own 404 (only POST exists)"

if [ "$(echo "$me" | json 'd["beta_access"]')" != True ]; then
  if [ -n "${ADMIN_API_KEY:-}" ]; then
    code=$(bearer "$ADMIN_API_KEY" -fsS -X POST "$API/v1/admin/invites" -H 'Content-Type: application/json' \
      -d '{"note": "smoke-prod", "max_uses": 1}' | json 'd["code"]') || fail "create an invite code (ADMIN_API_KEY)"
    call -X POST "$API/v1/me/invite" -d "{\"code\": \"$code\"}" || fail "redeem the invite code"
    echo "→ smoke user: beta access with a one-use invite code"
  elif [ "$(bearer "$TOKEN" -s -o /dev/null -w '%{http_code}' "$API/v1/days")" = 403 ]; then
    fail "BETA_GATE is on and the smoke user has no access: run again with ADMIN_API_KEY (the Secret Manager secret admin-api-key: scripts/lib/admin.sh)"
  else
    echo "! the smoke user has no beta access: fine while BETA_GATE is off; run once with ADMIN_API_KEY before turning it on"
  fi
fi
policy=$(call "$API/v1/config" | json 'd["privacy_policy_version"]') || fail "GET /v1/config"
if [ "$(echo "$me" | json '(d["consent"] or {}).get("policy_version")')" != "$policy" ]; then
  call -X POST "$API/v1/me/consent" -d "{\"policy_version\": \"$policy\", \"app_version\": \"smoke-prod\"}" \
    || fail "POST /v1/me/consent"
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
  bearer "$TOKEN" -s -o "$tmp/body" -D "$tmp/hdrs" -w '%{http_code}' -X "$method" \
    -H 'Content-Type: application/json' ${data:+-d "$data"} "$url" > "$tmp/status"
  mode_of "$tmp/status" "$tmp/hdrs" "$tmp/body" "$code"
}
call -X DELETE "$API/v1/me/consent" >/dev/null || fail "withdraw the smoke user's consent (gate probe)"
consent_mode=$(probe POST "$INGEST/v1/sync" consent_required '{"device_id": "5e0c0a5e-0000-4000-8000-0000000000aa", "samples": [], "sleep": [], "workouts": []}')
call -X POST "$API/v1/me/consent" -d "{\"policy_version\": \"$policy\", \"app_version\": \"smoke-prod\"}" >/dev/null \
  || fail "consent again after the probe (the smoke user is left without consent)"
invite_mode="unknown (no ADMIN_API_KEY)"
if [ -n "${ADMIN_API_KEY:-}" ]; then
  me_id=$(echo "$me" | json 'd["id"]')
  admin() { bearer "$ADMIN_API_KEY" -fsS -X "$1" "$API/v1/admin/users/$me_id/access"; }
  admin DELETE >/dev/null || fail "take the smoke user's beta access (gate probe)"
  invite_mode=$(probe GET "$API/v1/days" invite_required)
  admin POST >/dev/null || fail "give the smoke user its beta access back (it is left without)"
fi
echo "✓ smoke-prod: edge, sign-in, /v1/me, consent $policy, a sync, $today scored with model $model; gates: invite $invite_mode, consent $consent_mode"
if [ "${SMOKE_EXPECT_GATES:-}" = on ] && { [ "$invite_mode" != on ] || [ "$consent_mode" != on ]; }; then
  fail "SMOKE_EXPECT_GATES=on, but the gates are: invite $invite_mode, consent $consent_mode"
fi
