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
# The direct-connection pass with the fake provider (D257, design §14), last: on by default in apikey mode
# (compose serves fake; a throwaway "<API_KEY>:<uuid>" user), off otherwise (no stack but compose serves fake).
CONNECT_SMOKE=${CONNECT_SMOKE:-$([ "$AUTH_MODE" = apikey ] && echo on || echo off)}
# Its coverage checks (score.coverage.sleep_origin "direct", hrv_metric "rmssd") need scoring to build coverage:
# the platform PR connect/scoring (PR 3) turns this default to on. Try it earlier with CONNECT_COVERAGE=on make smoke.
CONNECT_COVERAGE=${CONNECT_COVERAGE:-off}

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

echo "→ consent to the current privacy policy (POST /v1/sync needs it)"
POLICY=$(curl -fsS "$API_URL/v1/config" -H "Authorization: Bearer $API_KEY" | python3 -c 'import json,sys;print(json.load(sys.stdin)["privacy_policy_version"])')
curl -fsS -o /dev/null -X POST "$API_URL/v1/me/consent" -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' -d "{\"policy_version\":\"$POLICY\",\"app_version\":\"smoke\"}"

# Compose sets no APP_ATTEST_TOKEN_KEYS: the App Attest routes answer 501 (D247). Local only, the
# deployed api has a ring once the warn PR pins it.
if [ "$AUTH_MODE" = apikey ]; then
  echo "→ POST /v1/app-attest/challenge answers 501 (compose has no token key)"
  CODE=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$API_URL/v1/app-attest/challenge" -H "Authorization: Bearer $API_KEY")
  [ "$CODE" = 501 ] || { echo "✗ POST /v1/app-attest/challenge: $CODE, want 501"; exit 1; }
fi

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
NOTIFY_METRICS_URL="${NOTIFY_METRICS_URL-$( [ "$AUTH_MODE" = apikey ] && echo http://localhost:9093/metrics)}" \
NAP="$NAP" AUTH_MODE="$AUTH_MODE" INGEST_URL="$INGEST_URL" API_URL="$API_URL" API_KEY="$API_KEY" python3 "$HERE/check_api.py" "$TO"

echo "→ deletions: upload a sample with a HealthKit id, delete it, send the deletion again"
INGEST_URL="$INGEST_URL" API_KEY="$API_KEY" python3 - <<'PY'
import json, os, sys, urllib.request, uuid
from datetime import datetime, timedelta, timezone

