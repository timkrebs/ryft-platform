#!/usr/bin/env bash
# shellcheck disable=SC1091
# scripts/load.sh helpers (make test-scripts): the target guard (localhost only unless LOAD_ALLOW_REMOTE=1), the
# URL host parsing it rests on, and the verdict on a fixture run (error-rate limit, median run, histogram math).
# No container, no network.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
fail() { echo "✗ load_test: $*" >&2; exit 1; }
# shellcheck source=../load.sh
. "$HERE/scripts/load.sh"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT

host() { [ "$(url_host "$1")" = "$2" ] || fail "url_host $1 = '$(url_host "$1")', want '$2'"; }
host http://localhost:8081 localhost
host http://LOCALHOST/v1/x localhost
host https://api.ryftapp.de/v1/me api.ryftapp.de
host http://user:pw@127.0.0.1:8082/x 127.0.0.1
host 'http://[::1]:8082/v1' ::1
host 'http://evil.example?h=localhost' evil.example
host 'http://localhost@evil.example/' evil.example
host 'http://localhost.evil.example/' localhost.evil.example

ok()  { (unset LOAD_ALLOW_REMOTE; guard_target "$1") || fail "guard refused $1"; }
no()  { if (unset LOAD_ALLOW_REMOTE; guard_target "$1"); then fail "guard let $1 through"; fi; }
ok http://localhost:8081
ok http://127.0.0.1:8082
ok 'http://[::1]:8082'
ok http://127.1.2.3
no https://api.ryftapp.de
no http://localhost.evil.example
no http://localhost@evil.example/
no http://127.0.0.1.evil.example
no http://10.0.0.5:8082
no http://0.0.0.0:8082
no ''
no 'http://evil.com#@localhost'
no 'http://evil.com;@localhost'
no 'http://evil.com\@localhost'
no 'http://user:p@ss@evil.com#@localhost'
no 'http://user:p@ss@localhost'
no 'http://user:pw@127.0.0.1:8082/x'
no 'http://localhost:8081/ x'
no 'http://local host:8081'
no 'http://localhost:8081/	x'
no 'ftp://localhost'
no 'localhost:8081'
(export LOAD_ALLOW_REMOTE=1; no_remote() { guard_target "$1" && fail "guard let $1 through with LOAD_ALLOW_REMOTE=1"; return 0; }
 no_remote https://ryftapp.de; no_remote https://api.ryftapp.de/v1; no_remote https://abc.supabase.co
 no_remote 'http://evil.com#@localhost'; no_remote 'http://a@b.example')
(LOAD_ALLOW_REMOTE=1 guard_target https://staging.example) || fail "LOAD_ALLOW_REMOTE=1 must allow"
(LOAD_ALLOW_REMOTE=0 guard_target https://staging.example) && fail "only LOAD_ALLOW_REMOTE=1 allows"

# verdict on fixtures: two measured runs; the scoring histogram gains 10 observations of 1-2 s and 10 of 2-5 s.
mk() { # <dir> <sync err rate> <sync p95> [thresholds_ok]
  mkdir -p "$1"
  cat > "$1/summary.json" <<EOF
{"schema":1,"users":50,"http_429":0,"consent_error_rate":0,"thresholds_ok":${4:-true},
 "routes":{"POST /v1/sync":{"count":50,"p50_ms":100,"p95_ms":$3,"p99_ms":400,"error_rate":$2},
           "GET /v1/days":{"count":1000,"p50_ms":5,"p95_ms":9,"p99_ms":20,"error_rate":0}}}
EOF
  for s in ingest api; do : > "$1/before-$s.prom"; : > "$1/after-$s.prom"; done
  printf 'ryft_sync_to_score_seconds_bucket{le="1"} 5\nryft_sync_to_score_seconds_bucket{le="2"} 5\nryft_sync_to_score_seconds_bucket{le="5"} 5\nryft_sync_to_score_seconds_bucket{le="+Inf"} 5\n' > "$1/before-scoring.prom"
  printf 'ryft_sync_to_score_seconds_bucket{le="1"} 5\nryft_sync_to_score_seconds_bucket{le="2"} 15\nryft_sync_to_score_seconds_bucket{le="5"} 25\nryft_sync_to_score_seconds_bucket{le="+Inf"} 25\n' > "$1/after-scoring.prom"
}
mk "$tmp/a/run-1" 0 300; mk "$tmp/a/run-2" 0.005 200
verdict "$tmp/a" 0.01 "$tmp/a/run-1" "$tmp/a/run-2" > "$tmp/a.out" || fail "0.5 % errors must pass a 1 % limit"
grep -q 'median run (by POST /v1/sync p95): run-2' "$tmp/a.out" || fail "median of two is the lower p95: $(cat "$tmp/a.out")"
# Delta of the histogram: 20 new observations, p50 at the 2 s bound, p95 = 2 + 3 * (19 - 10) / 10 = 4.7 s.
grep -q 'sync->score *20 *2.0 *4.7' "$tmp/a.out" || fail "histogram quantiles wrong: $(cat "$tmp/a.out")"
[ -s "$tmp/a/baseline.json" ] || fail "baseline.json not written"
mk "$tmp/b/run-1" 0.02 300
if verdict "$tmp/b" 0.01 "$tmp/b/run-1" >/dev/null 2>"$tmp/b.err"; then fail "2 % errors must fail a 1 % limit"; fi
grep -q 'FAIL run-1 POST /v1/sync' "$tmp/b.err" || fail "the failing route is not named: $(cat "$tmp/b.err")"
# A failed k6 threshold (LOAD_P95_SYNC_MS) fails the verdict although the error rate is 0.
mk "$tmp/c/run-1" 0 9000 false
if verdict "$tmp/c" 0.01 "$tmp/c/run-1" >/dev/null 2>"$tmp/c.err"; then fail "a failed k6 threshold must fail"; fi
grep -q 'FAIL run-1: a k6 threshold failed' "$tmp/c.err" || fail "threshold failure not named: $(cat "$tmp/c.err")"
mk "$tmp/d/run-1" 0 9000 true
verdict "$tmp/d" 0.01 "$tmp/d/run-1" >/dev/null 2>&1 || fail "slow but within thresholds (none set) must pass"

# LOAD_OUT: never / or $HOME, a foreign non-empty directory is refused, an empty or marked one is fine.
out_dir_ok / && fail "/ accepted"
out_dir_ok "$HOME" && fail "HOME accepted"
out_dir_ok "" && fail "empty path accepted"
mkdir "$tmp/foreign"; : > "$tmp/foreign/precious"
out_dir_ok "$tmp/foreign" && fail "a foreign non-empty dir accepted"
mkdir "$tmp/empty"; out_dir_ok "$tmp/empty" || fail "empty dir refused"
out_dir_ok "$tmp/new/sub" || fail "a new path refused"
mkdir "$tmp/marked"; : > "$tmp/marked/.ryft-load-out"; : > "$tmp/marked/x"; out_dir_ok "$tmp/marked" || fail "marked dir refused"
echo "ok: load.sh helpers"
