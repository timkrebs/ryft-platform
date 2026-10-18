#!/usr/bin/env bash
# End-to-end smoke test against a running stack (docker compose or port-forwarded k8s).
#   INGEST_URL=http://localhost:8081 API_URL=http://localhost:8082 API_KEY=... ./scripts/smoke.sh
set -euo pipefail
INGEST_URL=${INGEST_URL:-http://localhost:8081}
API_URL=${API_URL:-http://localhost:8082}
API_KEY=${API_KEY:-dev-api-key-change-me}
DAYS=${DAYS:-10}
HERE=$(cd "$(dirname "$0")" && pwd)

echo "→ waiting for ingest and api to be ready"
for url in "$INGEST_URL" "$API_URL"; do
  for i in $(seq 1 60); do
    curl -fsS "$url/readyz" >/dev/null 2>&1 && break
    [ "$i" = 60 ] && { echo "✗ $url/readyz not ready after 60 s"; exit 1; }
    sleep 1
  done
done

echo "→ generating $DAYS synthetic days"
python3 "$HERE/synth.py" "$DAYS" > /tmp/ryft-synth.json

echo "→ POST /v1/sync"
curl -fsS -X POST "$INGEST_URL/v1/sync" -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' --data-binary @/tmp/ryft-synth.json | python3 -m json.tool

FROM=$(python3 -c "from datetime import date,timedelta;print(date.today()-timedelta(days=$DAYS))")
TO=$(date +%F)
echo "→ POST /v1/recompute $FROM..$TO (chronological so baselines build up)"
curl -fsS -X POST "$API_URL/v1/recompute" -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' -d "{\"from\":\"$FROM\",\"to\":\"$TO\"}"; echo

echo "→ waiting for scoring"
for i in $(seq 1 30); do
  sleep 1
  REC=$(curl -fsS "$API_URL/v1/days/$TO" -H "Authorization: Bearer $API_KEY" | python3 -c 'import json,sys;print(json.load(sys.stdin)["score"]["recovery"])')
  [ "$REC" != "None" ] && break
done

echo "→ GET /v1/days (last $DAYS days)"
curl -fsS "$API_URL/v1/days?from=$FROM&to=$TO" -H "Authorization: Bearer $API_KEY" | python3 "$HERE/print_days.py"

echo "→ API contract checks"
INGEST_URL="$INGEST_URL" API_URL="$API_URL" API_KEY="$API_KEY" python3 "$HERE/check_api.py" "$TO"
