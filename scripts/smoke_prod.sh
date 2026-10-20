#!/usr/bin/env bash
# The production smoke after a deploy (docs/OPERATIONS.md → Deploy and roll back):
# the edge answers for api and ingest, the dedicated smoke user signs in, reads
# /v1/me, sends one tiny sync and reads the day back once scoring has scored it
# with the current model. Changes nothing but that user's step count for today.
#
#   SUPABASE_URL=https://<ref>.supabase.co SUPABASE_ANON_KEY=... \
#   SMOKE_EMAIL=smoke@ryftapp.de SMOKE_PASSWORD=... make smoke-prod
set -euo pipefail
API=${API_URL:-https://api.ryftapp.de}
INGEST=${INGEST_URL:-$API}
if [ -z "${SMOKE_TOKEN:-}" ]; then   # SMOKE_TOKEN: a bearer instead of the sign-in (the local stack's API key)
  : "${SUPABASE_URL:?set SUPABASE_URL}" "${SUPABASE_ANON_KEY:?set SUPABASE_ANON_KEY}"
  : "${SMOKE_EMAIL:?set SMOKE_EMAIL (the dedicated smoke user)}" "${SMOKE_PASSWORD:?set SMOKE_PASSWORD}"
fi
fail() { echo "✗ smoke-prod: $*" >&2; exit 1; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

code=$(curl -s -o /dev/null -w '%{http_code}' "$API/v1/me")
[ "$code" = 401 ] || fail "GET $API/v1/me without a token: $code, want 401 (api behind the edge)"

TOKEN=${SMOKE_TOKEN:-}
if [ -z "$TOKEN" ]; then
  TOKEN=$(curl -fsS -X POST "${SUPABASE_URL%/}/auth/v1/token?grant_type=password" \
    -H "apikey: $SUPABASE_ANON_KEY" -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,os;print(json.dumps({"email":os.environ["SMOKE_EMAIL"],"password":os.environ["SMOKE_PASSWORD"]}))')" \
    | json 'd["access_token"]') || fail "sign-in of $SMOKE_EMAIL"
fi
call() { curl -fsS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"; }
code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$INGEST/v1/sync")
[ "$code" = 404 ] || fail "GET $INGEST/v1/sync: $code, want ingest's own 404 (only POST exists)"

me=$(call "$API/v1/me") || fail "GET /v1/me"
[ "$(echo "$me" | json 'd["beta_access"]')" = True ] || fail "the smoke user has no beta access (redeem a code: docs/OPERATIONS.md → Beta)"
policy=$(call "$API/v1/config" | json 'd["privacy_policy_version"]') || fail "GET /v1/config"
if [ "$(echo "$me" | json '(d["consent"] or {}).get("policy_version")')" != "$policy" ]; then
  call -X POST "$API/v1/me/consent" -d "{\"policy_version\": \"$policy\", \"app_version\": \"smoke-prod\"}" \
    || fail "POST /v1/me/consent"
fi
model=$(call "$API/v1/model" | json 'd["current"]') || fail "GET /v1/model"

tz=$(echo "$me" | json 'd["tz"]')
today=$(TZ="$tz" date +%F)
now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
sync=$(call -X POST "$INGEST/v1/sync" -d "{\"device_id\": \"5e0c0a5e-0000-4000-8000-0000000000aa\",
  \"device_name\": \"smoke-prod\", \"app_version\": \"smoke-prod\",
  \"samples\": [{\"type\": \"steps\", \"start\": \"$now\", \"end\": \"$now\", \"value\": 1, \"unit\": \"count\", \"source\": \"smoke-prod\"}],
  \"sleep\": [], \"workouts\": []}") || fail "POST /v1/sync"
echo "$sync" | json '"'"$today"'" in d["days_touched"] or sys.exit(1)' >/dev/null || fail "the sync did not touch $today: $sync"

for _ in $(seq 1 60); do
  day=$(call "$API/v1/days/$today") || fail "GET /v1/days/$today"
  if echo "$day" | json 'd["score"]["computed_at"] >= "'"${now%Z}"'" or sys.exit(1)' >/dev/null 2>&1; then
    got=$(echo "$day" | json 'd["score"]["model_version"]')
    [ "$got" = "$model" ] || fail "$today scored with model $got, the api serves $model"
    echo "✓ smoke-prod: edge, sign-in, /v1/me, consent $policy, a sync, $today scored with model $model"
    exit 0
  fi
  sleep 1
done
fail "$today was not rescored within 60 s of the sync (scoring, NATS: docs/OPERATIONS.md → Dead letters)"