def post(body):
    req = urllib.request.Request(os.environ["INGEST_URL"] + "/v1/sync", data=json.dumps(body).encode(), method="POST",
                                headers={"Authorization": "Bearer " + os.environ["API_KEY"], "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r)

def fail(msg, got):
    print("✗ deletions: " + msg + ": " + json.dumps(got)); sys.exit(1)

at = (datetime.now(timezone.utc) - timedelta(days=2)).replace(second=0, microsecond=0).isoformat().replace("+00:00", "Z")
hk, device = str(uuid.uuid4()), "5eed0000-0000-4000-8000-00000000de1e"  # fixed: no new device row per run
sample = {"type": "steps", "start": at, "end": at, "value": 1, "unit": "count", "source": "smoke-deletion", "id": hk.upper()}
up = post({"device_id": device, "samples": [sample], "sleep": [], "workouts": []})
if up["accepted"]["samples"] != 1:
    fail("the sample with an id is accepted", up)
deletion = {"device_id": device, "samples": [], "sleep": [], "workouts": [], "deleted": [{"kind": "sample", "id": hk}]}
first = post(deletion)
if first["deleted"]["samples"] != 1 or first["ignored"]["samples"]["unknown_id"] != 0 or not first["days_touched"]:
    fail("the first deletion removes the sample and touches its day", first)
again = post(deletion)
if again["deleted"]["samples"] != 0 or again["ignored"]["samples"]["unknown_id"] != 1:
    fail("the re-sent deletion is unknown_id", again)
print("  ok: deleted 1, re-sent is unknown_id 1")
PY

echo "→ fold: upload two copies of one workout 60 s apart, expect one workout afterwards"
INGEST_URL="$INGEST_URL" API_URL="$API_URL" API_KEY="$API_KEY" python3 - <<'PY'
import json, os, sys, urllib.request, uuid
from datetime import datetime, timedelta, timezone

def call(method, url, body=None):
    req = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None, method=method,
                                headers={"Authorization": "Bearer " + os.environ["API_KEY"], "Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.load(r)

def fail(msg, got):
    print("✗ fold: " + msg + ": " + json.dumps(got)); sys.exit(1)

device = "5eed0000-0000-4000-8000-00000000de1e"  # fixed: no new device row per run
t0 = (datetime.now(timezone.utc) - timedelta(days=3)).replace(hour=4, minute=10, second=0, microsecond=0)
iso = lambda t: t.isoformat().replace("+00:00", "Z")
def copy(shift):
    s = t0 + timedelta(seconds=shift)
    return {"id": str(uuid.uuid4()), "activity": "swimming", "start": iso(s), "end": iso(s + timedelta(minutes=30)), "source": "smoke-fold"}
older, newer = copy(0), copy(60)
post = lambda w: call("POST", os.environ["INGEST_URL"] + "/v1/sync", {"device_id": device, "samples": [], "sleep": [], "workouts": [w]})
first = post(older)
if first["accepted"]["workouts"] != 1:
    fail("the first copy is accepted", first)
second = post(newer)
if second["accepted"]["workouts"] != 1 or second["deleted"]["workouts"] != 0:
    fail("the second copy is accepted and a fold is not counted as deleted", second)
day = second["days_touched"][0]
mine = lambda: [w for w in call("GET", os.environ["API_URL"] + "/v1/days/" + day)["workouts"] if w["source"] == "smoke-fold" and w["start"][:10] == t0.date().isoformat()]
got = mine()
if [w["id"] for w in got] != [newer["id"]]:
    fail("one workout remains, the newer upload", got)
del_ = call("POST", os.environ["INGEST_URL"] + "/v1/sync", {"device_id": device, "samples": [], "sleep": [], "workouts": [],
                                                           "deleted": [{"kind": "workout", "id": newer["id"]}]})
if del_["deleted"]["workouts"] != 1:
    fail("the remaining copy is deletable (leaves the synthetic user clean)", del_)
print("  ok: two copies 60 s apart, one workout")
PY

if [ "$CONNECT_SMOKE" != on ]; then
  echo "→ connections: skipped (CONNECT_SMOKE=$CONNECT_SMOKE; on in apikey mode against compose)"
  exit 0
fi
echo "→ connections: the fake provider end to end (start, callback, confirm, signed push, scored, disconnect)"
# The push secret is compose's: CONNECT_FAKE_PUSH_SECRET from the environment, else the dev default that
# docker-compose.yml gives ingest (read from there, so the value lives in one place).
COMPOSE_FILE="$HERE/../docker-compose.yml" CONNECT_COVERAGE="$CONNECT_COVERAGE" \
INGEST_URL="$INGEST_URL" API_URL="$API_URL" API_KEY="$API_KEY" python3 - <<'PY'
import hashlib, hmac, json, os, re, sys, time, urllib.error, urllib.parse, urllib.request, uuid
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo

API, INGEST = os.environ["API_URL"], os.environ["INGEST_URL"]
KEY = f"{os.environ['API_KEY']}:{uuid.uuid4()}"  # a throwaway account, deleted at the end
WAIT = 60  # seconds for the inbox consumer and scoring (outbox → Pub/Sub → ingest → Pub/Sub → scoring)


def fail(msg):
    print("✗ connections: " + msg)
    sys.exit(1)


def check(cond, msg):
    if not cond:
        fail(msg)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None  # the 302 surfaces as an HTTPError with its Location


OPENER = urllib.request.build_opener(NoRedirect)


def send(method, url, body=None, headers=None, auth=True):
    """Status, headers and the decoded body (None when empty)."""
    h = {"Content-Type": "application/json", **(headers or {})}
    if auth:
        h["Authorization"] = "Bearer " + KEY
    data = body if isinstance(body, (bytes, type(None))) else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method, headers=h)
    try:
        with OPENER.open(req, timeout=30) as r:
            status, hdrs, raw = r.status, r.headers, r.read()
    except urllib.error.HTTPError as e:
        status, hdrs, raw = e.code, e.headers, e.read()
    return status, hdrs, json.loads(raw) if raw.strip() else None


def call(method, path, body=None, want=200, base=API):
    status, _, out = send(method, base + path, body)
    check(status == want, f"{method} {path}: {status}, want {want}: {json.dumps(out)[:300]}")
    return out


def poll(what, fn):
    """fn() until it is truthy, at most WAIT seconds; its last value."""
    deadline = time.monotonic() + WAIT
    while True:
        got = fn()
        if got or time.monotonic() > deadline:
            return got
        time.sleep(1)


def push_secret():
    if os.environ.get("CONNECT_FAKE_PUSH_SECRET"):
        return os.environ["CONNECT_FAKE_PUSH_SECRET"]
    with open(os.environ["COMPOSE_FILE"]) as f:
        m = re.search(r"CONNECT_FAKE_PUSH_SECRET:\s*\$\{CONNECT_FAKE_PUSH_SECRET:-([^}]+)\}", f.read())
    check(m, "no CONNECT_FAKE_PUSH_SECRET in the environment and no default in docker-compose.yml")
    return m.group(1)


def push(body, signature=None):
    """POST the fake's push to ingest, signed with hex(HMAC-SHA256(secret, body)) unless a signature is given."""
    raw = json.dumps(body, separators=(",", ":")).encode()
    sig = signature or hmac.new(push_secret().encode(), raw, hashlib.sha256).hexdigest()
    status, _, out = send("POST", INGEST + "/connect/fake/push", raw, {"X-Ryft-Fake-Signature": sig}, auth=False)
    return status, out


def fake_entries(listing, key):
    return [c for c in listing[key] if c["provider"] == "fake"]


def check_coverage(day):
    """PR 3 (connect/scoring): the scored day says where its night and its HRV came from. Off until scoring
    builds coverage (CONNECT_COVERAGE in scripts/smoke.sh)."""
    def cov():
        c = call("GET", f"/v1/days/{day}")["score"].get("coverage") or {}
        return c if c.get("sleep_origin") == "direct" and c.get("hrv_metric") == "rmssd" else None
    got = poll("coverage", cov)
    check(got, f"day {day}: coverage.sleep_origin direct and coverage.hrv_metric rmssd, got "
               f"{json.dumps(call('GET', f'/v1/days/{day}')['score'].get('coverage'))}")
    print(f"  ok: coverage of {day}: sleep_origin direct, hrv_metric rmssd")


connected = False
try:
    # 1. consent (start and confirm need it, and ingest stores nothing without it)
    policy = call("GET", "/v1/config")["privacy_policy_version"]
    status, _, out = send("POST", API + "/v1/me/consent", {"policy_version": policy, "app_version": "smoke"})
    check(status == 204, f"POST /v1/me/consent: {status} {out}")
    tz = ZoneInfo(call("GET", "/v1/me")["tz"])

    # 2. available lists fake, with room
    before = call("GET", "/v1/me/connections")
    check(fake_entries(before, "connections") == [], f"a new account has no fake connection: {before}")
    avail = fake_entries(before, "available")
    check(len(avail) == 1 and avail[0]["accepting"] is True and avail[0]["provides"] == ["hrv", "sleep"]
          and avail[0]["history"] == "none",
          f"available lists fake, accepting (compose: CONNECT_FAKE=on, CONNECT_PROVIDERS=fake): {before}")

    # 3. start
    st = call("POST", "/v1/me/connections/fake/start", {})
    authorize = st.get("authorize_url", "")
    parts = urllib.parse.urlsplit(authorize)
    query = urllib.parse.parse_qs(parts.query)
    check(parts.path == "/v1/connect/fake/callback" and query.get("state") and query.get("code") and st.get("expires_at"),
          "start answers the fake's authorize_url (the callback itself, with state and code) and expires_at")
    puid = hashlib.sha256(query["state"][0].encode()).hexdigest()[:16]  # the fake's provider user id

    # 4. the callback, not followed: 302 to the app with a ticket
    def callback():
        status, hdrs, _ = send("GET", authorize, auth=False)
        loc = urllib.parse.urlsplit(hdrs.get("Location") or "")
        return status, hdrs, loc, urllib.parse.parse_qs(loc.query)
    status, hdrs, loc, q = callback()
    check(status == 302 and f"{loc.scheme}://{loc.netloc}{loc.path}" == "ryft://connect/fake" and q.get("status") == ["ok"]
          and re.fullmatch(r"[0-9a-f]{32}", (q.get("ticket") or [""])[0]) and hdrs.get("Cache-Control") == "no-store",
          f"callback: {status}, want 302 ryft://connect/fake?status=ok&ticket=<32 hex>, no-store "
          f"(status={q.get('status')}, ticket present={bool(q.get('ticket'))})")
    ticket = q["ticket"][0]

    # 5. confirm, 6. connected
    conn = call("POST", "/v1/me/connections/fake/confirm", {"ticket": ticket})
    connected = True
    check(conn["provider"] == "fake" and conn["status"] == "connected" and conn["provider_user_id"] == puid
          and conn["last_data_at"] is None and sorted(conn["permissions"]) == ["read_hrv", "read_sleep"],
          f"confirm answers the connection, connected, the fake's user id, no data yet: {conn}")
    listed = fake_entries(call("GET", "/v1/me/connections"), "connections")
    check(len(listed) == 1 and listed[0]["status"] == "connected" and listed[0]["provider_user_id"] == puid,
          f"GET /v1/me/connections shows fake connected: {listed}")
    print("  ok: start → 302 ryft://connect/fake?status=ok&ticket=… → confirm → connected")

    # 10 (design §14). The used authorize_url again: the state is single use
    status, _, _, q = callback()
    check(status == 302 and q.get("status") == ["expired"] and "ticket" not in q,
          f"the used authorize_url again: {status} status={q.get('status')}, want 302 status=expired without a ticket")

    # 7. a signed push of yesterday's night (light, deep, rem, light: 450 min asleep) and an RMSSD in it
    day = (datetime.now(tz) - timedelta(days=1)).date()
    wake = datetime(day.year, day.month, day.day, 7, 0, tzinfo=tz).astimezone(timezone.utc)
    iso = lambda t: t.strftime("%Y-%m-%dT%H:%M:%SZ")
    stages = [("light", 120), ("deep", 90), ("rem", 90), ("light", 150)]
    t, segments = wake - timedelta(minutes=sum(m for _, m in stages)), []
    for stage, minutes in stages:
        segments.append({"start": iso(t), "end": iso(t + timedelta(minutes=minutes)), "stage": stage})
        t += timedelta(minutes=minutes)
    hrv_at = wake - timedelta(hours=5)
    body = {"items": [
        {"kind": "sleep", "record_id": f"smoke-night-{puid}", "user": puid, "version": 1,
         "night": {"state": "final", "segments": segments}},
        {"kind": "hrv", "record_id": f"smoke-hrv-{puid}", "user": puid, "version": 1,
         "hrv": {"start": iso(hrv_at), "end": iso(hrv_at + timedelta(minutes=5)), "rmssd_ms": 48.5}}]}
    status, out = push(body, signature=hmac.new(b"not the secret", b"{}", hashlib.sha256).hexdigest())
    check(status == 401, f"a push with a wrong signature: {status} {out}, want 401")
    for attempt in ("push", "the same push again (a duplicate)"):
        status, out = push(body)
        check(status == 200, f"{attempt}: {status} {out}, want 200")
    print("  ok: wrong signature 401; signed push 200; the same again 200")

    # 8. scored: the day's night is the one pushed, once (the duplicate added nothing)
    def scored():
        d = call("GET", f"/v1/days/{day}")
        return d if d["score"].get("sleep_actual_min") == 450 and d["sleep"] else None
    d = poll("scored", scored)
    if not d:
        d = call("GET", f"/v1/days/{day}")
        fail(f"day {day} never scored with the pushed night: sleep_actual_min {d['score'].get('sleep_actual_min')}, "
             f"want 450 (inbox consumer → ApplyDirect → ingested → scoring)")
    mins = {s["stage"]: s["min"] for s in d["sleep"]["stages"]}
    check(d["sleep"]["total_min"] == 450 and d["sleep"]["has_stages"] and mins.get("deep") == 90 and mins.get("rem") == 90
          and mins.get("light") == 270 and [s["stage"] for s in d["sleep"]["segments"]] == ["light", "deep", "rem", "light"]
          and d["sleep"]["segments"][0]["start"] == segments[0]["start"] and d["sleep"]["segments"][-1]["end"] == segments[-1]["end"],
          f"day {day}: the night as pushed, once: {json.dumps(d['sleep'])[:400]}")
    print(f"  ok: {day} scored with the direct night (450 min: light 270, deep 90, rem 90)")

    # 9. User.sources shows the connection as a direct source; the connection has data
    src = [s for s in call("GET", "/v1/me")["sources"] if s["origin"] == "direct"]
    check(len(src) == 1 and src[0]["name"] == "Fake" and src[0]["bundle_id"] is None and src[0]["kinds"] == ["hrv", "sleep"],
          f"User.sources has the fake as one direct source (name Fake, kinds hrv and sleep): {src}")
    listed = fake_entries(call("GET", "/v1/me/connections"), "connections")
    check(listed and listed[0]["last_data_at"], f"last_data_at is set after the push: {listed}")
    print("  ok: User.sources lists Fake with origin direct; last_data_at set")

    if os.environ.get("CONNECT_COVERAGE") == "on":
        check_coverage(day.isoformat())
    else:
        print("  - coverage (sleep_origin, hrv_metric) not checked: CONNECT_COVERAGE=off until connect/scoring")

    # 11. disconnect: gone, its data deleted and the day rescored without it (D257)
    out = call("DELETE", "/v1/me/connections/fake")
    connected = False
    check(out == {"status": "disconnected", "deregistered": True, "data_deleted": True},
          f"DELETE /v1/me/connections/fake: {out}, want disconnected, deregistered true, data_deleted true")
    after = call("GET", "/v1/me/connections")
    check(fake_entries(after, "connections") == [], f"the connection is gone after the disconnect: {after}")
    src = [s for s in call("GET", "/v1/me")["sources"] if s["origin"] == "direct"]
    check(src == [], f"User.sources has no direct source after the disconnect: {src}")
    def emptied():
        d = call("GET", f"/v1/days/{day}")
        return d if d["sleep"] is None and d["score"].get("sleep_actual_min") is None else None
    check(poll("rescored", emptied), f"day {day} still has the direct night after the disconnect: "
                                     f"{json.dumps(call('GET', f'/v1/days/{day}')['sleep'])[:300]}")
    print(f"  ok: disconnect → deregistered, the connection and Fake gone, {day} rescored without the night")
finally:
    # Leave the stack clean (the fake's user cap counts other accounts): end a connection a failure left, then
    # delete the throwaway account.
    if connected:
        send("DELETE", API + "/v1/me/connections/fake")
    send("DELETE", API + "/v1/me")
PY
