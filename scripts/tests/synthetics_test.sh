#!/usr/bin/env bash
# scripts/synthetics.sh (make test-scripts): the committed definitions validate, each rule of the
# offline check rejects what it should, and apply upserts by job name against a local stand-in for
# the Synthetic Monitoring API (no credentials, no network beyond 127.0.0.1).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
S=$HERE/scripts/synthetics.sh
F=$HERE/deploy/grafana/synthetics/ryftapp.json
tmp=$(mktemp -d)
pid=
trap '[ -z "$pid" ] || { kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null || true; }; rm -rf "$tmp"' EXIT
fail() { echo "✗ synthetics_test: $*" >&2; exit 1; }
command -v jq >/dev/null || fail "jq is needed"
command -v python3 >/dev/null || fail "python3 is needed (the API stand-in)"

"$S" check >/dev/null || fail "the committed definitions do not validate"

# rejected: a jq edit of the committed file, and a word of the expected message
reject() {
  jq "$1" "$F" > "$tmp/bad.json"
  out=$(SYNTHETICS_FILE=$tmp/bad.json "$S" check 2>&1) && fail "accepted: $1"
  grep -q "$2" <<<"$out" || fail "'$1' failed, but not with '$2': $out"
}
reject '.checks[0].frequency = 30000' 'frequency'
reject '.checks[0].timeout = 400000' 'timeout'
reject '.checks[1].settings.http.failIfBodyNotMatchesRegexp = ["(unclosed"]' 'regexp'
reject '.checks[2].job = "x"' 'job must match'
reject '.checks[3].settings.http.validStatusCodes = []' 'validStatusCodes'
reject '.checks[0].settings.http.noFollowRedirects = false' 'noFollowRedirects'
reject '.checks[0].settings.http.failIfNotSSL = false' 'failIfNotSSL'
reject '.checks[0].target = "http://ryftapp.de/"' 'https URL'
reject '.checks[1].job = .checks[0].job' 'duplicate job'
reject '.probes = []' 'probes'
reject '.probes = ["a","b","c"] | .checks[0].frequency = 60000' 'free tier'

# the stand-in: two probes, one existing check of ours, one of someone else's
cat > "$tmp/api.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
PROBES = [{"id": 11, "name": "Frankfurt", "region": "EMEA", "public": True, "online": True},
          {"id": 12, "name": "Amsterdam", "region": "EMEA", "public": True, "online": True}]
CHECKS = [{"id": 5, "tenantId": 99, "job": "ryft-site-home", "target": "https://ryftapp.de/"},
          {"id": 6, "tenantId": 99, "job": "other", "target": "https://example.org/"}]
class H(BaseHTTPRequestHandler):
    def _send(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer tok": return self._send(401, {})
        if self.path == "/api/v1/probe/list": return self._send(200, PROBES)
        if self.path == "/api/v1/check/list": return self._send(200, CHECKS)
        self._send(404, {})
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        sys.stderr.write("%s id=%s tenant=%s probes=%s job=%s\n" % (
            self.path, body.get("id"), body.get("tenantId"), json.dumps(body.get("probes")), body.get("job")))
        sys.stderr.flush()
        self._send(200, body)
    def log_message(self, *a): pass
HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY
port=$((20000 + RANDOM % 20000))
python3 -I "$tmp/api.py" "$port" 2> "$tmp/api.log" &
pid=$!
for _ in 1 2 3 4 5 6 7 8 9 10; do curl -s -o /dev/null "http://127.0.0.1:$port/" && break; sleep 0.3; done
export SM_API_URL=http://127.0.0.1:$port

SM_ACCESS_TOKEN=bad "$S" probes >/dev/null 2>&1 && fail "a wrong token was accepted"
[ "$(SM_ACCESS_TOKEN=tok "$S" probes | wc -l | tr -d ' ')" = 2 ] || fail "probes: expected two lines"

SYNTHETICS_DRY_RUN=1 SM_ACCESS_TOKEN=tok "$S" apply >/dev/null
[ ! -s "$tmp/api.log" ] || fail "a dry run wrote to the API"

SM_ACCESS_TOKEN=tok "$S" apply > "$tmp/apply.out"
grep -q '^/api/v1/check/update id=5 tenant=99 probes=\[11, 12\] job=ryft-site-home$' "$tmp/api.log" || fail "the existing check was not updated by job: $(cat "$tmp/api.log")"
[ "$(grep -c '^/api/v1/check/add id=None' "$tmp/api.log")" = 3 ] || fail "expected three adds: $(cat "$tmp/api.log")"
grep -q 'other' "$tmp/apply.out" || fail "a check outside the file was not reported"
grep -q 'job=other' "$tmp/api.log" && fail "a check outside the file was touched"

jq '.probes = ["Frankfurt","Nowhere"]' "$F" > "$tmp/np.json"
out=$(SYNTHETICS_FILE=$tmp/np.json SM_ACCESS_TOKEN=tok "$S" apply 2>&1) && fail "an unknown probe was accepted"
grep -q 'Amsterdam' <<<"$out" || fail "the probes on offer were not listed: $out"

echo "✓ synthetics_test"
