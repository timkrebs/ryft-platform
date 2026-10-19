#!/usr/bin/env bash
# End-to-end smoke test against a running stack (docker compose or port-forwarded k8s).
#   INGEST_URL=http://localhost:8081 API_URL=http://localhost:8082 API_KEY=... ./scripts/smoke.sh
set -euo pipefail
INGEST_URL=${INGEST_URL:-http://localhost:8081}
API_URL=${API_URL:-http://localhost:8082}
API_KEY=${API_KEY:-dev-api-key-change-me}
DAYS=${DAYS:-21} # 14+ nights for the learned sleep baseline, 5+ for consistency
NAP=${NAP:-40}   # minutes napped yesterday afternoon (check_api expects the credit)
HERE=$(cd "$(dirname "$0")" && pwd)
AUTH_MODE=${AUTH_MODE:-apikey}

# AUTH_MODE=supabase: sign the test user in (GoTrue password grant) and use its
# access token as the bearer. It writes synthetic days into that user's data.
if [ "$AUTH_MODE" = supabase ]; then
  : "${SUPABASE_URL:?set SUPABASE_URL}" "${SUPABASE_ANON_KEY:?set SUPABASE_ANON_KEY}"
  : "${SMOKE_EMAIL:?set SMOKE_EMAIL}" "${SMOKE_PASSWORD:?set SMOKE_PASSWORD}"
  echo "→ signing in $SMOKE_EMAIL at $SUPABASE_URL"
  API_KEY=$(curl -fsS -X POST "${SUPABASE_URL%/}/auth/v1/token?grant_type=password" \
    -H "apikey: $SUPABASE_ANON_KEY" -H 'Content-Type: application/json' \
    -d "$(python3 -c 'import json,os;print(json.dumps({"email":os.environ["SMOKE_EMAIL"],"password":os.environ["SMOKE_PASSWORD"]}))')" \
    | python3 -c 'import json,sys;print(json.load(sys.stdin)["access_token"])')
fi

echo "→ waiting for ingest and api to be ready"
for url in "$INGEST_URL" "$API_URL"; do
  for i in $(seq 1 60); do
    case "$url" in
      # The public host routes only /v1: a JSON 401 there means the service answers.
      https://*) [ "$(curl -s -o /dev/null -w '%{http_code}' "$url/v1/me")" = 401 ] && break ;;
      *) curl -fsS "$url/readyz" >/dev/null 2>&1 && break ;;
    esac
    [ "$i" = 60 ] && { echo "✗ $url not ready after 60 s"; exit 1; }
    sleep 1
  done
done

echo "→ generating $DAYS synthetic days"
python3 "$HERE/synth.py" "$DAYS" --nap "$NAP" > /tmp/ryft-synth.json

echo "→ POST /v1/sync"
curl -fsS -X POST "$INGEST_URL/v1/sync" -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' --data-binary @/tmp/ryft-synth.json | python3 -m json.tool

FROM=$(python3 -c "from datetime import date,timedelta;print(date.today()-timedelta(days=$DAYS))")
TO=$(date +%F)
echo "→ POST /v1/recompute $FROM..$TO (chronological so baselines build up)"
recompute() {
  curl -sS -o /dev/stderr -w '%{http_code}' -X POST "$API_URL/v1/recompute" -H "Authorization: Bearer $API_KEY" \
    -H 'Content-Type: application/json' -d "{\"from\":\"$FROM\",\"to\":\"$TO\"}"
}
CODE=$(recompute); echo
if [ "$CODE" = 429 ]; then # one recompute per user per minute: a smoke run right after another waits
  echo "→ recompute rate-limited, retrying in 61 s"; sleep 61; CODE=$(recompute); echo
fi
[ "$CODE" = 202 ] || { echo "✗ POST /v1/recompute: $CODE"; exit 1; }

echo "→ waiting for scoring"
for i in $(seq 1 30); do
  sleep 1
  REC=$(curl -fsS "$API_URL/v1/days/$TO" -H "Authorization: Bearer $API_KEY" | python3 -c 'import json,sys;print(json.load(sys.stdin)["score"]["recovery"])')
  [ "$REC" != "None" ] && break
done

echo "→ GET /v1/days (last $DAYS days)"
curl -fsS "$API_URL/v1/days?from=$FROM&to=$TO" -H "Authorization: Bearer $API_KEY" | python3 "$HERE/print_days.py"

echo "→ API contract checks"
ADMIN_API_KEY="${ADMIN_API_KEY-$( [ "$AUTH_MODE" = apikey ] && echo dev-admin-key-change-me-0123456789abcdef)}" \
NAP="$NAP" AUTH_MODE="$AUTH_MODE" INGEST_URL="$INGEST_URL" API_URL="$API_URL" API_KEY="$API_KEY" python3 "$HERE/check_api.py" "$TO"
