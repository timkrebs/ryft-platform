#!/usr/bin/env bash
# Load baseline against the LOCAL compose stack (make up): 50 synthetic users, each syncs one full day of
# Apple-Watch-like records, then reads GET /v1/days over 30 days. k6 runs in a container (podman or docker, $CONTAINER
# as in the Makefile) pinned by digest, on the compose network, so the numbers are not the port-forward's.
#   make up && make load            # a warm-up run, then 3 measured runs; prints a table, exits 1 above 1 % errors
#
# Users: AUTH_MODE=apikey accepts the bearer "<API_KEY>:<uuid>" as that user (internal/auth/apikey.go), so one
# key stands for any number of accounts; no auth code is touched. Every run uses a fresh set of accounts.
# Output: $LOAD_OUT (default scripts/load/out/, git-ignored): run-N/summary.json (k6), run-N/*.prom (the services'
# metrics before and after), baseline.json (all measured runs and the median run: the file a later nightly job
# compares with, see verdict()).
#
# Env (all optional): INGEST_URL API_URL API_KEY | LOAD_USERS=50 LOAD_READS=20 LOAD_THINK_MS=250 LOAD_RAMP_MS=100
#   LOAD_RUNS=3 LOAD_WARMUP=1 LOAD_ANCHOR_DATE=YYYY-MM-DD (default: yesterday, UTC) LOAD_OUT LOAD_K6_IMAGE
#   LOAD_MAX_ERROR_RATE=0.01 (fail above) LOAD_P95_SYNC_MS / LOAD_P95_DAYS_MS (optional k6 thresholds)
#   LOAD_DRAIN_TIMEOUT=180 (s to wait for scoring) LOAD_ALLOW_REMOTE=1 (see below)
# Targets must be localhost; anything else is refused unless LOAD_ALLOW_REMOTE=1. Remote runs skip the
# metrics scrape (no compose network). Never point this at production.
set -euo pipefail

# Pinned: grafana/k6 v2.3.0, the current release on 2026-11-04; digest of the multi-arch index, resolved with the
# registry API (docker-content-digest of GET /v2/grafana/k6/manifests/2.3.0). Bump both together.
K6_TAG=2.3.0
K6_DIGEST=sha256:2dd4756ec6e14e62778916e1eb850108e4b499304c02df56760fa0bdb0fba0b8
K6_IMAGE_DEFAULT="docker.io/grafana/k6:${K6_TAG}@${K6_DIGEST}"

# ---- pure helpers (tested by scripts/tests/load_test.sh) -----------------------------------------------

