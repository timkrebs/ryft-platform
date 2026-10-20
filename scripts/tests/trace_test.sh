#!/usr/bin/env bash
# trace.sh against a fake kubectl (make test-scripts). With the compose stack up
# (make up), also the account lookup against the local api.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
fake=$(mktemp -d)
trap 'rm -rf "$fake"' EXIT
cat > "$fake/kubectl" <<'K'
#!/bin/sh
# Refuses the call that found nothing in the incident of 2026-10-20 (no --tail=-1).
case " $* " in *" --tail=-1 "*) ;; *) echo "fake kubectl: without --tail=-1 only 10 lines a pod" >&2; exit 3 ;; esac
case " $* " in
  *" -n ryft-ingest "*)
    echo '[pod/ingest-1/ingest] {"time":"2026-10-20T17:43:42.155Z","level":"INFO","msg":"http","service":"ingest","method":"POST","path":"/v1/sync","status":403,"bytes":172,"dur_ms":9,"code":"consent_required","request_id":"01a15a69-7142-7a8b-aba2-c099a5fcd1e4","user_id":"00000000-0000-0000-0000-000000000001","route":"POST /v1/sync"}'
    echo '[pod/ingest-2/ingest] {"time":"2026-10-20T17:44:00Z","level":"INFO","msg":"http","service":"ingest","status":200,"request_id":"01a15a69-0000-7000-8000-000000000000"}' ;;
  *" -n ryft-api "*)
    echo '[pod/api-2/api] {"time":"2026-10-20T17:43:42.150Z","level":"ERROR","msg":"request failed","service":"api","err":"boom","request_id":"01a15a69-7142-7a8b-aba2-c099a5fcd1e4"}' ;;
esac
K
chmod +x "$fake/kubectl"
fail() { echo "✗ trace_test: $*" >&2; exit 1; }
run() { PATH="$fake:$PATH" /bin/bash "$HERE/trace.sh" "$@"; }

out=$(run 01a15a69-71427a8b-aba2-c099a5fcd1e4 --no-user 2>"$fake/err") || fail "trace failed: $(cat "$fake/err")"
grep -q 'request id: 01a15a69-71427a8b-aba2-c099a5fcd1e4 → 01a15a69-7142-7a8b-aba2-c099a5fcd1e4' "$fake/err" || fail "the lost hyphen is not put back: $(cat "$fake/err")"
[ "$(echo "$out" | wc -l | tr -d ' ')" = 2 ] || fail "want 2 lines (ingest, api), got: $out"
echo "$out" | python3 -c '
import json, sys
rows = [json.loads(l) for l in sys.stdin]
i = next(r for r in rows if r["service"] == "ingest")
a = next(r for r in rows if r["service"] == "api")
assert i["status"] == 403 and i["code"] == "consent_required" and i["pod"] == "ingest-1" and i["user"].startswith("00000000"), i
assert a["msg"] == "request failed" and a["error"] == "boom" and "status" not in a, a
' || fail "unexpected lines: $out"
if run 01a15a69-dead-beef-aba2-c099a5fcd1e4 --no-user >/dev/null 2>"$fake/err"; then fail "an unknown id must fail"; fi
grep -q 'in no pod' "$fake/err" || fail "unknown id message: $(cat "$fake/err")"

if curl -fsS -o /dev/null http://localhost:8082/readyz 2>/dev/null; then
  out=$(API_URL=http://localhost:8082 ADMIN_API_KEY=dev-admin-key-change-me-0123456789abcdef run 01a15a69-7142-7a8b-aba2-c099a5fcd1e4 2>/dev/null) \
    || fail "trace with the user lookup"
  echo "$out" | grep -q '^user 00000000 (apikey' || fail "no user summary: $out"
  echo "✓ trace_test: lines from all pods, the lost hyphen, the account (local api)"
else
  echo "✓ trace_test: lines from all pods, the lost hyphen (no local api: the account lookup was skipped)"
fi
