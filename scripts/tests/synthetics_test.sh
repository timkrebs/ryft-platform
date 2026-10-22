#!/usr/bin/env bash
# scripts/synthetics.sh (make test-scripts): the committed definitions validate, each rule of the
# offline check rejects what it should, and apply upserts by job name against a local stand-in for
# the Synthetic Monitoring API (127.0.0.1 only; no credentials, no network beyond that).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
S=$HERE/scripts/synthetics.sh
F=$HERE/deploy/grafana/synthetics/ryftapp.json
tmp=$(mktemp -d)
pid=
stop_mock() { [ -z "$pid" ] || { kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null || true; pid=; }; }
trap 'stop_mock; rm -rf "$tmp"' EXIT
fail() { echo "✗ synthetics_test: $*" >&2; exit 1; }
command -v jq >/dev/null || fail "jq is needed"
command -v python3 >/dev/null || fail "python3 is needed (the API stand-in)"
command -v go >/dev/null || fail "go is needed (the RE2 check)"

"$S" check >/dev/null || fail "the committed definitions do not validate"

# rejected: a jq edit of the committed file, and a word of the expected message
reject() {
  jq "$1" "$F" > "$tmp/bad.json"
  out=$(SYNTHETICS_FILE=$tmp/bad.json "$S" check 2>&1) && fail "accepted: $1"
  grep -q "$2" <<<"$out" || fail "'$1' failed, but not with '$2': $out"
}
reject '.checks[0].frequency = 30000' 'frequency'
reject '.checks[0].timeout = 400000' 'timeout'
reject '.checks[1].settings.http.failIfBodyNotMatchesRegexp = ["(unclosed"]' 'RE2'
reject '.checks[1].settings.http.failIfBodyNotMatchesRegexp = ["(?=lookahead)"]' 'RE2'
reject '.checks[1].settings.http.failIfBodyNotMatchesRegexp = ["(a)\\1"]' 'RE2'
reject '.checks[0].settings.http.failIfHeaderNotMatchesRegexp[0].regexp = "(?<=x)y"' 'RE2'
reject '.checks[2].job = "x"' 'job must match'
reject '.checks[3].settings.http.validStatusCodes = []' 'validStatusCodes'
reject '.checks[0].settings.http.noFollowRedirects = false' 'noFollowRedirects'
reject '.checks[0].settings.http.failIfNotSSL = false' 'failIfNotSSL'
reject '.checks[0].target = "http://ryftapp.de/"' 'target must be'
reject '.checks[0].target = "https://evil.example/"' 'target must be'
reject '.checks[0].extra = 1' 'unknown key'
reject '.checks[0].settings.http.proxyURL = "http://x"' 'unknown http setting'
reject '.checks[1].job = .checks[0].job' 'duplicate job'
reject '.probes = []' 'probes'
reject '.probes = ["a","b","c"] | .checks[0].frequency = 60000' 'free tier'
reject '.checks |= map(.enabled = false)' 'disabled'
# SyntheticsMissing names the jobs: the enabled checks must be the same set
reject '.checks[1].enabled = false' 'SyntheticsMissing'
reject 'del(.checks[3])' 'SyntheticsMissing'
reject '.checks += [(.checks[0] | .job = "ryft-site-extra")]' 'SyntheticsMissing'
reject '.checks[0].settings.http.failIfHeaderNotMatchesRegexp[0].extra = 1' 'unknown key extra'
reject '.checks[0].labels = [{"name": "a", "value": "b", "x": 1}]' 'unknown key x'
# the markers must exist in the site's own sources
reject '.checks[1].settings.http.failIfBodyNotMatchesRegexp = ["(?s).*a sentence the policy does not hold.*"]' 'does not match'
reject '.checks[2].settings.http.failIfBodyNotMatchesRegexp = ["(?s).*XXXXXXXXXX.dev.timkrebs.ryft.*"]' 'does not match'
# a relative SYNTHETICS_FILE is relative to where the caller stands
(cd "$tmp" && cp "$F" rel.json && SYNTHETICS_FILE=rel.json "$S" check >/dev/null) || fail "a relative SYNTHETICS_FILE was not resolved from the cwd"

# ---- the stand-in: stateful, port 0, modes via argv ---------------------------------
cat > "$tmp/api.py" <<'PY'
import json, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
mode, logpath, portfile = sys.argv[1], sys.argv[2], sys.argv[3]
PROBES = [{"id": 11, "name": "Frankfurt", "region": "EMEA", "public": True, "online": True},
          {"id": 12, "name": "Amsterdam", "region": "EMEA", "public": True, "online": True}]