# url_host <url>: the host part, without scheme, userinfo, port or path; IPv6 keeps no brackets.
url_host() {
  local u=${1#*://}
  u=${u%%/*}; u=${u%%\?*}; u=${u##*@}
  case $u in
    \[*\]*) u=${u#\[}; u=${u%%\]*} ;;
    *) u=${u%%:*} ;;
  esac
  printf '%s\n' "$u" | tr '[:upper:]' '[:lower:]'
}

# is_local_host <host>: localhost, *.localhost, 127.0.0.0/8, ::1.
is_local_host() {
  case $1 in
    localhost|*.localhost|::1) return 0 ;;
    127.*) [[ $1 =~ ^127(\.[0-9]{1,3}){3}$ ]] ;;
    *) return 1 ;;
  esac
}

# guard_target <url>: 0 when the load may go there.
guard_target() {
  local h; h=$(url_host "$1")
  is_local_host "$h" || [ "${LOAD_ALLOW_REMOTE:-}" = 1 ]
}

# ---- the run -------------------------------------------------------------------------------------------

log() { printf '→ %s\n' "$*" >&2; }
die() { printf '✗ %s\n' "$*" >&2; exit 1; }

# k6 <args...>: the pinned container; mounts scripts/load (read-only) and the run's output dir.
k6() {
  local -a net=()
  [ -z "$NET" ] || net=(--network "$NET")
  "$CONTAINER" run --rm ${net[@]+"${net[@]}"} -v "$LOADDIR:/load:ro" -v "$RUNDIR:/out" "$K6_IMAGE" "$@"
}

# scrape <file-prefix>: ingest, api and scoring /metrics (not published to the host) into $RUNDIR/<prefix>-<svc>.prom.
scrape() {
  [ -n "$NET" ] || return 0
  local p=$1 s
  k6 run -q -e SCRAPE="$p-ingest=http://ingest:9090/metrics,$p-api=http://api:9090/metrics,$p-scoring=http://scoring:9090/metrics" /load/scrape.js >/dev/null 2>&1 \
    || die "metrics scrape failed (is the stack up? network $NET)"
  for s in ingest api scoring; do [ -s "$RUNDIR/$p-$s.prom" ] || die "empty metrics from $s"; done
}

# prom_value <file> <metric-name-with-labels-or-bare>: sum of the matching series.
prom_sum() { awk -v m="$2" '$1 == m || index($1, m "{") == 1 { s += $2 } END { printf "%d\n", s }' "$1"; }

# wait_scoring_drained: poll until ryft_sync_to_score_seconds_count stops rising (two equal readings 3 s apart) or the deadline.
wait_scoring_drained() {
  local deadline=$((SECONDS + ${LOAD_DRAIN_TIMEOUT:-180})) last=-1 now
  [ -n "$NET" ] || return 0
  while :; do
    scrape drain
    now=$(prom_sum "$RUNDIR/drain-scoring.prom" ryft_sync_to_score_seconds_count)
    if [ "$now" = "$last" ] && [ "$now" -gt "$BASE_SCORED" ]; then return 0; fi
    last=$now
    [ "$SECONDS" -lt "$deadline" ] || { log "scoring still working after ${LOAD_DRAIN_TIMEOUT:-180} s; using what it finished"; return 0; }
    sleep 3
  done
}

main() {
  CONTAINER=${CONTAINER:-$(command -v docker >/dev/null 2>&1 && echo docker || echo podman)}
  COMPOSE=${COMPOSE:-$CONTAINER compose}
  INGEST_URL=${INGEST_URL:-http://localhost:8081}
  API_URL=${API_URL:-http://localhost:8082}
  API_KEY=${API_KEY:-dev-api-key-change-me}
  local users=${LOAD_USERS:-50} runs=${LOAD_RUNS:-3} warm=${LOAD_WARMUP:-1}
  HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
  LOADDIR=$HERE/load
  K6_IMAGE=${LOAD_K6_IMAGE:-$K6_IMAGE_DEFAULT}
  local out=${LOAD_OUT:-$LOADDIR/out}

  guard_target "$INGEST_URL" || die "refusing INGEST_URL=$INGEST_URL: not localhost (LOAD_ALLOW_REMOTE=1 overrides; never production)"
  guard_target "$API_URL" || die "refusing API_URL=$API_URL: not localhost (LOAD_ALLOW_REMOTE=1 overrides; never production)"
  case $users$runs$warm in *[!0-9]*|'') die "LOAD_USERS, LOAD_RUNS, LOAD_WARMUP must be integers" ;; esac
  [ "$users" -ge 1 ] && [ "$runs" -ge 1 ] || die "LOAD_USERS and LOAD_RUNS must be at least 1"

  log "waiting for ingest and api"
  local url i
  for url in "$INGEST_URL" "$API_URL"; do
    for i in $(seq 1 60); do curl -fsS "$url/readyz" >/dev/null 2>&1 && break; [ "$i" = 60 ] && die "$url not ready after 60 s (make up)"; sleep 1; done
  done

  # Where k6 runs: on the compose network of the api container (service names, no host port-forward); with
  # LOAD_ALLOW_REMOTE=1 and a non-local URL directly against the URLs given, without the metrics scrape.
  NET=''
  local k6_ingest=$INGEST_URL k6_api=$API_URL
  if is_local_host "$(url_host "$INGEST_URL")" && is_local_host "$(url_host "$API_URL")"; then
    local cid
    # shellcheck disable=SC2086 # COMPOSE is a command with arguments
    cid=$($COMPOSE ps -q api 2>/dev/null | head -n 1) || true
    [ -n "$cid" ] || die "no compose container 'api' found for this directory (make up; COMPOSE_PROJECT_NAME if you started it under another name)"
    # shellcheck disable=SC2016 # a Go template, not shell
    NET=$("$CONTAINER" inspect --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$cid" | awk '{print $1}')
    [ -n "$NET" ] || die "cannot find the compose network of $cid"
    k6_ingest=http://ingest:8080 k6_api=http://api:8080
    log "stack: $("$CONTAINER" inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$cid"), network $NET"
  else
    log "remote target (LOAD_ALLOW_REMOTE=1): k6 goes direct, no metrics scrape"
  fi

  local anchor=${LOAD_ANCHOR_DATE:-$(python3 -I -c 'from datetime import datetime,timedelta,timezone;print((datetime.now(timezone.utc)-timedelta(days=1)).date())')}
  [[ $anchor =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || die "LOAD_ANCHOR_DATE must be YYYY-MM-DD"
  # Account ids are (base, run, user): a repeated invocation without `make down` still meets fresh accounts.
  local base=$(( $(date +%s) / 60 % 10000 * 10 ))
  rm -rf "$out"; mkdir -p "$out"
  log "k6 $K6_IMAGE; $users users, anchor day $anchor, $warm warm-up + $runs measured run(s), output $out"

  local r total=$((warm + runs)) code=0 failed=0
  local -a summaries=()
  for r in $(seq 0 $((total - 1))); do
    RUNDIR=$out/$([ "$r" -lt "$warm" ] && echo "warmup-$((r + 1))" || echo "run-$((r - warm + 1))")
    mkdir -p "$RUNDIR"; chmod 777 "$RUNDIR" # k6 runs as a non-root user inside the VM
    scrape before
    BASE_SCORED=0; [ -z "$NET" ] || BASE_SCORED=$(prom_sum "$RUNDIR/before-scoring.prom" ryft_sync_to_score_seconds_count)
    log "$(basename "$RUNDIR"): $users users"
    code=0
    k6 run -q \
      -e INGEST_URL="$k6_ingest" -e API_URL="$k6_api" -e API_KEY="$API_KEY" -e ANCHOR="$anchor" -e RUN=$((base + r)) \
      -e USERS="$users" -e READS="${LOAD_READS:-20}" -e THINK_MS="${LOAD_THINK_MS:-250}" -e RAMP_MS="${LOAD_RAMP_MS:-100}" \
      -e MAX_ERROR_RATE="${LOAD_MAX_ERROR_RATE:-0.01}" -e P95_SYNC_MS="${LOAD_P95_SYNC_MS:-}" -e P95_DAYS_MS="${LOAD_P95_DAYS_MS:-}" \
      /load/day.js >"$RUNDIR/k6.log" 2>&1 || code=$?
    if [ ! -s "$RUNDIR/summary.json" ]; then tail -20 "$RUNDIR/k6.log" >&2; die "k6 produced no summary (exit $code)"; fi
    [ "$code" = 0 ] || [ "$code" = 99 ] || { tail -20 "$RUNDIR/k6.log" >&2; die "k6 exit $code"; }
    wait_scoring_drained
    scrape after
    [ "$r" -lt "$warm" ] || summaries+=("$RUNDIR")
  done

  # Table, baseline.json and the verdict.
  [ "${#summaries[@]}" -gt 0 ] || die "no measured run"
  verdict "$out" "${LOAD_MAX_ERROR_RATE:-0.01}" "${summaries[@]}" || failed=1
  [ "$failed" = 0 ] || exit 1
}

# verdict <out-dir> <max-error-rate> <run-dir>...: prints the table, writes <out-dir>/baseline.json, exits 1 when any
# measured run's error rate (sync or days) is above the limit. This is the place for a later `--compare baseline.json`
# (fail on a 30 % regression of p95 vs the recorded median run); baseline.json holds everything it needs.
verdict() {
  python3 -I - "$@" <<'PY'
import json, re, sys, os

out, limit, dirs = sys.argv[1], float(sys.argv[2]), sys.argv[3:]

def prom(path):
    """metric name -> {frozenset(labels): value}"""
    m = {}
    try:
        for line in open(path):
            if line.startswith("#") or not line.strip():
                continue
            mt = re.match(r'^([a-zA-Z_:][\w:]*)(\{(.*)\})?\s+(\S+)', line)
            if not mt:
                continue
            labels = frozenset(re.findall(r'(\w+)="([^"]*)"', mt.group(3) or ""))
            m.setdefault(mt.group(1), {})[labels] = float(mt.group(4))
    except OSError:
        pass
    return m

def hist_delta(before, after, name):
    """cumulative buckets (le -> count) and total count, after minus before"""
    def buckets(m):
        d = {}
        for labels, v in m.get(name + "_bucket", {}).items():
            le = dict(labels).get("le")
            d[float("inf") if le == "+Inf" else float(le)] = d.get(float("inf") if le == "+Inf" else float(le), 0) + v
        return d
    b, a = buckets(before), buckets(after)
    return sorted((le, a[le] - b.get(le, 0)) for le in a)

def quantile(q, bk):
    """histogram_quantile: linear inside the bucket the rank falls in; the +Inf bucket answers its lower bound"""
    if not bk or bk[-1][1] <= 0:
        return None
    rank, lo, prev = q * bk[-1][1], 0.0, 0.0
    for le, c in bk:
        if c >= rank:
            if le == float("inf"):
                return lo
            return lo + (le - lo) * ((rank - prev) / (c - prev)) if c > prev else le
        lo, prev = le, c
    return lo

def counter(before, after, name):
    return sum(after.get(name, {}).values()) - sum(before.get(name, {}).values())

runs = []
for d in dirs:
    s = json.load(open(os.path.join(d, "summary.json")))
    pre = {k: prom(os.path.join(d, "before-%s.prom" % k)) for k in ("ingest", "api", "scoring")}
    post = {k: prom(os.path.join(d, "after-%s.prom" % k)) for k in ("ingest", "api", "scoring")}
    bk = hist_delta(pre["scoring"], post["scoring"], "ryft_sync_to_score_seconds")
    s["name"] = os.path.basename(d)
    s["sync_to_score"] = {"count": int(bk[-1][1]) if bk else 0, "p50_s": quantile(.5, bk), "p95_s": quantile(.95, bk), "p99_s": quantile(.99, bk)}
    s["server_rate_limited"] = int(counter(pre["ingest"], post["ingest"], "ryft_rate_limited_total") + counter(pre["api"], post["api"], "ryft_rate_limited_total"))
    runs.append(s)

def f(x, nd=0):
    return "-" if x is None else ("%.*f" % (nd, x))

hdr = "%-8s %-14s %6s %8s %8s %8s %7s" % ("run", "route", "n", "p50 ms", "p95 ms", "p99 ms", "err %")
print(hdr); print("-" * len(hdr))
for r in runs:
    for route, v in r["routes"].items():
        print("%-8s %-14s %6d %8s %8s %8s %7s" % (r["name"], route, v["count"], f(v["p50_ms"], 1), f(v["p95_ms"], 1), f(v["p99_ms"], 1), f((v["error_rate"] or 0) * 100, 2)))
    ss = r["sync_to_score"]
    print("%-8s %-14s %6d %8s %8s %8s   (ryft_sync_to_score_seconds, seconds; 429s: client %d, server %d)" % (
        r["name"], "sync->score", ss["count"], f(ss["p50_s"], 1), f(ss["p95_s"], 1), f(ss["p99_s"], 1), r["http_429"], r["server_rate_limited"]))

def med(vals):
    vals = sorted(v for v in vals if v is not None)
    return vals[(len(vals) - 1) // 2] if vals else None

median = sorted(runs, key=lambda r: r["routes"]["POST /v1/sync"]["p95_ms"] or 0)[(len(runs) - 1) // 2]
print("\nmedian run (by POST /v1/sync p95): %s; spread over %d run(s):" % (median["name"], len(runs)))
for route in median["routes"]:
    for k in ("p50_ms", "p95_ms", "p99_ms"):
        xs = [r["routes"][route][k] for r in runs]
        print("  %-14s %-7s min %8s  median %8s  max %8s" % (route, k[:3], f(min(xs), 1), f(med(xs), 1), f(max(xs), 1)))
xs = [r["sync_to_score"]["p95_s"] for r in runs]
print("  %-14s %-7s min %8s  median %8s  max %8s  (s)" % ("sync->score", "p95", f(min(x for x in xs if x is not None) if any(x is not None for x in xs) else None, 1), f(med(xs), 1), f(max(x for x in xs if x is not None) if any(x is not None for x in xs) else None, 1)))

json.dump({"schema": 1, "limit_error_rate": limit, "median_run": median["name"], "runs": runs}, open(os.path.join(out, "baseline.json"), "w"), indent=2)
print("\nwritten: %s" % os.path.join(out, "baseline.json"))

bad = [(r["name"], route, v["error_rate"]) for r in runs for route, v in r["routes"].items() if (v["error_rate"] or 0) > limit]
bad += [(r["name"], "consent", r["consent_error_rate"]) for r in runs if (r["consent_error_rate"] or 0) > limit]
for n, route, e in bad:
    print("FAIL %s %s: error rate %.2f %% > %.2f %%" % (n, route, e * 100, limit * 100), file=sys.stderr)
sys.exit(1 if bad else 0)
PY
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