CHECKS = [] if mode == "null-list" else [
    {"id": 5, "tenantId": 99, "job": "ryft-site-home", "target": "https://ryftapp.de/"},
    {"id": 6, "tenantId": 99, "job": "other", "target": "https://example.org/"}]
nextid = [100]
def log(s):
    with open(logpath, "a") as f: f.write(s + "\n")
class H(BaseHTTPRequestHandler):
    def _send(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def _auth(self):
        if self.headers.get("Authorization") != "Bearer tok-en.1":
            self._send(401, {}); return False
        return True
    def do_GET(self):
        if not self._auth(): return
        if self.path == "/api/v1/probe/list": return self._send(200, PROBES)
        if self.path == "/api/v1/check/list": return self._send(200, None if mode == "null-list" else CHECKS)
        self._send(404, {})
    def do_POST(self):
        if not self._auth(): return
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        log("POST %s auth=ok id=%s tenant=%s probes=%s job=%s" % (
            self.path, body.get("id"), body.get("tenantId"), json.dumps(body.get("probes")), body.get("job")))
        if mode == "fail:" + str(body.get("job")): return self._send(500, {"err": "boom"})
        body.update({"created": 1.0, "modified": 2.0, "tenantId": 99, "offset": 0})
        if self.path.endswith("/add"):
            body["id"] = nextid[0]; nextid[0] += 1; CHECKS.append(body)
        else:
            CHECKS[:] = [body if c["id"] == body["id"] else c for c in CHECKS]
        self._send(200, body)
    def log_message(self, *a): pass
srv = HTTPServer(("127.0.0.1", 0), H)
open(portfile, "w").write(str(srv.server_address[1]))
srv.serve_forever()
PY
start_mock() {  # mode -> sets SM_API_URL, empties the log
  stop_mock
  : > "$tmp/api.log"; rm -f "$tmp/port"
  python3 -I "$tmp/api.py" "$1" "$tmp/api.log" "$tmp/port" &
  pid=$!
  for _ in $(seq 1 50); do [ -s "$tmp/port" ] && break; sleep 0.1; done
  [ -s "$tmp/port" ] || fail "the stand-in did not start"
  SM_API_URL=http://127.0.0.1:$(cat "$tmp/port")
  export SM_API_URL
}
export SYNTHETICS_TEST_LOCAL=1
export SM_ACCESS_TOKEN=tok-en.1

# the URL guard: only the stack's Backend address over https, loopback only in test mode
for u in http://127.0.0.1@evil.example/ https://synthetic-monitoring-api-eu-west-2.grafana.net.evil.example/ \
         https://evil-synthetic-monitoring-api.grafana.net/ http://127.0.0.1.evil.example:80 \
         https://synthetic-monitoring-api-eu-west-2.grafana.net/x; do
  SYNTHETICS_TEST_LOCAL='' SM_API_URL=$u "$S" probes >/dev/null 2>&1 && fail "accepted SM_API_URL=$u"
  SM_API_URL=$u "$S" probes >/dev/null 2>&1 && fail "accepted in test mode: SM_API_URL=$u"
done
SYNTHETICS_TEST_LOCAL='' SM_API_URL=http://127.0.0.1:9 "$S" probes >/dev/null 2>&1 && fail "loopback accepted outside test mode"
SYNTHETICS_TEST_LOCAL=yes SM_API_URL=http://127.0.0.1:9 "$S" probes >/dev/null 2>&1 && fail "test mode on for a value other than 1"
# a token with a quote, a backslash or whitespace is not a token (and cannot break the curl config)
for t in 'a"b' 'a\b' 'a b' $'a\nb' ''; do
  SM_API_URL=http://127.0.0.1:9 SM_ACCESS_TOKEN=$t "$S" probes >/dev/null 2>&1 && fail "accepted a bad token"
done

start_mock plain
SM_ACCESS_TOKEN=bad "$S" probes >/dev/null 2>&1 && fail "a wrong token was accepted"
[ "$(SM_ACCESS_TOKEN=tok-en.1 "$S" probes | wc -l | tr -d ' ')" = 2 ] || fail "probes: expected two lines"

# the token never reaches curl's argv, and ~/.curlrc is skipped (-q first)
mkdir "$tmp/bin"
real_curl=$(command -v curl)
printf '#!/bin/sh\nprintf "%%s\\n" "$*" >> "%s/curl.argv"\nexec "%s" "$@"\n' "$tmp" "$real_curl" > "$tmp/bin/curl"
chmod +x "$tmp/bin/curl"
PATH=$tmp/bin:$PATH "$S" probes >/dev/null
[ -s "$tmp/curl.argv" ] || fail "the curl stub saw nothing"
grep -q 'tok-en' "$tmp/curl.argv" && fail "the token is on curl's command line"
grep -qv '^-q ' "$tmp/curl.argv" && fail "curl was not started with -q"

# a dry run writes nothing and prints a field diff for the check that exists
out=$(SYNTHETICS_DRY_RUN=1 "$S" apply)
[ ! -s "$tmp/api.log" ] || fail "a dry run wrote to the API"
grep -q 'ryft-site-home: would update' <<<"$out" || fail "dry run: no update for the existing check: $out"
grep -q 'settings.http.method' <<<"$out" || fail "dry run: no field diff: $out"
grep -q 'ryft-site-aasa: would add' <<<"$out" || fail "dry run: no add: $out"

# apply: update by job (with id and tenantId), three adds, others untouched, POSTs authenticated
out=$("$S" apply)
grep -q '^POST /api/v1/check/update auth=ok id=5 tenant=99 probes=\[11, 12\] job=ryft-site-home$' "$tmp/api.log" || fail "the existing check was not updated by job: $(cat "$tmp/api.log")"
[ "$(grep -c '^POST /api/v1/check/add auth=ok id=None' "$tmp/api.log")" = 3 ] || fail "expected three adds: $(cat "$tmp/api.log")"
grep -q 'not in the file: other' <<<"$out" || fail "a check outside the file was not reported"
grep -q 'job=other' "$tmp/api.log" && fail "a check outside the file was touched"
# a second apply is a true no-op
: > "$tmp/api.log"
out=$("$S" apply)
[ ! -s "$tmp/api.log" ] || fail "a second apply wrote: $(cat "$tmp/api.log")"
[ "$(grep -c '^unchanged: ' <<<"$out")" = 4 ] || fail "a second apply: expected four unchanged: $out"
# a changed field is found again
jq '.checks[0].timeout = 8000' "$F" > "$tmp/t8.json"
SYNTHETICS_FILE=$tmp/t8.json "$S" apply >/dev/null
[ "$(grep -c '^POST' "$tmp/api.log")" = 1 ] || fail "a changed timeout should be exactly one update: $(cat "$tmp/api.log")"
# a field REMOVED from the file is a change too (and so is putting it back)
"$S" apply >/dev/null
expect_updates() {  # <jq edit of the committed file> <number of updates it must cause>
  jq "$1" "$F" > "$tmp/v.json"
  : > "$tmp/api.log"
  SYNTHETICS_FILE=$tmp/v.json "$S" apply >/dev/null
  [ "$(grep -c '^POST /api/v1/check/update' "$tmp/api.log")" = "$2" ] || fail "'$1': expected $2 update(s): $(cat "$tmp/api.log")"
  : > "$tmp/api.log"
  "$S" apply >/dev/null
  [ "$(grep -c '^POST /api/v1/check/update' "$tmp/api.log")" = "$2" ] || fail "'$1': putting it back should be $2 update(s): $(cat "$tmp/api.log")"
}
expect_updates 'del(.checks[1].settings.http.failIfBodyNotMatchesRegexp)' 1
expect_updates '.checks[3].settings.http.validStatusCodes = [301]' 1
expect_updates 'del(.checks[0].settings.http.failIfHeaderNotMatchesRegexp)' 1
expect_updates '.probes = ["Frankfurt"]' 4   # every check lists the probes

# an unknown or duplicate probe name stops before any write
start_mock plain
jq '.probes = ["Frankfurt","Nowhere"]' "$F" > "$tmp/np.json"
out=$(SYNTHETICS_FILE=$tmp/np.json "$S" apply 2>&1) && fail "an unknown probe was accepted"
grep -q 'Amsterdam' <<<"$out" || fail "the probes on offer were not listed: $out"
[ ! -s "$tmp/api.log" ] || fail "a write happened before the probe lookup failed"

# a fresh stack: check/list is null
start_mock null-list
"$S" apply >/dev/null || fail "apply on an empty stack (null list) failed"
[ "$(grep -c '^POST /api/v1/check/add' "$tmp/api.log")" = 4 ] || fail "an empty stack: expected four adds: $(cat "$tmp/api.log")"

# a failure in the middle stops cleanly and names what was applied
start_mock fail:ryft-site-aasa
out=$("$S" apply 2>&1) && fail "a failed POST did not fail apply"
grep -q 'stopped at ryft-site-aasa' <<<"$out" || fail "no stop message: $out"
grep -q 'ryft-site-www-redirect' "$tmp/api.log" && fail "apply went on after the failure"

echo "✓ synthetics_test"
